package ui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

const maxTerminalSearchResults = 10000

type terminalSearchMatch struct {
	row       int
	startCell int
	endCell   int
}

type terminalSearchCellSpan struct {
	startRune int
	endRune   int
	startCell int
	endCell   int
}

type terminalSearchRow struct {
	text  string
	spans []terminalSearchCellSpan
}

type terminalSearchState struct {
	query     string
	matches   []terminalSearchMatch
	index     int
	regex     bool
	truncated bool
	err       string
	editing   bool
	dirty     bool
}

func beginTerminalSearch(pane *terminalPane) {
	if pane == nil || pane.terminal == nil {
		return
	}
	pane.scrollbackViewing = true
	pane.scrollbackOffset = 0
	pane.search = &terminalSearchState{editing: true}
}

func terminalBufferTextRows(pane *terminalPane) []string {
	searchRows := terminalBufferSearchRows(pane)
	rows := make([]string, 0, len(searchRows))
	for _, row := range searchRows {
		rows = append(rows, row.text)
	}
	return rows
}

func terminalBufferSearchRows(pane *terminalPane) []terminalSearchRow {
	if pane == nil || pane.terminal == nil {
		return nil
	}
	rows := make([]terminalSearchRow, 0, pane.terminal.ScrollbackLen()+pane.terminal.Height())
	if history := pane.terminal.Scrollback(); history != nil {
		for rowIndex := 0; rowIndex < history.Len(); rowIndex++ {
			rows = append(rows, terminalSearchRowFromCells(history.Line(rowIndex)))
		}
	}
	for y := 0; y < pane.terminal.Height(); y++ {
		line := make(uv.Line, pane.terminal.Width())
		for x := 0; x < pane.terminal.Width(); x++ {
			cell := pane.terminal.CellAt(x, y)
			if cell != nil {
				line[x] = *cell
			} else {
				line[x] = uv.EmptyCell
			}
		}
		rows = append(rows, terminalSearchRowFromCells(line))
	}
	return rows
}

func terminalSearchRowFromCells(cells uv.Line) terminalSearchRow {
	var text strings.Builder
	spans := make([]terminalSearchCellSpan, 0, len(cells))
	runeOffset := 0
	for x, cell := range cells {
		if cell.Width == 0 {
			continue
		}
		content := cell.Content
		if content == "" {
			content = " "
		}
		cellRunes := []rune(content)
		if len(cellRunes) == 0 {
			continue
		}
		text.WriteString(content)
		cellWidth := max(1, cell.Width)
		spans = append(spans, terminalSearchCellSpan{
			startRune: runeOffset,
			endRune:   runeOffset + len(cellRunes),
			startCell: x,
			endCell:   min(len(cells), x+cellWidth),
		})
		runeOffset += len(cellRunes)
	}
	return terminalSearchRow{text: text.String(), spans: spans}
}

func findTerminalMatches(rows []string, query string) []int {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil
	}
	matches := make([]int, 0)
	for index, row := range rows {
		if strings.Contains(strings.ToLower(row), needle) {
			matches = append(matches, index)
		}
	}
	return matches
}

func findTerminalSearchMatches(rows []terminalSearchRow, query string) ([]terminalSearchMatch, bool) {
	needle := []rune(strings.TrimSpace(query))
	if len(needle) == 0 {
		return nil, false
	}
	matches := make([]terminalSearchMatch, 0)
	for rowIndex, row := range rows {
		rowRunes := []rune(row.text)
		for start := 0; start+len(needle) <= len(rowRunes); start++ {
			if !strings.EqualFold(string(rowRunes[start:start+len(needle)]), string(needle)) {
				continue
			}
			if match, ok := terminalSearchMatchForRunes(row, rowIndex, start, start+len(needle)); ok {
				if len(matches) == maxTerminalSearchResults {
					return matches, true
				}
				matches = append(matches, match)
				start += len(needle) - 1 // Move past this match; results do not overlap.
			}
		}
	}
	return matches, false
}

func findTerminalRegexSearchMatches(rows []terminalSearchRow, query string) ([]terminalSearchMatch, bool, error) {
	if query == "" {
		return nil, false, nil
	}
	expression, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil, false, err
	}
	matches := make([]terminalSearchMatch, 0)
	for rowIndex, row := range rows {
		for _, byteRange := range expression.FindAllStringIndex(row.text, -1) {
			if byteRange[0] == byteRange[1] {
				continue
			}
			startRune := utf8.RuneCountInString(row.text[:byteRange[0]])
			endRune := utf8.RuneCountInString(row.text[:byteRange[1]])
			if match, ok := terminalSearchMatchForRunes(row, rowIndex, startRune, endRune); ok {
				if len(matches) == maxTerminalSearchResults {
					return matches, true, nil
				}
				matches = append(matches, match)
			}
		}
	}
	return matches, false, nil
}

func terminalSearchMatchForRunes(row terminalSearchRow, rowIndex, startRune, endRune int) (terminalSearchMatch, bool) {
	startCell, endCell := -1, -1
	for _, span := range row.spans {
		if span.endRune <= startRune || span.startRune >= endRune {
			continue
		}
		if startCell == -1 || span.startCell < startCell {
			startCell = span.startCell
		}
		endCell = max(endCell, span.endCell)
	}
	if startCell < 0 || endCell <= startCell {
		return terminalSearchMatch{}, false
	}
	return terminalSearchMatch{row: rowIndex, startCell: startCell, endCell: endCell}, true
}

