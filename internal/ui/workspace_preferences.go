package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func (m *modelState) toggleWorkspaceFooterShortcuts() tea.Cmd {
	if m.loading || m.footerShortcutsUpdating {
		m.statusText = "本地设置正在加载或保存，请稍后再试"
		m.statusErr = false
		return nil
	}
	previous := m.config.Workspace.ShowFooterShortcuts
	enabled := !previous
	m.config.Workspace.ShowFooterShortcuts = enabled
	if catalog, ok := m.catalog.(workspaceFooterPreferenceCatalog); ok {
		m.footerShortcutsUpdating = true
		return func() tea.Msg {
			return footerShortcutsPreferenceSavedMsg{
				enabled: enabled, previous: previous,
				err: catalog.SetFooterShortcutsVisible(context.Background(), enabled),
			}
		}
	}
	if enabled {
		m.statusText = "底部快捷键提示已开启（当前存储不支持持久化设置）"
	} else {
		m.statusText = "底部快捷键提示已关闭（当前存储不支持持久化设置）"
	}
	m.statusErr = false
	return nil
}

func (m *modelState) handleFooterShortcutsPreferenceSaved(msg footerShortcutsPreferenceSavedMsg) {
	m.footerShortcutsUpdating = false
	if msg.err != nil {
		m.config.Workspace.ShowFooterShortcuts = msg.previous
		m.statusText = "保存底部快捷键设置失败：" + msg.err.Error()
		m.statusErr = true
		return
	}
	if msg.enabled {
		m.statusText = "底部快捷键提示已开启，设置已保存在本机"
	} else {
		m.statusText = "底部快捷键提示已关闭，设置已保存在本机"
	}
	m.statusErr = false
}
