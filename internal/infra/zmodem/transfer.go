package zmodem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxReceiveBytes = int64(16 << 30)
	maxProtocolOffset      = int64(^uint32(0))
	transferChunkSize      = 1024
	maxTransferRetries     = 5
	canFC32                = 0x20
	defaultReceiveFlags    = 0x23 // CANFDX | CANOVIO | CANFC32
)

var (
	ErrCancelled   = errors.New("zmodem: transfer cancelled")
	ErrFileTooBig  = errors.New("zmodem: file exceeds receive size limit")
	ErrUnsafeName  = errors.New("zmodem: sender provided an unsafe file name")
	ErrBadMetadata = errors.New("zmodem: invalid file metadata")
)

type Progress struct {
	Name      string
	Bytes     int64
	Total     int64
	Direction string
}

type ReceiveOptions struct {
	Directory     string
	Overwrite     bool
	MaxFileBytes  int64
	PreserveMode  bool
	PreserveMtime bool
}

type ReceivedFile struct {
	Name string
	Path string
	Size int64
}

type fileMetadata struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

// Receive handles a peer-initiated ZMODEM download session. Its reader must
// already contain the peer's initial ZRQINIT bytes. The caller is responsible
// for asking the user to approve the destination directory before starting.
func Receive(ctx context.Context, wire *Wire, options ReceiveOptions, progress func(Progress)) ([]ReceivedFile, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if wire == nil || wire.r == nil || wire.w == nil {
		return nil, errors.New("zmodem: transfer wire is not configured")
	}
	if options.Directory == "" {
		return nil, errors.New("zmodem: destination directory is required")
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = defaultMaxReceiveBytes
	}
	directory, err := filepath.Abs(options.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve destination directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect destination directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("zmodem: destination path is not a directory")
	}

	var completed []ReceivedFile
	for {
		if err := ctx.Err(); err != nil {
			_ = writeCancel(wire)
			return completed, ErrCancelled
		}
		frame, err := wire.ReadHeader()
		if err != nil {
			if ctx.Err() != nil {
				_ = writeCancel(wire)
				return completed, ErrCancelled
			}
			return completed, fmt.Errorf("read ZMODEM header: %w", err)
		}
		switch frame.Type {
		case FrameZRQINIT:
			if err := writeReceiveReady(wire); err != nil {
				return completed, err
			}
		case FrameZFILE:
			metaPacket, _, err := wire.ReadSubpacket()
			if err != nil {
				return completed, fmt.Errorf("read ZFILE metadata: %w", err)
			}
			meta, err := parseFileMetadata(metaPacket)
			if err != nil {
				_ = wire.WriteHeader(Frame{Type: FrameZSKIP}, true)
				continue
			}
			if meta.size > options.MaxFileBytes || meta.size > maxProtocolOffset {
				_ = wire.WriteHeader(Frame{Type: FrameZSKIP}, true)
				return completed, fmt.Errorf("%w: %q (%d bytes)", ErrFileTooBig, meta.name, meta.size)
			}
			if err := isSafeFileName(meta.name); err != nil {
				_ = wire.WriteHeader(Frame{Type: FrameZSKIP}, true)
				continue
			}
			target := filepath.Join(directory, meta.name)
			if !options.Overwrite {
				if _, err := os.Lstat(target); err == nil {
					_ = wire.WriteHeader(Frame{Type: FrameZSKIP}, true)
					continue
				} else if !errors.Is(err, os.ErrNotExist) {
					return completed, fmt.Errorf("inspect destination file %q: %w", meta.name, err)
				}
			}
			received, err := receiveFile(ctx, wire, target, meta, options, progress)
			if err != nil {
				_ = writeCancel(wire)
				return completed, err
			}
			if received != nil {
				completed = append(completed, *received)
			}
			if err := writeReceiveReady(wire); err != nil {
				return completed, err
			}
		case FrameZFIN:
			if err := wire.WriteHeader(Frame{Type: FrameZFIN}, true); err != nil {
				return completed, err
			}
			var finish [2]byte
			if _, err := io.ReadFull(wire.r, finish[:]); err != nil {
				return completed, fmt.Errorf("read ZMODEM finish marker: %w", err)
			}
			if finish != [2]byte{'O', 'O'} {
				return completed, fmt.Errorf("zmodem: invalid finish marker %q", finish[:])
			}
			return completed, nil
		case FrameZABORT, FrameZCAN:
			return completed, ErrCancelled
		default:
			if err := wire.WriteHeader(Frame{Type: FrameZNAK}, true); err != nil {
				return completed, err
			}
		}
	}
}