func refreshTerminalSearch(pane *terminalPane) {
	if pane == nil || pane.search == nil {
		return
	}
	if pane.search.regex {
		matches, truncated, err := findTerminalRegexSearchMatches(terminalBufferSearchRows(pane), pane.search.query)
		pane.search.matches = matches
		pane.search.truncated = truncated
		if err != nil {
			pane.search.err = err.Error()
		} else {
			pane.search.err = ""
		}
	} else {
		pane.search.matches, pane.search.truncated = findTerminalSearchMatches(terminalBufferSearchRows(pane), pane.search.query)
		pane.search.err = ""
	}
	pane.search.index = clamp(pane.search.index, 0, max(0, len(pane.search.matches)-1))
	pane.search.dirty = false
}

func renderTerminalSearchMatchLine(pane *terminalPane, match terminalSearchMatch) string {
	if pane == nil || pane.terminal == nil {
		return ""
	}
	historyLength := pane.terminal.ScrollbackLen()
	var line uv.Line
	if match.row < historyLength {
		history := pane.terminal.Scrollback()
		if history == nil || match.row < 0 || match.row >= history.Len() {
			return ""
		}
		line = append(uv.Line(nil), history.Line(match.row)...)
	} else {
		screenRow := match.row - historyLength
		if screenRow < 0 || screenRow >= pane.terminal.Height() {
			return ""
		}
		line = make(uv.Line, pane.terminal.Width())
		for x := range line {
			if cell := pane.terminal.CellAt(x, screenRow); cell != nil {
				line[x] = *cell
			} else {
				line[x] = uv.EmptyCell
			}
		}
	}
	for x := max(0, match.startCell); x < min(match.endCell, len(line)); x++ {
		if line[x].IsZero() {
			continue
		}
		line[x].Style.Fg = palette.canvas
		line[x].Style.Bg = palette.accent
		line[x].Style.Attrs |= uv.AttrBold
	}
	return line.Render()
}

func appendTerminalSearchText(pane *terminalPane, text string) {
	if pane == nil || pane.search == nil || !pane.search.editing {
		return
	}
	query := []rune(pane.search.query)
	for _, character := range text {
		if len(query) >= 256 {
			break
		}
		if !unicode.IsControl(character) && character != '\n' && character != '\r' {
			query = append(query, character)
		}
	}
	pane.search.query = string(query)
}

func setTerminalSearchMatch(pane *terminalPane, matchRow, bodyHeight int) {
	if pane == nil || pane.terminal == nil {
		return
	}
	historyLength := pane.terminal.ScrollbackLen()
	totalRows := historyLength + pane.terminal.Height()
	bodyHeight = max(1, bodyHeight)
	start := max(0, matchRow-bodyHeight/2)
	maxStart := max(0, totalRows-bodyHeight)
	start = clamp(start, 0, maxStart)
	pane.scrollbackOffset = clamp(totalRows-bodyHeight-start, 0, historyLength)
	pane.scrollbackViewing = true
}

func moveTerminalSearch(pane *terminalPane, delta, bodyHeight int) {
	if pane == nil || pane.search == nil {
		return
	}
	if pane.search.dirty {
		refreshTerminalSearch(pane)
	}
	if len(pane.search.matches) == 0 {
		return
	}
	pane.search.index = (pane.search.index + delta%len(pane.search.matches) + len(pane.search.matches)) % len(pane.search.matches)
	setTerminalSearchMatch(pane, pane.search.matches[pane.search.index].row, bodyHeight)
}

func (m *modelState) updateTerminalSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pane := m.activeTerminalPane()
	if pane == nil || pane.search == nil {
		return m, nil
	}
	key := msg.Key()
	if pane.search.editing {
		if key.Mod&tea.ModCtrl != 0 && (key.Code == 'r' || key.Code == 'R') {
			pane.search.regex = !pane.search.regex
			pane.search.err = ""
			return m, nil
		}
		switch key.Code {
		case tea.KeyEscape:
			pane.search = nil
			pane.scrollbackViewing = false
			pane.scrollbackOffset = 0
		case tea.KeyEnter:
			pane.search.editing = false
			refreshTerminalSearch(pane)
			pane.search.index = 0
			moveTerminalSearch(pane, 0, m.activePaneBodyHeight())
		case tea.KeyBackspace:
			query := []rune(pane.search.query)
			if len(query) > 0 {
				pane.search.query = string(query[:len(query)-1])
			}
			pane.search.err = ""
		default:
			if key.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
				appendTerminalSearchText(pane, key.Text)
			}
		}
		return m, nil
	}
	if key.Code == tea.KeyEscape || key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "q") {
		pane.search = nil
		pane.scrollbackViewing = false
		pane.scrollbackOffset = 0
		return m, nil
	}
	switch key.Text {
	case "n":
		moveTerminalSearch(pane, 1, m.activePaneBodyHeight())
	case "N":
		moveTerminalSearch(pane, -1, m.activePaneBodyHeight())
	case "e", "E":
		pane.search.editing = true
	default:
		switch key.Code {
		case tea.KeyUp:
			moveTerminalSearch(pane, 1, m.activePaneBodyHeight())
		case tea.KeyDown:
			moveTerminalSearch(pane, -1, m.activePaneBodyHeight())
		case tea.KeyHome:
			if pane.search.dirty {
				refreshTerminalSearch(pane)
			}
			if len(pane.search.matches) > 0 {
				pane.search.index = 0
				setTerminalSearchMatch(pane, pane.search.matches[0].row, m.activePaneBodyHeight())
			}
		case tea.KeyEnd:
			if pane.search.dirty {
				refreshTerminalSearch(pane)
			}
			if len(pane.search.matches) > 0 {
				pane.search.index = len(pane.search.matches) - 1
				setTerminalSearchMatch(pane, pane.search.matches[pane.search.index].row, m.activePaneBodyHeight())
			}
		}
	}
	return m, nil
}
