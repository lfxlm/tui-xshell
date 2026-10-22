package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type batchEditField uint8

const (
	batchEditGroup batchEditField = iota
	batchEditFavorite
	batchEditDescription
	batchEditUser
	batchEditPort
	batchEditIdentityFile
	batchEditNameTemplate
)

type batchEditState struct {
	hostIDs        []string
	selectedHostID string
	field          batchEditField
	groupID        string
	groupEnabled   bool
	favoriteMode   int // 0 keep, 1 favorite all, 2 unfavorite all
	description    string
	descriptionOn  bool
	user           string
	userOn         bool
	port           string
	portOn         bool
	identityFile   string
	identityFileOn bool
	nameTemplate   string
	nameTemplateOn bool
	originalNames  map[string]string
	busy           bool
	errorText      string
}

func (m *modelState) toggleBatchHostSelection() {
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请切换到主机列表后用空格选择主机", true
		return
	}
	if host := m.selectedHost(); host != nil {
		if m.batchSelectedHostIDs == nil {
			m.batchSelectedHostIDs = make(map[string]bool)
		}
		if m.batchSelectedHostIDs[host.Id] {
			delete(m.batchSelectedHostIDs, host.Id)
		} else {
			m.batchSelectedHostIDs[host.Id] = true
		}
		if len(m.batchSelectedHostIDs) == 0 {
			m.batchSelectedHostIDs = nil
		}
		m.statusText = fmt.Sprintf("已选择 %d 台主机 · B 批量编辑 · Esc 清除选择", len(m.batchSelectedHostIDs))
		m.statusErr = false
		return
	}
	m.statusText, m.statusErr = "请先选择主机", true
}

func (m modelState) batchSelectedCount() int {
	return len(m.batchSelectedHostIDs)
}

func (m modelState) selectedBatchHostIDs() []string {
	ids := make([]string, 0, len(m.batchSelectedHostIDs))
	for _, host := range m.hosts {
		if m.batchSelectedHostIDs[host.Id] {
			ids = append(ids, host.Id)
		}
	}
	return ids
}

func (m *modelState) openBatchEdit() (tea.Model, tea.Cmd) {
	if m.loading {
		m.statusText, m.statusErr = "请等待本地数据加载完成", false
		return m, nil
	}
	if m.activePane != hostPane {
		m.statusText, m.statusErr = "请先切换到主机列表", true
		return m, nil
	}
	ids := m.selectedBatchHostIDs()
	if len(ids) == 0 {
		m.statusText, m.statusErr = "先用空格选择一台或多台主机，再按 B 批量编辑", false
		return m, nil
	}
	if _, ok := m.catalog.(batchHostCatalog); !ok {
		m.statusText, m.statusErr = "当前主机目录不支持事务式批量修改", true
		return m, nil
	}
	first := m.hostByID(ids[0])
	state := &batchEditState{hostIDs: ids, favoriteMode: 0, originalNames: make(map[string]string, len(ids))}
	for _, id := range ids {
		if host := m.hostByID(id); host != nil {
			state.originalNames[id] = host.Name
		}
	}
	if first != nil {
		state.groupID = first.GroupId
	}
	if host := m.selectedHost(); host != nil {
		state.selectedHostID = host.Id
	}
	m.batchEdit = state
	return m, nil
}

