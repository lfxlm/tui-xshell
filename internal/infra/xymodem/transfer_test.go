package xymodem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestXMODEMRoundTrip(t *testing.T) {
	content := bytes.Repeat([]byte("xymodem-data-"), 90)
	content = append(content, []byte("end")...)
	source := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "received.bin")
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	receiveDone := make(chan error, 1)
	go func() {
		files, err := Receive(context.Background(), left, ReceiveOptions{Protocol: XMODEM, Path: target}, nil)
		if err == nil && (len(files) != 1 || files[0].Size != int64(len(content))) {
			err = io.ErrUnexpectedEOF
		}
		receiveDone <- err
	}()
	if err := Send(context.Background(), right, XMODEM, []string{source}, nil); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := <-receiveDone; err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("received %d bytes, want exact %d-byte payload", len(got), len(content))
	}
}

func TestYMODEMBatchRoundTrip(t *testing.T) {
	sources := []string{
		filepath.Join(t.TempDir(), "empty.txt"),
		filepath.Join(t.TempDir(), "large.bin"),
	}
	contents := [][]byte{{}, bytes.Repeat([]byte{0, 0x1a, 0xff, 'G'}, 400)}
	for index, source := range sources {
		if err := os.WriteFile(source, contents[index], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targetDir := t.TempDir()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	type receiveResult struct {
		files []ReceivedFile
		err   error
	}
	receiveDone := make(chan receiveResult, 1)
	go func() {
		files, err := Receive(context.Background(), left, ReceiveOptions{Protocol: YMODEM, Directory: targetDir}, nil)
		receiveDone <- receiveResult{files: files, err: err}
	}()
	if err := Send(context.Background(), right, YMODEM, sources, nil); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	result := <-receiveDone
	if result.err != nil {
		t.Fatalf("Receive() error = %v", result.err)
	}
	if len(result.files) != len(sources) {
		t.Fatalf("received %d files, want %d", len(result.files), len(sources))
	}
	for index, name := range []string{"empty.txt", "large.bin"} {
		got, err := os.ReadFile(filepath.Join(targetDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, contents[index]) {
			t.Fatalf("%s differs: received %d bytes, want %d", name, len(got), len(contents[index]))
		}
	}
}

func TestYMODEMHeaderRetransmitsAfterRepeatedCRCRequest(t *testing.T) {
	sender, receiver := net.Pipe()
	defer sender.Close()
	defer receiver.Close()
	deadline := time.Now().Add(3 * time.Second)
	if err := sender.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	packet := makeRawPacket(0, make([]byte, blockSize128), soh)
	peerDone := make(chan error, 1)
	go func() {
		first := make([]byte, len(packet))
		if _, err := io.ReadFull(receiver, first); err != nil {
			peerDone <- err
			return
		}
		if !bytes.Equal(first, packet) {
			peerDone <- errors.New("first YMODEM header differs from expected packet")
			return
		}
		if err := writeByte(receiver, crcRequest); err != nil {
			peerDone <- err
			return
		}
		retry := make([]byte, len(packet))
		if _, err := io.ReadFull(receiver, retry); err != nil {
			peerDone <- err
			return
		}
		if !bytes.Equal(retry, packet) {
			peerDone <- errors.New("retransmitted YMODEM header differs from original")
			return
		}
		peerDone <- writeByte(receiver, ack)
	}()
	if err := sendYHeader(context.Background(), sender, packet); err != nil {
		t.Fatalf("sendYHeader() error = %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("YMODEM header peer error = %v", err)
	}
}

func TestXMODEMSenderNegotiatesChecksumMode(t *testing.T) {
	source := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(source, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	sender, receiver := net.Pipe()
	defer sender.Close()
	defer receiver.Close()
	peerDone := make(chan error, 1)
	go func() {
		defer close(peerDone)
		if err := writeByte(receiver, nak); err != nil {
			peerDone <- err
			return
		}
		packet := make([]byte, 3+blockSize128+1)
		if _, err := io.ReadFull(receiver, packet); err != nil {
			peerDone <- err
			return
		}
		if packet[0] != soh || packet[1] != 1 || packet[2] != 0xfe {
			peerDone <- errors.New("invalid checksum-mode XMODEM header")
			return
		}
		var sum byte
		for _, value := range packet[3 : 3+blockSize128] {
			sum += value
		}
		if packet[len(packet)-1] != sum {
			peerDone <- errors.New("invalid checksum-mode XMODEM payload checksum")
			return
		}
		if err := writeByte(receiver, ack); err != nil {
			peerDone <- err
			return
		}
		marker, err := readByte(receiver)
		if err != nil {
			peerDone <- err
			return
		}
		if marker != eot {
			peerDone <- errors.New("sender did not finish with EOT")
			return
		}
		peerDone <- writeByte(receiver, ack)
	}()
	if err := Send(context.Background(), sender, XMODEM, []string{source}, nil); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("checksum receiver error = %v", err)
	}
}

func TestYMODEMHeaderValidation(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "metadata is NUL separated", data: []byte("file.bin\x00123\x00"), want: "file.bin"},
		{name: "reject missing size", data: []byte("file.bin\x00")},
		{name: "reject invalid size", data: []byte("file.bin\x00x\x00")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, size, empty, err := parseYHeader(test.data)
			if test.want == "" {
				if err == nil {
					t.Fatal("parseYHeader() error = nil, want malformed header error")
				}
				return
			}
			if err != nil || empty || name != test.want || size != 123 {
				t.Fatalf("parseYHeader() = %q, %d, %v, %v", name, size, empty, err)
			}
		})
	}
}

func TestUnsafeYMODEMNames(t *testing.T) {
	for _, name := range []string{"../escape", `folder\escape`, ".", ""} {
		if err := safeName(name); err == nil {
			t.Errorf("safeName(%q) = nil, want error", name)
		}
	}
	if err := safeName("ok.txt"); err != nil {
		t.Fatalf("safeName(ok.txt) error = %v", err)
	}
}
