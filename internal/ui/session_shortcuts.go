package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type hostMetadataCatalog interface {
	SetHostFavorite(context.Context, string, bool) error
	RecordHostConnection(context.Context, string, time.Time) error
}

type favoriteOrderCatalog interface {
	MoveFavoriteHost(context.Context, string, int) ([]string, error)
}

func (m *modelState) toggleDefaultSession() tea.Cmd {
	if m.loading {
		m.statusText, m.statusErr = "请等待本地数据加载完成", false
		return nil
	}
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return nil
	}
	if m.defaultSessionUpdating {
		m.statusText, m.statusErr = "默认会话正在保存，请稍候", false
		return nil
	}
	host := m.selectedHost()
	if host == nil {
		m.statusText, m.statusErr = "请先选择主机", true
		return nil
	}
	catalog, ok := m.catalog.(defaultSessionCatalog)
	if !ok {
		m.statusText, m.statusErr = "当前主机目录不支持默认会话", true
		return nil
	}
	hostID := host.Id
	if m.defaultHostID == hostID {
		hostID = ""
	}
	m.defaultSessionUpdating = true
	m.statusText, m.statusErr = "正在保存默认会话…", false
	return func() tea.Msg {
		return defaultSessionUpdatedMsg{hostID: hostID, err: catalog.SetDefaultHostID(context.Background(), hostID)}
	}
}

func (m *modelState) toggleSelectedHostFavorite() tea.Cmd {
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return nil
	}
	if m.favoriteUpdatingID != "" || m.favoriteOrderUpdating {
		m.statusText, m.statusErr = "收藏状态正在保存，请稍候", false
		return nil
	}
	host := m.selectedHost()
	if host == nil {
		m.statusText, m.statusErr = "请先选择主机", true
		return nil
	}
	catalog, ok := m.catalog.(hostMetadataCatalog)
	if !ok {
		m.statusText, m.statusErr = "当前主机目录不支持收藏", true
		return nil
	}
	favorite := !host.Favorite
	m.favoriteUpdatingID = host.Id
	m.statusText, m.statusErr = "正在保存收藏状态…", false
	return func() tea.Msg {
		err := catalog.SetHostFavorite(context.Background(), host.Id, favorite)
		return hostFavoriteUpdatedMsg{hostID: host.Id, favorite: favorite, err: err}
	}
}

func (m *modelState) recordRecentHostConnection(host model.Host) tea.Cmd {
	if host.Id == "" {
		return nil
	}
	catalog, ok := m.catalog.(hostMetadataCatalog)
	if !ok {
		return nil
	}
	connectedAt := time.Now().UTC()
	return func() tea.Msg {
		err := catalog.RecordHostConnection(context.Background(), host.Id, connectedAt)
		return hostConnectionRecordedMsg{hostID: host.Id, at: connectedAt, err: err}
	}
}

// favoriteShortcutHosts returns a stable list for the 1–9 home-screen
// shortcuts. Persisted order leads; name and ID provide deterministic fallback
// ordering for older or imported favorites without a local slot.
func (m modelState) favoriteShortcutHosts() []model.Host {
	hosts := make([]model.Host, 0, len(m.hosts))
	for _, host := range m.hosts {
		if host.Favorite {
			hosts = append(hosts, host)
		}
	}
	sort.SliceStable(hosts, func(i, j int) bool { return favoriteHostLess(hosts[i], hosts[j]) })
	if len(hosts) > 9 {
		hosts = hosts[:9]
	}
	return hosts
}

func favoriteHostLess(left, right model.Host) bool {
	leftOrder, rightOrder := left.FavoriteOrder, right.FavoriteOrder
	maxInt := int(^uint(0) >> 1)
	if leftOrder <= 0 {
		leftOrder = maxInt
	}
	if rightOrder <= 0 {
		rightOrder = maxInt
	}
	if leftOrder != rightOrder {
		return leftOrder < rightOrder
	}
	leftName, rightName := strings.ToLower(left.Name), strings.ToLower(right.Name)
	if leftName == rightName {
		return left.Id < right.Id
	}
	return leftName < rightName
}

func (m modelState) nextFavoriteOrder() int {
	next := 1
	for _, host := range m.hosts {
		if host.Favorite && host.FavoriteOrder >= next {
			next = host.FavoriteOrder + 1
		}
	}
	return next
}

func (m *modelState) moveSelectedFavorite(direction int) tea.Cmd {
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return nil
	}
	if m.favoriteOrderUpdating || m.favoriteUpdatingID != "" {
		m.statusText, m.statusErr = "收藏快捷顺序正在保存，请稍候", false
		return nil
	}
	host := m.selectedHost()
	if host == nil || !host.Favorite {
		m.statusText, m.statusErr = "请先收藏并选择要调整的主机", true
		return nil
	}
	catalog, ok := m.catalog.(favoriteOrderCatalog)
	if !ok {
		m.statusText, m.statusErr = "当前主机目录不支持快捷顺序调整", true
		return nil
	}
	m.favoriteOrderUpdating = true
	m.statusText, m.statusErr = "正在保存收藏快捷顺序…", false
	return func() tea.Msg {
		ids, err := catalog.MoveFavoriteHost(context.Background(), host.Id, direction)
		return hostFavoriteOrderUpdatedMsg{ids: ids, err: err}
	}
}

func (m modelState) favoriteShortcutNumbers() map[string]int {
	hosts := m.favoriteShortcutHosts()
	numbers := make(map[string]int, len(hosts))
	for index, host := range hosts {
		numbers[host.Id] = index + 1
	}
	return numbers
}

func (m *modelState) connectFavoriteShortcut(key string) tea.Cmd {
	if m.loading || m.activePane != hostPane || len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return nil
	}
	number := int(key[0] - '0')
	numbers, _ := m.currentShortcutNumbers()
	for hostID, shortcut := range numbers {
		if shortcut == number {
			if host := m.hostByID(hostID); host != nil {
				return m.startHostConnect(*host, m.newTabTarget())
			}
			break
		}
	}
	return nil
}
