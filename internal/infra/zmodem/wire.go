// Package zmodem implements the wire framing used by the ZMODEM file-transfer
// protocol. The transfer state machine is intentionally kept separate from the
// terminal UI so it can be verified against byte streams without a live SSH
// session.
package zmodem

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
)

const (
	ZDLE         byte = 0x18
	ZPAD         byte = '*'
	ZBIN         byte = 'A'
	ZHEX         byte = 'B'
	ZBIN32       byte = 'C'
	XON          byte = 0x11
	XOFF         byte = 0x13
	ZCRCE        byte = 'h'
	ZCRCG        byte = 'i'
	ZCRCQ        byte = 'j'
	ZCRCW        byte = 'k'
	ZDLEE        byte = 'X'
	ZRUB0        byte = 'l'
	ZRUB1        byte = 'm'
	maxFrameData      = 1 << 20
)

const (
	FrameZRQINIT byte = iota
	FrameZRINIT
	FrameZSINIT
	FrameZACK
	FrameZFILE
	FrameZSKIP
	FrameZNAK
	FrameZABORT
	FrameZFIN
	FrameZRPOS
	FrameZDATA
	FrameZEOF
	FrameZFERR
	FrameZCRC
	FrameZCHALLENGE
)

const (
	FrameZCOMPL   byte = 16
	FrameZCAN     byte = 17
	FrameZFREECNT byte = 18
	FrameZCOMMAND byte = 19
	FrameZSTDERR  byte = 20
)

var (
	ErrInvalidFrame   = errors.New("zmodem: invalid frame")
	ErrInvalidCRC     = errors.New("zmodem: CRC mismatch")
	ErrInvalidSubpack = errors.New("zmodem: invalid data subpacket")
	ErrSubpacketLarge = errors.New("zmodem: data subpacket exceeds limit")
)

// Frame is a decoded ZMODEM header. Position carries the header's four-byte
// position field; the same bytes are flags for frame types that define flags.
type Frame struct {
	Type     byte
	Position uint32
	Data     [4]byte
	UseData  bool
	Format   byte
}

// Wire owns a buffered reader so it never drops bytes read beyond a frame.
// Create one Wire for a transfer and use it for the whole session.
type Wire struct {
	r             *bufio.Reader
	w             io.Writer
	EscapeControl bool
	UseCRC32      bool
	readCRC32     bool
}

func NewWire(r io.Reader, w io.Writer) *Wire {
	return &Wire{r: bufio.NewReader(r), w: w}
}

// DrainBuffered returns bytes read ahead by the wire parser but not consumed by
// the protocol. A terminal owner can render these after the transfer ends.
func (wire *Wire) DrainBuffered() []byte {
	if wire == nil || wire.r == nil {
		return nil
	}
	count := wire.r.Buffered()
	if count == 0 {
		return nil
	}
	buffered, err := wire.r.Peek(count)
	if err != nil {
		return nil
	}
	result := append([]byte(nil), buffered...)
	_, _ = wire.r.Discard(count)
	return result
}

// CRC16 returns the CRC-16/XMODEM value used by 16-bit ZMODEM frames.
func CRC16(data []byte) uint16 {
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

// WriteHeader writes a header using hexadecimal encoding when hexHeader is
// true, otherwise using the selected binary CRC format. ZRQINIT and ZRINIT
// should use hex headers for compatibility with non-8-bit-clean links.
func (wire *Wire) WriteHeader(frame Frame, hexHeader bool) error {
	if wire == nil || wire.w == nil {
		return errors.New("zmodem: writer is not configured")
	}
	var payload [5]byte
	payload[0] = frame.Type
	if frame.UseData {
		copy(payload[1:], frame.Data[:])
	} else {
		payload[1] = byte(frame.Position)
		payload[2] = byte(frame.Position >> 8)
		payload[3] = byte(frame.Position >> 16)
		payload[4] = byte(frame.Position >> 24)
	}
	crc := CRC16(payload[:])

	if hexHeader {
		encoded := make([]byte, 0, 21)
		encoded = append(encoded, ZPAD, ZPAD, ZDLE, ZHEX)
		var pair [2]byte
		for _, b := range payload {
			hex.Encode(pair[:], []byte{b})
			encoded = append(encoded, pair[:]...)
		}
		hex.Encode(pair[:], []byte{byte(crc >> 8)})
		encoded = append(encoded, pair[:]...)
		hex.Encode(pair[:], []byte{byte(crc)})
		encoded = append(encoded, pair[:]...)
		encoded = append(encoded, '\r', 0x8a)
		if frame.Type != FrameZACK && frame.Type != FrameZFIN {
			encoded = append(encoded, XON)
		}
		return writeFull(wire.w, encoded)
	}

	if wire.UseCRC32 {
		encoded := []byte{ZPAD, ZDLE, ZBIN32}
		for _, b := range payload {
			encoded = appendEscaped(encoded, b, wire.EscapeControl)
		}
		var checksum [4]byte
		binary.LittleEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(payload[:]))
		for _, b := range checksum {
			encoded = appendEscaped(encoded, b, wire.EscapeControl)
		}
		return writeFull(wire.w, encoded)
	}

	encoded := []byte{ZPAD, ZDLE, ZBIN}
	for _, b := range payload {
		encoded = appendEscaped(encoded, b, wire.EscapeControl)
	}
	encoded = appendEscaped(encoded, byte(crc>>8), wire.EscapeControl)
	encoded = appendEscaped(encoded, byte(crc), wire.EscapeControl)
	return writeFull(wire.w, encoded)
}

