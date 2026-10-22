package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sshclient"
)

type sshHostKeyManagerState struct {
	entries          []sshclient.KnownHostEntry
	index            int
	loading          bool
	busy             bool
	confirmingDelete bool
	confirmingImport bool
	pathAction       sshHostKeyPathAction
	pathInput        string
	importPlan       *sshclient.KnownHostsImportPlan
	operationText    string
	errorText        string
}

type sshHostKeyPathAction uint8

const (
	sshHostKeyPathNone sshHostKeyPathAction = iota
	sshHostKeyPathImport
	sshHostKeyPathExport
)

type sshHostKeysLoadedMsg struct {
	entries []sshclient.KnownHostEntry
	err     error
}

type sshHostKeyRemovedMsg struct {
	err error
}

type sshHostKeysImportPlannedMsg struct {
	plan sshclient.KnownHostsImportPlan
	err  error
}

type sshHostKeysImportAppliedMsg struct {
	added int
	total int
	err   error
}

type sshHostKeysExportedMsg struct {
	path    string
	records int
	err     error
}

func (m *modelState) openSSHHostKeyManager() tea.Cmd {
	m.sshHostKeyManager = &sshHostKeyManagerState{loading: true}
	return m.loadSSHHostKeysCmd()
}

func (m modelState) loadSSHHostKeysCmd() tea.Cmd {
	return func() tea.Msg {
		entries, err := sshclient.ListKnownHostEntries()
		return sshHostKeysLoadedMsg{entries: entries, err: err}
	}
}

func (m *modelState) handleSSHHostKeysLoaded(msg sshHostKeysLoadedMsg) {
	state := m.sshHostKeyManager
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取 SSH 主机指纹失败：" + msg.err.Error()
		return
	}
	state.entries = msg.entries
	state.index = clamp(state.index, 0, max(0, len(state.entries)-1))
	state.errorText = ""
}

func (m *modelState) handleSSHHostKeyRemoved(msg sshHostKeyRemovedMsg) tea.Cmd {
	state := m.sshHostKeyManager
	if state == nil {
		return nil
	}
	state.busy = false
	state.confirmingDelete = false
	if msg.err != nil {
		state.errorText = "移除失败：" + msg.err.Error()
		return nil
	}
	state.loading = true
	state.errorText = ""
	m.statusText, m.statusErr = "已移除 SSH 主机信任记录；下次连接会重新询问指纹", false
	return m.loadSSHHostKeysCmd()
}

func (m modelState) removeSSHHostKeyCmd(entry sshclient.KnownHostEntry) tea.Cmd {
	return func() tea.Msg {
		return sshHostKeyRemovedMsg{err: sshclient.RemoveKnownHostEntry(entry)}
	}
}

func (m modelState) planSSHHostKeysImportCmd(path string) tea.Cmd {
	return func() tea.Msg {
		plan, err := sshclient.PlanKnownHostsImport(path)
		return sshHostKeysImportPlannedMsg{plan: plan, err: err}
	}
}

func (m modelState) applySSHHostKeysImportCmd(plan sshclient.KnownHostsImportPlan) tea.Cmd {
	return func() tea.Msg {
		added, err := sshclient.ApplyKnownHostsImport(plan)
		return sshHostKeysImportAppliedMsg{added: added, total: plan.TotalRecords, err: err}
	}
}

func (m modelState) exportSSHHostKeysCmd(path string) tea.Cmd {
	return func() tea.Msg {
		records, err := sshclient.ExportKnownHostsFile(path)
		return sshHostKeysExportedMsg{path: path, records: records, err: err}
	}
}

func (m *modelState) handleSSHHostKeysImportPlanned(msg sshHostKeysImportPlannedMsg) {
	state := m.sshHostKeyManager
	if state == nil {
		return
	}
	state.busy = false
	state.operationText = ""
	if msg.err != nil {
		state.errorText = "导入预览失败：" + msg.err.Error()
		return
	}
	state.importPlan = &msg.plan
	state.confirmingImport = true
	state.errorText = ""
}

func (m *modelState) handleSSHHostKeysImportApplied(msg sshHostKeysImportAppliedMsg) tea.Cmd {
	state := m.sshHostKeyManager
	if state == nil {
		return nil
	}
	state.busy = false
	state.confirmingImport = false
	state.importPlan = nil
	state.operationText = ""
	if msg.err != nil {
		state.errorText = "导入失败：" + msg.err.Error()
		return nil
	}
	state.loading = true
	state.errorText = ""
	m.statusText = fmt.Sprintf("SSH 主机指纹导入完成：新增 %d 条，共 %d 条", msg.added, msg.total)
	m.statusErr = false
	return m.loadSSHHostKeysCmd()
}

