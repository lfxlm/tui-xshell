package ui

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"tui-ssh-workbench/internal/plugin"
)

const (
	pluginCommandTimeout = 30 * time.Second
	pluginQueryLimit     = 256
)

type pluginPaletteState struct {
	commands   []plugin.Command
	query      string
	index      int
	running    string
	runCancel  context.CancelFunc
	generation uint64
	output     string
	errorText  string
}

type pluginCommandCompletedMsg struct {
	generation uint64
	fullName   string
	output     string
	err        error
}

type pluginCommandTimeoutMsg struct {
	generation uint64
	fullName   string
}

func (m *modelState) openPluginPalette() {
	commands := []plugin.Command(nil)
	if m.pluginRegistry != nil {
		commands = m.pluginRegistry.Commands()
	}
	m.pluginPalette = &pluginPaletteState{commands: commands}
}

func (m *modelState) closePluginPalette() {
	if m.pluginPalette != nil && m.pluginPalette.runCancel != nil {
		m.pluginPalette.runCancel()
	}
	m.pluginPalette = nil
}

func (m modelState) pluginPaletteMatches() []plugin.Command {
	state := m.pluginPalette
	if state == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(state.query))
	if query == "" {
		return append([]plugin.Command(nil), state.commands...)
	}
	matches := make([]plugin.Command, 0, len(state.commands))
	for _, command := range state.commands {
		searchable := strings.ToLower(command.FullName + " " + command.Description)
		if strings.Contains(searchable, query) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m *modelState) updatePluginPaletteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.pluginPalette
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if key.Code == tea.KeyEscape {
		if state.running != "" {
			if state.runCancel != nil {
				state.runCancel()
			}
			state.generation++
			state.running = ""
			state.runCancel = nil
			state.errorText = "已请求取消插件命令"
			return m, nil
		}
		m.closePluginPalette()
		return m, nil
	}
	if state.running != "" {
		return m, nil
	}

	matches := m.pluginPaletteMatches()
	switch {
	case key.Code == tea.KeyUp || key.Text == "k":
		state.index = clamp(state.index-1, 0, max(0, len(matches)-1))
	case key.Code == tea.KeyDown || key.Text == "j":
		state.index = clamp(state.index+1, 0, max(0, len(matches)-1))
	case key.Code == tea.KeyEnter:
		if len(matches) == 0 {
			state.errorText = "没有匹配的插件命令"
			return m, nil
		}
		command := matches[clamp(state.index, 0, len(matches)-1)]
		return m, m.runPluginCommand(command)
	case key.Code == tea.KeyBackspace || key.Code == tea.KeyDelete:
		state.query = removeLastRune(state.query)
		state.index = 0
		state.errorText = ""
	case key.Mod&tea.ModCtrl != 0 && (key.Code == 'u' || key.Code == 'U'):
		state.query = ""
		state.index = 0
		state.errorText = ""
	case key.Mod&(tea.ModCtrl|tea.ModAlt) == 0 && key.Text != "":
		state.query = appendPluginSearchText(state.query, key.Text)
		state.index = 0
		state.errorText = ""
	}
	return m, nil
}

func (m *modelState) appendPluginPalettePaste(content string) {
	if m.pluginPalette == nil || m.pluginPalette.running != "" {
		return
	}
	m.pluginPalette.query = appendPluginSearchText(m.pluginPalette.query, content)
	m.pluginPalette.index = 0
	m.pluginPalette.errorText = ""
}

func appendPluginSearchText(current, added string) string {
	remaining := pluginQueryLimit - len([]rune(current))
	if remaining <= 0 {
		return current
	}
	var clean strings.Builder
	addedRunes := 0
	for _, char := range added {
		if unicode.IsPrint(char) && !unicode.IsControl(char) {
			if addedRunes >= remaining {
				break
			}
			clean.WriteRune(char)
			addedRunes++
		}
	}
	return current + clean.String()
}

