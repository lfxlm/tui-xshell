package rloginclient

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDialHandshakeInputAndResize(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type serverResult struct{ err error }
	result := make(chan serverResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- serverResult{err}
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		handshake := make([]byte, len("\x00client\x00operator\x00xterm-256color/9600\x00"))
		if _, err := io.ReadFull(conn, handshake); err != nil {
			result <- serverResult{err}
			return
		}
		if string(handshake) != "\x00client\x00operator\x00xterm-256color/9600\x00" {
			result <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		if _, err := conn.Write([]byte{0}); err != nil {
			result <- serverResult{err}
			return
		}
		initialSize := make([]byte, 12)
		if _, err := io.ReadFull(conn, initialSize); err != nil {
			result <- serverResult{err}
			return
		}
		if want := string(windowSizeMessage(100, 30)); string(initialSize) != want {
			result <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		if _, err := conn.Write([]byte("ready")); err != nil {
			result <- serverResult{err}
			return
		}
		input := make([]byte, 4)
		if _, err := io.ReadFull(conn, input); err != nil {
			result <- serverResult{err}
			return
		}
		if string(input) != "pwd\n" {
			result <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		resized := make([]byte, 12)
		if _, err := io.ReadFull(conn, resized); err != nil {
			result <- serverResult{err}
			return
		}
		if string(resized) != string(windowSizeMessage(120, 40)) {
			result <- serverResult{err: io.ErrUnexpectedEOF}
			return
		}
		result <- serverResult{}
	}()

	session, err := Dial(context.Background(), listener.Addr().String(), "client", "operator", "xterm-256color/9600", time.Second, 100, 30)
	if err != nil {
		t.Fatalf("dial Rlogin server: %v", err)
	}
	defer session.Close()
	output := make([]byte, len("ready"))
	if _, err := io.ReadFull(session, output); err != nil || string(output) != "ready" {
		t.Fatalf("read Rlogin output = %q, err=%v", output, err)
	}
	if err := session.Write([]byte("pwd\n")); err != nil {
		t.Fatalf("write Rlogin input: %v", err)
	}
	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("resize Rlogin terminal: %v", err)
	}
	if got := <-result; got.err != nil {
		t.Fatalf("Rlogin test server: %v", got.err)
	}
}

func TestDialReportsServerRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		buffer := make([]byte, len("\x00client\x00operator\x00xterm-256color/9600\x00"))
		_, _ = io.ReadFull(conn, buffer)
		_, _ = conn.Write([]byte{1})
		_, _ = conn.Write([]byte("permission denied\n"))
	}()
	_, err = Dial(context.Background(), listener.Addr().String(), "client", "operator", "", time.Second, 80, 24)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Rlogin refusal error = %v, want server message", err)
	}
}

func TestDialRejectsInvalidHandshakeAndSize(t *testing.T) {
	for name, call := range map[string]func() error{
		"empty address": func() error {
			_, err := Dial(context.Background(), "", "client", "operator", "", time.Second, 80, 24)
			return err
		},
		"NUL username": func() error {
			_, err := Dial(context.Background(), "127.0.0.1:1", "cl\x00ient", "operator", "", time.Second, 80, 24)
			return err
		},
		"invalid size": func() error {
			_, err := Dial(context.Background(), "127.0.0.1:1", "client", "operator", "", time.Second, 0, 24)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid Rlogin request unexpectedly succeeded")
			}
		})
	}
}
