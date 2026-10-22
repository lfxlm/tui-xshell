package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type composePaneState struct {
	tabID         string
	activePaneID  string
	targetPaneIDs []string
	syncInput     bool
	lines         []string
	row           int
	column        int
	top           int
	errorText     string
}

func (m *modelState) openComposePane() {
	tab := m.activeTab()
	pane := m.activeTerminalPane()
	if tab == nil || pane == nil || pane.session == nil {
		m.statusText = "当前 pane 没有已连接的终端，无法打开多行输入面板"
		m.statusErr = true
		return
	}

	targetIDs := []string{pane.id}
	if tab.syncInput {
		targetIDs = targetIDs[:0]
		for _, target := range collectPanes(tab.root) {
			if target != nil && target.session != nil {
				targetIDs = append(targetIDs, target.id)
			}
		}
	}
	m.composePane = &composePaneState{
		tabID: tab.id, activePaneID: pane.id,
		targetPaneIDs: targetIDs, syncInput: tab.syncInput,
		lines: []string{""},
	}
}

func (m *modelState) updateComposePaneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.composePane
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if key.Code == tea.KeyEscape {
		m.composePane = nil
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 && key.Code == tea.KeyEnter {
		m.sendComposePane()
		return m, nil
	}
	if len(state.lines) == 0 {
		state.lines = []string{""}
	}
	line := []rune(state.lines[state.row])
	switch key.Code {
	case tea.KeyEnter:
		left, right := string(line[:state.column]), string(line[state.column:])
		state.lines[state.row] = left
		insertComposeLineAfter(state, state.row, right)
		state.row++
		state.column = 0
	case tea.KeyBackspace:
		if state.column > 0 {
			line = append(line[:state.column-1], line[state.column:]...)
			state.column--
			state.lines[state.row] = string(line)
		} else if state.row > 0 {
			previousLength := len([]rune(state.lines[state.row-1]))
			state.lines[state.row-1] += state.lines[state.row]
			state.lines = append(state.lines[:state.row], state.lines[state.row+1:]...)
			state.row--
			state.column = previousLength
		}
	case tea.KeyDelete:
		if state.column < len(line) {
			line = append(line[:state.column], line[state.column+1:]...)
			state.lines[state.row] = string(line)
		} else if state.row < len(state.lines)-1 {
			state.lines[state.row] += state.lines[state.row+1]
			state.lines = append(state.lines[:state.row+1], state.lines[state.row+2:]...)
		}
	case tea.KeyLeft:
		if state.column > 0 {
			state.column--
		} else if state.row > 0 {
			state.row--
			state.column = len([]rune(state.lines[state.row]))
		}
	case tea.KeyRight:
		if state.column < len(line) {
			state.column++
		} else if state.row < len(state.lines)-1 {
			state.row++
			state.column = 0
		}
	case tea.KeyUp:
		if state.row > 0 {
			state.row--
			state.column = min(state.column, len([]rune(state.lines[state.row])))
		}
	case tea.KeyDown:
		if state.row < len(state.lines)-1 {
			state.row++
			state.column = min(state.column, len([]rune(state.lines[state.row])))
		}
	case tea.KeyHome:
		state.column = 0
	case tea.KeyEnd:
		state.column = len(line)
	case tea.KeyTab:
		state.insertText("  ")
	default:
		if key.Mod == 0 && key.Text != "" {
			state.insertText(key.Text)
		}
	}
	state.errorText = ""
	state.keepCursorVisible(m.composeVisibleRows())
	return m, nil
}

