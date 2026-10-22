package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

func workspaceKeyName(key tea.Key) string {
	if key.Mod&tea.ModCtrl != 0 {
		if key.Mod != tea.ModCtrl {
			return ""
		}
		code := key.Code
		if code >= 'A' && code <= 'Z' {
			code += 'a' - 'A'
		}
		if code >= 'a' && code <= 'z' {
			return "ctrl+" + string(code)
		}
		return ""
	}
	if key.Mod == 0 {
		switch key.Code {
		case tea.KeyLeft:
			return "left"
		case tea.KeyRight:
			return "right"
		case tea.KeyUp:
			return "up"
		case tea.KeyDown:
			return "down"
		case tea.KeyEscape:
			return "escape"
		}
	}
	if key.Text != "" {
		if len([]rune(key.Text)) == 1 {
			return strings.ToLower(key.Text)
		}
		return ""
	}
	if key.Code >= 'A' && key.Code <= 'Z' {
		return string(key.Code + ('a' - 'A'))
	}
	if key.Code >= 0x21 && key.Code <= 0x7e {
		return strings.ToLower(string(key.Code))
	}
	return ""
}

func workspaceLeaderMatches(key tea.Key, config model.WorkspaceConfig) bool {
	return workspaceKeyName(key) == config.WithDefaults().Leader
}

func workspaceLeaderAction(key tea.Key, config model.WorkspaceConfig) string {
	name := workspaceKeyName(key)
	if name == "" {
		return ""
	}
	for action, binding := range config.WithDefaults().Bindings {
		if action != "external_edit" && binding != "" && binding == name {
			return action
		}
	}
	return ""
}

func (m modelState) workspaceLeaderKeyLabel() string {
	leader := m.config.WithDefaults().Workspace.Leader
	return displayWorkspaceKey(leader)
}

func (m modelState) workspaceLeaderChord(action string) string {
	config := m.config.WithDefaults().Workspace
	binding := config.Bindings[action]
	if binding == "" {
		return m.workspaceLeaderKeyLabel() + " 后该动作已禁用"
	}
	return m.workspaceLeaderKeyLabel() + " 后按 " + displayWorkspaceKey(binding)
}

func displayWorkspaceKey(key string) string {
	switch key {
	case "left":
		return "←"
	case "right":
		return "→"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "escape":
		return "Esc"
	}
	if strings.HasPrefix(key, "ctrl+") {
		return "Ctrl+" + strings.ToUpper(strings.TrimPrefix(key, "ctrl+"))
	}
	return strings.ToUpper(key)
}

func (m modelState) workspaceLeaderSummary() string {
	config := m.config.WithDefaults().Workspace
	labels := []struct {
		action string
		name   string
	}{
		{"split_vertical", "左右分屏"}, {"split_horizontal", "上下分屏"}, {"compose", "多行输入"},
		{"pane_grow", "扩大 pane"}, {"pane_shrink", "缩小 pane"},
		{"pane_move_next", "移动 pane"},
		{"screen_left", "视口左移"}, {"screen_right", "视口右移"}, {"screen_up", "视口上移"}, {"screen_down", "视口下移"},
		{"toggle_recording", "输出记录"}, {"toggle_script_recording", "脚本录制"}, {"view_logs", "日志"}, {"sync_input", "同步输入"},
		{"quick_command", "快速命令"}, {"plugins", "插件"}, {"scrollback", "历史"},
		{"screen_lock", "锁定屏幕"}, {"screen_lock_password", "锁屏密码"},
		{"search", "搜索"}, {"clear_screen", "清屏"}, {"command_history", "命令历史"}, {"send_ascii_file", "发送文本"}, {"xymodem", "X/YMODEM"},
		{"sftp", "SFTP"}, {"port_forward", "转发"},
		{"reconnect", "重连"}, {"auto_reconnect", "自动重连"}, {"pane_previous", "上个 pane"},
		{"pane_next", "下个 pane"}, {"tab_previous", "上个标签"}, {"tab_next", "下个标签"},
		{"pane_menu", "pane 菜单"}, {"tab_menu", "标签菜单"}, {"close_pane", "关闭 pane"},
		{"tab_groups", "标签组"},
		{"close_tab", "关闭标签"}, {"saved_layouts", "保存/恢复布局"}, {"home", "主机列表"}, {"help", "快捷键帮助"},
	}
	parts := make([]string, 0, len(labels))
	for _, item := range labels {
		binding := config.Bindings[item.action]
		if binding == "" {
			continue
		}
		parts = append(parts, displayWorkspaceKey(binding)+" "+item.name)
	}
	for index := 1; index <= 9; index++ {
		action := "tab_" + strconv.Itoa(index)
		if binding := config.Bindings[action]; binding != "" {
			parts = append(parts, displayWorkspaceKey(binding)+" 跳转标签")
		}
	}
	return m.workspaceLeaderKeyLabel() + " 前缀：" + strings.Join(parts, " · ") + " · Esc 取消"
}
