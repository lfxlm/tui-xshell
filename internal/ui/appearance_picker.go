package ui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"tui-ssh-workbench/internal/model"
)

type appearancePickerState struct {
	index     int
	saving    bool
	errorText string
}

type appearanceSchemeOption struct {
	scheme string
	name   string
	note   string
}

var appearanceSchemeOptions = []appearanceSchemeOption{
	{scheme: model.AppearanceDefault, name: "Workbench Dark", note: "蓝绿强调，清晰通用"},
	{scheme: model.AppearanceNord, name: "Nord", note: "冷灰蓝，柔和沉静"},
	{scheme: model.AppearanceDracula, name: "Dracula", note: "紫蓝底色，明快鲜明"},
	{scheme: model.AppearanceSolarized, name: "Solarized Dark", note: "暖灰文字，低刺激"},
	{scheme: model.AppearanceHighContrast, name: "High Contrast", note: "黑底亮色，强对比"},
	{scheme: model.AppearanceCatppuccin, name: "Catppuccin Mocha", note: "柔和紫灰与浅蓝"},
	{scheme: model.AppearanceTokyoNight, name: "Tokyo Night", note: "深蓝紫与亮蓝强调"},
	{scheme: model.AppearanceGruvbox, name: "Gruvbox Dark", note: "暖棕底色与自然绿"},
	{scheme: model.AppearanceOneDark, name: "One Dark", note: "中性深灰与天蓝强调"},
}

type appearanceSchemeSavedMsg struct {
	scheme   string
	previous string
	err      error
}

func (m *modelState) openAppearancePicker() {
	current := m.config.Appearance.WithDefaults().Scheme
	index := 0
	for optionIndex, option := range appearanceSchemeOptions {
		if option.scheme == current {
			index = optionIndex
			break
		}
	}
	m.appearancePicker = &appearancePickerState{index: index}
}

func (m modelState) appearancePickerView() string {
	picker := m.appearancePicker
	if picker == nil {
		return ""
	}
	lines := []string{
		mutedStyle.Render("选择工作台界面配色；只改变工作台 UI，不改终端程序的 ANSI 颜色。"),
		"",
	}
	for index, option := range appearanceSchemeOptions {
		colors := appearanceSchemes[option.scheme]
		swatch := lipgloss.NewStyle().Foreground(colors.primary).Background(colors.canvas).Render("██") +
			lipgloss.NewStyle().Foreground(colors.accent).Background(colors.canvas).Render("██") +
			lipgloss.NewStyle().Foreground(colors.warning).Background(colors.canvas).Render("██")
		line := fmt.Sprintf("%-20s  %-24s  %s", option.name, option.note, swatch)
		if index == picker.index {
			line = selectedAccent.Width(56).Render(fitText("› "+line, 56))
		} else {
			line = secondaryStyle.Render("  " + line)
		}
		lines = append(lines, fitText(line, 56))
	}
	if picker.errorText != "" {
		lines = append(lines, "", warningStyle.Render(picker.errorText))
	}
	if picker.saving {
		lines = append(lines, "", accentStyle.Render("◌ 正在保存配色…"))
	} else {
		lines = append(lines, "", mutedStyle.Render("↑ / ↓ 选择 · Enter 应用并保存 · Esc 返回"))
	}
	return m.modalView("工作台配色", lines, 72)
}

func (m *modelState) updateAppearancePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := m.appearancePicker
	if picker == nil || picker.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape":
		m.appearancePicker = nil
	case "up", "k":
		picker.index = (picker.index - 1 + len(appearanceSchemeOptions)) % len(appearanceSchemeOptions)
	case "down", "j":
		picker.index = (picker.index + 1) % len(appearanceSchemeOptions)
	case "enter":
		option := appearanceSchemeOptions[picker.index]
		previous := m.config.Appearance.WithDefaults().Scheme
		m.config.Appearance.Scheme = option.scheme
		applyAppearance(m.config.Appearance)
		if catalog, ok := m.catalog.(appearancePreferenceCatalog); ok {
			picker.saving = true
			picker.errorText = ""
			return m, func() tea.Msg {
				return appearanceSchemeSavedMsg{
					scheme: option.scheme, previous: previous,
					err: catalog.SetAppearanceScheme(context.Background(), option.scheme),
				}
			}
		}
		m.appearancePicker = nil
		m.statusText = "工作台配色已切换；当前存储不支持保存此设置"
		m.statusErr = false
	}
	return m, nil
}

func (m *modelState) handleAppearanceSchemeSaved(msg appearanceSchemeSavedMsg) {
	picker := m.appearancePicker
	if picker == nil || !picker.saving {
		return
	}
	if msg.err != nil {
		m.config.Appearance.Scheme = msg.previous
		applyAppearance(m.config.Appearance)
		picker.saving = false
		picker.errorText = "保存配色失败：" + msg.err.Error()
		for index, option := range appearanceSchemeOptions {
			if option.scheme == msg.previous {
				picker.index = index
				break
			}
		}
		return
	}
	m.appearancePicker = nil
	m.statusText = "工作台配色已保存"
	m.statusErr = false
}
