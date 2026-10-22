package ui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const (
	workspaceTabGroupMaxRunes   = 48
	workspaceTabGroupUnassigned = "未分组"
)

type tabGroupOption struct {
	name  string
	count int
}

type tabGroupEditMode uint8

const (
	tabGroupCreateMode tabGroupEditMode = iota
	tabGroupRenameMode
)

type tabGroupManagerState struct {
	selected int
	editing  bool
	mode     tabGroupEditMode
	oldName  string
	name     string
	error    string
}

func normalizeWorkspaceTabGroupName(value string) (string, error) {
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", fmt.Errorf("标签组名称不能包含控制字符")
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("标签组名称不能为空")
	}
	if value == workspaceTabGroupUnassigned {
		return "", fmt.Errorf("标签组名称不能使用保留名称「%s」", workspaceTabGroupUnassigned)
	}
	if len([]rune(value)) > workspaceTabGroupMaxRunes {
		return "", fmt.Errorf("标签组名称不能超过 %d 个字符", workspaceTabGroupMaxRunes)
	}
	return value, nil
}

func (m modelState) workspaceTabGroupOptions() []tabGroupOption {
	options := []tabGroupOption{{name: ""}}
	indices := map[string]int{"": 0}
	for _, tab := range m.tabs {
		if tab == nil {
			continue
		}
		index, exists := indices[tab.groupName]
		if !exists {
			index = len(options)
			indices[tab.groupName] = index
			options = append(options, tabGroupOption{name: tab.groupName})
		}
		options[index].count++
	}
	return options
}

func (m *modelState) openWorkspaceTabGroupManager() {
	if m.activeTab() == nil {
		m.statusText, m.statusErr = "当前没有打开的标签", true
		return
	}
	manager := &tabGroupManagerState{}
	activeGroup := m.activeTab().groupName
	for index, option := range m.workspaceTabGroupOptions() {
		if option.name == activeGroup {
			manager.selected = index
			break
		}
	}
	m.tabGroupManager = manager
}

