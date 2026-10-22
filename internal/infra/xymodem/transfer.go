// Package xymodem implements XMODEM and YMODEM file transfers over a byte
// stream. It is kept independent of terminal UI so peers can be tested using
// ordinary io.ReadWriter implementations.
package xymodem

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Protocol uint8

const (
	XMODEM Protocol = iota + 1
	YMODEM
)

const (
	soh          = 0x01
	stx          = 0x02
	eot          = 0x04
	ack          = 0x06
	nak          = 0x15
	can          = 0x18
	crcRequest   = 'C'
	blockSize128 = 128
	blockSize1K  = 1024
	maxRetries   = 10
	defaultLimit = int64(16 << 30)
)

var (
	ErrCancelled  = errors.New("x/y modem: transfer cancelled")
	ErrTooLarge   = errors.New("x/y modem: file exceeds receive size limit")
	ErrUnsafeName = errors.New("x/y modem: sender provided an unsafe file name")
)

type Progress struct {
	Name      string
	Bytes     int64
	Total     int64
	Direction string
}

type ReceiveOptions struct {
	Protocol     Protocol
	Directory    string // required for YMODEM
	Path         string // required for XMODEM
	Overwrite    bool
	MaxFileBytes int64
}

type ReceivedFile struct {
	Name string
	Path string
	Size int64
}

// Receive receives one XMODEM file at options.Path or a YMODEM batch into
// options.Directory. XMODEM does not carry a file size, so its conventional
// trailing SUB (0x1a) padding is removed; files genuinely ending in SUB cannot
// be represented exactly by the protocol.
func Receive(ctx context.Context, rw io.ReadWriter, options ReceiveOptions, progress func(Progress)) ([]ReceivedFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if rw == nil {
		return nil, errors.New("x/y modem: transfer stream is not configured")
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = defaultLimit
	}
	switch options.Protocol {
	case XMODEM:
		if strings.TrimSpace(options.Path) == "" {
			return nil, errors.New("x/y modem: XMODEM destination path is required")
		}
		file, err := receiveXMODEM(ctx, rw, options, progress)
		if err != nil {
			return nil, err
		}
		return []ReceivedFile{file}, nil
	case YMODEM:
		if strings.TrimSpace(options.Directory) == "" {
			return nil, errors.New("x/y modem: YMODEM destination directory is required")
		}
		return receiveYMODEM(ctx, rw, options, progress)
	default:
		return nil, errors.New("x/y modem: unsupported protocol")
	}
}

// Send sends the listed regular files using XMODEM (exactly one file) or a
// YMODEM batch. YMODEM uses its block-zero filename and byte-count metadata.
func Send(ctx context.Context, rw io.ReadWriter, protocol Protocol, paths []string, progress func(Progress)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if rw == nil {
		return errors.New("x/y modem: transfer stream is not configured")
	}
	if len(paths) == 0 {
		return errors.New("x/y modem: at least one file path is required")
	}
	files, err := inspectFiles(paths)
	if err != nil {
		return err
	}
	switch protocol {
	case XMODEM:
		if len(files) != 1 {
			return errors.New("x/y modem: XMODEM sends exactly one file")
		}
		return sendXMODEM(ctx, rw, files[0], progress)
	case YMODEM:
		return sendYMODEM(ctx, rw, files, progress)
	default:
		return errors.New("x/y modem: unsupported protocol")
	}
}

type sendFile struct {
	path string
	name string
	size int64
}

func inspectFiles(paths []string) ([]sendFile, error) {
	files := make([]sendFile, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("x/y modem: only regular files can be sent: %q", path)
		}
		name := filepath.Base(path)
		if err := safeName(name); err != nil {
			return nil, fmt.Errorf("%w: %q", err, name)
		}
		if len(name)+1+len(strconv.FormatInt(info.Size(), 10)) >= blockSize128 {
			return nil, fmt.Errorf("x/y modem: YMODEM header is too long for %q", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("x/y modem: duplicate file name in batch: %q", name)
		}
		seen[name] = struct{}{}
		files = append(files, sendFile{path: path, name: name, size: info.Size()})
	}
	return files, nil
}

