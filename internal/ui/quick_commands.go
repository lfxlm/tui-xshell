package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"tui-ssh-workbench/internal/model"
)

type quickCommandManagerState struct {
	commands         []model.QuickCommand
	index            int
	loading          bool
	editing          bool
	commandID        string
	name             string
	command          string
	field            int // 0: name, 1: command text
	cursor           int // rune offset in the active field
	confirmingDelete bool
	saving           bool
	errorText        string
}

type quickCommandsLoadedMsg struct {
	commands []model.QuickCommand
	err      error
}

type quickCommandSavedMsg struct {
	command model.QuickCommand
	err     error
}

type quickCommandDeletedMsg struct {
	id  string
	err error
}

func (m *modelState) openQuickCommandManager() tea.Cmd {
	if _, ok := m.catalog.(quickCommandCatalog); !ok {
		m.statusText = "当前目录存储不支持快速命令"
		m.statusErr = true
		return nil
	}
	m.quickCommands = &quickCommandManagerState{loading: true}
	return m.loadQuickCommandsCmd()
}

func (m modelState) loadQuickCommandsCmd() tea.Cmd {
	store, ok := m.catalog.(quickCommandCatalog)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		commands, err := store.ListQuickCommands(context.Background())
		return quickCommandsLoadedMsg{commands: commands, err: err}
	}
}

func (m modelState) saveQuickCommandCmd(command model.QuickCommand) tea.Cmd {
	store, ok := m.catalog.(quickCommandCatalog)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		saved, err := store.SaveQuickCommand(context.Background(), command)
		return quickCommandSavedMsg{command: saved, err: err}
	}
}

func (m modelState) deleteQuickCommandCmd(id string) tea.Cmd {
	store, ok := m.catalog.(quickCommandCatalog)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return quickCommandDeletedMsg{id: id, err: store.DeleteQuickCommand(context.Background(), id)}
	}
}

func (m *modelState) handleQuickCommandsLoaded(msg quickCommandsLoadedMsg) {
	state := m.quickCommands
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取失败：" + msg.err.Error()
		return
	}
	state.commands = msg.commands
	sortQuickCommands(state.commands)
	state.index = clamp(state.index, 0, max(0, len(state.commands)-1))
	state.errorText = ""
}

func (m *modelState) handleQuickCommandSaved(msg quickCommandSavedMsg) {
	state := m.quickCommands
	if state == nil {
		return
	}
	state.saving = false
	if msg.err != nil {
		state.errorText = "保存失败：" + msg.err.Error()
		return
	}
	index := quickCommandIndex(state.commands, msg.command.Id)
	if index >= 0 {
		state.commands[index] = msg.command
	} else {
		state.commands = append(state.commands, msg.command)
	}
	sortQuickCommands(state.commands)
	state.index = quickCommandIndex(state.commands, msg.command.Id)
	state.editing = false
	state.errorText = ""
	m.statusText = "快速命令已保存"
	m.statusErr = false
}

func (m *modelState) handleQuickCommandDeleted(msg quickCommandDeletedMsg) {
	state := m.quickCommands
	if state == nil {
		return
	}
	state.saving = false
	state.confirmingDelete = false
	if msg.err != nil {
		state.errorText = "删除失败：" + msg.err.Error()
		return
	}
	if index := quickCommandIndex(state.commands, msg.id); index >= 0 {
		state.commands = append(state.commands[:index], state.commands[index+1:]...)
		state.index = clamp(state.index, 0, max(0, len(state.commands)-1))
	}
	m.statusText = "快速命令已删除"
	m.statusErr = false
}

func sortQuickCommands(commands []model.QuickCommand) {
	sort.Slice(commands, func(left, right int) bool {
		leftName, rightName := strings.ToLower(commands[left].Name), strings.ToLower(commands[right].Name)
		if leftName == rightName {
			return commands[left].Id < commands[right].Id
		}
		return leftName < rightName
	})
}

func quickCommandIndex(commands []model.QuickCommand, id string) int {
	for index := range commands {
		if commands[index].Id == id {
			return index
		}
	}
	return -1
}

