package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestTerminalLinkAtFindsPlainURLAndEmitsOSC8Link(t *testing.T) {
	terminal := vt.NewSafeEmulator(48, 3)
	_, _ = terminal.Write([]byte("see https://example.com/path, now"))
	pane := &terminalPane{terminal: terminal}

	if got, want := terminalLinkAt(pane, 0, 11), "https://example.com/path"; got != want {
		t.Fatalf("terminalLinkAt() = %q, want %q", got, want)
	}
	rows := renderTerminalRowsWithLinks(pane)
	if len(rows) == 0 || !strings.Contains(rows[0], "\x1b]8;;https://example.com/path") {
		t.Fatalf("plain URL was not rendered as an OSC 8 hyperlink: %q", rows)
	}
}

func TestTerminalLinkAtPreservesOSC8TargetAndRejectsUnsafeSchemes(t *testing.T) {
	terminal := vt.NewSafeEmulator(48, 3)
	_, _ = terminal.Write([]byte("\x1b]8;;https://docs.example.test/help\x1b\\docs\x1b]8;;\x1b\\"))
	pane := &terminalPane{terminal: terminal}
	if got, want := terminalLinkAt(pane, 0, 1), "https://docs.example.test/help"; got != want {
		t.Fatalf("OSC 8 URL = %q, want %q", got, want)
	}
	if _, err := normalizeTerminalURL("javascript:alert(1)"); err == nil {
		t.Fatal("unsafe URL scheme was accepted")
	}
}