func (m *modelState) updateBatchEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.batchEdit
	if state == nil || state.busy {
		return m, nil
	}
	key := msg.String()
	switch key {
	case "esc", "escape", "ctrl+c":
		m.batchEdit = nil
		return m, nil
	case "up", "k":
		state.field = batchEditField(clamp(int(state.field)-1, int(batchEditGroup), int(batchEditNameTemplate)))
	case "down", "j", "tab":
		state.field = batchEditField(clamp(int(state.field)+1, int(batchEditGroup), int(batchEditNameTemplate)))
	case "shift+tab":
		state.field = batchEditField(clamp(int(state.field)-1, int(batchEditGroup), int(batchEditNameTemplate)))
	case "ctrl+t":
		m.toggleBatchEditField()
	case "left", "h", "right", "l":
		direction := 1
		if key == "left" || key == "h" {
			direction = -1
		}
		switch state.field {
		case batchEditGroup:
			state.groupID = m.cycleGroupID(state.groupID, direction)
		case batchEditFavorite:
			state.favoriteMode = (state.favoriteMode + direction + 3) % 3
		}
	case " ", "space":
		if state.field == batchEditGroup {
			state.groupEnabled = !state.groupEnabled
		} else if state.field == batchEditFavorite {
			state.favoriteMode = (state.favoriteMode + 1) % 3
		} else {
			m.appendBatchEditText(" ")
		}
	case "backspace", "ctrl+h":
		m.backspaceBatchEditText()
	case "enter":
		return m.submitBatchEdit()
	default:
		if text := msg.Key().Text; text != "" {
			m.appendBatchEditText(text)
		}
	}
	state.errorText = ""
	return m, nil
}

func (m *modelState) toggleBatchEditField() {
	if m.batchEdit == nil {
		return
	}
	switch m.batchEdit.field {
	case batchEditGroup:
		m.batchEdit.groupEnabled = !m.batchEdit.groupEnabled
	case batchEditDescription:
		m.batchEdit.descriptionOn = !m.batchEdit.descriptionOn
	case batchEditUser:
		m.batchEdit.userOn = !m.batchEdit.userOn
	case batchEditPort:
		m.batchEdit.portOn = !m.batchEdit.portOn
	case batchEditIdentityFile:
		m.batchEdit.identityFileOn = !m.batchEdit.identityFileOn
	case batchEditNameTemplate:
		m.batchEdit.nameTemplateOn = !m.batchEdit.nameTemplateOn
	}
}

func (m *modelState) appendBatchEditText(text string) {
	state := m.batchEdit
	if state == nil {
		return
	}
	appendLimited := func(current *string, limit int) {
		if len([]rune(*current))+len([]rune(text)) <= limit {
			*current += text
		}
	}
	switch state.field {
	case batchEditDescription:
		if state.descriptionOn {
			appendLimited(&state.description, 1024)
		}
	case batchEditUser:
		if state.userOn {
			appendLimited(&state.user, 256)
		}
	case batchEditPort:
		if state.portOn {
			appendLimited(&state.port, 5)
		}
	case batchEditIdentityFile:
		if state.identityFileOn {
			appendLimited(&state.identityFile, 4096)
		}
	case batchEditNameTemplate:
		if state.nameTemplateOn {
			appendLimited(&state.nameTemplate, 512)
		}
	}
}

func (m *modelState) backspaceBatchEditText() {
	state := m.batchEdit
	if state == nil {
		return
	}
	var value *string
	switch state.field {
	case batchEditDescription:
		if state.descriptionOn {
			value = &state.description
		}
	case batchEditUser:
		if state.userOn {
			value = &state.user
		}
	case batchEditPort:
		if state.portOn {
			value = &state.port
		}
	case batchEditIdentityFile:
		if state.identityFileOn {
			value = &state.identityFile
		}
	case batchEditNameTemplate:
		if state.nameTemplateOn {
			value = &state.nameTemplate
		}
	}
	if value != nil {
		runes := []rune(*value)
		if len(runes) > 0 {
			*value = string(runes[:len(runes)-1])
		}
	}
}

