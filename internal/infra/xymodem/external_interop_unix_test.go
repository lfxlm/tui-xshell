//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package xymodem

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestInteroperatesWithLRZSZ(t *testing.T) {
	xReceive, xReceiveErr := exec.LookPath("lrx")
	yReceive, yReceiveErr := exec.LookPath("lrb")
	xSend, xSendErr := exec.LookPath("lsx")
	ySend, ySendErr := exec.LookPath("lsb")
	if xReceiveErr != nil && yReceiveErr != nil && xSendErr != nil && ySendErr != nil {
		t.Skip("install lrzsz and add lrx/lrb/lsx/lsb to PATH to run external X/YMODEM interoperability")
	}
	if xReceiveErr == nil {
		t.Run("send_xmodem_to_lrx", func(t *testing.T) { sendXMODEMToPeer(t, xReceive) })
	}
	if xSendErr == nil {
		t.Run("receive_xmodem_from_lsx", func(t *testing.T) { receiveXMODEMFromPeer(t, xSend) })
	}
	if yReceiveErr == nil {
		t.Run("send_ymodem_to_lrb", func(t *testing.T) { sendYMODEMToPeer(t, yReceive) })
	}
	if ySendErr == nil {
		t.Run("receive_ymodem_from_lsb", func(t *testing.T) { receiveYMODEMFromPeer(t, ySend) })
	}
}

func sendXMODEMToPeer(t *testing.T, receiver string) {
	t.Helper()
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	content := make([]byte, 4097)
	for index := range content {
		content[index] = byte(index*31 + index/7)
	}
	source := filepath.Join(sourceDir, "upload-x.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	diagnostics := &diagnosticBuffer{}
	// lrzsz's XMODEM receiver requires a destination filename. With no
	// positional argument, lrx follows its YMODEM receive path instead.
	command := exec.CommandContext(ctx, receiver, "-X", "-y", "upload-x.bin")
	command.Dir = destinationDir
	command.Stderr = diagnostics
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start rx: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		_ = terminal.Close()
		if !waited {
			_ = command.Wait()
		}
	})
	defer terminal.Close()
	done := make(chan error, 1)
	go func() { done <- Send(ctx, terminal, XMODEM, []string{source}, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("send XMODEM to rx: %v\n%s", err, diagnostics.String())
		}
	case <-ctx.Done():
		_ = terminal.Close()
		t.Fatalf("send XMODEM to rx timed out: %v\n%s", ctx.Err(), diagnostics.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("rx exit: %v\n%s", err, diagnostics.String())
	}
	waited = true
	actual, err := os.ReadFile(filepath.Join(destinationDir, filepath.Base(source)))
	if err != nil {
		t.Fatalf("read rx output: %v", err)
	}
	if len(actual) < len(content) || !bytes.Equal(actual[:len(content)], content) {
		t.Fatalf("rx output content differs: got %d bytes, source has %d", len(actual), len(content))
	}
	for _, value := range actual[len(content):] {
		if value != 0x1a {
			t.Fatalf("rx output has non-SUB XMODEM padding after source content")
		}
	}
}

func receiveXMODEMFromPeer(t *testing.T, sender string) {
	t.Helper()
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	content := make([]byte, 1027)
	for index := range content {
		content[index] = byte(index*19 + index/11)
	}
	source := filepath.Join(sourceDir, "download-x.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	diagnostics := &diagnosticBuffer{ready: make(chan struct{})}
	command := exec.CommandContext(ctx, sender, "-X", "-q", "--debug=readline,transfer", source)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("open sx diagnostics: %v", err)
	}
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start sx: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		_ = terminal.Close()
		if !waited {
			_ = command.Wait()
		}
	})
	defer terminal.Close()
	go func() { _, _ = io.Copy(diagnostics, stderr) }()
	select {
	case <-diagnostics.ready:
	case <-ctx.Done():
		t.Fatalf("sx did not become ready: %v\n%s", ctx.Err(), diagnostics.String())
	}
	target := filepath.Join(destinationDir, filepath.Base(source))
	done := make(chan error, 1)
	stream := &recordingStream{ReadWriter: terminal}
	go func() {
		_, err := Receive(ctx, stream, ReceiveOptions{Protocol: XMODEM, Path: target}, nil)
		done <- err
	}()
	waitTransfer(t, ctx, terminal, done, diagnostics, "receive XMODEM from sx", func() string {
		return fmt.Sprintf("read=% x wrote=% x", stream.snapshotRead(), stream.snapshotWritten())
	})
	if err := command.Wait(); err != nil {
		t.Fatalf("sx exit: %v\nread=% x wrote=% x\n%s", err, stream.snapshotRead(), stream.snapshotWritten(), diagnostics.String())
	}
	waited = true
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read Go receiver output: %v", err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("Go receiver output differs: got %d bytes, want %d\nread=% x wrote=% x\n%s", len(actual), len(content), stream.snapshotRead(), stream.snapshotWritten(), diagnostics.String())
	}
}

type diagnosticBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (buffer *diagnosticBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	n, err := buffer.data.Write(data)
	isReady := bytes.Contains(buffer.data.Bytes(), []byte("Calling read:"))
	buffer.mu.Unlock()
	if isReady && buffer.ready != nil {
		buffer.once.Do(func() { close(buffer.ready) })
	}
	return n, err
}

func (buffer *diagnosticBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.String()
}