func receiveXMODEM(ctx context.Context, rw io.ReadWriter, options ReceiveOptions, progress func(Progress)) (ReceivedFile, error) {
	target, err := filepath.Abs(options.Path)
	if err != nil {
		return ReceivedFile{}, fmt.Errorf("resolve destination: %w", err)
	}
	if err := checkDestination(target, options.Overwrite); err != nil {
		return ReceivedFile{}, err
	}
	tmp, tmpPath, err := createTemp(target)
	if err != nil {
		return ReceivedFile{}, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	if err := writeByte(rw, crcRequest); err != nil {
		return ReceivedFile{}, err
	}
	var size int64
	sequence := byte(1)
	for retries := 0; ; {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ReceivedFile{}, ErrCancelled
		}
		marker, err := readByte(rw)
		if err != nil {
			return ReceivedFile{}, fmt.Errorf("read XMODEM frame: %w", err)
		}
		switch marker {
		case can:
			return ReceivedFile{}, ErrCancelled
		case eot:
			if err := writeByte(rw, ack); err != nil {
				return ReceivedFile{}, err
			}
			// XMODEM has no length metadata. Remove only the conventional SUB
			// padding at EOF; an actual trailing SUB is indistinguishable.
			if err := trimSUBPadding(tmp, &size); err != nil {
				return ReceivedFile{}, err
			}
			if err := finishFile(tmp, tmpPath, target, options.Overwrite); err != nil {
				return ReceivedFile{}, err
			}
			return ReceivedFile{Name: filepath.Base(target), Path: target, Size: size}, nil
		case soh, stx:
			payloadSize := blockSize128
			if marker == stx {
				payloadSize = blockSize1K
			}
			packet := make([]byte, payloadSize+4)
			if _, err := io.ReadFull(rw, packet); err != nil {
				return ReceivedFile{}, fmt.Errorf("read XMODEM data block: %w", err)
			}
			seq, inverse := packet[0], packet[1]
			payload := packet[2 : 2+payloadSize]
			wantCRC := binary.BigEndian.Uint16(packet[2+payloadSize:])
			if seq+inverse != 0xff || crc16(payload) != wantCRC {
				retries++
				if retries > maxRetries {
					cancelPeer(rw)
					return ReceivedFile{}, errors.New("x/y modem: too many invalid XMODEM blocks")
				}
				if err := writeByte(rw, nak); err != nil {
					return ReceivedFile{}, err
				}
				continue
			}
			if seq == sequence {
				if size+int64(payloadSize) > options.MaxFileBytes {
					cancelPeer(rw)
					return ReceivedFile{}, ErrTooLarge
				}
				if err := writeFull(tmp, payload); err != nil {
					return ReceivedFile{}, fmt.Errorf("write temporary download: %w", err)
				}
				size += int64(payloadSize)
				sequence++
				retries = 0
				if progress != nil {
					progress(Progress{Name: filepath.Base(target), Bytes: size, Direction: "download"})
				}
			} else if seq != sequence-1 { // retransmitted previous block
				retries++
				if retries > maxRetries {
					cancelPeer(rw)
					return ReceivedFile{}, errors.New("x/y modem: unexpected XMODEM block sequence")
				}
			}
			if err := writeByte(rw, ack); err != nil {
				return ReceivedFile{}, err
			}
		default:
			retries++
			if retries > maxRetries {
				cancelPeer(rw)
				return ReceivedFile{}, errors.New("x/y modem: too many unexpected XMODEM bytes")
			}
			if err := writeByte(rw, nak); err != nil {
				return ReceivedFile{}, err
			}
		}
	}
}

