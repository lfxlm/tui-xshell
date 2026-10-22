//go:build windows

package rloginclient

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestUrgentWindowRequestResendsCurrentSizeWithoutDisplayingControl(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		handshake := make([]byte, len("\x00client\x00operator\x00xterm-256color/9600\x00"))
		if _, err := io.ReadFull(connection, handshake); err != nil {
			serverResult <- err
			return
		}
		if _, err := connection.Write([]byte{0}); err != nil {
			serverResult <- err
			return
		}
		initial := make([]byte, 12)
		if _, err := io.ReadFull(connection, initial); err != nil {
			serverResult <- err
			return
		}
		if string(initial) != string(windowSizeMessage(100, 30)) {
			serverResult <- io.ErrUnexpectedEOF
			return
		}
		if err := sendWindowsUrgentByte(connection, 0x80); err != nil {
			serverResult <- err
			return
		}
		resend := make([]byte, 12)
		if _, err := io.ReadFull(connection, resend); err != nil {
			serverResult <- err
			return
		}
		if string(resend) != string(windowSizeMessage(100, 30)) {
			serverResult <- io.ErrUnexpectedEOF
			return
		}
		_, err = connection.Write([]byte("visible"))
		serverResult <- err
	}()

	session, err := Dial(context.Background(), listener.Addr().String(), "client", "operator", "", time.Second, 100, 30)
	if err != nil {
		t.Fatalf("dial Rlogin server: %v", err)
	}
	defer session.Close()
	visible := make([]byte, len("visible"))
	if _, err := io.ReadFull(session, visible); err != nil || string(visible) != "visible" {
		t.Fatalf("terminal output = %q, err=%v", visible, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("Rlogin urgent-data server: %v", err)
	}
}

func TestUrgentDiscardOutputControlDropsPendingBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
		handshake := make([]byte, len("\x00client\x00operator\x00xterm-256color/9600\x00"))
		if _, err := io.ReadFull(connection, handshake); err != nil {
			serverResult <- err
			return
		}
		if _, err := connection.Write([]byte{0}); err != nil {
			serverResult <- err
			return
		}
		initial := make([]byte, 12)
		if _, err := io.ReadFull(connection, initial); err != nil {
			serverResult <- err
			return
		}
		if _, err := connection.Write([]byte("discard")); err != nil {
			serverResult <- err
			return
		}
		if err := sendWindowsUrgentByte(connection, 0x02); err != nil {
			serverResult <- err
			return
		}
		_, err = connection.Write([]byte("keep"))
		serverResult <- err
	}()

	session, err := Dial(context.Background(), listener.Addr().String(), "client", "operator", "", time.Second, 80, 24)
	if err != nil {
		t.Fatalf("dial Rlogin server: %v", err)
	}
	defer session.Close()
	visible := make([]byte, len("keep"))
	if _, err := io.ReadFull(session, visible); err != nil || string(visible) != "keep" {
		t.Fatalf("terminal output after discard = %q, err=%v", visible, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("Rlogin urgent-data server: %v", err)
	}
}

func sendWindowsUrgentByte(connection net.Conn, value byte) error {
	tcp, ok := connection.(*net.TCPConn)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return err
	}
	var operationErr error
	err = raw.Control(func(fd uintptr) {
		data := []byte{value}
		operationErr = windows.Sendto(windows.Handle(fd), data, windows.MSG_OOB, nil)
	})
	if err != nil {
		return err
	}
	return operationErr
}