func (m *modelState) updateQuickCommandKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.quickCommands
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if state.editing {
		return m.updateQuickCommandEditorKey(key)
	}
	if state.confirmingDelete {
		if state.saving {
			return m, nil
		}
		switch {
		case key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "n"):
			state.confirmingDelete = false
		case key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y"):
			if state.index < 0 || state.index >= len(state.commands) {
				state.confirmingDelete = false
				return m, nil
			}
			state.saving = true
			return m, m.deleteQuickCommandCmd(state.commands[state.index].Id)
		}
		return m, nil
	}

	switch {
	case key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "q"):
		m.quickCommands = nil
	case state.loading || state.saving:
		return m, nil
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		state.index = clamp(state.index-1, 0, max(0, len(state.commands)-1))
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		state.index = clamp(state.index+1, 0, max(0, len(state.commands)-1))
	case key.Code == tea.KeyPgUp:
		state.index = clamp(state.index-8, 0, max(0, len(state.commands)-1))
	case key.Code == tea.KeyPgDown:
		state.index = clamp(state.index+8, 0, max(0, len(state.commands)-1))
	case strings.EqualFold(key.Text, "n"):
		m.beginQuickCommandEdit(nil)
	case strings.EqualFold(key.Text, "e"):
		if state.index >= 0 && state.index < len(state.commands) {
			m.beginQuickCommandEdit(&state.commands[state.index])
		}
	case strings.EqualFold(key.Text, "d"):
		if state.index >= 0 && state.index < len(state.commands) {
			state.confirmingDelete = true
			state.errorText = ""
		}
	case key.Code == tea.KeyEnter:
		return m, m.runQuickCommand()
	}
	return m, nil
}

func (m *modelState) beginQuickCommandEdit(command *model.QuickCommand) {
	state := m.quickCommands
	if state == nil {
		return
	}
	state.editing = true
	state.confirmingDelete = false
	state.errorText = ""
	state.field = 0
	state.cursor = 0
	state.commandID, state.name, state.command = "", "", ""
	if command != nil {
		state.commandID, state.name, state.command = command.Id, command.Name, command.Command
	}
}

func (m *modelState) updateQuickCommandEditorKey(key tea.Key) (tea.Model, tea.Cmd) {
	state := m.quickCommands
	if state == nil {
		return m, nil
	}
	if state.saving {
		return m, nil
	}
	if key.Code == tea.KeyEscape {
		state.editing = false
		state.errorText = ""
		return m, nil
	}
	if key.Mod&tea.ModCtrl != 0 && (key.Code == 's' || key.Code == 'S' || key.Code == tea.KeyEnter) {
		return m.saveQuickCommand()
	}
	if state.field == 0 && key.Code == tea.KeyEnter {
		state.field = 1
		state.cursor = len([]rune(state.command))
		state.errorText = ""
		return m, nil
	}
	if key.Code == tea.KeyTab {
		state.field = 1 - state.field
		state.cursor = len([]rune(m.quickCommandFieldValue()))
		state.errorText = ""
		return m, nil
	}
	value := []rune(m.quickCommandFieldValue())
	switch key.Code {
	case tea.KeyLeft:
		state.cursor = max(0, state.cursor-1)
	case tea.KeyRight:
		state.cursor = min(len(value), state.cursor+1)
	case tea.KeyHome:
		state.cursor = 0
	case tea.KeyEnd:
		state.cursor = len(value)
	case tea.KeyBackspace:
		if state.cursor > 0 {
			value = append(value[:state.cursor-1], value[state.cursor:]...)
			state.cursor--
			m.setQuickCommandFieldValue(string(value))
		}
	case tea.KeyDelete:
		if state.cursor < len(value) {
			value = append(value[:state.cursor], value[state.cursor+1:]...)
			m.setQuickCommandFieldValue(string(value))
		}
	case tea.KeyEnter:
		value = insertQuickCommandRune(value, state.cursor, '\n')
		state.cursor++
		m.setQuickCommandFieldValue(string(value))
	default:
		if key.Mod == 0 && key.Text != "" {
			limit := 80
			if state.field == 1 {
				limit = 4096
			}
			for _, character := range key.Text {
				if character < ' ' || character == 0x7f {
					continue
				}
				if len(value) >= limit {
					break
				}
				value = insertQuickCommandRune(value, state.cursor, character)
				state.cursor++
			}
			m.setQuickCommandFieldValue(string(value))
		}
	}
	state.errorText = ""
	return m, nil
}

