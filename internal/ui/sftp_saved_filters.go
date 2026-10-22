package ui

import (
	"context"
	"errors"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type sftpFilterManagerState struct {
	filters          []model.SFTPFilter
	side             sftpSide
	index            int
	loading          bool
	busy             bool
	confirmingDelete bool
	errorText        string
}

type sftpFiltersLoadedMsg struct {
	filters []model.SFTPFilter
	err     error
}

type sftpFilterSavedMsg struct {
	filter model.SFTPFilter
	err    error
}

type sftpFilterDeletedMsg struct {
	id  string
	err error
}

func (m *modelState) openSFTPFilterManager() tea.Cmd {
	if m.sftp == nil || m.sftp.busy {
		return nil
	}
	store, ok := m.catalog.(sftpFilterCatalog)
	if !ok {
		m.sftp.status, m.sftp.statusErr = "当前目录存储不支持已保存过滤方案", true
		return nil
	}
	side := m.sftp.activeSide
	if !m.sftp.standalone {
		side = sftpRemoteSide
	}
	m.sftpFilters = &sftpFilterManagerState{side: side, loading: true}
	return func() tea.Msg {
		filters, err := store.ListSFTPFilters(context.Background())
		return sftpFiltersLoadedMsg{filters: filters, err: err}
	}
}

func (m modelState) saveSFTPFilterCmd(filter model.SFTPFilter) tea.Cmd {
	store, ok := m.catalog.(sftpFilterCatalog)
	if !ok {
		return func() tea.Msg { return sftpFilterSavedMsg{err: errors.New("saved filter storage is unavailable")} }
	}
	return func() tea.Msg {
		saved, err := store.SaveSFTPFilter(context.Background(), filter)
		return sftpFilterSavedMsg{filter: saved, err: err}
	}
}

func (m modelState) deleteSFTPFilterCmd(id string) tea.Cmd {
	store, ok := m.catalog.(sftpFilterCatalog)
	if !ok {
		return func() tea.Msg {
			return sftpFilterDeletedMsg{id: id, err: errors.New("saved filter storage is unavailable")}
		}
	}
	return func() tea.Msg {
		return sftpFilterDeletedMsg{id: id, err: store.DeleteSFTPFilter(context.Background(), id)}
	}
}

func (m *modelState) handleSFTPFiltersLoaded(msg sftpFiltersLoadedMsg) {
	state := m.sftpFilters
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取失败：" + msg.err.Error()
		return
	}
	state.filters = msg.filters
	sort.Slice(state.filters, func(i, j int) bool {
		return strings.ToLower(state.filters[i].Name) < strings.ToLower(state.filters[j].Name)
	})
	state.index = clamp(state.index, 0, max(0, len(state.filters)-1))
	state.errorText = ""
}

func (m *modelState) handleSFTPFilterSaved(msg sftpFilterSavedMsg) {
	if m.sftp == nil {
		return
	}
	if msg.err != nil {
		m.sftp.status, m.sftp.statusErr = "保存过滤方案失败："+msg.err.Error(), true
		return
	}
	m.sftp.status, m.sftp.statusErr = "过滤方案已保存："+msg.filter.Name, false
}

func (m *modelState) handleSFTPFilterDeleted(msg sftpFilterDeletedMsg) {
	state := m.sftpFilters
	if state == nil {
		return
	}
	state.busy = false
	state.confirmingDelete = false
	if msg.err != nil {
		state.errorText = "删除失败：" + msg.err.Error()
		return
	}
	for index := range state.filters {
		if state.filters[index].Id == msg.id {
			state.filters = append(state.filters[:index], state.filters[index+1:]...)
			break
		}
	}
	state.index = clamp(state.index, 0, max(0, len(state.filters)-1))
	state.errorText = ""
	if m.sftp != nil {
		m.sftp.status, m.sftp.statusErr = "已删除过滤方案", false
	}
}

func (m *modelState) updateSFTPFiltersKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sftpFilters
	if state == nil {
		return m, nil
	}
	key := msg.Key()
	if state.confirmingDelete {
		if state.busy {
			return m, nil
		}
		switch {
		case key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "n"):
			state.confirmingDelete = false
		case key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y"):
			if state.index < 0 || state.index >= len(state.filters) {
				state.confirmingDelete = false
				return m, nil
			}
			state.busy = true
			return m, m.deleteSFTPFilterCmd(state.filters[state.index].Id)
		}
		return m, nil
	}
	if key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "q") {
		m.sftpFilters = nil
		return m, nil
	}
	if state.loading || state.busy {
		return m, nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		state.index = clamp(state.index-1, 0, max(0, len(state.filters)-1))
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		state.index = clamp(state.index+1, 0, max(0, len(state.filters)-1))
	case strings.EqualFold(key.Text, "d"):
		if state.index >= 0 && state.index < len(state.filters) {
			state.confirmingDelete = true
			state.errorText = ""
		}
	case key.Code == tea.KeyEnter:
		if state.index < 0 || state.index >= len(state.filters) || m.sftp == nil {
			return m, nil
		}
		filter, err := parseSFTPEntryFilter(state.filters[state.index].Expression)
		if err != nil {
			state.errorText = "过滤方案无效：" + err.Error()
			return m, nil
		}
		setSFTPFilter(m.sftp, state.side, filter)
		m.sftp.activeSide = state.side
		m.sftp.status, m.sftp.statusErr = "已应用过滤方案："+state.filters[state.index].Name, false
		m.sftpFilters = nil
	}
	return m, nil
}

func (m modelState) sftpFilterManagerView() string {
	state := m.sftpFilters
	if state == nil {
		return ""
	}
	if state.confirmingDelete && state.index >= 0 && state.index < len(state.filters) {
		filter := state.filters[state.index]
		lines := []string{
			warningStyle.Render("此操作无法撤销。"),
			"确定删除过滤方案「" + filter.Name + "」吗？",
			"",
			helpKeyStyle.Render("Enter / y ") + "确认   " + helpKeyStyle.Render("Esc / n ") + "取消",
		}
		if state.busy {
			lines = append(lines, "", accentStyle.Render("◌ 正在删除…"))
		}
		return m.modalView("删除过滤方案", lines, 72)
	}
	sideName := "远程栏"
	if state.side == sftpLocalSide {
		sideName = "本地栏"
	}
	lines := []string{mutedStyle.Render("方案会保存在本机 SQLite；选择后应用到打开管理器时的 " + sideName + "。")}
	if state.loading {
		lines = append(lines, "", accentStyle.Render("◌ 正在读取过滤方案…"))
	} else if len(state.filters) == 0 {
		lines = append(lines, "", mutedStyle.Render("暂无已保存方案。在过滤输入框中按 Ctrl+S 保存。"))
	} else {
		lines = append(lines, "")
		visible := min(len(state.filters), 10)
		start := clamp(state.index-visible+1, 0, max(0, len(state.filters)-visible))
		for index := start; index < start+visible; index++ {
			filter := state.filters[index]
			line := "  " + filter.Name + "  " + mutedStyle.Render(filter.Expression)
			if index == state.index {
				line = selectedAccent.Render("› ") + line[2:]
			}
			lines = append(lines, line)
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	help := "↑/↓ 选择 · Enter 应用 · d 删除 · Esc 关闭"
	if state.busy {
		help = "正在删除过滤方案…"
	}
	lines = append(lines, "", mutedStyle.Render(help))
	return m.modalView("已保存的文件过滤方案", lines, 100)
}
