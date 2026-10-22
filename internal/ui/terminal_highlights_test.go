package ui

import (
	"image/color"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func TestTerminalHighlightUsesRenderedCellsAcrossWritesAndANSI(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 3)
	_, _ = terminal.Write([]byte("中"))
	_, _ = terminal.Write([]byte("\x1b[31mERROR"))
	_, _ = terminal.Write([]byte("\x1b[0m ready"))
	pane := &terminalPane{terminal: terminal}
	rowIndex := terminal.ScrollbackLen()
	line := terminalLineCells(pane, rowIndex)
	rules := compileTerminalHighlights([]model.TerminalHighlightRule{{
		Name: "errors", Pattern: "error", IgnoreCase: true,
		Foreground: "#FFFFFF", Background: "#7F1D1D", Bold: true,
	}})
	styleTerminalHighlightLine(line, rules)

	if got := ansi.Strip(line.Render()); !strings.Contains(got, "中ERROR ready") {
		t.Fatalf("highlighting changed rendered text: %q", got)
	}
	for x := 2; x < 7; x++ {
		cell := line[x]
		if !terminalTestColorEquals(cell.Style.Fg, 255, 255, 255) || !terminalTestColorEquals(cell.Style.Bg, 127, 29, 29) || cell.Style.Attrs&uv.AttrBold == 0 {
			t.Errorf("cell %d style = %#v, want white on red and bold", x, cell.Style)
		}
	}
	if line[0].Style.Bg != nil || line[7].Style.Bg != nil {
		t.Fatalf("highlight escaped match cell range: before=%#v after=%#v", line[0].Style, line[7].Style)
	}
}

func TestTerminalHighlightRulesApplyInOrderAndPreserveUnspecifiedStyle(t *testing.T) {
	terminal := vt.NewSafeEmulator(20, 2)
	_, _ = terminal.Write([]byte("ERROR"))
	line := terminalLineCells(&terminalPane{terminal: terminal}, 0)
	rules := compileTerminalHighlights([]model.TerminalHighlightRule{
		{Name: "error", Pattern: "ERROR", Foreground: "#AA0000", Background: "#000000"},
		{Name: "important", Pattern: "OR", Background: "#FFFF00", Bold: true},
	})
	styleTerminalHighlightLine(line, rules)

	if !terminalTestColorEquals(line[0].Style.Fg, 170, 0, 0) || !terminalTestColorEquals(line[0].Style.Bg, 0, 0, 0) {
		t.Fatalf("first rule style = %#v, want foreground/background from first match", line[0].Style)
	}
	if !terminalTestColorEquals(line[3].Style.Fg, 170, 0, 0) || !terminalTestColorEquals(line[3].Style.Bg, 255, 255, 0) || line[3].Style.Attrs&uv.AttrBold == 0 {
		t.Errorf("overlapping cell style = %#v, want first foreground and second background/bold", line[3].Style)
	}
}

func TestTerminalHighlightAppliesToScrollbackRowsInPaneView(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 2)
	terminal.SetScrollbackSize(20)
	_, _ = terminal.Write([]byte("old ERROR\r\ncurrent\r\n"))
	if terminal.ScrollbackLen() == 0 {
		t.Fatal("terminal did not create scrollback")
	}
	pane := &terminalPane{
		host:              model.Host{Name: "test"},
		session:           &workspaceTestSession{},
		terminal:          terminal,
		scrollbackViewing: true,
		scrollbackOffset:  terminalScrollbackMax(&terminalPane{terminal: terminal}, 3),
	}
	state := modelState{terminalHighlights: compileTerminalHighlights([]model.TerminalHighlightRule{{
		Name: "errors", Pattern: "ERROR", Background: "#FF0000",
	}})}
	view := state.renderTerminalPane(pane, paneRect{width: 24, height: 6})
	if !strings.Contains(ansi.Strip(view), "old ERROR") {
		t.Fatalf("old scrollback row was not rendered:\n%s", view)
	}
	if !strings.Contains(view, "48;2;255;0;0") {
		t.Fatalf("scrollback match was not highlighted:\n%s", view)
	}
}

func terminalTestColorEquals(value color.Color, r, g, b uint8) bool {
	if value == nil {
		return false
	}
	gotR, gotG, gotB, _ := value.RGBA()
	return uint8(gotR>>8) == r && uint8(gotG>>8) == g && uint8(gotB>>8) == b
}