// ReadHeader scans terminal noise until it finds a valid hexadecimal, 16-bit
// binary, or 32-bit binary header. Invalid candidates are ignored, as required
// for noisy terminal streams.
func (wire *Wire) ReadHeader() (Frame, error) {
	if wire == nil || wire.r == nil {
		return Frame{}, errors.New("zmodem: reader is not configured")
	}
	for {
		first, err := wire.r.ReadByte()
		if err != nil {
			return Frame{}, err
		}
		if first&0x7f != ZPAD {
			continue
		}
		second, err := wire.r.ReadByte()
		if err != nil {
			return Frame{}, err
		}
		format := byte(0)
		if second&0x7f == ZDLE {
			format, err = wire.r.ReadByte()
			if err != nil {
				return Frame{}, err
			}
		} else if second&0x7f == ZPAD {
			third, readErr := wire.r.ReadByte()
			if readErr != nil {
				return Frame{}, readErr
			}
			if third&0x7f != ZDLE {
				continue
			}
			format, err = wire.r.ReadByte()
			if err != nil {
				return Frame{}, err
			}
		} else {
			continue
		}
		switch format & 0x7f {
		case ZHEX:
			frame, err := wire.readHexHeader()
			if err == nil {
				wire.readCRC32 = false
				return frame, nil
			}
		case ZBIN:
			frame, err := wire.readBinaryHeader()
			if err == nil {
				wire.readCRC32 = false
				return frame, nil
			}
		case ZBIN32:
			frame, err := wire.readBinaryHeader32()
			if err == nil {
				wire.readCRC32 = true
				return frame, nil
			}
		}
	}
}

func (wire *Wire) readHexHeader() (Frame, error) {
	var encoded [14]byte
	if _, err := io.ReadFull(wire.r, encoded[:]); err != nil {
		return Frame{}, err
	}
	var payload [5]byte
	for i := range payload {
		encoded[i*2] &= 0x7f
		encoded[i*2+1] &= 0x7f
		decoded, err := hex.DecodeString(string(encoded[i*2 : i*2+2]))
		if err != nil {
			return Frame{}, ErrInvalidFrame
		}
		payload[i] = decoded[0]
	}
	for i := 10; i < len(encoded); i++ {
		encoded[i] &= 0x7f
	}
	decodedCRC, err := hex.DecodeString(string(encoded[10:14]))
	if err != nil || uint16(decodedCRC[0])<<8|uint16(decodedCRC[1]) != CRC16(payload[:]) {
		return Frame{}, ErrInvalidCRC
	}
	// Consume the CR/LF trailer without peeking for optional XON: a peek on a
	// live stream can block forever after ZFIN/ZACK, where XON is omitted.
	trailer, trailerErr := wire.r.ReadByte()
	if trailerErr == nil && trailer == '\r' {
		if next, err := wire.r.ReadByte(); err == nil && next&0x7f != '\n' {
			_ = wire.r.UnreadByte()
		}
	} else if trailerErr == nil && trailer&0x7f != '\n' {
		_ = wire.r.UnreadByte()
	}
	return frameFromPayload(payload[:], ZHEX), nil
}

func (wire *Wire) readBinaryHeader() (Frame, error) {
	var payload [7]byte
	for i := 0; i < 7; i++ {
		value, err := wire.readEscapedByte()
		if err != nil {
			return Frame{}, err
		}
		payload[i] = value
	}
	if CRC16(payload[:5]) != uint16(payload[5])<<8|uint16(payload[6]) {
		return Frame{}, ErrInvalidCRC
	}
	return frameFromPayload(payload[:5], ZBIN), nil
}

func (wire *Wire) readBinaryHeader32() (Frame, error) {
	var payload [9]byte
	for i := range payload {
		value, err := wire.readEscapedByte()
		if err != nil {
			return Frame{}, err
		}
		payload[i] = value
	}
	if crc32.ChecksumIEEE(payload[:5]) != binary.LittleEndian.Uint32(payload[5:]) {
		return Frame{}, ErrInvalidCRC
	}
	return frameFromPayload(payload[:5], ZBIN32), nil
}