func (m *modelState) handleSSHHostKeysExported(msg sshHostKeysExportedMsg) {
	state := m.sshHostKeyManager
	if state == nil {
		return
	}
	state.busy = false
	state.operationText = ""
	if msg.err != nil {
		state.errorText = "导出失败：" + msg.err.Error()
		return
	}
	state.errorText = ""
	m.statusText = fmt.Sprintf("已导出 %d 条 SSH 主机指纹到 %s", msg.records, msg.path)
	m.statusErr = false
}

func (m modelState) updateSSHHostKeyManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sshHostKeyManager
	if state == nil || state.busy || state.loading {
		return m, nil
	}
	if state.pathAction != sshHostKeyPathNone {
		return m.updateSSHHostKeyPathKey(msg)
	}
	if state.confirmingImport {
		switch msg.String() {
		case "enter", "y":
			if state.importPlan == nil {
				state.confirmingImport = false
				state.errorText = "导入预览已失效，请重新选择文件。"
				return m, nil
			}
			plan := *state.importPlan
			state.busy = true
			state.operationText = "正在导入 SSH 主机指纹…"
			state.errorText = ""
			return m, m.applySSHHostKeysImportCmd(plan)

		case "esc", "escape", "n", "q", "ctrl+c":
			state.confirmingImport = false
			state.importPlan = nil
		}
		return m, nil
	}
	if state.confirmingDelete {
		switch msg.String() {
		case "enter", "y":
			entry := selectedSSHHostKey(state)
			if entry == nil {
				state.confirmingDelete = false
				return m, nil
			}
			state.busy = true
			state.errorText = ""
			return m, m.removeSSHHostKeyCmd(*entry)
		case "esc", "escape", "n", "q", "ctrl+c":
			state.confirmingDelete = false
			return m, nil
		default:
			return m, nil
		}
	}
	switch msg.String() {
	case "esc", "escape", "q", "ctrl+c":
		m.sshHostKeyManager = nil
	case "up", "k":
		state.index = clamp(state.index-1, 0, max(0, len(state.entries)-1))
		state.errorText = ""
	case "down", "j":
		state.index = clamp(state.index+1, 0, max(0, len(state.entries)-1))
		state.errorText = ""
	case "r":
		state.loading = true
		state.errorText = ""
		return m, m.loadSSHHostKeysCmd()
	case "i":
		state.pathAction = sshHostKeyPathImport
		state.pathInput = ""
		state.errorText = ""
	case "e":
		state.pathAction = sshHostKeyPathExport
		state.pathInput = ""
		state.errorText = ""
	case "d", "delete":
		if selectedSSHHostKey(state) == nil {
			state.errorText = "没有可移除的主机信任记录。"
			return m, nil
		}
		state.confirmingDelete = true
		state.errorText = ""
	}
	return m, nil
}

func (m *modelState) updateSSHHostKeyPathKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sshHostKeyManager
	key := msg.Key()
	switch {
	case key.Code == tea.KeyEscape:
		state.pathAction = sshHostKeyPathNone
		state.pathInput = ""
		state.errorText = ""
	case key.Code == tea.KeyEnter:
		path, err := expandSSHHostKeyPath(state.pathInput)
		if err != nil {
			state.errorText = err.Error()
			return m, nil
		}
		if path == "" {
			state.errorText = "请输入文件路径。"
			return m, nil
		}
		action := state.pathAction
		state.pathAction = sshHostKeyPathNone
		state.pathInput = ""
		state.errorText = ""
		state.busy = true
		if action == sshHostKeyPathImport {
			state.operationText = "正在检查导入文件…"
			return m, m.planSSHHostKeysImportCmd(path)
		}
		state.operationText = "正在导出 SSH 主机指纹…"
		return m, m.exportSSHHostKeysCmd(path)
	case key.Code == tea.KeyBackspace || key.Code == tea.KeyDelete || msg.String() == "ctrl+h":
		input := []rune(state.pathInput)
		if len(input) > 0 {
			state.pathInput = string(input[:len(input)-1])
		}
		state.errorText = ""
	case key.Code == 'u' && key.Mod&tea.ModCtrl != 0:
		state.pathInput = ""
		state.errorText = ""
	default:
		if key.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			return m, nil
		}
		var builder strings.Builder
		for _, character := range key.Text {
			if !unicode.IsControl(character) {
				builder.WriteRune(character)
			}
		}
		text := builder.String()
		if text != "" && len([]rune(state.pathInput))+len([]rune(text)) <= 4096 {
			state.pathInput += text
			state.errorText = ""
		}
	}
	return m, nil
}

func expandSSHHostKeyPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析用户主目录失败：%w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Clean(path), nil
}

func selectedSSHHostKey(state *sshHostKeyManagerState) *sshclient.KnownHostEntry {
	if state == nil || state.index < 0 || state.index >= len(state.entries) {
		return nil
	}
	return &state.entries[state.index]
}