func (m *modelState) appendQuickCommandPaste(content string) {
	state := m.quickCommands
	if state == nil || !state.editing || state.saving {
		return
	}
	value := []rune(m.quickCommandFieldValue())
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	if state.field == 0 {
		content = strings.ReplaceAll(strings.ReplaceAll(content, "\n", " "), "\t", " ")
	}
	limit := 80
	if state.field == 1 {
		limit = 4096
	}
	for _, character := range content {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			continue
		}
		if len(value) >= limit {
			break
		}
		value = insertQuickCommandRune(value, state.cursor, character)
		state.cursor++
	}
	m.setQuickCommandFieldValue(string(value))
	state.errorText = ""
}

func insertQuickCommandRune(value []rune, at int, character rune) []rune {
	at = clamp(at, 0, len(value))
	value = append(value, 0)
	copy(value[at+1:], value[at:])
	value[at] = character
	return value
}

func (m *modelState) quickCommandFieldValue() string {
	if m.quickCommands == nil {
		return ""
	}
	if m.quickCommands.field == 0 {
		return m.quickCommands.name
	}
	return m.quickCommands.command
}

func (m *modelState) setQuickCommandFieldValue(value string) {
	if m.quickCommands == nil {
		return
	}
	if m.quickCommands.field == 0 {
		m.quickCommands.name = value
	} else {
		m.quickCommands.command = value
	}
}

func (m *modelState) saveQuickCommand() (tea.Model, tea.Cmd) {
	state := m.quickCommands
	if state == nil {
		return m, nil
	}
	if strings.TrimSpace(state.name) == "" {
		state.errorText = "请填写命令名称"
		state.field = 0
		state.cursor = len([]rune(state.name))
		return m, nil
	}
	if strings.TrimSpace(state.command) == "" {
		state.errorText = "请填写命令内容"
		state.field = 1
		state.cursor = len([]rune(state.command))
		return m, nil
	}
	command := model.QuickCommand{Id: state.commandID, Name: state.name, Command: state.command}
	state.saving = true
	state.errorText = ""
	return m, m.saveQuickCommandCmd(command)
}

func (m *modelState) runQuickCommand() tea.Cmd {
	state := m.quickCommands
	if state == nil || state.index < 0 || state.index >= len(state.commands) {
		return nil
	}
	command := state.commands[state.index].Command
	if pane := m.activeTerminalPane(); pane == nil || pane.session == nil {
		m.statusText = "当前 pane 没有已连接的终端，无法运行快速命令"
		m.statusErr = true
		state.errorText = m.statusText
		return nil
	}
	writeErrors := m.writeWorkspaceInput(func(pane *terminalPane) []byte {
		return buildQuickCommandInput(command, pane.bracketedPaste)
	}, "发送快速命令失败：")
	m.quickCommands = nil
	if writeErrors > 0 {
		m.statusText = fmt.Sprintf("快速命令已发送，但 %d 个 pane 写入失败", writeErrors)
		m.statusErr = true
	} else {
		m.statusText = "快速命令已发送"
		m.statusErr = false
	}
	return nil
}

func buildQuickCommandInput(command string, bracketedPaste bool) []byte {
	command = strings.TrimRight(command, "\r\n")
	var input strings.Builder
	for _, line := range strings.Split(command, "\n") {
		if bracketedPaste {
			input.WriteString("\x1b[200~")
		}
		input.WriteString(line)
		if bracketedPaste {
			input.WriteString("\x1b[201~")
		}
		input.WriteByte('\r')
	}
	return []byte(input.String())
}

