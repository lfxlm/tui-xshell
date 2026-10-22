package ui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

const maxTerminalClipboardBytes = 1 << 20

type terminalSelectionPoint struct {
	row int
	col int
}

type terminalClipboardPasteRequest struct {
	tabID   string
	paneIDs []string
}

type terminalClipboardCopyCompletedMsg struct {
	text string
	err  error
}

func (m *modelState) beginTerminalClipboardPaste() tea.Cmd {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if !m.workspaceVisible || tab == nil || pane == nil || pane.session == nil {
		return nil
	}
	if m.clipboardPasteRequest != nil {
		m.statusText = "正在读取剪贴板，请稍候"
		m.statusErr = false
		return nil
	}

	destinations := []*terminalPane{pane}
	if tab.syncInput {
		destinations = collectPanes(tab.root)
	}
	paneIDs := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		if destination != nil && destination.session != nil {
			paneIDs = append(paneIDs, destination.id)
		}
	}
	if len(paneIDs) == 0 {
		return nil
	}
	m.clipboardPasteRequest = &terminalClipboardPasteRequest{tabID: tab.id, paneIDs: paneIDs}
	m.statusText = "正在读取终端剪贴板…"
	m.statusErr = false
	return func() tea.Msg { return tea.ReadClipboard() }
}

func (m *modelState) handleTerminalClipboard(msg tea.ClipboardMsg) tea.Cmd {
	request := m.clipboardPasteRequest
	m.clipboardPasteRequest = nil
	if request == nil {
		return nil
	}
	if !m.workspaceVisible {
		return nil
	}
	if len(msg.Content) > maxTerminalClipboardBytes {
		m.statusText = "剪贴板内容超过 1 MiB 限制，未发送"
		m.statusErr = true
		return nil
	}
	var tab *workspaceTab
	for _, candidate := range m.tabs {
		if candidate.id == request.tabID {
			tab = candidate
			break
		}
	}
	if tab == nil {
		return nil
	}
	writeErrors := 0
	for _, paneID := range request.paneIDs {
		pane := findPane(tab.root, paneID)
		if pane == nil || pane.session == nil {
			continue
		}
		paste := msg.Content
		if pane.bracketedPaste {
			paste = "\x1b[200~" + paste + "\x1b[201~"
		}
		wirePaste, err := encodeTerminalInput(pane, []byte(paste))
		if err != nil {
			pane.statusText = "发送终端剪贴板失败：" + err.Error()
			pane.statusErr = true
			writeErrors++
			continue
		}
		if err := pane.session.Write(wirePaste); err != nil {
			pane.statusText = "发送终端剪贴板失败：" + err.Error()
			pane.statusErr = true
			writeErrors++
		} else {
			captureTerminalCommandInput(pane, []byte(paste))
		}
	}
	if writeErrors == 0 {
		m.statusText = "剪贴板内容已发送"
		m.statusErr = false
	}
	return nil
}

// terminalSelection holds a stable snapshot of the visible terminal cells so
// output arriving during a drag cannot change the text being selected.
type terminalSelection struct {
	rows     []uv.Line
	anchor   terminalSelectionPoint
	focus    terminalSelectionPoint
	dragging bool
	column   bool
}

func snapshotTerminalViewport(pane *terminalPane, bodyHeight int) []uv.Line {
	if pane == nil || pane.terminal == nil || bodyHeight <= 0 {
		return nil
	}
	width := max(1, pane.terminal.Width())
	screenRows := make([]uv.Line, pane.terminal.Height())
	for y := range screenRows {
		screenRows[y] = make(uv.Line, width)
		for x := 0; x < width; x++ {
			if cell := pane.terminal.CellAt(x, y); cell != nil {
				screenRows[y][x] = *cell
			} else {
				screenRows[y][x] = uv.EmptyCell
			}
		}
	}

	visible := screenRows
	if pane.scrollbackViewing {
		history := pane.terminal.Scrollback()
		historyLength := 0
		if history != nil {
			historyLength = history.Len()
		}
		allRows := make([]uv.Line, 0, historyLength+len(screenRows))
		for row := 0; row < historyLength; row++ {
			allRows = append(allRows, cloneTerminalLine(history.Line(row), width))
		}
		allRows = append(allRows, screenRows...)
		offset := clamp(pane.scrollbackOffset, 0, terminalScrollbackMax(pane, bodyHeight))
		start := max(0, len(allRows)-bodyHeight-offset)
		end := min(start+bodyHeight, len(allRows))
		visible = allRows[start:end]
	} else {
		start := clamp(pane.verticalOffset, 0, max(0, len(screenRows)-bodyHeight))
		end := min(start+bodyHeight, len(screenRows))
		visible = screenRows[start:end]
	}

	rows := make([]uv.Line, bodyHeight)
	for row := range rows {
		if row < len(visible) {
			rows[row] = cloneTerminalLine(visible[row], width)
		} else {
			rows[row] = blankTerminalLine(width)
		}
	}
	return rows
}