func (m modelState) workspaceTabGroupManagerView() string {
	manager := m.tabGroupManager
	if manager == nil {
		return ""
	}
	if manager.editing {
		action := "新建"
		if manager.mode == tabGroupRenameMode {
			action = "重命名"
		}
		lines := []string{
			"名称：" + manager.name + "▏",
			"",
			mutedStyle.Render(fmt.Sprintf("名称最多 %d 个字符；组名仅保存在标签布局中。", workspaceTabGroupMaxRunes)),
		}
		if manager.error != "" {
			lines = append(lines, "", warningStyle.Render(manager.error))
		}
		lines = append(lines, "", mutedStyle.Render("Enter 保存 · Esc 返回"))
		return m.modalView(action+"标签组", lines, 72)
	}
	options := m.workspaceTabGroupOptions()
	lines := []string{mutedStyle.Render("按组查看标签数量；移动或重命名不会断开连接。"), ""}
	for index, option := range options {
		name := option.name
		if name == "" {
			name = workspaceTabGroupUnassigned
		}
		line := fmt.Sprintf("%-24s %2d 个标签", name, option.count)
		if index == manager.selected {
			lines = append(lines, selectedStyle.Render("› "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if manager.error != "" {
		lines = append(lines, "", warningStyle.Render(manager.error))
	}
	lines = append(lines, "", mutedStyle.Render("↑/↓ 选择 · Enter 切换到组 · m 移动当前标签 · n 新建 · r 重命名 · d 解散 · Esc 关闭"))
	return m.modalView("标签组", lines, 78)
}

func (m *modelState) updateWorkspaceTabGroupManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	manager := m.tabGroupManager
	if manager == nil {
		return m, nil
	}
	key := msg.Key()
	if manager.editing {
		switch {
		case key.Code == tea.KeyEscape || key.Text == "ctrl+c":
			manager.editing = false
			manager.name = ""
			manager.error = ""
		case key.Code == tea.KeyEnter:
			name, err := normalizeWorkspaceTabGroupName(manager.name)
			if err == nil {
				for _, option := range m.workspaceTabGroupOptions() {
					if option.name != manager.oldName && strings.EqualFold(option.name, name) {
						err = fmt.Errorf("标签组「%s」已存在", option.name)
						break
					}
				}
			}
			if err != nil {
				manager.error = err.Error()
				return m, nil
			}
			if manager.mode == tabGroupRenameMode {
				for _, tab := range m.tabs {
					if tab != nil && tab.groupName == manager.oldName {
						tab.groupName = name
					}
				}
				m.normalizeWorkspaceTabOrder()
				m.statusText = fmt.Sprintf("已将标签组「%s」重命名为「%s」", manager.oldName, name)
			} else {
				m.moveActiveWorkspaceTabToGroup(name)
				m.statusText = fmt.Sprintf("已创建标签组「%s」并移动当前标签", name)
			}
			m.statusErr = false
			m.tabGroupManager = nil
			return m, nil
		case key.Code == tea.KeyBackspace || key.Code == tea.KeyDelete:
			runes := []rune(manager.name)
			if len(runes) > 0 {
				manager.name = string(runes[:len(runes)-1])
			}
			manager.error = ""
		default:
			if key.Text != "" && len([]rune(manager.name))+len([]rune(key.Text)) <= workspaceTabGroupMaxRunes {
				manager.name += key.Text
				manager.error = ""
			}
		}
		return m, nil
	}
	options := m.workspaceTabGroupOptions()
	switch {
	case key.Code == tea.KeyEscape || key.Text == "q":
		m.tabGroupManager = nil
	case key.Code == tea.KeyUp || key.Text == "k":
		manager.selected = max(0, manager.selected-1)
		manager.error = ""
	case key.Code == tea.KeyDown || key.Text == "j":
		manager.selected = min(len(options)-1, manager.selected+1)
		manager.error = ""
	case key.Code == tea.KeyEnter:
		option := options[clamp(manager.selected, 0, len(options)-1)]
		for _, tab := range m.tabs {
			if tab != nil && tab.groupName == option.name {
				m.activateWorkspaceTab(tab.id)
				m.workspaceVisible = true
				m.tabGroupManager = nil
				m.statusText = "已切换到标签组「" + groupDisplayName(option.name) + "」"
				m.statusErr = false
				return m, tea.Batch(m.resizeActiveWorkspace()...)
			}
		}
		manager.error = "此组目前没有标签"
	case key.Text == "m":
		option := options[clamp(manager.selected, 0, len(options)-1)]
		active := m.activeTab()
		if active == nil {
			manager.error = "当前没有可移动的标签"
			return m, nil
		}
		if active.groupName == option.name {
			manager.error = "当前标签已经属于这个组"
			return m, nil
		}
		m.moveActiveWorkspaceTabToGroup(option.name)
		m.tabGroupManager = nil
		m.statusText = "已将当前标签移动到「" + groupDisplayName(option.name) + "」"
		m.statusErr = false
		return m, nil
	case key.Text == "n":
		manager.editing = true
		manager.mode = tabGroupCreateMode
		manager.oldName = ""
		manager.name = ""
		manager.error = ""
	case key.Text == "r":
		option := options[clamp(manager.selected, 0, len(options)-1)]
		if option.name == "" {
			manager.error = "未分组不能重命名"
			return m, nil
		}
		manager.editing = true
		manager.mode = tabGroupRenameMode
		manager.oldName = option.name
		manager.name = option.name
		manager.error = ""
	case key.Text == "d":
		option := options[clamp(manager.selected, 0, len(options)-1)]
		if option.name == "" {
			manager.error = "未分组没有可解散的标签组"
			return m, nil
		}
		for _, tab := range m.tabs {
			if tab != nil && tab.groupName == option.name {
				tab.groupName = ""
			}
		}
		m.normalizeWorkspaceTabOrder()
		manager.selected = 0
		manager.error = ""
		m.statusText = "已解散标签组「" + option.name + "」，标签仍保持打开"
		m.statusErr = false
	}
	return m, nil
}

func (m *modelState) moveActiveWorkspaceTabToGroup(groupName string) {
	tab := m.activeTab()
	if tab == nil {
		return
	}
	tab.groupName = groupName
	m.normalizeWorkspaceTabOrder()
}

func (m *modelState) normalizeWorkspaceTabOrder() {
	if len(m.tabs) < 2 {
		return
	}
	groupOrder := []string{""}
	seen := map[string]bool{"": true}
	for _, tab := range m.tabs {
		if tab == nil || seen[tab.groupName] {
			continue
		}
		seen[tab.groupName] = true
		groupOrder = append(groupOrder, tab.groupName)
	}
	ordered := make([]*workspaceTab, 0, len(m.tabs))
	for _, groupName := range groupOrder {
		for _, tab := range m.tabs {
			if tab != nil && tab.groupName == groupName {
				ordered = append(ordered, tab)
			}
		}
	}
	m.tabs = ordered
}

func groupDisplayName(name string) string {
	if name == "" {
		return workspaceTabGroupUnassigned
	}
	return name
}

func (m modelState) workspaceTabGroupNameAt(index int) string {
	if index < 0 || index >= len(m.tabs) || m.tabs[index] == nil {
		return ""
	}
	return groupDisplayName(m.tabs[index].groupName)
}

func (m *modelState) addWorkspaceTab(tab *workspaceTab) {
	if tab == nil {
		return
	}
	m.tabs = append(m.tabs, tab)
	m.normalizeWorkspaceTabOrder()
}
