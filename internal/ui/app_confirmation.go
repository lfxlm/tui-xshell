package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m *modelState) requestQuitConfirmation() {
	m.contextMenu = nil
	m.workspaceLeaderPending = false
	m.appConfirmation = &appConfirmationState{kind: appConfirmQuit}
}

func (m *modelState) requestTabClose(tabID string) {
	if m.tabByID(tabID) == nil {
		return
	}
	m.contextMenu = nil
	m.workspaceLeaderPending = false
	m.appConfirmation = &appConfirmationState{kind: appConfirmCloseTab, tabID: tabID}
}

func (m *modelState) updateAppConfirmationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	confirmation := m.appConfirmation
	if confirmation == nil {
		return m, nil
	}
	switch strings.ToLower(msg.String()) {
	case "esc", "escape", "n", "q", "ctrl+c":
		m.appConfirmation = nil
		return m, nil
	case "enter", "y":
		m.appConfirmation = nil
		if confirmation.kind == appConfirmQuit {
			if err := m.saveTerminalScriptRecordingsBeforeQuit(); err != nil {
				m.statusText = "退出前保存脚本录制失败：" + err.Error()
				m.statusErr = true
				return m, nil
			}
			return m, tea.Quit
		}
		if m.tabByID(confirmation.tabID) == nil {
			m.statusText = "标签已不存在"
			m.statusErr = true
			return m, nil
		}
		return m, tea.Batch(m.performTabClose(confirmation.tabID)...)
	}
	return m, nil
}

func (m modelState) appConfirmationView() string {
	if m.appConfirmation == nil {
		return ""
	}
	var title string
	var lines []string
	switch m.appConfirmation.kind {
	case appConfirmQuit:
		title = "退出确认"
		lines = []string{
			warningStyle.Render("确定退出 SSH 终端工作台吗？"),
			"所有已打开的终端和 SFTP 连接都会关闭。",
			"未完成的文件传输会中断。",
			"",
			helpKeyStyle.Render("Enter / y") + " 退出   " + helpKeyStyle.Render("Esc / n / q") + " 返回",
		}
	case appConfirmCloseTab:
		title = "关闭标签确认"
		lines = []string{warningStyle.Render("关闭此标签会结束其中的连接。")}
		if tab := m.tabByID(m.appConfirmation.tabID); tab != nil {
			name := workspaceTabName(tab)
			lines = append(lines, "标签："+sanitizeRemoteText(name))
			if tab.sftp != nil {
				lines = append(lines, "正在进行的文件传输可能会中断。")
			}
		}
		lines = append(lines, "", helpKeyStyle.Render("Enter / y")+" 关闭   "+helpKeyStyle.Render("Esc / n / q")+" 取消")
	default:
		return ""
	}
	return m.modalView(title, lines, 82)
}
