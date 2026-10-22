package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/model"
)

type credentialVault interface {
	Set(reference, secret string) error
	Get(reference string) (string, error)
	Delete(reference string) error
}

type credentialPromptState struct {
	host      model.Host
	value     string
	errorText string
	saving    bool
}

type credentialSavedMsg struct {
	host       model.Host
	err        error
	cleanupErr error
}

type credentialDeletedMsg struct {
	host model.Host
	err  error
}

type sshCredentialsLoadedMsg struct {
	host      model.Host
	target    connectionTarget
	passwords map[string]string
	warning   string
}

type ftpCredentialLoadedMsg struct {
	host     model.Host
	target   connectionTarget
	password string
	err      error
}

func hostHasSavedCredential(host model.Host, hosts []model.Host) bool {
	for _, authHost := range sshCredentialHosts(host, hosts) {
		if authHost.CredentialRef != "" {
			return true
		}
	}
	return false
}

func (m modelState) hostWithAuthProfile(host model.Host) model.Host {
	if profile := m.authProfileByID(m.effectiveAuthProfileID(host)); profile != nil {
		return host.WithAuthProfile(*profile)
	}
	return host
}

func (m modelState) effectiveAuthProfileID(host model.Host) string {
	if host.AuthProfileID != "" {
		return host.AuthProfileID
	}
	return m.inheritedGroupAuthProfileID(host.GroupId)
}

func (m modelState) inheritedGroupAuthProfileID(groupID string) string {
	byID := make(map[string]model.Group, len(m.groups))
	for _, group := range m.groups {
		byID[group.Id] = group
	}
	seen := make(map[string]struct{})
	for groupID != "" {
		if _, ok := seen[groupID]; ok {
			return ""
		}
		seen[groupID] = struct{}{}
		group, ok := byID[groupID]
		if !ok {
			return ""
		}
		if group.AuthProfileID != "" {
			return group.AuthProfileID
		}
		groupID = group.ParentId
	}
	return ""
}

func (m modelState) hostWithGroupStartDirs(host model.Host) model.Host {
	localStartDir, remoteStartDir := m.inheritedGroupStartDirs(host.GroupId)
	if host.LocalStartDir == "" {
		host.LocalStartDir = localStartDir
	}
	if host.RemoteStartDir == "" {
		host.RemoteStartDir = remoteStartDir
	}
	return host
}

// inheritedGroupStartDirs resolves each path independently so a child can
// override just the local or remote default while inheriting the other one.
func (m modelState) inheritedGroupStartDirs(groupID string) (string, string) {
	byID := make(map[string]model.Group, len(m.groups))
	for _, group := range m.groups {
		byID[group.Id] = group
	}
	seen := make(map[string]struct{})
	localStartDir, remoteStartDir := "", ""
	for groupID != "" {
		if _, ok := seen[groupID]; ok {
			break
		}
		seen[groupID] = struct{}{}
		group, ok := byID[groupID]
		if !ok {
			break
		}
		if localStartDir == "" && group.LocalStartDir != "" {
			localStartDir = group.LocalStartDir
		}
		if remoteStartDir == "" && group.RemoteStartDir != "" {
			remoteStartDir = group.RemoteStartDir
		}
		if localStartDir != "" && remoteStartDir != "" {
			break
		}
		groupID = group.ParentId
	}
	return localStartDir, remoteStartDir
}

func (m modelState) authProfileByID(id string) *model.AuthProfile {
	if id == "" {
		return nil
	}
	for index := range m.authProfiles {
		if m.authProfiles[index].Id == id {
			return &m.authProfiles[index]
		}
	}
	return nil
}

func (m modelState) hostsWithAuthProfiles() []model.Host {
	hosts := append([]model.Host(nil), m.hosts...)
	for index := range hosts {
		hosts[index] = m.hostWithAuthProfile(hosts[index])
	}
	return hosts
}

// sshCredentialHosts returns the authentication chain in connection order,
// from outermost jump host to the target. A visited set protects this lookup
// from malformed imported data even though normal catalog writes reject cycles.
func sshCredentialHosts(target model.Host, hosts []model.Host) []model.Host {
	byID := make(map[string]model.Host, len(hosts))
	for _, host := range hosts {
		byID[host.Id] = host
	}
	visited := make(map[string]bool)
	result := make([]model.Host, 0, len(target.ProxyJumpList)+1)
	var appendChain func(model.Host)
	appendChain = func(host model.Host) {
		if host.Id != "" && visited[host.Id] {
			return
		}
		if host.Id != "" {
			visited[host.Id] = true
		}
		for _, parentID := range host.ProxyJumpList {
			if parent, ok := byID[parentID]; ok {
				appendChain(parent)
			}
		}
		result = append(result, host)
	}
	appendChain(target)
	return result
}

func (m *modelState) openCredentialPrompt() {
	host := m.selectedHost()
	if m.activePane != hostPane || host == nil {
		m.statusText, m.statusErr = "请先在主机列表选择 SSH 或 FTP/FTPS 主机", true
		return
	}
	if host.Kind != model.HostKindSSH && host.Kind != model.HostKindFTP {
		m.statusText, m.statusErr = "系统凭据仅支持 SSH 和 FTP/FTPS 主机", true
		return
	}
	m.credentialPrompt = &credentialPromptState{host: *host}
}

