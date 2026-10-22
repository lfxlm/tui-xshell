package zmodem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReceiveFileOverWire(t *testing.T) {
	directory := t.TempDir()
	receiverConn, senderConn := net.Pipe()
	defer receiverConn.Close()
	defer senderConn.Close()
	_ = receiverConn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = senderConn.SetDeadline(time.Now().Add(5 * time.Second))
	receiverWire := NewWire(receiverConn, receiverConn)
	senderWire := NewWire(senderConn, senderConn)
	type result struct {
		files []ReceivedFile
		err   error
	}
	done := make(chan result, 1)
	go func() {
		files, err := Receive(context.Background(), receiverWire, ReceiveOptions{Directory: directory}, nil)
		done <- result{files: files, err: err}
	}()

	if err := senderWire.WriteHeader(Frame{Type: FrameZRQINIT}, true); err != nil {
		t.Fatal(err)
	}
	ready, err := senderWire.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if ready.Type != FrameZRINIT || ready.Data != [4]byte{0, 0, 0, defaultReceiveFlags} {
		t.Fatalf("receiver ready frame = %#v", ready)
	}

	content := bytes.Repeat([]byte("zmodem payload\x00"), 111)
	metadata := fmt.Sprintf("archive.bin\x00%d 0 100600\x00", len(content))
	if err := senderWire.WriteHeader(Frame{Type: FrameZFILE}, false); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteSubpacket([]byte(metadata), ZCRCW); err != nil {
		t.Fatal(err)
	}
	position, err := senderWire.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if position.Type != FrameZRPOS || position.Position != 0 {
		t.Fatalf("receiver position = %#v", position)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZDATA}, false); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteSubpacket(content, ZCRCE); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZEOF, Position: uint32(len(content))}, false); err != nil {
		t.Fatal(err)
	}
	ready, err = senderWire.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if ready.Type != FrameZRINIT {
		t.Fatalf("post-file frame = %#v, want ZRINIT", ready)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZFIN}, true); err != nil {
		t.Fatal(err)
	}
	finish, err := senderWire.ReadHeader()
	if err != nil {
		select {
		case early := <-done:
			t.Fatalf("receive session ended before finish response: files=%#v error=%v; read error=%v", early.files, early.err, err)
		default:
		}
		t.Fatal(err)
	}
	if finish.Type != FrameZFIN {
		t.Fatalf("finish frame = %#v, want ZFIN", finish)
	}
	if err := writeFull(senderConn, []byte("OO")); err != nil {
		t.Fatal(err)
	}

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.files) != 1 || got.files[0].Name != "archive.bin" || got.files[0].Size != int64(len(content)) {
		t.Fatalf("received files = %#v", got.files)
	}
	actual, err := os.ReadFile(filepath.Join(directory, "archive.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("received file content mismatch: got %d bytes, want %d", len(actual), len(content))
	}
}

func TestReceiveSkipsExistingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "existing.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiverConn, senderConn := net.Pipe()
	defer receiverConn.Close()
	defer senderConn.Close()
	_ = receiverConn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = senderConn.SetDeadline(time.Now().Add(5 * time.Second))
	receiverWire := NewWire(receiverConn, receiverConn)
	senderWire := NewWire(senderConn, senderConn)
	done := make(chan error, 1)
	go func() {
		_, err := Receive(context.Background(), receiverWire, ReceiveOptions{Directory: directory}, nil)
		done <- err
	}()
	if err := senderWire.WriteHeader(Frame{Type: FrameZRQINIT}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := senderWire.ReadHeader(); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZFILE}, false); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteSubpacket([]byte("existing.txt\x007 0 100600\x00"), ZCRCW); err != nil {
		t.Fatal(err)
	}
	response, err := senderWire.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != FrameZSKIP {
		t.Fatalf("existing file response = %#v, want ZSKIP", response)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZFIN}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := senderWire.ReadHeader(); err != nil {
		t.Fatal(err)
	}
	if err := writeFull(senderConn, []byte("OO")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "keep me" {
		t.Fatalf("existing file changed: %q", actual)
	}
}

