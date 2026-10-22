package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type sftpBookmarkManagerState struct {
	bookmarks        []model.SFTPBookmark
	side             sftpSide
	scopeKey         string
	remote           bool
	index            int
	loading          bool
	busy             bool
	nameInput        bool
	nameValue        string
	confirmingDelete bool
	errorText        string
}

type sftpBookmarksLoadedMsg struct {
	scopeKey  string
	remote    bool
	bookmarks []model.SFTPBookmark
	err       error
}

type sftpBookmarkSavedMsg struct {
	bookmark model.SFTPBookmark
	err      error
}

type sftpBookmarkDeletedMsg struct {
	id  string
	err error
}

func sftpBookmarkScopeKey(state *sftpBrowserState, side sftpSide) string {
	if state == nil || !sftpSideIsRemote(state, side) {
		return "local"
	}
	host := state.host
	if side == sftpLocalSide {
		host = state.peerHost
	}
	if host.Id != "" {
		return "host:" + host.Id
	}
	identity := strings.Join([]string{
		string(host.Kind), strings.ToLower(strings.TrimSpace(host.IP)),
		fmt.Sprint(host.Port), strings.TrimSpace(host.User),
		string(host.FileProtocol), host.TLSMode,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))
	return "endpoint:" + hex.EncodeToString(digest[:])
}

func sftpBookmarkDefaultName(directory string, remote bool) string {
	base := filepath.Base(filepath.Clean(directory))
	if remote {
		base = path.Base(path.Clean(directory))
	}
	if base == "." || base == string(filepath.Separator) || base == "/" || base == "" {
		return "根目录"
	}
	return base
}

func (m *modelState) openSFTPBookmarkManager() tea.Cmd {
	state := m.sftp
	if state == nil || state.busy {
		return nil
	}
	side := state.activeSide
	if !state.standalone {
		side = sftpRemoteSide
	}
	remote := sftpSideIsRemote(state, side)
	if remote && sftpClientForSide(state, side) == nil {
		state.status, state.statusErr = "当前文件栏没有可用的远程连接", true
		return nil
	}
	scopeKey := sftpBookmarkScopeKey(state, side)
	store, ok := m.catalog.(sftpBookmarkCatalog)
	if !ok {
		state.status, state.statusErr = "当前目录存储不支持目录书签", true
		return nil
	}
	m.sftpBookmarks = &sftpBookmarkManagerState{side: side, scopeKey: scopeKey, remote: remote, loading: true}
	return func() tea.Msg {
		bookmarks, err := store.ListSFTPBookmarks(context.Background(), scopeKey, remote)
		return sftpBookmarksLoadedMsg{scopeKey: scopeKey, remote: remote, bookmarks: bookmarks, err: err}
	}
}

func (m modelState) saveSFTPBookmarkCmd(bookmark model.SFTPBookmark) tea.Cmd {
	store, ok := m.catalog.(sftpBookmarkCatalog)
	if !ok {
		return func() tea.Msg {
			return sftpBookmarkSavedMsg{err: errors.New("directory bookmark storage is unavailable")}
		}
	}
	return func() tea.Msg {
		saved, err := store.SaveSFTPBookmark(context.Background(), bookmark)
		return sftpBookmarkSavedMsg{bookmark: saved, err: err}
	}
}

func (m modelState) deleteSFTPBookmarkCmd(id string) tea.Cmd {
	store, ok := m.catalog.(sftpBookmarkCatalog)
	if !ok {
		return func() tea.Msg {
			return sftpBookmarkDeletedMsg{id: id, err: errors.New("directory bookmark storage is unavailable")}
		}
	}
	return func() tea.Msg {
		return sftpBookmarkDeletedMsg{id: id, err: store.DeleteSFTPBookmark(context.Background(), id)}
	}
}

func (m *modelState) handleSFTPBookmarksLoaded(msg sftpBookmarksLoadedMsg) {
	manager := m.sftpBookmarks
	if manager == nil || manager.scopeKey != msg.scopeKey || manager.remote != msg.remote {
		return
	}
	manager.loading = false
	if msg.err != nil {
		manager.errorText = "读取目录书签失败：" + msg.err.Error()
		return
	}
	manager.bookmarks = append([]model.SFTPBookmark(nil), msg.bookmarks...)
	sortSFTPBookmarks(manager.bookmarks)
	manager.index = clamp(manager.index, 0, max(0, len(manager.bookmarks)-1))
	manager.errorText = ""
}