func receiveFile(ctx context.Context, wire *Wire, target string, meta fileMetadata, options ReceiveOptions, progress func(Progress)) (*ReceivedFile, error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".zmodem-*.part")
	if err != nil {
		return nil, fmt.Errorf("create temporary download: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := wire.WriteHeader(Frame{Type: FrameZRPOS}, true); err != nil {
		return nil, err
	}
	var transferred int64
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, ErrCancelled
		}
		frame, err := wire.ReadHeader()
		if err != nil {
			return nil, fmt.Errorf("read ZDATA header for %q: %w", meta.name, err)
		}
		if frame.Type == FrameZABORT || frame.Type == FrameZCAN {
			return nil, ErrCancelled
		}
		if frame.Type == FrameZEOF {
			if int64(frame.Position) != transferred || transferred != meta.size {
				retries++
				if retries > maxTransferRetries {
					return nil, fmt.Errorf("zmodem: exceeded retry limit while receiving %q", meta.name)
				}
				if err := wire.WriteHeader(Frame{Type: FrameZRPOS, Position: uint32(transferred)}, true); err != nil {
					return nil, err
				}
				continue
			}
			if err := finishReceivedFile(tmp, tmpPath, target, meta, options); err != nil {
				return nil, err
			}
			return &ReceivedFile{Name: meta.name, Path: target, Size: transferred}, nil
		}
		if frame.Type != FrameZDATA || int64(frame.Position) != transferred {
			retries++
			if retries > maxTransferRetries {
				return nil, fmt.Errorf("zmodem: exceeded retry limit while receiving %q", meta.name)
			}
			if err := wire.WriteHeader(Frame{Type: FrameZRPOS, Position: uint32(transferred)}, true); err != nil {
				return nil, err
			}
			continue
		}

		for {
			if err := ctx.Err(); err != nil {
				return nil, ErrCancelled
			}
			data, marker, err := wire.ReadSubpacket()
			if err != nil {
				if errors.Is(err, ErrInvalidCRC) {
					retries++
					if retries > maxTransferRetries {
						return nil, fmt.Errorf("zmodem: exceeded retry limit while receiving %q", meta.name)
					}
					if writeErr := wire.WriteHeader(Frame{Type: FrameZRPOS, Position: uint32(transferred)}, true); writeErr != nil {
						return nil, writeErr
					}
					break
				}
				return nil, fmt.Errorf("read data for %q: %w", meta.name, err)
			}
			if transferred+int64(len(data)) > meta.size || transferred+int64(len(data)) > options.MaxFileBytes {
				return nil, fmt.Errorf("zmodem: received more data than declared for %q", meta.name)
			}
			if err := writeFull(tmp, data); err != nil {
				return nil, fmt.Errorf("write temporary download: %w", err)
			}
			transferred += int64(len(data))
			retries = 0
			if progress != nil {
				progress(Progress{Name: meta.name, Bytes: transferred, Total: meta.size, Direction: "download"})
			}
			switch marker {
			case ZCRCG:
				continue
			case ZCRCQ:
				if err := wire.WriteHeader(Frame{Type: FrameZACK, Position: uint32(transferred)}, true); err != nil {
					return nil, err
				}
				continue
			case ZCRCW:
				if err := wire.WriteHeader(Frame{Type: FrameZACK, Position: uint32(transferred)}, true); err != nil {
					return nil, err
				}
				// ZCRCW ends this ZDATA frame. The sender waits for our ACK and
				// opens a new ZDATA frame for the remaining bytes.
				break
			case ZCRCE:
				break
			}
			break
		}
	}
}

