package ui

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxTerminalCommandHistoryEntries = 100
	maxTerminalCommandInputBytes     = 4096
	maxPendingTerminalCommands       = 16
	maxTerminalCommandEchoBytes      = 64 << 10
	terminalCommandEchoTimeout       = 8 * time.Second
)

var sensitiveTerminalPromptPattern = regexp.MustCompile(`(?i)^(?:(?:.*[>$#%❯➜→]\s+)|(?:.*'s\s+)|(?:\[sudo\]\s*))?(?:(?:enter|new|current|confirm|sudo)\s+|请输入|新|当前|确认)*(?:password|passphrase|passcode|verification\s+code|one[- ]time\s+(?:password|code)|otp|pin|token|secret|密码|口令|验证码|令牌|密钥口令|login|username|user name)(?:\s+for\s+[^:\r\n]+)?\s*[:：]\s*$`)

type pendingTerminalCommand struct {
	command       string
	outputVersion uint64
	createdAt     time.Time
}

type terminalCommandHistoryViewState struct {
	tabID           string
	paneID          string
	index           int
	top             int
	confirmingClear bool
}

func captureTerminalCommandInput(pane *terminalPane, data []byte) {
	if pane == nil || pane.terminal == nil || pane.session == nil {
		return
	}
	prunePendingTerminalCommands(pane, time.Now())
	sensitivePrompt := terminalHasSensitiveInputPrompt(pane)
	if pane.terminal.IsAltScreen() || pane.terminal.CursorHidden() || sensitivePrompt {
		if pane.scriptRecording != nil && sensitivePrompt {
			pane.scriptRecording.skippedSensitive = true
		}
		resetTerminalCommandDraft(pane)
		return
	}

	input := strings.NewReplacer("\x1b[200~", "", "\x1b[201~", "").Replace(string(data))
	for offset := 0; offset < len(input); {
		if pane.commandInputLastCR {
			pane.commandInputLastCR = false
			if input[offset] == '\n' {
				offset++
				continue
			}
		}

		if input[offset] == 0x1b {
			sequence, size := terminalCommandEscapeSequence(input[offset:])
			if size == 0 {
				pane.commandInputInvalid = true
				offset++
				continue
			}
			switch sequence {
			case "left":
				pane.commandInputCursor = max(0, pane.commandInputCursor-1)
			case "right":
				pane.commandInputCursor = min(len(pane.commandInput), pane.commandInputCursor+1)
			case "home":
				pane.commandInputCursor = 0
			case "end":
				pane.commandInputCursor = len(pane.commandInput)
			case "delete":
				deleteTerminalCommandRune(pane)
			case "history":
				pane.commandInputInvalid = true
			}
			offset += size
			continue
		}

		value := input[offset]
		offset++
		switch value {
		case '\r', '\n':
			appendPendingTerminalCommand(pane)
			pane.commandInputLastCR = value == '\r'
		case 0x01: // Ctrl+A
			pane.commandInputCursor = 0
		case 0x05: // Ctrl+E
			pane.commandInputCursor = len(pane.commandInput)
		case 0x03: // Ctrl+C
			resetTerminalCommandDraft(pane)
		case 0x04: // Ctrl+D
			if len(pane.commandInput) == 0 {
				pane.commandInputInvalid = true
			} else {
				deleteTerminalCommandRune(pane)
			}
		case 0x08, 0x7f: // Backspace
			if pane.commandInputCursor > 0 {
				pane.commandInput = append(pane.commandInput[:pane.commandInputCursor-1], pane.commandInput[pane.commandInputCursor:]...)
				pane.commandInputCursor--
			}
		case 0x09: // Tab completion changes text without a predictable key event.
			pane.commandInputInvalid = true
		case 0x0b: // Ctrl+K
			pane.commandInput = pane.commandInput[:pane.commandInputCursor]
		case 0x0c: // Ctrl+L
			// Clear-screen shortcuts do not change the input line.
		case 0x15: // Ctrl+U
			pane.commandInput = append([]rune(nil), pane.commandInput[pane.commandInputCursor:]...)
			pane.commandInputCursor = 0
		case 0x17: // Ctrl+W
			for pane.commandInputCursor > 0 && (pane.commandInput[pane.commandInputCursor-1] == ' ' || pane.commandInput[pane.commandInputCursor-1] == '\t') {
				pane.commandInput = append(pane.commandInput[:pane.commandInputCursor-1], pane.commandInput[pane.commandInputCursor:]...)
				pane.commandInputCursor--
			}
			for pane.commandInputCursor > 0 && pane.commandInput[pane.commandInputCursor-1] != ' ' && pane.commandInput[pane.commandInputCursor-1] != '\t' {
				pane.commandInput = append(pane.commandInput[:pane.commandInputCursor-1], pane.commandInput[pane.commandInputCursor:]...)
				pane.commandInputCursor--
			}
		default:
			if value < 0x20 {
				pane.commandInputInvalid = true
				continue
			}
			if pane.commandInputInvalid {
				continue
			}
			runeValue, size := utf8.DecodeRuneInString(input[offset-1:])
			if runeValue == utf8.RuneError && size == 1 {
				pane.commandInputInvalid = true
				continue
			}
			offset += size - 1
			if len(string(pane.commandInput))+size > maxTerminalCommandInputBytes {
				pane.commandInput = nil
				pane.commandInputCursor = 0
				pane.commandInputInvalid = true
				continue
			}
			pane.commandInput = append(pane.commandInput, 0)
			copy(pane.commandInput[pane.commandInputCursor+1:], pane.commandInput[pane.commandInputCursor:len(pane.commandInput)-1])
			pane.commandInput[pane.commandInputCursor] = runeValue
			pane.commandInputCursor++
		}
	}
}