func sortSFTPBookmarks(bookmarks []model.SFTPBookmark) {
	sort.Slice(bookmarks, func(i, j int) bool {
		left, right := strings.ToLower(bookmarks[i].Name), strings.ToLower(bookmarks[j].Name)
		if left != right {
			return left < right
		}
		return bookmarks[i].Id < bookmarks[j].Id
	})
}

func (m *modelState) handleSFTPBookmarkSaved(msg sftpBookmarkSavedMsg) {
	manager := m.sftpBookmarks
	if manager == nil {
		return
	}
	manager.busy = false
	if msg.err != nil {
		manager.nameInput = true
		manager.errorText = "保存失败：" + msg.err.Error()
		return
	}
	manager.nameInput = false
	updated := false
	for index := range manager.bookmarks {
		if manager.bookmarks[index].Id == msg.bookmark.Id {
			manager.bookmarks[index] = msg.bookmark
			updated = true
			break
		}
	}
	if !updated {
		manager.bookmarks = append(manager.bookmarks, msg.bookmark)
	}
	sortSFTPBookmarks(manager.bookmarks)
	for index := range manager.bookmarks {
		if manager.bookmarks[index].Id == msg.bookmark.Id {
			manager.index = index
			break
		}
	}
	manager.errorText = ""
	if m.sftp != nil {
		m.sftp.status, m.sftp.statusErr = "已保存目录书签："+msg.bookmark.Name, false
	}
}

func (m *modelState) handleSFTPBookmarkDeleted(msg sftpBookmarkDeletedMsg) {
	manager := m.sftpBookmarks
	if manager == nil {
		return
	}
	manager.busy = false
	manager.confirmingDelete = false
	if msg.err != nil {
		manager.errorText = "删除失败：" + msg.err.Error()
		return
	}
	for index := range manager.bookmarks {
		if manager.bookmarks[index].Id == msg.id {
			manager.bookmarks = append(manager.bookmarks[:index], manager.bookmarks[index+1:]...)
			break
		}
	}
	manager.index = clamp(manager.index, 0, max(0, len(manager.bookmarks)-1))
	manager.errorText = ""
	if m.sftp != nil {
		m.sftp.status, m.sftp.statusErr = "已删除目录书签", false
	}
}

func (m *modelState) updateSFTPBookmarksKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	manager := m.sftpBookmarks
	if manager == nil {
		return m, nil
	}
	key := msg.Key()
	if manager.nameInput {
		switch key.Code {
		case tea.KeyEscape:
			manager.nameInput = false
			manager.nameValue = ""
		case tea.KeyEnter:
			name := strings.TrimSpace(manager.nameValue)
			if name == "" {
				manager.errorText = "书签名称不能为空"
				return m, nil
			}
			if m.sftp == nil {
				manager.errorText = "文件工作区已关闭"
				return m, nil
			}
			bookmark := model.SFTPBookmark{
				ScopeKey: manager.scopeKey, Remote: manager.remote,
				Name: name, Path: m.currentSFTPPathFor(m.sftp, manager.side),
			}
			if manager.remote {
				bookmark.Path = path.Clean(bookmark.Path)
			} else {
				bookmark.Path = filepath.Clean(bookmark.Path)
			}
			manager.busy = true
			manager.errorText = ""
			return m, m.saveSFTPBookmarkCmd(bookmark)
		case tea.KeyBackspace, tea.KeyDelete:
			text := []rune(manager.nameValue)
			if len(text) > 0 {
				manager.nameValue = string(text[:len(text)-1])
			}
		}
		if key.Text != "" && key.Mod == 0 && len([]rune(manager.nameValue))+len([]rune(key.Text)) <= 80 {
			manager.nameValue += key.Text
		}
		return m, nil
	}
	if manager.confirmingDelete {
		switch strings.ToLower(key.Text) {
		case "y":
			if manager.index < 0 || manager.index >= len(manager.bookmarks) {
				manager.confirmingDelete = false
				return m, nil
			}
			manager.busy = true
			return m, m.deleteSFTPBookmarkCmd(manager.bookmarks[manager.index].Id)
		case "n":
			manager.confirmingDelete = false
			return m, nil
		}
		if key.Code == tea.KeyEnter {
			if manager.index < 0 || manager.index >= len(manager.bookmarks) {
				manager.confirmingDelete = false
				return m, nil
			}
			manager.busy = true
			return m, m.deleteSFTPBookmarkCmd(manager.bookmarks[manager.index].Id)
		}
		if key.Code == tea.KeyEscape {
			manager.confirmingDelete = false
		}
		return m, nil
	}
	if key.Code == tea.KeyEscape || strings.EqualFold(key.Text, "q") {
		m.sftpBookmarks = nil
		return m, nil
	}
	if manager.loading || manager.busy {
		return m, nil
	}
	switch {
	case key.Code == tea.KeyUp || strings.EqualFold(key.Text, "k"):
		manager.index = clamp(manager.index-1, 0, max(0, len(manager.bookmarks)-1))
	case key.Code == tea.KeyDown || strings.EqualFold(key.Text, "j"):
		manager.index = clamp(manager.index+1, 0, max(0, len(manager.bookmarks)-1))
	case strings.EqualFold(key.Text, "a"):
		if m.sftp == nil {
			manager.errorText = "文件工作区已关闭"
			return m, nil
		}
		manager.nameInput = true
		manager.nameValue = sftpBookmarkDefaultName(m.currentSFTPPathFor(m.sftp, manager.side), manager.remote)
		manager.errorText = ""
	case strings.EqualFold(key.Text, "d"):
		if manager.index >= 0 && manager.index < len(manager.bookmarks) {
			manager.confirmingDelete = true
		}
	case key.Code == tea.KeyEnter:
		if manager.index < 0 || manager.index >= len(manager.bookmarks) || m.sftp == nil {
			return m, nil
		}
		bookmark := manager.bookmarks[manager.index]
		side := manager.side
		m.sftpBookmarks = nil
		m.sftp.activeSide = side
		m.sftp.synchronizedBrowsing = false
		return m, m.navigateSFTPDirectory(side, bookmark.Path)
	}
	return m, nil
}