func (m *modelState) updateCredentialPromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.credentialPrompt
	key := msg.String()
	switch key {
	case "esc", "escape", "ctrl+c":
		prompt.value = ""
		m.credentialPrompt = nil
		return m, nil
	case "enter":
		if prompt.saving {
			return m, nil
		}
		if prompt.value == "" {
			prompt.errorText = "请输入要保存在系统钥匙串中的凭据。"
			return m, nil
		}
		return m, m.saveHostCredential(prompt)
	case "ctrl+d":
		if prompt.saving || strings.TrimSpace(prompt.host.CredentialRef) == "" {
			return m, nil
		}
		prompt.value = ""
		return m, m.deleteHostCredential(prompt)
	case "backspace", "ctrl+h":
		value := []rune(prompt.value)
		if len(value) > 0 {
			prompt.value = string(value[:len(value)-1])
		}
		prompt.errorText = ""
	default:
		text := msg.Key().Text
		if text != "" && len([]rune(prompt.value))+len([]rune(text)) <= 512 {
			prompt.value += text
			prompt.errorText = ""
		}
	}
	return m, nil
}

func (m *modelState) saveHostCredential(prompt *credentialPromptState) tea.Cmd {
	if m.catalog == nil || m.credentials == nil {
		prompt.errorText = "主机目录或系统钥匙串不可用。"
		return nil
	}
	prompt.saving = true
	prompt.value = strings.Clone(prompt.value)
	host := prompt.host
	secret := prompt.value
	oldReference := host.CredentialRef
	catalog, vault := m.catalog, m.credentials
	return func() tea.Msg {
		reference, err := credentialstore.NewReference()
		if err != nil {
			return credentialSavedMsg{host: host, err: err}
		}
		if err := vault.Set(reference, secret); err != nil {
			return credentialSavedMsg{host: host, err: err}
		}
		host.CredentialRef = reference
		if err := catalog.UpdateHost(context.Background(), host); err != nil {
			cleanupErr := vault.Delete(reference)
			return credentialSavedMsg{host: host, err: err, cleanupErr: cleanupErr}
		}
		var cleanupErr error
		if oldReference != "" && oldReference != reference {
			cleanupErr = vault.Delete(oldReference)
		}
		return credentialSavedMsg{host: host, cleanupErr: cleanupErr}
	}
}

func (m *modelState) deleteHostCredential(prompt *credentialPromptState) tea.Cmd {
	if m.catalog == nil || m.credentials == nil {
		prompt.errorText = "主机目录或系统钥匙串不可用。"
		return nil
	}
	prompt.saving = true
	host := prompt.host
	oldReference := host.CredentialRef
	host.CredentialRef = ""
	catalog, vault := m.catalog, m.credentials
	return func() tea.Msg {
		if err := catalog.UpdateHost(context.Background(), host); err != nil {
			return credentialDeletedMsg{host: host, err: err}
		}
		if err := vault.Delete(oldReference); err != nil {
			return credentialDeletedMsg{host: host, err: fmt.Errorf("配置引用已清除，但钥匙串条目删除失败：%w", err)}
		}
		return credentialDeletedMsg{host: host}
	}
}

func (m *modelState) handleCredentialSaved(msg credentialSavedMsg) {
	m.credentialPrompt = nil
	if msg.err != nil {
		m.statusText = "保存凭据失败：" + msg.err.Error()
		if msg.cleanupErr != nil {
			m.statusText += "；清理新钥匙串条目也失败：" + msg.cleanupErr.Error()
		}
		m.statusErr = true
		return
	}
	m.replaceHost(msg.host)
	m.statusText, m.statusErr = "已将凭据保存到系统钥匙串", false
	if msg.cleanupErr != nil {
		m.statusText = "凭据已更新；旧钥匙串条目清理失败：" + msg.cleanupErr.Error()
		m.statusErr = true
	}
}

func (m *modelState) handleCredentialDeleted(msg credentialDeletedMsg) {
	m.credentialPrompt = nil
	if msg.err != nil {
		m.statusText, m.statusErr = "删除凭据失败："+msg.err.Error(), true
		if msg.host.Id != "" && msg.host.CredentialRef == "" {
			// SQLite has already cleared the reference. Reflect that state even if
			// removing an orphaned vault entry failed.
			m.replaceHost(msg.host)
		}
		return
	}
	m.replaceHost(msg.host)
	m.statusText, m.statusErr = "已删除系统钥匙串凭据", false
}

func (m *modelState) replaceHost(updated model.Host) {
	for index := range m.hosts {
		if m.hosts[index].Id == updated.Id {
			m.hosts[index] = updated
			return
		}
	}
}

func (m modelState) credentialPromptView() string {
	prompt := m.credentialPrompt
	if prompt == nil {
		return ""
	}
	mask := strings.Repeat("•", min(24, len([]rune(prompt.value))))
	credentialKind := "SSH 登录密码或私钥口令"
	if prompt.host.Kind == model.HostKindFTP {
		credentialKind = "FTP/FTPS 登录密码"
	}
	lines := []string{
		brandStyle.Render("◆  系统钥匙串凭据"),
		mutedStyle.Render("主机：" + prompt.host.Name),
		"凭据写入操作系统钥匙串；SQLite 仅保存随机引用。",
		"",
		sectionStyle.Render(credentialKind),
		selectedStyle.Width(64).Render(mask + "▏"),
	}
	if prompt.host.CredentialRef != "" {
		lines = append(lines, "", mutedStyle.Render("此主机已有已保存凭据；Ctrl+D 删除。"))
	}
	if prompt.errorText != "" {
		lines = append(lines, "", warningStyle.Render("! "+prompt.errorText))
	}
	if prompt.saving {
		lines = append(lines, "", accentStyle.Render("◌  正在更新系统钥匙串和 SQLite 引用…"))
	} else {
		lines = append(lines, "", mutedStyle.Render("Enter 保存/更新  ·  Ctrl+D 删除  ·  Esc 取消"))
	}
	return m.modalView("管理主机凭据", lines, 76)
}