func terminalCommandEscapeSequence(input string) (string, int) {
	sequences := []struct {
		sequence string
		action   string
	}{
		{"\x1b[1;5D", "left"}, {"\x1b[1;5C", "right"},
		{"\x1b[1~", "home"}, {"\x1b[4~", "end"},
		{"\x1b[3~", "delete"}, {"\x1b[H", "home"}, {"\x1b[F", "end"},
		{"\x1bOD", "left"}, {"\x1bOC", "right"}, {"\x1bOH", "home"}, {"\x1bOF", "end"},
		{"\x1b[D", "left"}, {"\x1b[C", "right"}, {"\x1b[A", "history"}, {"\x1b[B", "history"},
	}
	for _, candidate := range sequences {
		if strings.HasPrefix(input, candidate.sequence) {
			return candidate.action, len(candidate.sequence)
		}
	}
	return "", 0
}

func deleteTerminalCommandRune(pane *terminalPane) {
	if pane == nil || pane.commandInputCursor >= len(pane.commandInput) {
		return
	}
	pane.commandInput = append(pane.commandInput[:pane.commandInputCursor], pane.commandInput[pane.commandInputCursor+1:]...)
}

func resetTerminalCommandDraft(pane *terminalPane) {
	if pane == nil {
		return
	}
	pane.commandInput = nil
	pane.commandInputCursor = 0
	pane.commandInputInvalid = false
	pane.commandInputLastCR = false
}

func appendPendingTerminalCommand(pane *terminalPane) {
	if pane == nil {
		return
	}
	command := string(pane.commandInput)
	if !pane.commandInputInvalid && strings.TrimSpace(command) != "" {
		recordTerminalScriptCommand(pane, command)
		now := time.Now()
		prunePendingTerminalCommands(pane, now)
		if terminalCursorLineEchoes(pane, command) {
			appendTerminalCommandHistory(pane, command)
		} else {
			if len(pane.pendingCommands) == 0 {
				clearTerminalCommandEchoOutput(pane)
				pane.commandEchoLine = []rune(pane.commandEchoPrompt)
				pane.commandEchoCursor = len(pane.commandEchoLine)
			}
			pane.pendingCommands = append(pane.pendingCommands, pendingTerminalCommand{
				command: command, outputVersion: pane.commandEchoVersion, createdAt: now,
			})
			if len(pane.pendingCommands) > maxPendingTerminalCommands {
				pane.pendingCommands = append([]pendingTerminalCommand(nil), pane.pendingCommands[len(pane.pendingCommands)-maxPendingTerminalCommands:]...)
			}
		}
	}
	resetTerminalCommandDraft(pane)
}