func (m modelState) quickCommandView() string {
	state := m.quickCommands
	if state == nil {
		return ""
	}
	if state.editing {
		return m.quickCommandEditorView()
	}
	if state.confirmingDelete && state.index >= 0 && state.index < len(state.commands) {
		command := state.commands[state.index]
		lines := []string{
			warningStyle.Render("此操作无法撤销。"),
			"确定删除快速命令「" + command.Name + "」吗？",
			"",
			helpKeyStyle.Render("Enter / y ") + "确认   " + helpKeyStyle.Render("Esc / n ") + "取消",
		}
		if state.saving {
			lines = append(lines, "", accentStyle.Render("◌ 正在删除…"))
		}
		return m.modalView("删除快速命令", lines, 72)
	}
	lines := []string{mutedStyle.Render("命令只保存在本地 SQLite；回车会立即发送并执行。")}
	if state.loading {
		lines = append(lines, "", accentStyle.Render("◌ 正在读取快速命令…"))
	} else if len(state.commands) == 0 {
		lines = append(lines, "", mutedStyle.Render("暂无快速命令，按 n 新增。"))
	} else {
		lines = append(lines, "")
		visible := min(len(state.commands), 10)
		start := clamp(state.index-visible+1, 0, max(0, len(state.commands)-visible))
		for index := start; index < start+visible; index++ {
			command := state.commands[index]
			preview := displayQuickCommand(command.Command)
			line := "  " + command.Name + strings.Repeat(" ", max(1, 22-lipgloss.Width(command.Name))) + preview
			if index == state.index {
				line = selectedAccent.Render("› ") + line[2:]
			}
			lines = append(lines, line)
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	targets := "当前 pane"
	if tab := m.activeTab(); tab != nil && tab.syncInput {
		targets = fmt.Sprintf("当前标签页 %d 个连接 pane", connectedPaneCount(tab))
	}
	lines = append(lines, "", mutedStyle.Render("目标："+targets+" · Enter 执行 · n 新增 · e 编辑 · d 删除 · Esc 关闭"))
	return m.modalView("快速命令", lines, 100)
}

func (m modelState) quickCommandEditorView() string {
	state := m.quickCommands
	title := "新增快速命令"
	if state.commandID != "" {
		title = "编辑快速命令"
	}
	fieldWidth := max(16, min(82, m.width-26))
	name := quickCommandEditorField(state.name, state.cursor, state.field == 0, fieldWidth)
	command := quickCommandEditorField(state.command, state.cursor, state.field == 1, fieldWidth)
	lines := []string{
		mutedStyle.Render("名称区分大小写不敏感且必须唯一。命令可多行；回车换行，Ctrl+Enter 或 Ctrl+S 保存。"),
		"",
		selectedStyle.Render("名称  ") + name,
		selectedStyle.Render("命令  ") + command,
		"",
		mutedStyle.Render("Tab 切换字段 · 方向键移动光标 · Esc 取消"),
	}
	if state.saving {
		lines = append(lines, "", accentStyle.Render("◌ 正在保存到 SQLite…"))
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	return m.modalView(title, lines, 100)
}

func quickCommandEditorField(value string, cursor int, active bool, width int) string {
	if !active {
		return ansi.Truncate(displayQuickCommand(value), width, "…")
	}
	runes := []rune(value)
	cursor = clamp(cursor, 0, len(runes))
	before := displayQuickCommand(string(runes[:cursor]))
	after := displayQuickCommand(string(runes[cursor:]))
	full := before + "▏" + after
	fullWidth := lipgloss.Width(full)
	if fullWidth <= width {
		return full
	}
	cursorCell := lipgloss.Width(before)
	budget := width
	start := 0
	showLeft, showRight := false, false
	for range 3 {
		start = clamp(cursorCell-budget/2, 0, max(0, fullWidth-budget))
		showLeft = start > 0
		showRight = start+budget < fullWidth
		nextBudget := width
		if showLeft {
			nextBudget--
		}
		if showRight {
			nextBudget--
		}
		if nextBudget == budget {
			break
		}
		budget = max(1, nextBudget)
	}
	end := min(fullWidth, start+budget)
	piece := ansi.Cut(full, start, end)
	if showLeft {
		piece = "…" + piece
	}
	if showRight {
		piece += "…"
	}
	return piece
}

func displayQuickCommand(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch {
		case character == '\n' || character == '\r':
			output.WriteString(" ↵ ")
		case character == '\t':
			output.WriteByte(' ')
		case character < ' ' || character == 0x7f:
			output.WriteRune('�')
		default:
			output.WriteRune(character)
		}
	}
	return output.String()
}
