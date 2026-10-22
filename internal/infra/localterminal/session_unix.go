//go:build darwin || linux || freebsd || dragonfly || openbsd || solaris

// Package localterminal starts interactive local shells backed by a PTY.
package localterminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

var ErrInputQueueFull = errors.New("local terminal input queue is full")

// Session owns a local shell process and its pseudo-terminal.
type Session struct {
	command *exec.Cmd
	master  *os.File

	input    chan []byte
	done     chan struct{}
	closeOne sync.Once
}

// Start launches the user's login shell with an interactive PTY.
func Start(width, height int) (*Session, error) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	command := exec.Command(shell, "-l")
	command.Env = terminalEnvironment(os.Environ())
	master, err := pty.StartWithSize(command, &pty.Winsize{
		Cols: uint16(clampDimension(width)),
		Rows: uint16(clampDimension(height)),
	})
	if err != nil {
		return nil, err
	}

	session := &Session{
		command: command,
		master:  master,
		input:   make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	go func() { _ = command.Wait() }()
	go session.writeLoop()
	return session, nil
}

func (s *Session) Read(buffer []byte) (int, error) {
	n, err := s.master.Read(buffer)
	if errors.Is(err, syscall.EIO) {
		return n, io.EOF
	}
	return n, err
}

// Write queues keystrokes so Bubble Tea's Update method never blocks on PTY I/O.
func (s *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	copyOfData := append([]byte(nil), data...)
	select {
	case <-s.done:
		return io.ErrClosedPipe
	default:
	}
	select {
	case s.input <- copyOfData:
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	default:
		return ErrInputQueueFull
	}
}

func (s *Session) Resize(width, height int) error {
	return pty.Setsize(s.master, &pty.Winsize{
		Cols: uint16(clampDimension(width)),
		Rows: uint16(clampDimension(height)),
	})
}

// Close stops the shell and releases its PTY. It is safe to call more than once.
func (s *Session) Close() error {
	var closeErr error
	s.closeOne.Do(func() {
		close(s.done)
		if s.command.Process != nil {
			if err := s.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				closeErr = err
			}
		}
		if err := s.master.Close(); closeErr == nil && err != nil && !errors.Is(err, os.ErrClosed) {
			closeErr = err
		}
	})
	return closeErr
}

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case data := <-s.input:
			if _, err := s.master.Write(data); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

func clampDimension(value int) int {
	if value < 1 {
		return 1
	}
	if value > 65535 {
		return 65535
	}
	return value
}

func terminalEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "TERM=") {
			result = append(result, entry)
		}
	}
	return append(result, "TERM=xterm-256color")
}
