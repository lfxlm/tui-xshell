package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

const (
	workspaceLayoutVersion  = 2
	maxWorkspaceLayoutTabs  = 64
	maxWorkspaceLayoutPanes = 128
	maxWorkspaceLayoutDepth = 16
)

type workspaceLayoutSnapshot struct {
	Version   int                          `json:"version"`
	ActiveTab int                          `json:"active_tab"`
	Tabs      []workspaceLayoutTabSnapshot `json:"tabs"`
}

type workspaceLayoutTabSnapshot struct {
	Root         *workspaceLayoutNode `json:"root"`
	ActivePane   int                  `json:"active_pane"`
	GroupName    string               `json:"group,omitempty"`
	Synchronized bool                 `json:"synchronized_input,omitempty"`
}

type workspaceLayoutNode struct {
	HostID   string               `json:"host_id,omitempty"`
	HostName string               `json:"host_name,omitempty"`
	Axis     string               `json:"axis,omitempty"`
	Ratio    int                  `json:"ratio,omitempty"`
	First    *workspaceLayoutNode `json:"first,omitempty"`
	Second   *workspaceLayoutNode `json:"second,omitempty"`
}

type workspaceLayoutRestoreConnection struct {
	tabID  string
	paneID string
	hostID string
}

type workspaceLayoutRestoreState struct {
	id          uint64
	name        string
	activeTabID string
	current     *workspaceLayoutRestoreConnection
	remaining   []workspaceLayoutRestoreConnection
	total       int
	failed      int
}

func (m *modelState) openWorkspaceLayoutManager() tea.Cmd {
	store, ok := m.catalog.(workspaceLayoutCatalog)
	if !ok {
		m.statusText, m.statusErr = "本地目录存储不支持保存工作区布局", true
		return nil
	}
	m.nextWorkspaceLayoutID++
	requestID := m.nextWorkspaceLayoutID
	m.workspaceLayouts = &workspaceLayoutManagerState{loading: true, requestID: requestID}
	return func() tea.Msg {
		layouts, err := store.ListWorkspaceLayouts(context.Background())
		return workspaceLayoutsLoadedMsg{requestID: requestID, layouts: layouts, err: err}
	}
}

func (m modelState) refreshWorkspaceLayouts() (modelState, tea.Cmd) {
	manager := m.workspaceLayouts
	store, ok := m.catalog.(workspaceLayoutCatalog)
	if manager == nil || !ok {
		return m, nil
	}
	m.nextWorkspaceLayoutID++
	requestID := m.nextWorkspaceLayoutID
	manager.requestID = requestID
	manager.loading = true
	return m, func() tea.Msg {
		layouts, err := store.ListWorkspaceLayouts(context.Background())
		return workspaceLayoutsLoadedMsg{requestID: requestID, layouts: layouts, err: err}
	}
}

func (m modelState) saveCurrentWorkspaceLayout(name string) tea.Cmd {
	manager := m.workspaceLayouts
	store, ok := m.catalog.(workspaceLayoutCatalog)
	if manager == nil || !ok {
		return nil
	}
	data, err := encodeWorkspaceLayout(m)
	if err != nil {
		manager.errorText = err.Error()
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		manager.errorText = "布局名称不能为空。"
		return nil
	}
	var id string
	for _, existing := range manager.layouts {
		if strings.EqualFold(existing.Name, name) {
			id = existing.Id
			break
		}
	}
	manager.busy = true
	manager.errorText = ""
	layout := model.WorkspaceLayout{Id: id, Name: name, Data: data}
	return func() tea.Msg {
		saved, err := store.SaveWorkspaceLayout(context.Background(), layout)
		return workspaceLayoutSavedMsg{layout: saved, err: err}
	}
}

