package sshclient

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
)

func TestParseX11Display(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		wantNetwork string
		wantAddress string
		wantScreen  uint32
		wantErr     bool
	}{
		{name: "local unix display", value: ":0", wantNetwork: "unix", wantAddress: "/tmp/.X11-unix/X0"},
		{name: "numbered screen", value: "unix:3.2", wantNetwork: "unix", wantAddress: "/tmp/.X11-unix/X3", wantScreen: 2},
		{name: "local tcp display", value: "localhost:10.1", wantNetwork: "tcp", wantAddress: "127.0.0.1:6010", wantScreen: 1},
		{name: "unix socket path", value: "/private/tmp/xquartz:0", wantNetwork: "unix", wantAddress: "/private/tmp/xquartz"},
		{name: "remote display rejected", value: "192.0.2.10:0", wantErr: true},
		{name: "malformed display rejected", value: ":0.bad", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseX11Display(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseX11Display(%q) error = %v", test.value, err)
			}
			if err == nil && (got.network != test.wantNetwork || got.address != test.wantAddress || got.screen != test.wantScreen) {
				t.Fatalf("display = %#v, want network=%q address=%q screen=%d", got, test.wantNetwork, test.wantAddress, test.wantScreen)
			}
		})
	}
}

func TestParseX11AuthCookie(t *testing.T) {
	cookie := "00112233445566778899aabbccddeeff"
	got, err := parseX11AuthCookie("localhost/unix:0 MIT-MAGIC-COOKIE-1 " + cookie + "\n")
	if err != nil || hex.EncodeToString(got) != cookie {
		t.Fatalf("parseX11AuthCookie = %x, %v", got, err)
	}
	if _, err := parseX11AuthCookie("localhost/unix:0 XDM-AUTHORIZATION-1 deadbeef\n"); err == nil {
		t.Fatal("unsupported X11 authorization protocol was accepted")
	}
	if _, err := parseX11AuthCookie("localhost/unix:0 MIT-MAGIC-COOKIE-1 xyz\n"); err == nil {
		t.Fatal("malformed X11 cookie was accepted")
	}
}

func TestRewriteX11SetupReplacesFakeCookie(t *testing.T) {
	fakeCookie := []byte("fake-cookie-1234")
	realCookie := []byte("a longer real cookie value")
	for name, order := range map[string]binary.ByteOrder{"little endian": binary.LittleEndian, "big endian": binary.BigEndian} {
		t.Run(name, func(t *testing.T) {
			input := makeX11Setup(order, x11AuthProtocol, fakeCookie, []byte("client-setup"))
			var output bytes.Buffer
			reader := bytes.NewReader(input)
			if err := rewriteX11Setup(reader, &output, fakeCookie, realCookie); err != nil {
				t.Fatalf("rewrite X11 setup: %v", err)
			}
			if _, err := io.Copy(&output, reader); err != nil {
				t.Fatalf("copy X11 setup tail: %v", err)
			}
			got := output.Bytes()
			if order.Uint16(got[8:10]) != uint16(len(realCookie)) {
				t.Fatalf("rewritten cookie length = %d", order.Uint16(got[8:10]))
			}
			namePadded := paddedX11Length(len(x11AuthProtocol))
			dataStart := 12 + namePadded
			if !bytes.Equal(got[dataStart:dataStart+len(realCookie)], realCookie) {
				t.Fatalf("rewritten cookie = %q, want %q", got[dataStart:dataStart+len(realCookie)], realCookie)
			}
			if !bytes.Equal(got[dataStart+paddedX11Length(len(realCookie)):], []byte("client-setup")) {
				t.Fatalf("X11 setup tail was not preserved: %q", got[dataStart+paddedX11Length(len(realCookie)):])
			}
		})
	}
}

func TestRewriteX11SetupRejectsWrongCookieAndProtocol(t *testing.T) {
	fakeCookie := []byte("fake-cookie-1234")
	for name, input := range map[string][]byte{
		"wrong cookie":       makeX11Setup(binary.LittleEndian, x11AuthProtocol, []byte("some-other-cookie"), nil),
		"wrong protocol":     makeX11Setup(binary.LittleEndian, "XDM-AUTHORIZATION-1", fakeCookie, nil),
		"invalid byte order": append([]byte{'?'}, make([]byte, 11)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := rewriteX11Setup(bytes.NewReader(input), io.Discard, fakeCookie, []byte("real-cookie-1234")); err == nil {
				t.Fatal("invalid X11 setup was accepted")
			}
		})
	}
}

func TestWriteX11BytesHandlesShortWrites(t *testing.T) {
	var output bytes.Buffer
	writer := shortX11Writer{writer: &output, max: 2}
	if err := writeX11Bytes(writer, []byte("complete")); err != nil {
		t.Fatalf("writeX11Bytes: %v", err)
	}
	if got := output.String(); got != "complete" {
		t.Fatalf("written bytes = %q, want complete", got)
	}
}

type shortX11Writer struct {
	writer io.Writer
	max    int
}

func (writer shortX11Writer) Write(data []byte) (int, error) {
	if len(data) > writer.max {
		data = data[:writer.max]
	}
	return writer.writer.Write(data)
}

func makeX11Setup(order binary.ByteOrder, protocol string, cookie, tail []byte) []byte {
	header := make([]byte, 12)
	if order == binary.LittleEndian {
		header[0] = 'l'
	} else {
		header[0] = 'B'
	}
	order.PutUint16(header[2:4], 11)
	order.PutUint16(header[4:6], 0)
	order.PutUint16(header[6:8], uint16(len(protocol)))
	order.PutUint16(header[8:10], uint16(len(cookie)))
	name := make([]byte, paddedX11Length(len(protocol)))
	copy(name, protocol)
	data := make([]byte, paddedX11Length(len(cookie)))
	copy(data, cookie)
	result := append(header, name...)
	result = append(result, data...)
	return append(result, tail...)
}