func sendXMODEM(ctx context.Context, rw io.ReadWriter, file sendFile, progress func(Progress)) error {
	checksumMode, err := expectStartup(ctx, rw, true)
	if err != nil {
		return err
	}
	input, err := os.Open(file.path)
	if err != nil {
		return err
	}
	defer input.Close()
	sequence := byte(1)
	var sent int64
	// Classic XMODEM uses 128-byte SOH blocks. YMODEM may use STX blocks;
	// avoid sending the optional XMODEM-1K extension to 128-byte-only peers.
	buf := make([]byte, blockSize128)
	for {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		n, readErr := io.ReadFull(input, buf)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		packet, err := makePacket(sequence, buf[:n], false, !checksumMode)
		if err != nil {
			return err
		}
		if err := sendPacket(ctx, rw, packet); err != nil {
			return err
		}
		sent += int64(n)
		sequence++
		if progress != nil {
			progress(Progress{Name: file.name, Bytes: sent, Total: file.size, Direction: "upload"})
		}
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	for retries := 0; retries < maxRetries; retries++ {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		if err := writeByte(rw, eot); err != nil {
			return err
		}
		response, err := readByte(rw)
		if err != nil {
			return err
		}
		if response == ack {
			return nil
		}
		if response == can {
			return ErrCancelled
		}
		if response != nak {
			continue
		}
	}
	cancelPeer(rw)
	return errors.New("x/y modem: receiver did not acknowledge XMODEM EOT")
}

func receiveYMODEM(ctx context.Context, rw io.ReadWriter, options ReceiveOptions, progress func(Progress)) ([]ReceivedFile, error) {
	directory, err := filepath.Abs(options.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve destination directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("inspect destination directory: %w", err)
	}
	if err := writeByte(rw, crcRequest); err != nil {
		return nil, err
	}
	var completed []ReceivedFile
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return completed, ErrCancelled
		}
		marker, err := readByte(rw)
		if err != nil {
			return completed, fmt.Errorf("read YMODEM header: %w", err)
		}
		if marker != soh {
			if marker == can {
				return completed, ErrCancelled
			}
			retries++
			if retries > maxRetries {
				cancelPeer(rw)
				return completed, errors.New("x/y modem: too many unexpected YMODEM header bytes")
			}
			if err := writeByte(rw, nak); err != nil {
				return completed, err
			}
			continue
		}
		packet := make([]byte, blockSize128+4)
		if _, err := io.ReadFull(rw, packet); err != nil {
			return completed, fmt.Errorf("read YMODEM block zero: %w", err)
		}
		if packet[0] != 0 || packet[1] != 0xff || crc16(packet[2:2+blockSize128]) != binary.BigEndian.Uint16(packet[2+blockSize128:]) {
			retries++
			if retries > maxRetries {
				cancelPeer(rw)
				return completed, errors.New("x/y modem: too many invalid YMODEM headers")
			}
			if err := writeByte(rw, nak); err != nil {
				return completed, err
			}
			continue
		}
		name, size, empty, err := parseYHeader(packet[2 : 2+blockSize128])
		if err != nil {
			cancelPeer(rw)
			return completed, err
		}
		if empty {
			if err := writeByte(rw, ack); err != nil {
				return completed, err
			}
			return completed, nil
		}
		retries = 0
		if size > options.MaxFileBytes {
			cancelPeer(rw)
			return completed, fmt.Errorf("%w: %q (%d bytes)", ErrTooLarge, name, size)
		}
		if err := safeName(name); err != nil {
			cancelPeer(rw)
			return completed, fmt.Errorf("%w: %q", err, name)
		}
		target := filepath.Join(directory, name)
		if err := checkDestination(target, options.Overwrite); err != nil {
			cancelPeer(rw)
			return completed, err
		}
		tmp, tmpPath, err := createTemp(target)
		if err != nil {
			cancelPeer(rw)
			return completed, err
		}
		if err := writeByte(rw, ack); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return completed, err
		}
		if err := writeByte(rw, crcRequest); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return completed, err
		}
		file, err := receiveYFile(ctx, rw, tmp, tmpPath, target, name, size, options.MaxFileBytes, options.Overwrite, progress)
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			if !errors.Is(err, ErrCancelled) {
				cancelPeer(rw)
			}
			return completed, err
		}
		completed = append(completed, file)
	}
}