func (m modelState) deleteWorkspaceLayout(id string) tea.Cmd {
	manager := m.workspaceLayouts
	store, ok := m.catalog.(workspaceLayoutCatalog)
	if manager == nil || !ok || id == "" {
		return nil
	}
	manager.busy = true
	manager.errorText = ""
	return func() tea.Msg {
		return workspaceLayoutDeletedMsg{id: id, err: store.DeleteWorkspaceLayout(context.Background(), id)}
	}
}

func encodeWorkspaceLayout(m modelState) (string, error) {
	terminalTabs := make([]*workspaceTab, 0, len(m.tabs))
	for _, tab := range m.tabs {
		if tab == nil || tab.sftp != nil {
			continue
		}
		terminalTabs = append(terminalTabs, tab)
	}
	if len(terminalTabs) == 0 {
		return "", errors.New("当前没有可保存的终端 pane 布局。")
	}
	if len(terminalTabs) > maxWorkspaceLayoutTabs {
		return "", fmt.Errorf("最多保存 %d 个终端标签的布局", maxWorkspaceLayoutTabs)
	}
	snapshot := workspaceLayoutSnapshot{Version: workspaceLayoutVersion, Tabs: make([]workspaceLayoutTabSnapshot, 0, len(terminalTabs))}
	for index, tab := range terminalTabs {
		if tab == nil || tab.root == nil {
			return "", fmt.Errorf("第 %d 个终端标签没有可保存的 pane", index+1)
		}
		root, err := snapshotWorkspaceNode(tab.root)
		if err != nil {
			return "", fmt.Errorf("保存第 %d 个标签: %w", index+1, err)
		}
		panes := collectPanes(tab.root)
		if len(panes) > maxWorkspaceLayoutPanes {
			return "", fmt.Errorf("第 %d 个标签的 pane 数超过上限 %d", index+1, maxWorkspaceLayoutPanes)
		}
		activePane := 0
		for paneIndex, pane := range panes {
			if pane.id == tab.activePaneID {
				activePane = paneIndex
				break
			}
		}
		snapshot.Tabs = append(snapshot.Tabs, workspaceLayoutTabSnapshot{
			Root: root, ActivePane: activePane, GroupName: tab.groupName, Synchronized: tab.syncInput,
		})
		if tab.id == m.activeTabID {
			snapshot.ActiveTab = index
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("编码布局: %w", err)
	}
	if len(data) > 256<<10 {
		return "", errors.New("布局数据超过 256 KiB，无法保存")
	}
	return string(data), nil
}

func snapshotWorkspaceNode(node *paneNode) (*workspaceLayoutNode, error) {
	if node == nil {
		return nil, errors.New("布局树中存在空节点")
	}
	if node.pane != nil {
		if node.first != nil || node.second != nil || node.pane.host.Id == "" {
			return nil, errors.New("含未保存或无效的临时连接；请先将主机保存到主机目录")
		}
		return &workspaceLayoutNode{HostID: node.pane.host.Id, HostName: node.pane.host.Name}, nil
	}
	if node.first == nil || node.second == nil || node.ratio < 1 || node.ratio > 99 {
		return nil, errors.New("分屏结构或比例无效")
	}
	axis := "vertical"
	if node.axis == splitHorizontal {
		axis = "horizontal"
	}
	first, err := snapshotWorkspaceNode(node.first)
	if err != nil {
		return nil, err
	}
	second, err := snapshotWorkspaceNode(node.second)
	if err != nil {
		return nil, err
	}
	return &workspaceLayoutNode{Axis: axis, Ratio: node.ratio, First: first, Second: second}, nil
}

func decodeWorkspaceLayout(data string) (workspaceLayoutSnapshot, error) {
	var snapshot workspaceLayoutSnapshot
	if len(data) == 0 || len(data) > 256<<10 {
		return snapshot, errors.New("布局数据为空或超过大小上限")
	}
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		return snapshot, fmt.Errorf("布局数据格式错误: %w", err)
	}
	if snapshot.Version != 1 && snapshot.Version != workspaceLayoutVersion {
		return snapshot, fmt.Errorf("不支持的布局版本 %d", snapshot.Version)
	}
	if len(snapshot.Tabs) == 0 || len(snapshot.Tabs) > maxWorkspaceLayoutTabs || snapshot.ActiveTab < 0 || snapshot.ActiveTab >= len(snapshot.Tabs) {
		return snapshot, errors.New("布局标签数量或活动标签索引无效")
	}
	for index, tab := range snapshot.Tabs {
		if tab.GroupName != "" {
			validated, err := normalizeWorkspaceTabGroupName(tab.GroupName)
			if err != nil {
				return snapshot, fmt.Errorf("第 %d 个标签的标签组无效: %w", index+1, err)
			}
			snapshot.Tabs[index].GroupName = validated
		}
		if tab.Root == nil {
			return snapshot, fmt.Errorf("第 %d 个标签缺少根节点", index+1)
		}
		count := 0
		if err := validateWorkspaceLayoutNode(tab.Root, 0, &count); err != nil {
			return snapshot, fmt.Errorf("第 %d 个标签布局无效: %w", index+1, err)
		}
		if count > maxWorkspaceLayoutPanes || tab.ActivePane < 0 || tab.ActivePane >= count {
			return snapshot, fmt.Errorf("第 %d 个标签的 pane 数量或活动 pane 索引无效", index+1)
		}
	}
	return snapshot, nil
}