func (m *modelState) submitBatchEdit() (tea.Model, tea.Cmd) {
	state := m.batchEdit
	if state == nil {
		return m, nil
	}
	patch := model.HostBatchPatch{}
	if state.groupEnabled {
		value := state.groupID
		patch.GroupID = &value
	}
	if state.favoriteMode == 1 {
		value := true
		patch.Favorite = &value
	} else if state.favoriteMode == 2 {
		value := false
		patch.Favorite = &value
	}
	if state.descriptionOn {
		value := state.description
		patch.Description = &value
	}
	if state.userOn {
		value := state.user
		patch.User = &value
	}
	if state.portOn {
		value, err := strconv.Atoi(strings.TrimSpace(state.port))
		if err != nil || value < 1 || value > 65535 {
			state.errorText = "端口必须是 1 到 65535 之间的数字。"
			state.field = batchEditPort
			return m, nil
		}
		patch.Port = &value
	}
	if state.identityFileOn {
		value := state.identityFile
		patch.IdentityFile = &value
	}
	if state.nameTemplateOn {
		names, err := renderBatchRenameNames(state.hostIDs, state.originalNames, state.nameTemplate)
		if err != nil {
			state.errorText = err.Error()
			state.field = batchEditNameTemplate
			return m, nil
		}
		patch.Names = names
	}
	if patch.GroupID == nil && patch.Favorite == nil && patch.Description == nil && patch.User == nil && patch.Port == nil && patch.IdentityFile == nil && len(patch.Names) == 0 {
		state.errorText = "至少启用一个要修改的字段。"
		return m, nil
	}
	catalog, ok := m.catalog.(batchHostCatalog)
	if !ok {
		state.errorText = "当前主机目录不支持事务式批量修改。"
		return m, nil
	}
	ids := append([]string(nil), state.hostIDs...)
	selectedHostID := state.selectedHostID
	hostCatalog := m.catalog
	state.busy = true
	return m, func() tea.Msg {
		if err := catalog.BatchUpdateHosts(context.Background(), ids, patch); err != nil {
			return batchHostsUpdatedMsg{ids: ids, selectedHostID: selectedHostID, patch: patch, updateErr: err}
		}
		hosts, err := hostCatalog.ListHosts(context.Background())
		return batchHostsUpdatedMsg{ids: ids, selectedHostID: selectedHostID, patch: patch, hosts: hosts, reloadErr: err}
	}
}

func applyBatchPatchToHost(host *model.Host, patch model.HostBatchPatch) {
	if name, ok := patch.Names[host.Id]; ok {
		host.Name = name
	}
	if patch.GroupID != nil {
		host.GroupId = *patch.GroupID
	}
	if patch.Favorite != nil {
		host.Favorite = *patch.Favorite
		if !*patch.Favorite {
			host.FavoriteOrder = 0
		}
	}
	if patch.Description != nil {
		host.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.User != nil {
		host.User = strings.TrimSpace(*patch.User)
	}
	if patch.Port != nil {
		host.Port = *patch.Port
	}
	if patch.IdentityFile != nil {
		host.IdentityFile = strings.TrimSpace(*patch.IdentityFile)
	}
}

func renderBatchRenameNames(hostIDs []string, originalNames map[string]string, template string) (map[string]string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return nil, fmt.Errorf("名称模板不能为空")
	}
	if len(hostIDs) > 1 && !strings.Contains(template, "{name}") && !strings.Contains(template, "{index}") {
		return nil, fmt.Errorf("多台主机请在模板中使用 {name} 或 {index}，避免全部改成同一个名称")
	}
	names := make(map[string]string, len(hostIDs))
	for index, id := range hostIDs {
		original, ok := originalNames[id]
		if !ok {
			return nil, fmt.Errorf("找不到主机 %s 的原名称，请取消后重新选择", id)
		}
		name := strings.NewReplacer(
			"{name}", original,
			"{index}", strconv.Itoa(index+1),
		).Replace(template)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("第 %d 台主机生成了空名称", index+1)
		}
		names[id] = name
	}
	return names, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func batchEditGroupLabel(m modelState, groupID string) string {
	if groupID == "" {
		return "未分组"
	}
	return m.groupPath(groupID)
}

func batchEditFavoriteLabel(mode int) string {
	switch mode {
	case 1:
		return "全部收藏"
	case 2:
		return "全部取消收藏"
	default:
		return "保持不变"
	}
}

func batchEditCountLabel(state *batchEditState) string {
	return fmt.Sprintf("已选择 %d 台主机", len(state.hostIDs))
}