func receiveYFile(ctx context.Context, rw io.ReadWriter, tmp *os.File, tmpPath, target, name string, expected, maxBytes int64, overwrite bool, progress func(Progress)) (ReceivedFile, error) {
	var received int64
	sequence := byte(1)
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return ReceivedFile{}, ErrCancelled
		}
		marker, err := readByte(rw)
		if err != nil {
			return ReceivedFile{}, err
		}
		if marker == can {
			return ReceivedFile{}, ErrCancelled
		}
		if marker == eot {
			if err := writeByte(rw, nak); err != nil {
				return ReceivedFile{}, err
			}
			second, err := readByte(rw)
			if err != nil {
				return ReceivedFile{}, err
			}
			if second != eot {
				return ReceivedFile{}, errors.New("x/y modem: expected second EOT in YMODEM finish")
			}
			if received != expected {
				return ReceivedFile{}, fmt.Errorf("x/y modem: YMODEM size mismatch for %q: received %d, expected %d", name, received, expected)
			}
			if err := writeByte(rw, ack); err != nil {
				return ReceivedFile{}, err
			}
			if err := writeByte(rw, crcRequest); err != nil {
				return ReceivedFile{}, err
			}
			if err := finishFile(tmp, tmpPath, target, overwrite); err != nil {
				return ReceivedFile{}, err
			}
			return ReceivedFile{Name: name, Path: target, Size: received}, nil
		}
		blockSize := 0
		switch marker {
		case soh:
			blockSize = blockSize128
		case stx:
			blockSize = blockSize1K
		default:
			retries++
			if retries > maxRetries {
				return ReceivedFile{}, errors.New("x/y modem: too many unexpected YMODEM bytes")
			}
			if err := writeByte(rw, nak); err != nil {
				return ReceivedFile{}, err
			}
			continue
		}
		packet := make([]byte, blockSize+4)
		if _, err := io.ReadFull(rw, packet); err != nil {
			return ReceivedFile{}, err
		}
		seq, inverse := packet[0], packet[1]
		payload := packet[2 : 2+blockSize]
		valid := seq+inverse == 0xff && crc16(payload) == binary.BigEndian.Uint16(packet[2+blockSize:])
		if !valid {
			retries++
			if retries > maxRetries {
				return ReceivedFile{}, errors.New("x/y modem: too many invalid YMODEM blocks")
			}
			if err := writeByte(rw, nak); err != nil {
				return ReceivedFile{}, err
			}
			continue
		}
		if seq == sequence {
			remaining := expected - received
			if remaining <= 0 {
				return ReceivedFile{}, fmt.Errorf("x/y modem: received data beyond declared size for %q", name)
			}
			writeSize := min(int64(len(payload)), remaining)
			if received+writeSize > maxBytes {
				return ReceivedFile{}, ErrTooLarge
			}
			if err := writeFull(tmp, payload[:writeSize]); err != nil {
				return ReceivedFile{}, err
			}
			received += writeSize
			sequence++
			retries = 0
			if progress != nil {
				progress(Progress{Name: name, Bytes: received, Total: expected, Direction: "download"})
			}
		} else if seq != sequence-1 {
			retries++
			if retries > maxRetries {
				return ReceivedFile{}, errors.New("x/y modem: unexpected YMODEM block sequence")
			}
			if err := writeByte(rw, nak); err != nil {
				return ReceivedFile{}, err
			}
			continue
		}
		if err := writeByte(rw, ack); err != nil {
			return ReceivedFile{}, err
		}
	}
}