func terminalCursorLineEchoes(pane *terminalPane, command string) bool {
	if pane == nil || pane.terminal == nil || command == "" {
		return false
	}
	line := strings.TrimRight(terminalCurrentCursorLine(pane), " ")
	return terminalCommandEchoLineMatches(line, command, true)
}

func terminalCursorPromptLineEchoes(pane *terminalPane, command string) bool {
	if pane == nil || pane.terminal == nil || command == "" {
		return false
	}
	line := strings.TrimRight(terminalCurrentCursorLine(pane), " ")
	return terminalCommandEchoLineMatches(line, command, false)
}

func prunePendingTerminalCommands(pane *terminalPane, now time.Time) {
	if pane == nil {
		return
	}
	pending := pane.pendingCommands[:0]
	for _, candidate := range pane.pendingCommands {
		if now.Sub(candidate.createdAt) <= terminalCommandEchoTimeout {
			pending = append(pending, candidate)
		}
	}
	pane.pendingCommands = pending
	if len(pending) == 0 {
		clearTerminalCommandEchoOutput(pane)
	}
}

func terminalHasSensitiveInputPrompt(pane *terminalPane) bool {
	if pane == nil || pane.terminal == nil {
		return false
	}
	width, height := pane.terminal.Width(), pane.terminal.Height()
	if width <= 0 || height <= 0 {
		return false
	}
	position := pane.terminal.CursorPosition()
	firstRow := max(0, min(height-2, position.Y))
	for row := firstRow; row <= position.Y && row < height; row++ {
		cells := make(uv.Line, width)
		for column := 0; column < width; column++ {
			if cell := pane.terminal.CellAt(column, row); cell != nil {
				cells[column] = *cell
			} else {
				cells[column] = uv.EmptyCell
			}
		}
		if sensitiveTerminalPromptPattern.MatchString(strings.TrimSpace(terminalSearchRowFromCells(cells).text)) {
			return true
		}
	}
	return false
}

func observeTerminalCommandEcho(pane *terminalPane, data []byte) {
	if pane == nil || len(data) == 0 {
		return
	}
	pane.commandEchoVersion++
	prunePendingTerminalCommands(pane, time.Now())
	if len(pane.pendingCommands) == 0 {
		rememberTerminalCommandPrompt(pane)
		clearTerminalCommandEchoOutput(pane)
		return
	}
	appendTerminalCommandEchoText(pane, ansi.Strip(string(data)))
	echo := pane.commandEchoBuffer + string(pane.commandEchoLine)
	now := time.Now()
	pending := pane.pendingCommands[:0]
	for _, candidate := range pane.pendingCommands {
		if now.Sub(candidate.createdAt) > terminalCommandEchoTimeout {
			continue
		}
		if pane.commandEchoVersion > candidate.outputVersion &&
			(terminalCursorPromptLineEchoes(pane, candidate.command) || terminalCommandEchoBufferContains(echo, candidate.command)) {
			appendTerminalCommandHistory(pane, candidate.command)
			continue
		}
		pending = append(pending, candidate)
	}
	pane.pendingCommands = pending
	if len(pending) == 0 {
		rememberTerminalCommandPrompt(pane)
		clearTerminalCommandEchoOutput(pane)
	}
}

func appendTerminalCommandHistory(pane *terminalPane, command string) {
	if pane == nil || strings.TrimSpace(command) == "" {
		return
	}
	pane.commandHistory = append(pane.commandHistory, command)
	if len(pane.commandHistory) > maxTerminalCommandHistoryEntries {
		pane.commandHistory = append([]string(nil), pane.commandHistory[len(pane.commandHistory)-maxTerminalCommandHistoryEntries:]...)
	}
}

func terminalCommandEchoBufferContains(text, command string) bool {
	for _, line := range strings.Split(text, "\n") {
		if terminalCommandEchoLineMatches(line, command, false) {
			return true
		}
	}
	return false
}

