//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package zmodem

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestInteroperatesWithLRZSZ(t *testing.T) {
	lrz, lsz, ok := lrzszBinaries()
	if !ok {
		t.Skip("set PATH to lrzsz binaries to run external ZMODEM interoperability")
	}
	t.Run("send_to_lrz", func(t *testing.T) { testSendToLRZ(t, lrz) })
	t.Run("receive_from_lsz", func(t *testing.T) { testReceiveFromLSZ(t, lsz) })
}

func lrzszBinaries() (string, string, bool) {
	lrz, lrzErr := exec.LookPath("lrz")
	lsz, lszErr := exec.LookPath("lsz")
	return lrz, lsz, lrzErr == nil && lszErr == nil
}

func testSendToLRZ(t *testing.T, lrz string) {
	t.Helper()
	sourceDir := t.TempDir()
	destinationDir := t.TempDir()
	files := map[string][]byte{
		"upload.bin": make([]byte, 32*1024+137),
		"second.bin": make([]byte, 2049),
		"empty-file": {},
	}
	paths := make([]string, 0, len(files))
	for name, content := range files {
		for i := range content {
			content[i] = byte(i*31 + i/7 + len(name))
		}
		path := filepath.Join(sourceDir, name)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	command := exec.CommandContext(ctx, lrz, "-y", "-v")
	command.Stderr = &diagnostics
	command.Dir = destinationDir
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start lrz: %v", err)
	}
	defer terminal.Close()

	wire := NewWire(terminal, terminal)
	done := make(chan error, 1)
	go func() { done <- Send(ctx, wire, paths, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("send to lrz (CRC32 negotiated=%v): %v\n%s", wire.UseCRC32, err, diagnostics.String())
		}
	case <-ctx.Done():
		_ = terminal.Close()
		<-done
		t.Fatalf("send to lrz timed out: %v", ctx.Err())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lrz exit: %v", err)
	}
	for name, content := range files {
		actual, err := os.ReadFile(filepath.Join(destinationDir, name))
		if err != nil {
			t.Fatalf("read lrz output %q: %v", name, err)
		}
		if !bytes.Equal(actual, content) {
			t.Fatalf("lrz output %q differs: got %d bytes, want %d", name, len(actual), len(content))
		}
	}
}

func testReceiveFromLSZ(t *testing.T, lsz string) {
	t.Helper()
	sourceDir := t.TempDir()
	destinationDir := t.TempDir()
	name := "download.bin"
	content := make([]byte, 32*1024+251)
	for i := range content {
		content[i] = byte(i*19 + i/11)
	}
	source := filepath.Join(sourceDir, name)
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, lsz, "-q", source)
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatalf("start lsz: %v", err)
	}
	defer terminal.Close()

	wire := NewWire(terminal, terminal)
	done := make(chan error, 1)
	go func() {
		_, err := Receive(ctx, wire, ReceiveOptions{Directory: destinationDir}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("receive from lsz: %v", err)
		}
	case <-ctx.Done():
		_ = terminal.Close()
		<-done
		t.Fatalf("receive from lsz timed out: %v", ctx.Err())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lsz exit: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(destinationDir, name))
	if err != nil {
		t.Fatalf("read Go receiver output: %v", err)
	}
	if !bytes.Equal(actual, content) {
		t.Fatalf("Go receiver output differs: got %d bytes, want %d", len(actual), len(content))
	}
}