func sendYMODEM(ctx context.Context, rw io.ReadWriter, files []sendFile, progress func(Progress)) error {
	if _, err := expectStartup(ctx, rw, false); err != nil {
		return err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		header := make([]byte, blockSize128)
		metadata := fmt.Sprintf("%s\x00%d", file.name, file.size)
		if len(metadata) >= len(header) {
			return fmt.Errorf("x/y modem: YMODEM header is too long for %q", file.name)
		}
		copy(header, metadata)
		if err := sendYHeader(ctx, rw, makeRawPacket(0, header, soh)); err != nil {
			return err
		}
		response, err := readByte(rw)
		if err != nil {
			return err
		}
		if response != crcRequest {
			return responseError(response)
		}
		input, err := os.Open(file.path)
		if err != nil {
			return err
		}
		sequence := byte(1)
		var sent int64
		buf := make([]byte, blockSize1K)
		for {
			if err := ctx.Err(); err != nil {
				input.Close()
				cancelPeer(rw)
				return ErrCancelled
			}
			n, readErr := io.ReadFull(input, buf)
			if readErr == io.EOF {
				break
			}
			if readErr != nil && readErr != io.ErrUnexpectedEOF {
				input.Close()
				return readErr
			}
			packet, err := makePacket(sequence, buf[:n], n > blockSize128, true)
			if err != nil {
				input.Close()
				return err
			}
			if err := sendPacket(ctx, rw, packet); err != nil {
				input.Close()
				return err
			}
			sent += int64(n)
			sequence++
			if progress != nil {
				progress(Progress{Name: file.name, Bytes: sent, Total: file.size, Direction: "upload"})
			}
			if readErr == io.ErrUnexpectedEOF {
				break
			}
		}
		if err := input.Close(); err != nil {
			return err
		}
		if err := sendEOT(ctx, rw, true); err != nil {
			return err
		}
		response, err = readByte(rw)
		if err != nil {
			return err
		}
		if response != crcRequest {
			return responseError(response)
		}
	}
	// An empty block zero terminates a YMODEM batch.
	return sendYHeader(ctx, rw, makeRawPacket(0, make([]byte, blockSize128), soh))
}

func sendYHeader(ctx context.Context, rw io.ReadWriter, packet []byte) error {
	for retries := 0; retries < maxRetries; retries++ {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		if err := writeFull(rw, packet); err != nil {
			return err
		}
		for wait := 0; wait < maxRetries; wait++ {
			if err := ctx.Err(); err != nil {
				cancelPeer(rw)
				return ErrCancelled
			}
			response, err := readByte(rw)
			if err != nil {
				return err
			}
			switch response {
			case ack:
				return nil
			case crcRequest: // the receiver still wants block zero; resend the header
				break
			case nak:
				break
			case can:
				return ErrCancelled
			default:
				continue
			}
			break
		}
	}
	cancelPeer(rw)
	return errors.New("x/y modem: receiver did not acknowledge YMODEM header")
}

func expectStartup(ctx context.Context, rw io.ReadWriter, allowChecksum bool) (bool, error) {
	for retries := 0; retries < maxRetries; retries++ {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return false, ErrCancelled
		}
		response, err := readByte(rw)
		if err != nil {
			return false, fmt.Errorf("wait for receiver: %w", err)
		}
		if response == crcRequest {
			return false, nil
		}
		if response == nak && allowChecksum {
			return true, nil
		}
		if response == can {
			return false, ErrCancelled
		}
	}
	return false, errors.New("x/y modem: receiver did not request transfer")
}

func sendPacket(ctx context.Context, rw io.ReadWriter, packet []byte) error {
	for retries := 0; retries < maxRetries; retries++ {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		if err := writeFull(rw, packet); err != nil {
			return err
		}
		response, err := readByte(rw)
		if err != nil {
			return err
		}
		switch response {
		case ack:
			return nil
		case nak, crcRequest:
			continue
		case can:
			return ErrCancelled
		default:
			continue
		}
	}
	cancelPeer(rw)
	return errors.New("x/y modem: block retry limit exceeded")
}

func sendEOT(ctx context.Context, rw io.ReadWriter, ymodem bool) error {
	for retries := 0; retries < maxRetries; retries++ {
		if err := ctx.Err(); err != nil {
			cancelPeer(rw)
			return ErrCancelled
		}
		if err := writeByte(rw, eot); err != nil {
			return err
		}
		response, err := readByte(rw)
		if err != nil {
			return err
		}
		if response == can {
			return ErrCancelled
		}
		if response == ack {
			return nil
		}
		if ymodem && response == nak {
			if err := writeByte(rw, eot); err != nil {
				return err
			}
			response, err = readByte(rw)
			if err != nil {
				return err
			}
			if response == ack {
				return nil
			}
		}
	}
	return errors.New("x/y modem: receiver did not acknowledge EOT")
}