func frameFromPayload(payload []byte, format byte) Frame {
	frame := Frame{
		Type: payload[0],
		Position: uint32(payload[1]) |
			uint32(payload[2])<<8 |
			uint32(payload[3])<<16 |
			uint32(payload[4])<<24,
		Format: format,
	}
	copy(frame.Data[:], payload[1:5])
	return frame
}

// WriteSubpacket writes data followed by a ZMODEM end marker and the selected
// CRC16 or CRC32 checksum.
// endMarker must be one of ZCRCE, ZCRCG, ZCRCQ, or ZCRCW.
func (wire *Wire) WriteSubpacket(data []byte, endMarker byte) error {
	if wire == nil || wire.w == nil {
		return errors.New("zmodem: writer is not configured")
	}
	if len(data) > maxFrameData {
		return ErrSubpacketLarge
	}
	if !validEndMarker(endMarker) {
		return ErrInvalidSubpack
	}
	encoded := make([]byte, 0, len(data)*2+8)
	crcData := make([]byte, 0, len(data)+1)
	for _, b := range data {
		encoded = appendEscaped(encoded, b, wire.EscapeControl)
		crcData = append(crcData, b)
	}
	encoded = append(encoded, ZDLE, endMarker)
	crcData = append(crcData, endMarker)
	if wire.UseCRC32 {
		var checksum [4]byte
		binary.LittleEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(crcData))
		for _, b := range checksum {
			encoded = appendEscaped(encoded, b, wire.EscapeControl)
		}
	} else {
		crc := CRC16(crcData)
		encoded = appendEscaped(encoded, byte(crc>>8), wire.EscapeControl)
		encoded = appendEscaped(encoded, byte(crc), wire.EscapeControl)
	}
	return writeFull(wire.w, encoded)
}

// ReadSubpacket reads one escaped data subpacket and verifies its CRC.
func (wire *Wire) ReadSubpacket() ([]byte, byte, error) {
	if wire == nil || wire.r == nil {
		return nil, 0, errors.New("zmodem: reader is not configured")
	}
	data := make([]byte, 0, 1024)
	for {
		value, err := wire.r.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		if value == XON || value == XOFF || value == 0x91 || value == 0x93 {
			continue
		}
		if value != ZDLE {
			data = append(data, value)
		} else {
			value, err = wire.r.ReadByte()
			if err != nil {
				return nil, 0, err
			}
			if validEndMarker(value) {
				crcData := append(append([]byte(nil), data...), value)
				if wire.readCRC32 {
					var encodedCRC [4]byte
					for i := range encodedCRC {
						encodedCRC[i], err = wire.readEscapedByte()
						if err != nil {
							return nil, 0, err
						}
					}
					if crc32.ChecksumIEEE(crcData) != binary.LittleEndian.Uint32(encodedCRC[:]) {
						return nil, 0, ErrInvalidCRC
					}
				} else {
					crcHigh, err := wire.readEscapedByte()
					if err != nil {
						return nil, 0, err
					}
					crcLow, err := wire.readEscapedByte()
					if err != nil {
						return nil, 0, err
					}
					if CRC16(crcData) != uint16(crcHigh)<<8|uint16(crcLow) {
						return nil, 0, ErrInvalidCRC
					}
				}
				return data, value, nil
			}
			if value == ZRUB0 {
				value = 0x7f
			} else if value == ZRUB1 {
				value = 0xff
			} else {
				value ^= 0x40
			}
			data = append(data, value)
		}
		if len(data) > maxFrameData {
			return nil, 0, ErrSubpacketLarge
		}
	}
}

func (wire *Wire) readEscapedByte() (byte, error) {
	for {
		value, err := wire.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if value == XON || value == XOFF || value == 0x91 || value == 0x93 {
			continue
		}
		if value != ZDLE {
			return value, nil
		}
		value, err = wire.r.ReadByte()
		if err != nil {
			return 0, err
		}
		if value == ZRUB0 {
			return 0x7f, nil
		}
		if value == ZRUB1 {
			return 0xff, nil
		}
		return value ^ 0x40, nil
	}
}

func appendEscaped(dst []byte, value byte, escapeControl bool) []byte {
	if value == 0x7f {
		return append(dst, ZDLE, ZRUB0)
	}
	if value == 0xff {
		return append(dst, ZDLE, ZRUB1)
	}
	shouldEscape := value == ZDLE || value == 0x10 || value == 0x90 || value == XON || value == 0x91 || value == XOFF || value == 0x93 || value == '\r'
	if escapeControl && ((value&0x60) == 0 || value == 0x7f || value == 0xff) {
		shouldEscape = true
	}
	if shouldEscape {
		return append(dst, ZDLE, value^0x40)
	}
	return append(dst, value)
}

func validEndMarker(marker byte) bool {
	return marker == ZCRCE || marker == ZCRCG || marker == ZCRCQ || marker == ZCRCW
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