func validateWorkspaceLayoutNode(node *workspaceLayoutNode, depth int, count *int) error {
	if node == nil || depth > maxWorkspaceLayoutDepth {
		return errors.New("布局树深度超出限制或节点为空")
	}
	if node.HostID != "" {
		if node.First != nil || node.Second != nil || node.Axis != "" || node.Ratio != 0 {
			return errors.New("pane 节点包含分屏字段")
		}
		*count++
		return nil
	}
	if node.First == nil || node.Second == nil || node.HostName != "" || (node.Axis != "vertical" && node.Axis != "horizontal") || node.Ratio < 1 || node.Ratio > 99 {
		return errors.New("分屏节点字段不完整")
	}
	if err := validateWorkspaceLayoutNode(node.First, depth+1, count); err != nil {
		return err
	}
	return validateWorkspaceLayoutNode(node.Second, depth+1, count)
}

func (m *modelState) restoreWorkspaceLayout(layout model.WorkspaceLayout) tea.Cmd {
	snapshot, err := decodeWorkspaceLayout(layout.Data)
	if err != nil {
		m.workspaceLayouts.errorText = "无法恢复布局：" + err.Error()
		return nil
	}
	if len(m.tabs)+len(snapshot.Tabs) > maxWorkspaceLayoutTabs {
		m.workspaceLayouts.errorText = fmt.Sprintf("恢复后标签总数会超过 %d 个，请先关闭部分标签", maxWorkspaceLayoutTabs)
		return nil
	}
	hosts := make(map[string]model.Host, len(m.hosts))
	for _, host := range m.hosts {
		hosts[host.Id] = host
	}
	if err := checkWorkspaceLayoutHosts(snapshot, hosts); err != nil {
		m.workspaceLayouts.errorText = "无法恢复布局：" + err.Error()
		return nil
	}
	state := &workspaceLayoutRestoreState{name: layout.Name}
	for tabIndex, tabSnapshot := range snapshot.Tabs {
		tabID := m.nextWorkspaceID("tab")
		root := m.restoreWorkspaceLayoutNode(tabSnapshot.Root, hosts, tabID, state)
		panes := collectPanes(root)
		activePane := clamp(tabSnapshot.ActivePane, 0, len(panes)-1)
		tab := &workspaceTab{
			id: tabID, groupName: tabSnapshot.GroupName, root: root,
			activePaneID: panes[activePane].id, syncInput: tabSnapshot.Synchronized,
		}
		m.tabs = append(m.tabs, tab)
		if tabIndex == snapshot.ActiveTab {
			state.activeTabID = tabID
		}
	}
	state.id = m.nextWorkspaceLayoutID + 1
	m.nextWorkspaceLayoutID = state.id
	state.total = len(state.remaining)
	m.layoutRestore = state
	m.workspaceLayouts = nil
	m.activateWorkspaceTab(state.activeTabID)
	m.workspaceVisible = true
	m.statusText = fmt.Sprintf("正在恢复布局「%s」：依次连接 %d 个 pane", layout.Name, state.total)
	m.statusErr = false
	return m.startNextWorkspaceLayoutConnection()
}