func makePacket(sequence byte, data []byte, prefer1K, useCRC bool) ([]byte, error) {
	blockSize := blockSize128
	if prefer1K && len(data) > blockSize128 {
		blockSize = blockSize1K
	}
	if len(data) > blockSize {
		return nil, errors.New("x/y modem: payload exceeds block size")
	}
	payload := make([]byte, blockSize)
	copy(payload, data)
	for i := len(data); i < len(payload); i++ {
		payload[i] = 0x1a
	}
	marker := byte(soh)
	if blockSize == blockSize1K {
		marker = stx
	}
	if useCRC {
		return makeRawPacket(sequence, payload, marker), nil
	}
	packet := []byte{marker, sequence, ^sequence}
	packet = append(packet, payload...)
	var checksum byte
	for _, value := range payload {
		checksum += value
	}
	return append(packet, checksum), nil
}

func makeRawPacket(sequence byte, payload []byte, marker byte) []byte {
	packet := make([]byte, 0, len(payload)+5)
	packet = append(packet, marker, sequence, ^sequence)
	packet = append(packet, payload...)
	var checksum [2]byte
	binary.BigEndian.PutUint16(checksum[:], crc16(payload))
	return append(packet, checksum[:]...)
}

func parseYHeader(data []byte) (name string, size int64, empty bool, err error) {
	nameEnd := strings.IndexByte(string(data), 0)
	if nameEnd < 0 {
		return "", 0, false, errors.New("x/y modem: malformed YMODEM filename")
	}
	name = string(data[:nameEnd])
	if name == "" {
		return "", 0, true, nil
	}
	metadata := data[nameEnd+1:]
	end := len(metadata)
	for index, value := range metadata {
		if value == 0 || value == ' ' || value == '\t' {
			end = index
			break
		}
	}
	if end == 0 {
		return "", 0, false, errors.New("x/y modem: YMODEM header has no size")
	}
	size, err = strconv.ParseInt(string(metadata[:end]), 10, 64)
	if err != nil || size < 0 {
		return "", 0, false, errors.New("x/y modem: invalid YMODEM file size")
	}
	return name, size, false, nil
}

func safeName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || filepath.Base(name) != name {
		return ErrUnsafeName
	}
	return nil
}

func checkDestination(path string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("x/y modem: destination already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect destination %q: %w", path, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return fmt.Errorf("inspect destination directory: %w", err)
	}
	return nil
}

func createTemp(target string) (*os.File, string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".xymodem-*.part")
	if err != nil {
		return nil, "", fmt.Errorf("create temporary download: %w", err)
	}
	return tmp, tmp.Name(), nil
}

func finishFile(tmp *os.File, tmpPath, target string, overwrite bool) error {
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary download: %w", err)
	}
	if overwrite {
		if err := os.Rename(tmpPath, target); err != nil {
			return fmt.Errorf("commit downloaded file: %w", err)
		}
		return nil
	}
	// The temporary file is created beside the destination, so a hard link is
	// atomic and cannot replace a file created after the initial existence check.
	if err := os.Link(tmpPath, target); err != nil {
		return fmt.Errorf("commit downloaded file without overwriting: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("remove temporary download after commit: %w", err)
	}
	return nil
}

func trimSUBPadding(file *os.File, size *int64) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	last := make([]byte, 1)
	for end > 0 {
		if _, err := file.ReadAt(last, end-1); err != nil {
			return err
		}
		if last[0] != 0x1a {
			break
		}
		end--
	}
	if err := file.Truncate(end); err != nil {
		return err
	}
	*size = end
	return nil
}

func crc16(data []byte) uint16 {
	var crc uint16
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func readByte(r io.Reader) (byte, error) {
	var value [1]byte
	_, err := io.ReadFull(r, value[:])
	return value[0], err
}

func writeByte(w io.Writer, value byte) error { return writeFull(w, []byte{value}) }

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func cancelPeer(w io.Writer) { _ = writeFull(w, []byte{can, can, can}) }

func responseError(response byte) error {
	if response == can {
		return ErrCancelled
	}
	return fmt.Errorf("x/y modem: unexpected peer response 0x%02x", response)
}