func (m *modelState) appendComposePanePaste(text string) {
	state := m.composePane
	if state == nil {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	state.insertText(text)
	state.errorText = ""
	state.keepCursorVisible(m.composeVisibleRows())
}

func (state *composePaneState) insertText(text string) {
	if state == nil || len(state.lines) == 0 {
		return
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(text, "\n")
	line := []rune(state.lines[state.row])
	insert := []rune(parts[0])
	line = append(line, make([]rune, len(insert))...)
	copy(line[state.column+len(insert):], line[state.column:len(line)-len(insert)])
	copy(line[state.column:], insert)
	state.lines[state.row] = string(line)
	state.column += len(insert)
	for _, part := range parts[1:] {
		line = []rune(state.lines[state.row])
		left, right := string(line[:state.column]), string(line[state.column:])
		state.lines[state.row] = left
		insertComposeLineAfter(state, state.row, part+right)
		state.row++
		state.column = len([]rune(part))
	}
}

func insertComposeLineAfter(state *composePaneState, row int, line string) {
	oldLength := len(state.lines)
	state.lines = append(state.lines, "")
	copy(state.lines[row+2:], state.lines[row+1:oldLength])
	state.lines[row+1] = line
}

func (state *composePaneState) keepCursorVisible(height int) {
	if state == nil || height <= 0 {
		return
	}
	if state.row < state.top {
		state.top = state.row
	} else if state.row >= state.top+height {
		state.top = state.row - height + 1
	}
	state.top = max(0, state.top)
}

func (m modelState) composeVisibleRows() int {
	return max(1, min(8, m.height-18))
}

func (m *modelState) sendComposePane() {
	state := m.composePane
	if state == nil {
		return
	}
	text := strings.Join(state.lines, "\r\n")
	if strings.TrimSpace(text) == "" {
		state.errorText = "请先输入要发送的内容"
		return
	}
	tab := m.tabByID(state.tabID)
	if tab == nil {
		state.errorText = "目标标签页已关闭"
		return
	}
	writeErrors, sent := 0, 0
	for _, paneID := range state.targetPaneIDs {
		pane := findPane(tab.root, paneID)
		if pane == nil || pane.session == nil {
			continue
		}
		payload := text
		if pane.bracketedPaste {
			payload = "\x1b[200~" + payload + "\x1b[201~"
		}
		wireData, err := encodeTerminalInput(pane, []byte(payload))
		if err != nil {
			pane.statusText = "发送多行输入失败：" + err.Error()
			pane.statusErr = true
			writeErrors++
			continue
		}
		if err := pane.session.Write(wireData); err != nil {
			pane.statusText = "发送多行输入失败：" + err.Error()
			pane.statusErr = true
			writeErrors++
			continue
		}
		captureTerminalCommandInput(pane, []byte(text))
		sent++
	}
	if sent == 0 {
		state.errorText = "没有可用的目标 pane；请取消后重新打开面板"
		return
	}
	if writeErrors > 0 {
		m.statusText = fmt.Sprintf("多行输入已发送到 %d 个 pane，%d 个 pane 发送失败", sent, writeErrors)
		m.statusErr = true
	} else {
		target := "当前 pane"
		if state.syncInput {
			target = fmt.Sprintf("当前标签页内 %d 个 pane", sent)
		}
		m.statusText = "多行输入已发送到" + target + "；没有额外补回车"
		m.statusErr = false
	}
	m.composePane = nil
}

func (m modelState) composePaneView() string {
	state := m.composePane
	if state == nil {
		return ""
	}
	width := max(1, min(92, m.width-20))
	visibleRows := m.composeVisibleRows()
	state.keepCursorVisible(visibleRows)
	lines := []string{mutedStyle.Render("先编辑并预览，再按 Ctrl+Enter 发送；不会额外补回车。Esc 放弃。"), ""}
	target := "当前 pane：" + state.activePaneID
	if state.syncInput {
		target = fmt.Sprintf("同步目标：当前标签页内 %d 个已连接 pane（开启时固定）", len(state.targetPaneIDs))
	}
	lines = append(lines, accentStyle.Render(target), "")
	end := min(len(state.lines), state.top+visibleRows)
	for row := state.top; row < end; row++ {
		prefix := fmt.Sprintf("%02d │ ", row+1)
		content := quickCommandEditorField(state.lines[row], state.column, row == state.row, width-lipgloss.Width(prefix))
		line := mutedStyle.Render(prefix) + content
		if row == state.row {
			line = selectedStyle.Render(prefix) + content
		}
		lines = append(lines, line)
	}
	if len(state.lines) > visibleRows {
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("第 %d-%d / %d 行", state.top+1, end, len(state.lines))))
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	lines = append(lines, "", mutedStyle.Render("Enter 换行 · Tab 缩进 · 方向键/Home/End 编辑 · 粘贴保留多行 · Ctrl+Enter 发送"))
	return m.modalView("Compose Pane · 多行输入", lines, min(108, m.width-4))
}
