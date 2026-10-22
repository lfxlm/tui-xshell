package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
)

func TestSFTPTextHighlightRecognizesCommonSourceFiles(t *testing.T) {
	lines := []string{
		"package main",
		"func main() {",
		"\tmessage := \"hello\"",
		"}",
	}
	highlighted := highlightSFTPText("main.go", lines)
	if len(highlighted) != len(lines) {
		t.Fatalf("highlighted line count=%d, want %d", len(highlighted), len(lines))
	}
	for index, line := range lines {
		want := sanitizeRemoteText(strings.ReplaceAll(line, "\t", "    "))
		if gotWidth := lipgloss.Width(highlighted[index]); gotWidth != lipgloss.Width(want) {
			t.Errorf("highlighted line %d width=%d, want %d", index+1, gotWidth, lipgloss.Width(want))
		}
	}
	for _, tokenType := range []chroma.TokenType{chroma.Keyword, chroma.Comment, chroma.LiteralString, chroma.LiteralNumber} {
		if _, ok := sftpSyntaxStyle(tokenType); !ok {
			t.Errorf("syntax token %s has no style", tokenType)
		}
	}
}

func TestSFTPTextHighlightLeavesUnknownExtensionsPlain(t *testing.T) {
	if highlighted := highlightSFTPText("payload.unknown-extension", []string{"plain text"}); highlighted != nil {
		t.Fatalf("unknown extension unexpectedly produced highlighted lines: %#v", highlighted)
	}
}
