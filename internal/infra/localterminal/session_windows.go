//go:build windows

// Package localterminal starts interactive local shells backed by a Windows
// ConPTY pseudo console.
package localterminal

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

var ErrInputQueueFull = errors.New("local terminal input queue is full")

// Session owns a local shell process and its ConPTY pseudo console.
type Session struct {
	console  *conpty.ConPty
	input    chan []byte
	done     chan struct{}
	closeOne sync.Once
}

// Start launches an interactive local shell in a Windows ConPTY.
func Start(width, height int) (*Session, error) {
	shell, arguments := windowsShell()
	commandLine := windows.ComposeCommandLine(append([]string{shell}, arguments...))
	console, err := conpty.Start(commandLine,
		conpty.ConPtyDimensions(clampDimension(width), clampDimension(height)),
		conpty.ConPtyEnv(terminalEnvironment(os.Environ())),
	)
	if err != nil {
		return nil, err
	}
	session := &Session{
		console: console,
		input:   make(chan []byte, 64),
		done:    make(chan struct{}),
	}
	go session.writeLoop()
	return session, nil
}

func windowsShell() (string, []string) {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell, windowsShellArguments(shell)
	}
	shell := strings.TrimSpace(os.Getenv("COMSPEC"))
	if shell == "" {
		systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
		if systemRoot != "" {
			shell = filepath.Join(systemRoot, "System32", "cmd.exe")
		} else {
			shell = "cmd.exe"
		}
	}
	return shell, windowsShellArguments(shell)
}

func windowsShellArguments(shell string) []string {
	base := strings.ToLower(filepath.Base(shell))
	if base == "bash" || base == "bash.exe" || base == "sh" || base == "sh.exe" || base == "zsh" || base == "zsh.exe" || base == "fish" || base == "fish.exe" {
		return []string{"-l"}
	}
	if base == "powershell.exe" || base == "pwsh.exe" {
		return []string{"-NoLogo"}
	}
	return nil
}

func (s *Session) Read(buffer []byte) (int, error) {
	return s.console.Read(buffer)
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
	return s.console.Resize(clampDimension(width), clampDimension(height))
}

// Close stops the shell and releases ConPTY resources. It is safe to call more
// than once.
func (s *Session) Close() error {
	var closeErr error
	s.closeOne.Do(func() {
		close(s.done)
		closeErr = s.console.Close()
	})
	return closeErr
}

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case data := <-s.input:
			if err := writeConPTYInput(s.console, data); err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

func writeConPTYInput(console *conpty.ConPty, data []byte) error {
	for len(data) > 0 {
		written, err := console.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func clampDimension(value int) int {
	if value < 1 {
		return 1
	}
	if value > 32767 {
		return 32767
	}
	return value
}

func terminalEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if !found || !strings.EqualFold(key, "TERM") {
			result = append(result, entry)
		}
	}
	return append(result, "TERM=xterm-256color")
}