func cloneTerminalLine(line uv.Line, width int) uv.Line {
	result := make(uv.Line, width)
	for x := range result {
		if x < len(line) {
			result[x] = line[x]
		} else {
			result[x] = uv.EmptyCell
		}
	}
	return result
}

func blankTerminalLine(width int) uv.Line {
	line := make(uv.Line, width)
	for x := range line {
		line[x] = uv.EmptyCell
	}
	return line
}

func (selection *terminalSelection) setFocus(row, col int) {
	if selection == nil || len(selection.rows) == 0 {
		return
	}
	row = clamp(row, 0, len(selection.rows)-1)
	width := len(selection.rows[row])
	col = clamp(col, 0, max(0, width-1))
	if width > 0 && selection.rows[row][col].Width == 0 && col > 0 {
		col--
	}
	selection.focus = terminalSelectionPoint{row: row, col: col}
}

func (selection *terminalSelection) RenderRows() []string {
	if selection == nil {
		return nil
	}
	start, end := selection.bounds()
	rows := make([]string, len(selection.rows))
	for rowIndex, original := range selection.rows {
		line := cloneTerminalLine(original, len(original))
		from, to, selected := selection.columnsForRow(rowIndex, start, end)
		if selected {
			for col := from; col <= to && col < len(line); col++ {
				target := col
				if line[target].Width == 0 && target > 0 {
					target--
				}
				line[target].Style.Attrs |= uv.AttrReverse
			}
		}
		rows[rowIndex] = line.Render()
	}
	return rows
}

func (selection *terminalSelection) Text() string {
	if selection == nil || len(selection.rows) == 0 {
		return ""
	}
	start, end := selection.bounds()
	fromRow, toRow := start.row, end.row
	if selection.column {
		fromRow, toRow = min(start.row, end.row), max(start.row, end.row)
	}
	var text strings.Builder
	for rowIndex := fromRow; rowIndex <= toRow; rowIndex++ {
		from, to, selected := selection.columnsForRow(rowIndex, start, end)
		if selected {
			line := selection.rows[rowIndex]
			var rowText strings.Builder
			for col := from; col <= to && col < len(line); col++ {
				cell := line[col]
				if cell.Width == 0 {
					continue
				}
				if cell.Content == "" {
					rowText.WriteByte(' ')
				} else {
					rowText.WriteString(cell.Content)
				}
			}
			text.WriteString(strings.TrimRight(rowText.String(), " "))
		}
		if rowIndex < toRow {
			text.WriteByte('\n')
		}
	}
	return text.String()
}

func (selection *terminalSelection) bounds() (terminalSelectionPoint, terminalSelectionPoint) {
	start, end := selection.anchor, selection.focus
	if start.row > end.row || (start.row == end.row && start.col > end.col) {
		start, end = end, start
	}
	return start, end
}

func (selection *terminalSelection) columnsForRow(row int, start, end terminalSelectionPoint) (int, int, bool) {
	if row < 0 || row >= len(selection.rows) || len(selection.rows[row]) == 0 {
		return 0, -1, false
	}
	if selection.column {
		return min(start.col, end.col), max(start.col, end.col), row >= min(start.row, end.row) && row <= max(start.row, end.row)
	}
	if row < start.row || row > end.row {
		return 0, -1, false
	}
	from, to := 0, len(selection.rows[row])-1
	if row == start.row {
		from = start.col
	}
	if row == end.row {
		to = end.col
	}
	return clamp(from, 0, len(selection.rows[row])-1), clamp(to, 0, len(selection.rows[row])-1), true
}