func finishReceivedFile(tmp *os.File, tmpPath, target string, meta fileMetadata, options ReceiveOptions) error {
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary download: %w", err)
	}
	if options.PreserveMode && meta.mode != 0 {
		if err := tmp.Chmod(meta.mode.Perm()); err != nil {
			return fmt.Errorf("set downloaded file permissions: %w", err)
		}
	}
	if options.PreserveMtime && !meta.mtime.IsZero() {
		if err := os.Chtimes(tmpPath, meta.mtime, meta.mtime); err != nil {
			return fmt.Errorf("set downloaded file time: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary download: %w", err)
	}
	if options.Overwrite {
		if err := os.Rename(tmpPath, target); err != nil {
			return fmt.Errorf("commit downloaded file: %w", err)
		}
		return nil
	}
	// Linking within the destination directory makes the no-overwrite decision
	// atomic: a concurrent file creation cannot be silently replaced.
	if err := os.Link(tmpPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("zmodem: destination appeared during transfer: %w", err)
		}
		// Some filesystems and Windows configurations do not support hard links.
		// Fall back to an exclusive create so an existing destination is still
		// never overwritten.
		source, openErr := os.Open(tmpPath)
		if openErr != nil {
			return fmt.Errorf("open temporary download for fallback commit: %w", openErr)
		}
		destination, createErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			_ = source.Close()
			return fmt.Errorf("commit downloaded file without overwrite: %w (hard link: %v)", createErr, err)
		}
		_, copyErr := io.Copy(destination, source)
		if copyErr == nil {
			copyErr = destination.Sync()
		}
		closeDestinationErr := destination.Close()
		closeSourceErr := source.Close()
		if copyErr != nil || closeDestinationErr != nil || closeSourceErr != nil {
			_ = os.Remove(target)
			return errors.Join(copyErr, closeDestinationErr, closeSourceErr)
		}
		if options.PreserveMode && meta.mode != 0 {
			if err := os.Chmod(target, meta.mode.Perm()); err != nil {
				_ = os.Remove(target)
				return fmt.Errorf("set downloaded file permissions: %w", err)
			}
		}
		if options.PreserveMtime && !meta.mtime.IsZero() {
			if err := os.Chtimes(target, meta.mtime, meta.mtime); err != nil {
				_ = os.Remove(target)
				return fmt.Errorf("set downloaded file time: %w", err)
			}
		}
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("remove temporary download after commit: %w", err)
	}
	return nil
}

// Send uploads local files to a peer that has already sent ZRINIT. The input
// reader must include that initial ZRINIT frame, and the caller should open the
// remote receiver (normally `rz`) before starting the transfer.
func Send(ctx context.Context, wire *Wire, paths []string, progress func(Progress)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if wire == nil || wire.r == nil || wire.w == nil {
		return errors.New("zmodem: transfer wire is not configured")
	}
	if len(paths) == 0 {
		return errors.New("zmodem: at least one file is required")
	}
	frame, err := wire.ReadHeader()
	if err != nil {
		return fmt.Errorf("wait for remote ZRINIT: %w", err)
	}
	if frame.Type != FrameZRINIT {
		return fmt.Errorf("zmodem: expected ZRINIT, received frame %#x", frame.Type)
	}
	wire.UseCRC32 = frame.Data[3]&canFC32 != 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			_ = writeCancel(wire)
			return ErrCancelled
		}
		if err := sendFile(ctx, wire, path, progress); err != nil {
			_ = writeCancel(wire)
			return err
		}
	}
	if err := wire.WriteHeader(Frame{Type: FrameZFIN}, true); err != nil {
		return err
	}
	frame, err = wire.ReadHeader()
	if err != nil {
		return fmt.Errorf("wait for remote ZFIN: %w", err)
	}
	if frame.Type != FrameZFIN {
		return fmt.Errorf("zmodem: expected ZFIN, received frame %#x", frame.Type)
	}
	return writeFull(wire.w, []byte("OO"))
}