func TestSendFileOverWire(t *testing.T) {
	for _, size := range []int{0, 2517} {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upload.bin")
			content := make([]byte, size)
			for i := range content {
				content[i] = byte(i*37 + 0x18)
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			senderConn, receiverConn := net.Pipe()
			defer senderConn.Close()
			defer receiverConn.Close()
			_ = senderConn.SetDeadline(time.Now().Add(5 * time.Second))
			_ = receiverConn.SetDeadline(time.Now().Add(5 * time.Second))
			senderWire := NewWire(senderConn, senderConn)
			receiverWire := NewWire(receiverConn, receiverConn)
			done := make(chan error, 1)
			go func() {
				done <- Send(context.Background(), senderWire, []string{path}, nil)
			}()
			if err := receiverWire.WriteHeader(Frame{Type: FrameZRINIT, Data: [4]byte{0, 0, 0, defaultReceiveFlags}, UseData: true}, true); err != nil {
				t.Fatal(err)
			}
			fileHeader, err := receiverWire.ReadHeader()
			if err != nil {
				t.Fatal(err)
			}
			if fileHeader.Type != FrameZFILE {
				t.Fatalf("file header = %#v, want ZFILE", fileHeader)
			}
			metadata, _, err := receiverWire.ReadSubpacket()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseFileMetadata(metadata)
			if err != nil || parsed.name != "upload.bin" || parsed.size != int64(size) {
				t.Fatalf("upload metadata = %#v, error = %v", parsed, err)
			}
			if err := receiverWire.WriteHeader(Frame{Type: FrameZRPOS}, true); err != nil {
				t.Fatal(err)
			}
			dataHeader, err := receiverWire.ReadHeader()
			if err != nil {
				t.Fatal(err)
			}
			if dataHeader.Type != FrameZDATA || dataHeader.Position != 0 {
				t.Fatalf("data header = %#v", dataHeader)
			}
			var received []byte
			for {
				chunk, marker, err := receiverWire.ReadSubpacket()
				if err != nil {
					t.Fatal(err)
				}
				received = append(received, chunk...)
				if marker == ZCRCW {
					if err := receiverWire.WriteHeader(Frame{Type: FrameZACK, Position: uint32(len(received))}, true); err != nil {
						t.Fatal(err)
					}
					if len(received) < len(content) {
						nextData, err := receiverWire.ReadHeader()
						if err != nil {
							t.Fatal(err)
						}
						if nextData.Type != FrameZDATA || nextData.Position != uint32(len(received)) {
							t.Fatalf("continued data header = %#v", nextData)
						}
					}
				}
				if marker == ZCRCE {
					break
				}
			}
			if !bytes.Equal(received, content) {
				t.Fatalf("uploaded content mismatch: got %d bytes, want %d", len(received), len(content))
			}
			end, err := receiverWire.ReadHeader()
			if err != nil {
				t.Fatal(err)
			}
			if end.Type != FrameZEOF || end.Position != uint32(size) {
				t.Fatalf("end header = %#v", end)
			}
			if err := receiverWire.WriteHeader(Frame{Type: FrameZRINIT}, true); err != nil {
				t.Fatal(err)
			}
			finish, err := receiverWire.ReadHeader()
			if err != nil {
				t.Fatal(err)
			}
			if finish.Type != FrameZFIN {
				t.Fatalf("finish request = %#v", finish)
			}
			if err := receiverWire.WriteHeader(Frame{Type: FrameZFIN}, true); err != nil {
				t.Fatal(err)
			}
			var trailing [2]byte
			if _, err := io.ReadFull(receiverWire.r, trailing[:]); err != nil {
				t.Fatal(err)
			}
			if trailing != [2]byte{'O', 'O'} {
				t.Fatalf("sender finish bytes = %q", trailing)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReceiveRejectsOverLimitFile(t *testing.T) {
	directory := t.TempDir()
	receiverConn, senderConn := net.Pipe()
	defer receiverConn.Close()
	defer senderConn.Close()
	_ = receiverConn.SetDeadline(time.Now().Add(5 * time.Second))
	_ = senderConn.SetDeadline(time.Now().Add(5 * time.Second))
	receiverWire := NewWire(receiverConn, receiverConn)
	senderWire := NewWire(senderConn, senderConn)
	done := make(chan error, 1)
	go func() {
		_, err := Receive(context.Background(), receiverWire, ReceiveOptions{Directory: directory, MaxFileBytes: 3}, nil)
		done <- err
	}()
	if err := senderWire.WriteHeader(Frame{Type: FrameZRQINIT}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := senderWire.ReadHeader(); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteHeader(Frame{Type: FrameZFILE}, false); err != nil {
		t.Fatal(err)
	}
	if err := senderWire.WriteSubpacket([]byte("large.bin\x004 0 100600\x00"), ZCRCW); err != nil {
		t.Fatal(err)
	}
	response, err := senderWire.ReadHeader()
	if err != nil {
		t.Fatal(err)
	}
	if response.Type != FrameZSKIP {
		t.Fatalf("oversize response = %#v, want ZSKIP", response)
	}
	if err := <-done; !errors.Is(err, ErrFileTooBig) {
		t.Fatalf("Receive error = %v, want ErrFileTooBig", err)
	}
}

func TestParseFileMetadataRejectsPathTraversal(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{{"../secret", "secret"}, {`..\secret`, "secret"}, {"/absolute/path", "path"}} {
		metadata, err := parseFileMetadata([]byte(test.input + "\x001 0 100600\x00"))
		if err != nil {
			t.Errorf("parseFileMetadata(%q) error = %v", test.input, err)
			continue
		}
		if metadata.name != test.want {
			t.Errorf("parseFileMetadata(%q) name = %q, want %q", test.input, metadata.name, test.want)
		}
	}
	if err := isSafeFileName("../secret"); !errors.Is(err, ErrUnsafeName) {
		t.Fatalf("isSafeFileName traversal error = %v, want ErrUnsafeName", err)
	}
}