func (m modelState) sftpBookmarkManagerView() string {
	manager := m.sftpBookmarks
	if manager == nil {
		return ""
	}
	sideName := "远程栏"
	if !manager.remote {
		sideName = "本地栏"
	}
	if manager.confirmingDelete && manager.index >= 0 && manager.index < len(manager.bookmarks) {
		bookmark := manager.bookmarks[manager.index]
		lines := []string{
			warningStyle.Render("此操作无法撤销。"),
			"确定删除目录书签「" + bookmark.Name + "」吗？",
			"路径：" + sanitizeRemoteText(bookmark.Path),
			"",
			helpKeyStyle.Render("Enter / y ") + "确认   " + helpKeyStyle.Render("Esc / n ") + "取消",
		}
		return m.modalView("删除目录书签", lines, 76)
	}
	lines := []string{mutedStyle.Render(sideName + "书签仅保存在本机，并按远程主机隔离。")}
	if manager.nameInput {
		lines = append(lines, "", "当前路径："+sanitizeRemoteText(m.currentSFTPPathFor(m.sftp, manager.side)), "书签名称："+manager.nameValue+"▏")
	} else if manager.loading {
		lines = append(lines, "", accentStyle.Render("◌ 正在读取目录书签…"))
	} else if len(manager.bookmarks) == 0 {
		lines = append(lines, "", mutedStyle.Render("暂无书签。按 a 将当前目录保存为书签。"))
	} else {
		lines = append(lines, "")
		visible := min(len(manager.bookmarks), 10)
		start := clamp(manager.index-visible+1, 0, max(0, len(manager.bookmarks)-visible))
		for index := start; index < start+visible; index++ {
			bookmark := manager.bookmarks[index]
			line := "  " + bookmark.Name + "  " + mutedStyle.Render(sanitizeRemoteText(bookmark.Path))
			if index == manager.index {
				line = selectedAccent.Render("› ") + line[2:]
			}
			lines = append(lines, fitLine(line, 74))
		}
	}
	if manager.errorText != "" {
		lines = append(lines, "", warningStyle.Render(manager.errorText))
	}
	help := "↑/↓ 选择 · a 保存当前目录 · Enter 跳转 · d 删除 · Esc 关闭"
	if manager.nameInput {
		help = "输入名称 · Enter 保存 · Esc 取消"
	} else if manager.busy {
		help = "正在保存或删除目录书签…"
	}
	lines = append(lines, "", mutedStyle.Render(help))
	return m.modalView("目录书签 · "+sideName, lines, 78)
}