func (m modelState) sshHostKeyManagerView() string {
	state := m.sshHostKeyManager
	if state == nil {
		return ""
	}
	if state.pathAction != sshHostKeyPathNone {
		title := "导入 SSH 主机指纹"
		description := "输入 OpenSSH known_hosts 文件路径。导入前会校验并展示记录数量。"
		if state.pathAction == sshHostKeyPathExport {
			title = "导出 SSH 主机指纹"
			description = "输入新文件路径；已存在的文件不会被覆盖。"
		}
		lines := []string{
			mutedStyle.Render(description),
			"路径：" + state.pathInput + "▌",
			mutedStyle.Render("支持 ~ 展开；Enter 继续，Ctrl+U 清空，Esc 取消。"),
		}
		if state.errorText != "" {
			lines = append(lines, "", warningStyle.Render("! "+state.errorText))
		}
		return m.modalView(title, lines, 100)
	}
	if state.confirmingImport {
		if state.importPlan == nil {
			return m.modalView("导入 SSH 主机指纹", []string{"导入预览已失效。", "", mutedStyle.Render("Esc 返回")}, 84)
		}
		plan := state.importPlan
		lines := []string{
			warningStyle.Render("只导入通过格式校验的 OpenSSH known_hosts 记录；请确认文件来自可信来源。"),
			"文件：" + compactSSHHostKeyPath(plan.SourcePath()),
			fmt.Sprintf("有效记录：%d 条", plan.TotalRecords),
			fmt.Sprintf("新增记录：%d 条", plan.NewRecords),
			fmt.Sprintf("已存在/重复：%d 条", plan.TotalRecords-plan.NewRecords),
			"",
			mutedStyle.Render("按 Enter / y 确认导入；按 Esc 取消。预览后文件若有变化，导入会被拒绝。"),
		}
		return m.modalView("确认导入 SSH 主机指纹", lines, 112)
	}
	if state.confirmingDelete {
		entry := selectedSSHHostKey(state)
		if entry == nil {
			return m.modalView("移除 SSH 主机指纹", []string{"没有选择记录。", "", mutedStyle.Render("Esc 返回")}, 84)
		}
		lines := []string{
			warningStyle.Render("移除后，下次连接会再次询问该主机指纹。"),
			"",
			"主机：" + strings.Join(entry.Hosts, ", "),
			"类型：" + entry.KeyType,
			"指纹：" + entry.Fingerprint,
		}
		if entry.Marker != "" {
			lines = append(lines, "标记："+entry.Marker)
		}
		lines = append(lines, "", mutedStyle.Render("按 Enter / y 移除此行；按 Esc 取消。"))
		return m.modalView("移除 SSH 主机指纹", lines, 100)
	}
	lines := []string{
		mutedStyle.Render("管理本应用 known_hosts 中已信任的服务端公钥；移除后该主机下次连接会重新询问指纹。"),
		"",
	}
	if state.busy {
		operationText := state.operationText
		if operationText == "" {
			operationText = "正在处理 SSH 主机指纹…"
		}
		lines = append(lines, accentStyle.Render("◌ "+operationText))
	} else if state.loading {
		lines = append(lines, accentStyle.Render("◌ 正在读取 SSH 主机指纹…"))
	} else if len(state.entries) == 0 {
		lines = append(lines, mutedStyle.Render("目前没有已信任的 SSH 主机指纹。"))
	} else {
		visible := min(len(state.entries), 8)
		start := clamp(state.index-visible+1, 0, max(0, len(state.entries)-visible))
		for index := start; index < start+visible; index++ {
			entry := state.entries[index]
			marker, style := "  ", mutedStyle
			if index == state.index {
				marker, style = "› ", selectedStyle
			}
			label := strings.Join(entry.Hosts, ", ")
			if entry.Marker != "" {
				label = entry.Marker + " " + label
			}
			lines = append(lines, style.Render(marker+label), mutedStyle.Render("    "+entry.KeyType+" · "+entry.Fingerprint))
		}
		if entry := selectedSSHHostKey(state); entry != nil {
			lines = append(lines, "", sectionStyle.Render("当前记录"),
				"行号："+fmt.Sprint(entry.LineNumber),
				"主机："+strings.Join(entry.Hosts, ", "),
				"算法："+entry.KeyType,
				"指纹："+entry.Fingerprint)
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render("! "+state.errorText))
	}
	lines = append(lines, "", helpKeyStyle.Render("i ")+"导入  "+helpKeyStyle.Render("e ")+"导出  "+helpKeyStyle.Render("d ")+"移除  "+helpKeyStyle.Render("r ")+"刷新  "+helpKeyStyle.Render("Esc ")+"返回")
	return m.modalView("SSH 主机指纹管理", lines, 112)
}

func compactSSHHostKeyPath(path string) string {
	const maxRunes = 80
	runes := []rune(path)
	if len(runes) <= maxRunes {
		return path
	}
	return "…" + string(runes[len(runes)-maxRunes+1:])
}