func (m *modelState) restoreLastWorkspace() tea.Cmd {
	if m.lastWorkspaceData == "" {
		m.statusText, m.statusErr = "没有可恢复的上次工作区", true
		return nil
	}
	if len(m.tabs) > 0 {
		m.statusText, m.statusErr = "请先关闭当前会话，再恢复上次工作区", true
		return nil
	}
	m.workspaceLayouts = &workspaceLayoutManagerState{}
	command := m.restoreWorkspaceLayout(model.WorkspaceLayout{Name: "上次工作区", Data: m.lastWorkspaceData})
	if m.layoutRestore == nil && m.workspaceLayouts != nil {
		m.statusText = m.workspaceLayouts.errorText
		if m.statusText == "" {
			m.statusText = "无法恢复上次工作区"
		}
		m.statusErr = true
		m.workspaceLayouts = nil
	}
	return command
}

func checkWorkspaceLayoutHosts(snapshot workspaceLayoutSnapshot, hosts map[string]model.Host) error {
	var walk func(*workspaceLayoutNode) error
	walk = func(node *workspaceLayoutNode) error {
		if node.HostID != "" {
			host, exists := hosts[node.HostID]
			if !exists {
				name := node.HostName
				if name == "" {
					name = node.HostID
				}
				return fmt.Errorf("主机「%s」已不在本地主机目录中", name)
			}
			if host.Kind == model.HostKindFTP {
				return fmt.Errorf("FTP/FTPS 主机「%s」不能放入终端布局", host.Name)
			}
			return nil
		}
		if err := walk(node.First); err != nil {
			return err
		}
		return walk(node.Second)
	}
	for index, tab := range snapshot.Tabs {
		if err := walk(tab.Root); err != nil {
			return fmt.Errorf("第 %d 个标签引用无效: %w", index+1, err)
		}
	}
	return nil
}

func (m *modelState) restoreWorkspaceLayoutNode(definition *workspaceLayoutNode, hosts map[string]model.Host, tabID string, restore *workspaceLayoutRestoreState) *paneNode {
	if definition.HostID != "" {
		host := hosts[definition.HostID]
		paneID := m.nextWorkspaceID("pane")
		terminal := vt.NewSafeEmulator(max(20, m.width), max(5, m.height-3))
		terminal.SetScrollbackSize(5000)
		pane := &terminalPane{id: paneID, host: host, terminal: terminal, statusText: "等待布局恢复连接…"}
		restore.remaining = append(restore.remaining, workspaceLayoutRestoreConnection{tabID: tabID, paneID: paneID, hostID: host.Id})
		return &paneNode{pane: pane}
	}
	axis := splitVertical
	if definition.Axis == "horizontal" {
		axis = splitHorizontal
	}
	return &paneNode{
		axis: axis, ratio: definition.Ratio,
		first:  m.restoreWorkspaceLayoutNode(definition.First, hosts, tabID, restore),
		second: m.restoreWorkspaceLayoutNode(definition.Second, hosts, tabID, restore),
	}
}