func terminalCommandEchoLineMatches(line, command string, allowEmptyPrompt bool) bool {
	if command == "" || !strings.HasSuffix(line, command) {
		return false
	}
	prefix := strings.TrimSpace(strings.TrimSuffix(line, command))
	if prefix == "" {
		return allowEmptyPrompt
	}
	for _, marker := range []string{"$", "#", "%", ">", "❯", "➜", "→"} {
		index := strings.LastIndex(prefix, marker)
		if index >= 0 && strings.TrimSpace(prefix[index+len(marker):]) == "" {
			return true
		}
	}
	return strings.HasPrefix(prefix, "PS ") && strings.HasSuffix(prefix, ">")
}

func rememberTerminalCommandPrompt(pane *terminalPane) {
	if pane == nil || pane.terminal == nil || terminalHasSensitiveInputPrompt(pane) {
		return
	}
	line := strings.TrimRight(terminalCurrentCursorLine(pane), " ")
	if terminalCommandPromptLine(line) {
		pane.commandEchoPrompt = line + " "
	}
}

func terminalCommandPromptLine(line string) bool {
	if line == "" || len(line) > 256 {
		return false
	}
	for _, marker := range []string{"$", "#", "%", ">", "❯", "➜", "→"} {
		if strings.HasSuffix(line, marker) {
			return true
		}
	}
	return strings.HasPrefix(line, "PS ") && strings.HasSuffix(line, ">")
}

func terminalCurrentCursorLine(pane *terminalPane) string {
	if pane == nil || pane.terminal == nil {
		return ""
	}
	position := pane.terminal.CursorPosition()
	if position.Y < 0 || position.Y >= pane.terminal.Height() {
		return ""
	}
	cells := make(uv.Line, pane.terminal.Width())
	for column := 0; column < pane.terminal.Width(); column++ {
		if cell := pane.terminal.CellAt(column, position.Y); cell != nil {
			cells[column] = *cell
		} else {
			cells[column] = uv.EmptyCell
		}
	}
	return terminalSearchRowFromCells(cells).text
}

func appendTerminalCommandEchoText(pane *terminalPane, text string) {
	for offset := 0; offset < len(text); {
		runeValue, size := utf8.DecodeRuneInString(text[offset:])
		offset += size
		switch runeValue {
		case '\r':
			pane.commandEchoCursor = 0
		case '\n':
			pane.commandEchoBuffer += string(pane.commandEchoLine) + "\n"
			pane.commandEchoLine = nil
			pane.commandEchoCursor = 0
		case '\b', 0x7f:
			pane.commandEchoCursor = max(0, pane.commandEchoCursor-1)
		default:
			if runeValue < 0x20 || runeValue == utf8.RuneError {
				continue
			}
			if pane.commandEchoCursor < len(pane.commandEchoLine) {
				pane.commandEchoLine[pane.commandEchoCursor] = runeValue
			} else {
				pane.commandEchoLine = append(pane.commandEchoLine, runeValue)
			}
			pane.commandEchoCursor++
		}
		if len(string(pane.commandEchoLine)) > maxTerminalCommandEchoBytes {
			pane.commandEchoLine = nil
			pane.commandEchoCursor = 0
		}
	}
	if len(pane.commandEchoBuffer) > maxTerminalCommandEchoBytes {
		pane.commandEchoBuffer = pane.commandEchoBuffer[len(pane.commandEchoBuffer)-maxTerminalCommandEchoBytes:]
	}
}

func clearTerminalCommandEcho(pane *terminalPane) {
	if pane == nil {
		return
	}
	pane.commandEchoPrompt = ""
	clearTerminalCommandEchoOutput(pane)
}

func clearTerminalCommandEchoOutput(pane *terminalPane) {
	if pane == nil {
		return
	}
	pane.commandEchoBuffer = ""
	pane.commandEchoLine = nil
	pane.commandEchoCursor = 0
}

func (m *modelState) openTerminalCommandHistory() {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil {
		m.statusText = "没有可查看命令历史的终端 pane"
		m.statusErr = true
		return
	}
	m.commandHistoryView = &terminalCommandHistoryViewState{tabID: tab.id, paneID: pane.id}
}