func sendFile(ctx context.Context, wire *Wire, path string, progress func(Progress)) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open upload file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect upload file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("zmodem: only regular files can be sent")
	}
	if info.Size() > maxProtocolOffset {
		return fmt.Errorf("%w: %q exceeds the 32-bit ZMODEM position range", ErrFileTooBig, path)
	}
	name := filepath.Base(path)
	metadata := fmt.Sprintf("%s\x00%d %o %o\x00", name, info.Size(), info.ModTime().Unix(), info.Mode().Perm())
	if err := wire.WriteHeader(Frame{Type: FrameZFILE}, false); err != nil {
		return err
	}
	if err := wire.WriteSubpacket([]byte(metadata), ZCRCW); err != nil {
		return err
	}
	frame, err := readHeaderSkippingRepeatedZRINIT(wire)
	if err != nil {
		return fmt.Errorf("wait for receiver position: %w", err)
	}
	if frame.Type == FrameZSKIP {
		return nil
	}
	if frame.Type != FrameZRPOS || uint64(frame.Position) > uint64(info.Size()) {
		return fmt.Errorf("zmodem: unexpected receiver response %#x at %d", frame.Type, frame.Position)
	}
	position := int64(frame.Position)
	if _, err := file.Seek(position, io.SeekStart); err != nil {
		return fmt.Errorf("seek upload file: %w", err)
	}
	for retries := 0; retries < maxTransferRetries; retries++ {
		if err := ctx.Err(); err != nil {
			return ErrCancelled
		}
		if err := wire.WriteHeader(Frame{Type: FrameZDATA, Position: uint32(position)}, false); err != nil {
			return err
		}
		buf := make([]byte, transferChunkSize)
		if info.Size() == 0 && position == 0 {
			if err := wire.WriteSubpacket(nil, ZCRCE); err != nil {
				return err
			}
		}
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				position += int64(n)
				marker := ZCRCW
				if position == info.Size() {
					marker = ZCRCE
				}
				if err := wire.WriteSubpacket(buf[:n], marker); err != nil {
					return err
				}
				if progress != nil {
					progress(Progress{Name: name, Bytes: position, Total: info.Size(), Direction: "upload"})
				}
				if marker == ZCRCW {
					response, err := readHeaderSkippingRepeatedZRINIT(wire)
					if err != nil {
						return fmt.Errorf("wait for receiver acknowledgment: %w", err)
					}
					if response.Type == FrameZRPOS {
						if uint64(response.Position) > uint64(info.Size()) {
							return fmt.Errorf("zmodem: receiver requested invalid position %d", response.Position)
						}
						position = int64(response.Position)
						if _, err := file.Seek(position, io.SeekStart); err != nil {
							return err
						}
						break
					}
					if response.Type != FrameZACK || int64(response.Position) < position {
						return fmt.Errorf("zmodem: unexpected receiver acknowledgment %#x at %d", response.Type, response.Position)
					}
					if position < info.Size() {
						if err := wire.WriteHeader(Frame{Type: FrameZDATA, Position: uint32(position)}, false); err != nil {
							return err
						}
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return fmt.Errorf("read upload file: %w", readErr)
			}
		}
		if position != info.Size() {
			continue
		}
		if err := wire.WriteHeader(Frame{Type: FrameZEOF, Position: uint32(info.Size())}, false); err != nil {
			return err
		}
		response, err := wire.ReadHeader()
		if err != nil {
			return fmt.Errorf("wait for next receiver state: %w", err)
		}
		if response.Type == FrameZRINIT {
			return nil
		}
		if response.Type == FrameZRPOS && uint64(response.Position) <= uint64(info.Size()) {
			position = int64(response.Position)
			if _, err := file.Seek(position, io.SeekStart); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("zmodem: unexpected post-file response %#x", response.Type)
	}
	return fmt.Errorf("zmodem: exceeded retry limit while sending %q at byte %d of %d", name, position, info.Size())
}

func readHeaderSkippingRepeatedZRINIT(wire *Wire) (Frame, error) {
	for {
		frame, err := wire.ReadHeader()
		if err != nil {
			return Frame{}, err
		}
		if frame.Type != FrameZRINIT {
			return frame, nil
		}
	}
}

func writeReceiveReady(wire *Wire) error {
	// CANFC32 lets modern senders select 32-bit CRC for binary headers and
	// data subpackets. Hex control headers remain protected with CRC16.
	return wire.WriteHeader(Frame{Type: FrameZRINIT, Data: [4]byte{0, 0, 0, defaultReceiveFlags}, UseData: true}, true)
}

func writeCancel(wire *Wire) error {
	if wire == nil || wire.w == nil {
		return nil
	}
	return writeFull(wire.w, []byte{ZDLE, ZDLE, ZDLE, ZDLE, ZDLE, ZDLE, ZDLE, ZDLE})
}

func parseFileMetadata(data []byte) (fileMetadata, error) {
	firstNUL := strings.IndexByte(string(data), 0)
	if firstNUL <= 0 {
		return fileMetadata{}, ErrBadMetadata
	}
	name := strings.ReplaceAll(string(data[:firstNUL]), "\\", "/")
	name = filepath.Base(name)
	if err := isSafeFileName(name); err != nil {
		return fileMetadata{}, err
	}
	remaining := data[firstNUL+1:]
	if secondNUL := strings.IndexByte(string(remaining), 0); secondNUL >= 0 {
		remaining = remaining[:secondNUL]
	}
	fields := strings.Fields(string(remaining))
	if len(fields) == 0 {
		return fileMetadata{}, ErrBadMetadata
	}
	size, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || size < 0 {
		return fileMetadata{}, ErrBadMetadata
	}
	metadata := fileMetadata{name: name, size: size}
	if len(fields) > 1 {
		if seconds, err := strconv.ParseInt(fields[1], 8, 64); err == nil && seconds > 0 {
			metadata.mtime = time.Unix(seconds, 0)
		}
	}
	if len(fields) > 2 {
		if mode, err := strconv.ParseUint(fields[2], 8, 32); err == nil {
			metadata.mode = os.FileMode(mode & 0o777)
		}
	}
	return metadata, nil
}

func isSafeFileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return ErrUnsafeName
	}
	if filepath.IsAbs(name) || filepath.Base(name) != name {
		return ErrUnsafeName
	}
	return nil
}