func (m *modelState) runPluginCommand(command plugin.Command) tea.Cmd {
	state := m.pluginPalette
	if state == nil || m.pluginRegistry == nil {
		return nil
	}
	state.generation++
	generation := state.generation
	ctx, cancel := context.WithTimeout(context.Background(), pluginCommandTimeout)
	state.runCancel = cancel
	state.running = command.FullName
	state.output = ""
	state.errorText = ""
	registry := m.pluginRegistry
	runCommand := func() tea.Msg {
		output, err := registry.Run(ctx, command.FullName)
		cancel()
		return pluginCommandCompletedMsg{generation: generation, fullName: command.FullName, output: output, err: err}
	}
	timeoutCommand := func() tea.Msg {
		timer := time.NewTimer(pluginCommandTimeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return pluginCommandTimeoutMsg{generation: generation, fullName: command.FullName}
		}
	}
	return tea.Batch(runCommand, timeoutCommand)
}

func (m *modelState) handlePluginCommandCompleted(msg pluginCommandCompletedMsg) {
	state := m.pluginPalette
	if state == nil || state.generation != msg.generation || state.running != msg.fullName {
		return
	}
	if state.runCancel != nil {
		state.runCancel()
	}
	state.runCancel = nil
	state.running = ""
	if msg.err != nil {
		state.errorText = msg.err.Error()
		state.output = ""
		return
	}
	state.errorText = ""
	state.output = msg.output
}

func (m *modelState) handlePluginCommandTimeout(msg pluginCommandTimeoutMsg) {
	state := m.pluginPalette
	if state == nil || state.generation != msg.generation || state.running != msg.fullName {
		return
	}
	if state.runCancel != nil {
		state.runCancel()
	}
	state.generation++
	state.runCancel = nil
	state.running = ""
	state.errorText = "插件命令超过 30 秒，已取消其上下文"
	state.output = ""
}

func (m modelState) pluginPaletteView() string {
	state := m.pluginPalette
	if state == nil {
		return ""
	}
	lines := []string{
		mutedStyle.Render("搜索已加载的插件命令；运行在后台，最长等待 30 秒。"),
		"",
		selectedAccent.Render("> ") + fitLine(state.query+"▏", max(1, min(86, m.width-24))),
	}
	matches := m.pluginPaletteMatches()
	if len(matches) == 0 {
		message := "没有匹配的插件命令。"
		if len(state.commands) == 0 {
			message = "当前没有加载插件命令。"
		}
		lines = append(lines, "", mutedStyle.Render(message))
	} else {
		lines = append(lines, "")
		visible := min(9, len(matches))
		start := clamp(state.index-visible+1, 0, max(0, len(matches)-visible))
		for index := start; index < start+visible; index++ {
			command := matches[index]
			label := command.FullName
			width := 24
			if lipgloss.Width(label) > width {
				label = fitLine(label, width)
			}
			line := "  " + label + strings.Repeat(" ", max(1, width-lipgloss.Width(label)+2)) + sanitizePluginText(command.Description)
			if index == state.index {
				line = selectedAccent.Render("› ") + line[2:]
			}
			lines = append(lines, line)
		}
	}
	if state.running != "" {
		lines = append(lines, "", accentStyle.Render("◌ 正在运行："+state.running+" · Esc 请求取消"))
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(sanitizePluginText(state.errorText)))
	}
	if state.output != "" {
		lines = append(lines, "", mutedStyle.Render("命令输出："))
		outputLines := strings.Split(sanitizePluginText(state.output), "\n")
		for _, line := range outputLines[:min(8, len(outputLines))] {
			lines = append(lines, fitLine(line, max(1, min(92, m.width-20))))
		}
	}
	lines = append(lines, "", mutedStyle.Render("↑/↓ 或 j/k 选择 · Enter 运行 · 输入筛选 · Ctrl+U 清除 · Esc 关闭"))
	return m.modalView("插件命令面板", lines, 108)
}

func sanitizePluginText(value string) string {
	value = ansi.Strip(value)
	var clean strings.Builder
	for _, char := range value {
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			clean.WriteRune(char)
		}
	}
	return clean.String()
}
