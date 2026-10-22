package zmodem

import (
	"bytes"
	"errors"
	"testing"
)

func TestCRC16XMODEM(t *testing.T) {
	if got, want := CRC16([]byte("123456789")), uint16(0x31c3); got != want {
		t.Fatalf("CRC16(123456789) = %#04x, want %#04x", got, want)
	}
}

func TestHeaderRoundTripHexAndBinary(t *testing.T) {
	for _, test := range []struct {
		name string
		form bool
		want byte
	}{
		{name: "hex", form: true, want: ZHEX},
		{name: "binary", form: false, want: ZBIN},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wireBytes bytes.Buffer
			writer := NewWire(nil, &wireBytes)
			writer.EscapeControl = true
			want := Frame{Type: FrameZRPOS, Position: 0x81401318}
			if err := writer.WriteHeader(want, test.form); err != nil {
				t.Fatal(err)
			}
			encoded := wireBytes.Bytes()
			formatIndex := 2
			if test.form {
				formatIndex = 3
			}
			if got := encoded[formatIndex]; got != test.want {
				t.Fatalf("header format byte = %#x, want %#x", got, test.want)
			}
			reader := NewWire(bytes.NewReader(encoded), nil)
			got, err := reader.ReadHeader()
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != want.Type || got.Position != want.Position || got.Format != test.want {
				t.Fatalf("decoded header = %#v, want type %#x position %#x format %#x", got, want.Type, want.Position, test.want)
			}
		})
	}
}

func TestReadHexHeaderIgnoresParityBit(t *testing.T) {
	var encoded bytes.Buffer
	if err := NewWire(nil, &encoded).WriteHeader(Frame{Type: FrameZRQINIT}, true); err != nil {
		t.Fatal(err)
	}
	withParity := append([]byte(nil), encoded.Bytes()...)
	for i := 0; i < len(withParity)-3; i++ {
		withParity[i] |= 0x80
	}
	reader := NewWire(bytes.NewReader(withParity), nil)
	got, err := reader.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != FrameZRQINIT || got.Position != 0 {
		t.Fatalf("decoded header = %#v, want ZRQINIT at position 0", got)
	}
}

func TestHexHeaderOmitsXONForZFINAndZACK(t *testing.T) {
	for _, frameType := range []byte{FrameZACK, FrameZFIN} {
		var wireBytes bytes.Buffer
		writer := NewWire(nil, &wireBytes)
		if err := writer.WriteHeader(Frame{Type: frameType}, true); err != nil {
			t.Fatal(err)
		}
		if got := wireBytes.Bytes()[wireBytes.Len()-1]; got == XON {
			t.Fatalf("frame %#x unexpectedly ended with XON", frameType)
		}
	}
}

func TestSubpacketRoundTripEscapesControlBytes(t *testing.T) {
	want := []byte{0x00, 0x10, 0x11, 0x13, 0x18, 0x40, '\r', 0x7f, 0xff, 0x91, 0x93}
	var wireBytes bytes.Buffer
	writer := NewWire(nil, &wireBytes)
	writer.EscapeControl = true
	if err := writer.WriteSubpacket(want, ZCRCW); err != nil {
		t.Fatal(err)
	}
	reader := NewWire(bytes.NewReader(wireBytes.Bytes()), nil)
	got, marker, err := reader.ReadSubpacket()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded data = %x, want %x", got, want)
	}
	if marker != ZCRCW {
		t.Fatalf("end marker = %#x, want %#x", marker, ZCRCW)
	}
}

func TestReadSubpacketRejectsBadCRC(t *testing.T) {
	var encoded bytes.Buffer
	writer := NewWire(nil, &encoded)
	if err := writer.WriteSubpacket([]byte("payload"), ZCRCE); err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), encoded.Bytes()...)
	corrupted[len(corrupted)-1] ^= 1
	reader := NewWire(bytes.NewReader(corrupted), nil)
	if _, _, err := reader.ReadSubpacket(); !errors.Is(err, ErrInvalidCRC) {
		t.Fatalf("ReadSubpacket error = %v, want ErrInvalidCRC", err)
	}
}

func TestReadHeaderSkipsTerminalNoise(t *testing.T) {
	var encoded bytes.Buffer
	writer := NewWire(nil, &encoded)
	want := Frame{Type: FrameZDATA, Position: 0x1234}
	if err := writer.WriteHeader(want, false); err != nil {
		t.Fatal(err)
	}
	stream := append([]byte("shell output\r\n"), encoded.Bytes()...)
	reader := NewWire(bytes.NewReader(stream), nil)
	got, err := reader.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.Position != want.Position {
		t.Fatalf("decoded header = %#v, want %#v", got, want)
	}
}