func (m modelState) terminalCommandHistoryPane() *terminalPane {
	if m.commandHistoryView == nil {
		return nil
	}
	tab := m.tabByID(m.commandHistoryView.tabID)
	if tab == nil {
		return nil
	}
	return findPane(tab.root, m.commandHistoryView.paneID)
}

func (m modelState) terminalCommandHistoryView() string {
	state := m.commandHistoryView
	pane := m.terminalCommandHistoryPane()
	if state == nil || pane == nil {
		return warningStyle.Render("命令历史已不可用 · Esc 返回")
	}
	lines := []string{}
	if state.confirmingClear {
		lines = append(lines,
			warningStyle.Render("确定清空此 pane 的命令历史？"),
			mutedStyle.Render("按 Y 确认，其他键取消。"),
		)
	} else if len(pane.commandHistory) == 0 {
		lines = append(lines,
			mutedStyle.Render("暂无已回显的命令。"),
			mutedStyle.Render("命令会在 Shell 回显后记入历史。"),
		)
	} else {
		visibleRows := max(1, min(12, m.height-11))
		state.index = clamp(state.index, 0, len(pane.commandHistory)-1)
		state.top = clamp(state.top, 0, max(0, len(pane.commandHistory)-visibleRows))
		for row := state.top; row < len(pane.commandHistory) && row < state.top+visibleRows; row++ {
			historyIndex := len(pane.commandHistory) - 1 - row
			line := fitText(pane.commandHistory[historyIndex], max(1, m.width-18))
			entry := fitLine(strings.TrimSpace(line), max(1, m.width-18))
			if row == state.index {
				lines = append(lines, selectedStyle.Render("› "+entry))
			} else {
				lines = append(lines, "  "+entry)
			}
		}
	}
	if len(pane.commandHistory) > 0 && !state.confirmingClear {
		lines = append(lines, "", mutedStyle.Render("↑/↓ 或 j/k 选择 · Enter/C 用 OSC 52 复制 · x 清空"))
	}
	lines = append(lines,
		warningStyle.Render("命令可能包含明文秘密；历史仅保存在当前进程内。"),
		mutedStyle.Render("仅记录终端回显匹配的命令 · Esc 返回"),
	)
	return m.modalView("命令历史 · "+pane.host.Name, lines, min(110, max(54, m.width-4)))
}

func (m *modelState) updateTerminalCommandHistoryKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.commandHistoryView
	if state == nil {
		return m, nil
	}
	pane := m.terminalCommandHistoryPane()
	if state.confirmingClear {
		state.confirmingClear = false
		if pane != nil && strings.EqualFold(msg.Key().Text, "y") {
			pane.commandHistory = nil
			clearTerminalCommandCapture(pane)
		}
		return m, nil
	}
	if pane == nil {
		m.commandHistoryView = nil
		return m, nil
	}
	key := msg.Key()
	visibleRows := max(1, min(12, m.height-11))
	last := max(0, len(pane.commandHistory)-1)
	switch {
	case key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "q"):
		m.commandHistoryView = nil
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		state.index = max(0, state.index-1)
		state.top = min(state.top, state.index)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		state.index = min(last, state.index+1)
		if state.index >= state.top+visibleRows {
			state.top = state.index - visibleRows + 1
		}
	case key.Code == tea.KeyHome:
		state.index, state.top = 0, 0
	case key.Code == tea.KeyEnd:
		state.index = last
		state.top = max(0, last-visibleRows+1)
	case len(pane.commandHistory) > 0 && (key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "c")):
		command := pane.commandHistory[len(pane.commandHistory)-1-state.index]
		m.commandHistoryView = nil
		m.statusText = "命令已复制到剪贴板，可粘贴回终端"
		m.statusErr = false
		return m, tea.SetClipboard(command)
	case len(pane.commandHistory) > 0 && strings.EqualFold(key.Text, "x"):
		state.confirmingClear = true
	}
	return m, nil
}

func clearTerminalCommandCapture(pane *terminalPane) {
	if pane == nil {
		return
	}
	pane.pendingCommands = nil
	clearTerminalCommandEcho(pane)
	resetTerminalCommandDraft(pane)
}