type recordingStream struct {
	ReadWriter io.ReadWriter
	mu         sync.Mutex
	read       []byte
	written    []byte
	writeDelay time.Duration
}

func (stream *recordingStream) Read(data []byte) (int, error) {
	n, err := stream.ReadWriter.Read(data)
	stream.mu.Lock()
	stream.read = append(stream.read, data[:n]...)
	stream.mu.Unlock()
	return n, err
}

func (stream *recordingStream) Write(data []byte) (int, error) {
	if stream.writeDelay > 0 {
		time.Sleep(stream.writeDelay)
	}
	n, err := stream.ReadWriter.Write(data)
	stream.mu.Lock()
	stream.written = append(stream.written, data[:n]...)
	stream.mu.Unlock()
	return n, err
}

func (stream *recordingStream) snapshotRead() []byte {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return append([]byte(nil), stream.read...)
}

func (stream *recordingStream) snapshotWritten() []byte {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return append([]byte(nil), stream.written...)
}

func traceSummary(stream *recordingStream) string {
	read := stream.snapshotRead()
	written := stream.snapshotWritten()
	readLength, writtenLength := len(read), len(written)
	if readLength > 128 {
		read = read[readLength-128:]
	}
	if writtenLength > 128 {
		written = written[writtenLength-128:]
	}
	return fmt.Sprintf("read(%d)=% x wrote(%d)=% x", readLength, read, writtenLength, written)
}

func sendYMODEMToPeer(t *testing.T, receiver string) {
	t.Helper()
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	files := map[string][]byte{
		"upload-y.bin": makePattern(8193, 17),
		"empty-y.bin":  {},
	}
	paths := make([]string, 0, len(files))
	for name, content := range files {
		path := filepath.Join(sourceDir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	diagnostics := &diagnosticBuffer{}
	command := exec.CommandContext(ctx, receiver, "-g", "-y")
	command.Dir = destinationDir
	command.Stderr = diagnostics
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start rb: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		_ = terminal.Close()
		if !waited {
			_ = command.Wait()
		}
	})
	defer terminal.Close()
	done := make(chan error, 1)
	// A real SSH or serial link adds time between a receiver's request and
	// the next frame. PTYs have effectively zero latency, which can race
	// lrzsz's post-request input purge; model a small wire delay here.
	stream := &recordingStream{ReadWriter: terminal, writeDelay: 2 * time.Millisecond}
	go func() { done <- Send(ctx, stream, YMODEM, paths, nil) }()
	waitTransfer(t, ctx, terminal, done, diagnostics, "send YMODEM to rb", func() string {
		return traceSummary(stream)
	})
	if err := command.Wait(); err != nil {
		t.Fatalf("rb exit: %v\n%s\n%s", err, traceSummary(stream), diagnostics.String())
	}
	waited = true
	assertFilesEqual(t, destinationDir, files)
}

func receiveYMODEMFromPeer(t *testing.T, sender string) {
	t.Helper()
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	files := map[string][]byte{
		"download-y.bin": makePattern(9237, 23),
		"empty-dl.bin":   {},
	}
	paths := make([]string, 0, len(files))
	for name, content := range files {
		path := filepath.Join(sourceDir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	diagnostics := &diagnosticBuffer{ready: make(chan struct{})}
	command := exec.CommandContext(ctx, sender, append([]string{"-g", "-q", "--debug=readline,transfer"}, paths...)...)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatalf("open sb diagnostics: %v", err)
	}
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start sb: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		_ = terminal.Close()
		if !waited {
			_ = command.Wait()
		}
	})
	defer terminal.Close()
	go func() { _, _ = io.Copy(diagnostics, stderr) }()
	select {
	case <-diagnostics.ready:
	case <-ctx.Done():
		t.Fatalf("sb did not become ready: %v\n%s", ctx.Err(), diagnostics.String())
	}
	done := make(chan error, 1)
	go func() {
		_, err := Receive(ctx, terminal, ReceiveOptions{Protocol: YMODEM, Directory: destinationDir}, nil)
		done <- err
	}()
	waitTransfer(t, ctx, terminal, done, diagnostics, "receive YMODEM from sb")
	if err := command.Wait(); err != nil {
		t.Fatalf("sb exit: %v\n%s", err, diagnostics.String())
	}
	waited = true
	assertFilesEqual(t, destinationDir, files)
}

func waitTransfer(t *testing.T, ctx context.Context, terminal *os.File, done <-chan error, diagnostics interface{ String() string }, operation string, details ...func() string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			if len(details) > 0 {
				t.Fatalf("%s: %v\n%s\n%s", operation, err, details[0](), diagnostics.String())
			}
			t.Fatalf("%s: %v\n%s", operation, err, diagnostics.String())
		}
	case <-ctx.Done():
		_ = terminal.Close()
		if len(details) > 0 {
			t.Fatalf("%s timed out: %v\n%s\n%s", operation, ctx.Err(), details[0](), diagnostics.String())
		}
		t.Fatalf("%s timed out: %v\n%s", operation, ctx.Err(), diagnostics.String())
	}
}

func assertFilesEqual(t *testing.T, directory string, files map[string][]byte) {
	t.Helper()
	for name, expected := range files {
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("%q differs: got %d bytes, want %d", name, len(actual), len(expected))
		}
	}
}

func makePattern(size, multiplier int) []byte {
	content := make([]byte, size)
	for index := range content {
		content[index] = byte(index*multiplier + index/13)
	}
	return content
}
