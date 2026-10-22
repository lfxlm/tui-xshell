package ui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

type terminalResponseTestSession struct {
	writes chan []byte
}

func (*terminalResponseTestSession) Read([]byte) (int, error) { return 0, io.EOF }
func (s *terminalResponseTestSession) Write(data []byte) error {
	s.writes <- append([]byte(nil), data...)
	return nil
}
func (*terminalResponseTestSession) Resize(int, int) error { return nil }
func (*terminalResponseTestSession) Close() error          { return nil }

func TestTerminalEmulatorRepliesAreForwardedToSession(t *testing.T) {
	session := &terminalResponseTestSession{writes: make(chan []byte, 4)}
	terminal := vt.NewSafeEmulator(80, 24)
	pane := &terminalPane{session: session, terminal: terminal}
	startTerminalResponseReader(pane)

	if _, err := terminal.Write([]byte("\x1b[6n\x1b]11;?\x1b\\")); err != nil {
		t.Fatalf("terminal emulator write: %v", err)
	}
	var replies strings.Builder
	for index := 0; index < 2; index++ {
		select {
		case data := <-session.writes:
			replies.Write(data)
		case <-time.After(time.Second):
			t.Fatal("terminal emulator response was not forwarded to the session")
		}
	}
	if got := replies.String(); !strings.Contains(got, "\x1b[1;1R") || !strings.Contains(got, "\x1b]11;rgb:") {
		t.Fatalf("forwarded terminal replies = %q, want cursor and background-color responses", got)
	}

	closeTerminalResponseReader(pane)
	if pane.terminalResponseReaderStarted {
		t.Fatal("terminal response reader remained marked as active after close")
	}
}
