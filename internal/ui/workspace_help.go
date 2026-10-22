package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

var workspaceHelpActions = []struct {
	key         string
	description string
}{
	{"split_vertical", "左右分屏并选择新连接"},
	{"split_horizontal", "上下分屏并选择新连接"},
	{"pane_grow", "扩大当前 pane（每次约 5%）"}, {"pane_shrink", "缩小当前 pane（每次约 5%）"},
	{"pane_move_next", "将当前 pane 与下一个 pane 交换位置"},
	{"screen_left", "固定尺寸终端视口左移"}, {"screen_right", "固定尺寸终端视口右移"},
	{"screen_up", "固定尺寸终端视口上移"}, {"screen_down", "固定尺寸终端视口下移"},
	{"pane_previous", "切换到上一个 pane"},
	{"pane_next", "切换到下一个 pane"},
	{"tab_previous", "切换到上一个标签"},
	{"tab_next", "切换到下一个标签"},
	{"tab_1", "跳转到第 1 个标签"}, {"tab_2", "跳转到第 2 个标签"},
	{"tab_3", "跳转到第 3 个标签"}, {"tab_4", "跳转到第 4 个标签"},
	{"tab_5", "跳转到第 5 个标签"}, {"tab_6", "跳转到第 6 个标签"},
	{"tab_7", "跳转到第 7 个标签"}, {"tab_8", "跳转到第 8 个标签"},
	{"tab_9", "跳转到第 9 个标签"},
	{"close_pane", "关闭当前 pane"}, {"close_tab", "关闭当前标签"},
	{"pane_menu", "打开当前 pane 菜单"}, {"tab_menu", "打开当前标签菜单"},
	{"tab_groups", "管理标签组、移动标签和切换分组"},
	{"sftp", "打开远程文件管理器"}, {"quick_command", "打开快速命令面板"},
	{"screen_lock", "锁定工作区屏幕"}, {"screen_lock_password", "设置、更改或移除锁屏密码"},
	{"scheduled_transfers", "管理定时上传和下载"},
	{"saved_layouts", "保存、恢复或删除标签与 pane 布局"},
	{"toggle_recording", "开始或停止终端输出记录"}, {"toggle_script_recording", "录制并保存 Shell expect/send 脚本"},
	{"view_logs", "查看终端输出日志"},
	{"plugins", "打开插件命令面板"}, {"compose", "打开多行 Compose Pane"},
	{"port_forward", "管理 SSH 端口转发"}, {"reconnect", "重新连接当前 pane"},
	{"auto_reconnect", "切换 SSH 自动重连"}, {"sync_input", "切换当前标签同步输入"},
	{"scrollback", "查看终端历史"}, {"search", "搜索终端历史和当前屏幕"},
	{"clear_screen", "清除当前终端画面与本地历史"},
	{"command_history", "查看当前 pane 已回显的命令历史"},
	{"send_ascii_file", "按字符/行间隔向当前 pane 发送文本文件"},
	{"xymodem", "启动 XMODEM/YMODEM 单文件或批量传输"},
	{"external_edit", "在 SFTP 编辑器中启动外部编辑器并自动保存"},
	{"home", "返回主机列表"}, {"help", "显示本快捷键帮助"},
}

func (m modelState) workspaceHelpView() string {
	width, height := max(1, m.width), max(8, m.height)
	config := m.config.WithDefaults().Workspace
	lines := []string{
		brandStyle.Render("◆  会话工作区快捷键"),
		mutedStyle.Render("前缀：" + displayWorkspaceKey(config.Leader) + " · 修改 config.toml 的 [workspace] 和 [workspace.bindings] 可自定义"),
		separatorStyle.Render(strings.Repeat("─", width)),
	}
	for _, item := range workspaceHelpActions {
		binding := config.Bindings[item.key]
		key := "未绑定"
		if binding != "" {
			key = displayWorkspaceKey(config.Leader) + " 后按 " + displayWorkspaceKey(binding)
		}
		lines = append(lines, fitLine(helpKeyStyle.Render(fmt.Sprintf("%-27s", key))+"  "+item.description, width))
	}
	lines = append(lines,
		separatorStyle.Render(strings.Repeat("─", width)),
		"拖动 pane 标题栏；目标高亮后松开即可交换 · 拖动分隔线调整大小 · 鼠标双击选词 · Shift 拖动选择文本 · "+terminalClipboardCopyShortcutLabel()+" 复制 · "+terminalClipboardPasteShortcutLabel()+" 粘贴 · Ctrl+] 关闭 pane",
		mutedStyle.Render("↑/↓ 滚动 · PgUp/PgDn 翻页 · Esc 或 ? 关闭帮助"),
	)
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	visible := max(1, height-5)
	top := clamp(m.workspaceHelpTop, 0, max(0, len(lines)-visible))
	end := min(len(lines), top+visible)
	return strings.Join(lines[top:end], "\n")
}

func (m *modelState) updateWorkspaceHelpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	page := max(1, m.height-7)
	maxTop := max(0, len(workspaceHelpActions)+6-max(1, m.height-5))
	switch {
	case key.Code == tea.KeyEscape || key.Text == "?" || strings.EqualFold(key.Text, "q"):
		m.workspaceHelp = false
		m.workspaceHelpTop = 0
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		m.workspaceHelpTop = max(0, m.workspaceHelpTop-1)
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		m.workspaceHelpTop = min(maxTop, m.workspaceHelpTop+1)
	case key.Code == tea.KeyPgUp:
		m.workspaceHelpTop = max(0, m.workspaceHelpTop-page)
	case key.Code == tea.KeyPgDown:
		m.workspaceHelpTop = min(maxTop, m.workspaceHelpTop+page)
	case key.Code == tea.KeyHome:
		m.workspaceHelpTop = 0
	case key.Code == tea.KeyEnd:
		m.workspaceHelpTop = maxTop
	}
	return m, nil
}
