package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func (m *modelState) openShortcutBarPicker() {
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return
	}
	if host := m.selectedHost(); host == nil {
		m.statusText, m.statusErr = "请先选择要加入连接栏的主机", true
		return
	}
	if len(m.shortcutBars) == 0 {
		m.statusText, m.statusErr = "还没有命名连接栏；按 b 创建一个", false
		return
	}
	if m.shortcutBarSavingID != "" {
		m.statusText, m.statusErr = "连接栏正在保存，请稍候", false
		return
	}
	m.shortcutBarPicker = &shortcutBarPickerState{hostID: m.selectedHost().Id}
}

func (m *modelState) updateShortcutBarPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := m.shortcutBarPicker
	if picker == nil {
		return m, nil
	}
	if picker.busy {
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape", "q", "ctrl+c":
		m.shortcutBarPicker = nil
	case "up", "k":
		picker.index = clamp(picker.index-1, 0, len(m.shortcutBars)-1)
	case "down", "j":
		picker.index = clamp(picker.index+1, 0, len(m.shortcutBars)-1)
	case "enter", " ":
		if len(m.shortcutBars) > 0 {
			return m, m.toggleShortcutBarMembership(m.shortcutBars[clamp(picker.index, 0, len(m.shortcutBars)-1)].Id, picker.hostID)
		}
	}
	return m, nil
}

func (m *modelState) toggleSelectedShortcutBarMembership() tea.Cmd {
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return nil
	}
	host := m.selectedHost()
	if host == nil {
		m.statusText, m.statusErr = "请先选择主机", true
		return nil
	}
	selected := m.selectedGroup()
	if selected.isShortcutBar {
		return m.toggleShortcutBarMembership(selected.id, host.Id)
	}
	m.openShortcutBarPicker()
	return nil
}

func (m *modelState) toggleShortcutBarMembership(barID, hostID string) tea.Cmd {
	if m.shortcutBarSavingID != "" {
		m.statusText, m.statusErr = "连接栏正在保存，请稍候", false
		return nil
	}
	bar := m.shortcutBarByID(barID)
	if bar == nil || m.hostByID(hostID) == nil {
		m.statusText, m.statusErr = "连接栏或主机已不存在，请刷新后重试", true
		return nil
	}
	ids := append([]string(nil), bar.HostIDs...)
	memberIndex := -1
	for index, id := range ids {
		if id == hostID {
			memberIndex = index
			break
		}
	}
	adding := memberIndex < 0
	if adding {
		ids = append(ids, hostID)
	} else {
		ids = append(ids[:memberIndex], ids[memberIndex+1:]...)
	}
	catalog, ok := m.catalog.(shortcutBarCatalog)
	if !ok {
		m.statusText, m.statusErr = "当前主机目录不支持命名连接栏", true
		return nil
	}
	m.shortcutBarSavingID = barID
	if m.shortcutBarPicker != nil {
		m.shortcutBarPicker.busy = true
		m.shortcutBarPicker.errorText = ""
	}
	return func() tea.Msg {
		err := catalog.SetShortcutBarHosts(context.Background(), barID, ids)
		return shortcutBarHostsUpdatedMsg{barID: barID, hostIDs: ids, err: err}
	}
}

func (m *modelState) moveSelectedShortcutBarHost(direction int) tea.Cmd {
	if m.activePane != hostPane || !m.selectedGroup().isShortcutBar {
		m.statusText, m.statusErr = "请在命名连接栏中选择主机后调整顺序", true
		return nil
	}
	if m.shortcutBarSavingID != "" {
		m.statusText, m.statusErr = "连接栏正在保存，请稍候", false
		return nil
	}
	bar := m.shortcutBarByID(m.selectedGroup().id)
	host := m.selectedHost()
	if bar == nil || host == nil {
		m.statusText, m.statusErr = "请先选择连接栏中的主机", true
		return nil
	}
	catalog, ok := m.catalog.(shortcutBarCatalog)
	if !ok {
		m.statusText, m.statusErr = "当前主机目录不支持调整连接栏顺序", true
		return nil
	}
	barID, hostID := bar.Id, host.Id
	m.shortcutBarSavingID = barID
	return func() tea.Msg {
		ids, err := catalog.MoveShortcutBarHost(context.Background(), barID, hostID, direction)
		return shortcutBarOrderUpdatedMsg{barID: barID, hostIDs: ids, selectedHost: hostID, err: err}
	}
}

func (m modelState) currentShortcutNumbers() (map[string]int, bool) {
	selected := m.selectedGroup()
	if !selected.isShortcutBar {
		return m.favoriteShortcutNumbers(), false
	}
	bar := m.shortcutBarByID(selected.id)
	if bar == nil {
		return map[string]int{}, true
	}
	known := make(map[string]bool, len(m.hosts))
	for _, host := range m.hosts {
		known[host.Id] = true
	}
	numbers := make(map[string]int, min(len(bar.HostIDs), 9))
	for _, hostID := range bar.HostIDs {
		if !known[hostID] || len(numbers) == 9 {
			continue
		}
		numbers[hostID] = len(numbers) + 1
	}
	return numbers, true
}
