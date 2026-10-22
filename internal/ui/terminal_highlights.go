package ui

import (
	"encoding/hex"
	"image/color"
	"regexp"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"tui-ssh-workbench/internal/model"
)

type terminalHighlightRule struct {
	name       string
	expression *regexp.Regexp
	foreground color.Color
	background color.Color
	bold       bool
}

func compileTerminalHighlights(rules []model.TerminalHighlightRule) []terminalHighlightRule {
	compiled := make([]terminalHighlightRule, 0, len(rules))
	for _, rule := range rules {
		pattern := rule.Pattern
		if rule.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		compiled = append(compiled, terminalHighlightRule{
			name:       rule.Name,
			expression: regexp.MustCompile(pattern),
			foreground: terminalHighlightColor(rule.Foreground),
			background: terminalHighlightColor(rule.Background),
			bold:       rule.Bold,
		})
	}
	return compiled
}

func terminalHighlightColor(value string) color.Color {
	if value == "" {
		return nil
	}
	decoded, _ := hex.DecodeString(value[1:]) // Config validation checks #RRGGBB before compilation.
	return color.RGBA{R: decoded[0], G: decoded[1], B: decoded[2], A: 0xff}
}

func terminalLineCells(pane *terminalPane, rowIndex int) uv.Line {
	if pane == nil || pane.terminal == nil || rowIndex < 0 {
		return nil
	}
	historyLength := pane.terminal.ScrollbackLen()
	if rowIndex < historyLength {
		history := pane.terminal.Scrollback()
		if history == nil || rowIndex >= history.Len() {
			return nil
		}
		return append(uv.Line(nil), history.Line(rowIndex)...)
	}
	screenRow := rowIndex - historyLength
	if screenRow < 0 || screenRow >= pane.terminal.Height() {
		return nil
	}
	line := make(uv.Line, pane.terminal.Width())
	for x := range line {
		if cell := pane.terminal.CellAt(x, screenRow); cell != nil {
			line[x] = *cell
		} else {
			line[x] = uv.EmptyCell
		}
	}
	return line
}

func renderTerminalHighlightedLine(pane *terminalPane, rowIndex int, rules []terminalHighlightRule) string {
	line := terminalLineCells(pane, rowIndex)
	if len(line) == 0 {
		return ""
	}
	styleTerminalHighlightLine(line, rules)
	return line.Render()
}

func styleTerminalHighlightLine(line uv.Line, rules []terminalHighlightRule) {
	row := terminalSearchRowFromCells(line)
	for _, rule := range rules {
		for _, match := range rule.expression.FindAllStringIndex(row.text, -1) {
			if match[0] == match[1] {
				continue
			}
			startRune := utf8.RuneCountInString(row.text[:match[0]])
			endRune := utf8.RuneCountInString(row.text[:match[1]])
			cellMatch, ok := terminalSearchMatchForRunes(row, 0, startRune, endRune)
			if !ok {
				continue
			}
			for x := max(0, cellMatch.startCell); x < min(cellMatch.endCell, len(line)); x++ {
				if rule.foreground != nil {
					line[x].Style.Fg = rule.foreground
				}
				if rule.background != nil {
					line[x].Style.Bg = rule.background
				}
				if rule.bold {
					line[x].Style.Attrs |= uv.AttrBold
				}
			}
		}
	}
}

func terminalVisibleRowStart(pane *terminalPane, bodyHeight int) int {
	if pane == nil || pane.terminal == nil {
		return 0
	}
	if bodyHeight < 0 {
		bodyHeight = 0
	}
	historyLength := pane.terminal.ScrollbackLen()
	totalRows := historyLength + pane.terminal.Height()
	if pane.scrollbackViewing {
		return max(0, totalRows-bodyHeight-clamp(pane.scrollbackOffset, 0, terminalScrollbackMax(pane, bodyHeight)))
	}
	return historyLength + clamp(pane.verticalOffset, 0, max(0, pane.terminal.Height()-bodyHeight))
}