func (m *modelState) startNextWorkspaceLayoutConnection() tea.Cmd {
	state := m.layoutRestore
	if state == nil {
		return nil
	}
	state.current = nil
	for len(state.remaining) > 0 {
		entry := state.remaining[0]
		state.remaining = state.remaining[1:]
		tab := m.tabByID(entry.tabID)
		pane := (*terminalPane)(nil)
		if tab != nil {
			pane = findPane(tab.root, entry.paneID)
		}
		if pane == nil {
			state.failed++
			continue
		}
		rect, ok := paneRectFor(tab.root, pane.id, paneRect{width: m.width, height: max(0, m.height-2)})
		if !ok {
			pane.statusText = "无法计算 pane 尺寸；按重连键可稍后重试"
			pane.statusErr = true
			state.failed++
			continue
		}
		state.current = &entry
		m.activateWorkspaceTab(tab.id)
		edges, _ := paneFrameEdgesFor(tab.root, pane.id)
		viewWidth, viewHeight := terminalPaneViewportSizeWithEdges(rect, edges)
		width, height := m.terminalSize(max(20, viewWidth), max(5, viewHeight))
		target := connectionTarget{
			kind: reconnectPaneTarget, tabID: tab.id, paneID: pane.id,
			width: width, height: height,
			reconnectGeneration: pane.reconnectGeneration, layoutRestoreID: state.id,
		}
		pane.statusText, pane.statusErr = "正在恢复连接…", false
		if command := m.startHostConnect(pane.host, target); command != nil {
			return command
		}
		pane.statusText = "无法启动连接；按重连键可稍后重试"
		pane.statusErr = true
		state.current = nil
		state.failed++
	}
	failed, total, name, activeTabID := state.failed, state.total, state.name, state.activeTabID
	m.layoutRestore = nil
	m.activateWorkspaceTab(activeTabID)
	m.workspaceVisible = true
	if failed > 0 {
		m.statusText = fmt.Sprintf("布局「%s」已恢复；%d/%d 个 pane 连接失败，可在对应 pane 重试", name, failed, total)
		m.statusErr = true
	} else {
		m.statusText = fmt.Sprintf("布局「%s」已恢复，%d 个 pane 已连接", name, total)
		m.statusErr = false
	}
	return tea.Batch(m.resizeActiveWorkspace()...)
}

func (m *modelState) finishWorkspaceLayoutConnection(target connectionTarget, failed bool) tea.Cmd {
	state := m.layoutRestore
	if target.layoutRestoreID == 0 || state == nil || state.id != target.layoutRestoreID {
		return nil
	}
	if failed {
		state.failed++
	}
	state.current = nil
	return m.startNextWorkspaceLayoutConnection()
}

func (m *modelState) abortWorkspaceLayoutRestore(reason string) {
	state := m.layoutRestore
	if state == nil {
		return
	}
	if state.current != nil {
		if tab := m.tabByID(state.current.tabID); tab != nil {
			if pane := findPane(tab.root, state.current.paneID); pane != nil && pane.session == nil {
				pane.statusText, pane.statusErr = reason, true
			}
		}
	}
	for _, entry := range state.remaining {
		if tab := m.tabByID(entry.tabID); tab != nil {
			if pane := findPane(tab.root, entry.paneID); pane != nil {
				pane.statusText, pane.statusErr = reason, true
			}
		}
	}
	m.layoutRestore = nil
	m.statusText, m.statusErr = reason, true
}

func (m modelState) updateWorkspaceLayoutManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	manager := m.workspaceLayouts
	if manager == nil {
		return m, nil
	}
	key := msg.String()
	if manager.busy || manager.loading {
		if key == "esc" || key == "escape" {
			m.workspaceLayouts = nil
		}
		return m, nil
	}
	if manager.editingName {
		switch key {
		case "esc", "escape":
			manager.editingName = false
			manager.name = ""
		case "enter":
			return m, m.saveCurrentWorkspaceLayout(manager.name)
		case "backspace", "ctrl+h":
			name := []rune(manager.name)
			if len(name) > 0 {
				manager.name = string(name[:len(name)-1])
			}
		case "ctrl+u":
			manager.name = ""
		default:
			for _, character := range msg.Key().Text {
				if unicode.IsControl(character) {
					continue
				}
				if len([]rune(manager.name)) < 80 {
					manager.name += string(character)
				}
			}
		}
		return m, nil
	}
	if manager.confirmingDelete {
		switch key {
		case "y", "enter":
			if len(manager.layouts) > 0 {
				return m, m.deleteWorkspaceLayout(manager.layouts[manager.index].Id)
			}
			manager.confirmingDelete = false
		case "n", "esc", "escape":
			manager.confirmingDelete = false
		}
		return m, nil
	}
	switch key {
	case "esc", "escape", "q":
		m.workspaceLayouts = nil
	case "up", "k":
		manager.index = clamp(manager.index-1, 0, max(0, len(manager.layouts)-1))
		manager.top = keepVisible(manager.index, manager.top, m.workspaceLayoutRows())
	case "down", "j":
		manager.index = clamp(manager.index+1, 0, max(0, len(manager.layouts)-1))
		manager.top = keepVisible(manager.index, manager.top, m.workspaceLayoutRows())
	case "enter":
		if len(manager.layouts) > 0 {
			command := m.restoreWorkspaceLayout(manager.layouts[manager.index])
			return m, command
		}
	case "s":
		manager.editingName = true
		manager.name = ""
		manager.errorText = ""
	case "d":
		if len(manager.layouts) > 0 {
			manager.confirmingDelete = true
		}
	case "y":
		if len(manager.layouts) > 0 {
			manager.confirmingDelete = true
		}
	case "r":
		return m.refreshWorkspaceLayouts()
	}
	return m, nil
}

func (m modelState) workspaceLayoutRows() int {
	return max(1, min(10, m.height-17))
}

func (m modelState) workspaceLayoutManagerView() string {
	manager := m.workspaceLayouts
	if manager == nil {
		return ""
	}
	tabs, panes := 0, 0
	for _, tab := range m.tabs {
		tabs++
		panes += countPanes(tab.root)
	}
	lines := []string{
		brandStyle.Render("◆  已保存的会话布局"),
		mutedStyle.Render(fmt.Sprintf("当前工作区：%d 个标签 · %d 个 pane", tabs, panes)),
		"",
	}
	if manager.loading {
		lines = append(lines, accentStyle.Render("◌  正在读取布局…"))
	} else if len(manager.layouts) == 0 {
		lines = append(lines, mutedStyle.Render("还没有保存布局。"))
	} else {
		rows := m.workspaceLayoutRows()
		top := clamp(manager.top, 0, max(0, len(manager.layouts)-rows))
		bottom := min(len(manager.layouts), top+rows)
		for index := top; index < bottom; index++ {
			layout := manager.layouts[index]
			stamp := layout.UpdatedAt.Local().Format("2006-01-02 15:04")
			line := fmt.Sprintf("%s  %s", layout.Name, stamp)
			style := mutedStyle
			if index == manager.index {
				style = selectedStyle
				line = "› " + line
			} else {
				line = "  " + line
			}
			lines = append(lines, style.Width(64).Render(sanitizeRemoteText(line)))
		}
	}
	if manager.editingName {
		lines = append(lines, "", sectionStyle.Render("布局名称"), selectedStyle.Width(64).Render(manager.name+"▏"),
			mutedStyle.Render("同名布局会更新原有内容。保存的是主机引用和 pane 结构，不含凭据或终端输出。"))
	}
	if manager.confirmingDelete && len(manager.layouts) > 0 {
		lines = append(lines, "", warningStyle.Render("确认删除布局「"+manager.layouts[manager.index].Name+"」？ y 确认 · n 取消"))
	}
	if manager.errorText != "" {
		lines = append(lines, "", fitText(manager.errorText, 68))
	}
	help := "↑↓/j/k 选择 · Enter 恢复 · s 保存当前布局 · d 删除 · r 刷新 · Esc 返回"
	if manager.editingName {
		help = "输入名称 · Enter 保存 · Ctrl+U 清空 · Esc 取消"
	}
	lines = append(lines, "", mutedStyle.Render(help))
	return m.modalView("会话布局", lines, 76)
}