func beginTerminalSelection(pane *terminalPane, bodyHeight, row, col int, column bool) bool {
	rows := snapshotTerminalViewport(pane, bodyHeight)
	if len(rows) == 0 {
		return false
	}
	selection := &terminalSelection{rows: rows, column: column, dragging: true}
	selection.setFocus(row, col)
	selection.anchor = selection.focus
	pane.selection = selection
	return true
}

// selectTerminalWord selects the contiguous Unicode word around a terminal
// cell. Punctuation and whitespace are selected as a single cell so a double
// click on a delimiter never unexpectedly absorbs neighboring words.
func selectTerminalWord(pane *terminalPane, bodyHeight, row, col int) bool {
	rows := snapshotTerminalViewport(pane, bodyHeight)
	if row < 0 || row >= len(rows) || len(rows[row]) == 0 {
		return false
	}
	line := rows[row]
	col = clamp(col, 0, len(line)-1)
	if line[col].Width == 0 && col > 0 {
		col--
	}
	if !terminalWordCell(line[col]) {
		pane.selection = &terminalSelection{
			rows:   rows,
			anchor: terminalSelectionPoint{row: row, col: col},
			focus:  terminalSelectionPoint{row: row, col: col},
		}
		return true
	}

	start, end := col, col
	for start > 0 {
		previous := start - 1
		if line[previous].Width == 0 {
			previous--
		}
		if previous < 0 || !terminalWordCell(line[previous]) {
			break
		}
		start = previous
	}
	for next := end + max(1, line[end].Width); next < len(line); {
		if line[next].Width == 0 {
			next++
			continue
		}
		if !terminalWordCell(line[next]) {
			break
		}
		end = next
		next += max(1, line[next].Width)
	}
	pane.selection = &terminalSelection{
		rows:   rows,
		anchor: terminalSelectionPoint{row: row, col: start},
		focus:  terminalSelectionPoint{row: row, col: end},
	}
	return true
}

func terminalWordCell(cell uv.Cell) bool {
	if cell.Content == "" {
		return false
	}
	for _, char := range cell.Content {
		if char != '_' && !unicode.IsLetter(char) && !unicode.IsNumber(char) && !unicode.IsMark(char) {
			return false
		}
	}
	return true
}

func terminalSelectionClipboardText(selection *terminalSelection) (string, string) {
	text := selection.Text()
	if text == "" {
		return "", "选区为空"
	}
	if len(text) > maxTerminalClipboardBytes {
		return "", "选区超过剪贴板上限（1 MiB）"
	}
	return text, ""
}

func terminalClipboardCopyShortcut(key tea.Key) bool {
	return terminalClipboardKeyShortcut(key, 'c', tea.ModShift, tea.ModCtrl) ||
		terminalClipboardKeyShortcut(key, 'c', tea.ModShift, tea.ModSuper)
}

func terminalClipboardPasteShortcut(key tea.Key) bool {
	return terminalClipboardKeyShortcut(key, 'v', tea.ModShift, tea.ModCtrl) ||
		terminalClipboardKeyShortcut(key, 'v', 0, tea.ModSuper)
}

func terminalClipboardKeyShortcut(key tea.Key, letter rune, extra, primary tea.KeyMod) bool {
	code := key.Code
	if code >= 'A' && code <= 'Z' {
		code += 'a' - 'A'
	}
	return code == letter && key.Mod&primary != 0 && key.Mod&extra == extra
}

func (m *modelState) copyTerminalSelection(pane *terminalPane) tea.Cmd {
	if pane == nil || pane.selection == nil {
		return nil
	}
	text, errText := terminalSelectionClipboardText(pane.selection)
	if errText != "" {
		m.statusText = errText
		m.statusErr = true
		return nil
	}
	m.statusText = "正在复制选区…"
	m.statusErr = false
	return writeTerminalSystemClipboard(text)
}

func (m *modelState) handleTerminalClipboardCopyCompleted(msg terminalClipboardCopyCompletedMsg) tea.Cmd {
	if msg.err == nil {
		m.statusText = "已复制到系统剪贴板"
		m.statusErr = false
		return nil
	}
	m.statusText = "本机剪贴板不可用，已尝试 OSC 52 复制"
	m.statusErr = false
	return tea.SetClipboard(msg.text)
}
