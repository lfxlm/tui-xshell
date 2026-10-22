package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/model"
)

type authProfileManagerState struct {
	index                  int
	loading                bool
	busy                   bool
	confirmingDelete       bool
	secretPrompt           bool
	confirmingSecretDelete bool
	secret                 string
	form                   *authProfileFormState
	errorText              string
}

type authProfileFormState struct {
	profile model.AuthProfile
	field   int
}

type authProfilesLoadedMsg struct {
	profiles []model.AuthProfile
	err      error
}

type authProfileSavedMsg struct {
	profile model.AuthProfile
	err     error
}

type authProfileDeletedMsg struct {
	id  string
	err error
}

type authProfileCredentialSavedMsg struct {
	profile    model.AuthProfile
	err        error
	cleanupErr error
}

type authProfileCredentialDeletedMsg struct {
	profile          model.AuthProfile
	referenceCleared bool
	err              error
}

func (m *modelState) openAuthProfileManager() tea.Cmd {
	if _, ok := m.catalog.(authProfileCatalog); !ok {
		m.statusText, m.statusErr = "本地存储不支持认证配置档", true
		return nil
	}
	m.authProfileManager = &authProfileManagerState{loading: true}
	return m.loadAuthProfilesCmd()
}

func (m modelState) loadAuthProfilesCmd() tea.Cmd {
	catalog, ok := m.catalog.(authProfileCatalog)
	if !ok {
		return func() tea.Msg { return authProfilesLoadedMsg{err: fmt.Errorf("本地存储不支持认证配置档")} }
	}
	return func() tea.Msg {
		profiles, err := catalog.ListAuthProfiles(context.Background())
		return authProfilesLoadedMsg{profiles: profiles, err: err}
	}
}

func (m *modelState) handleAuthProfilesLoaded(msg authProfilesLoadedMsg) {
	state := m.authProfileManager
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取认证配置档失败：" + msg.err.Error()
		return
	}
	selectedID := ""
	if state.index >= 0 && state.index < len(m.authProfiles) {
		selectedID = m.authProfiles[state.index].Id
	}
	m.authProfiles = msg.profiles
	state.index = 0
	for index, profile := range msg.profiles {
		if profile.Id == selectedID {
			state.index = index
			break
		}
	}
	state.index = clamp(state.index, 0, max(0, len(msg.profiles)-1))
	state.errorText = ""
}

func (m *modelState) updateAuthProfileManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.authProfileManager
	if state == nil {
		return m, nil
	}
	if state.busy || state.loading {
		return m, nil
	}
	if state.secretPrompt {
		return m.updateAuthProfileSecretKey(msg)
	}
	if state.form != nil {
		return m.updateAuthProfileFormKey(msg)
	}
	if state.confirmingDelete {
		switch msg.String() {
		case "enter", "y":
			profile := m.selectedAuthProfile()
			if profile == nil {
				state.confirmingDelete = false
				return m, nil
			}
			state.busy = true
			return m, m.deleteAuthProfileCmd(profile.Id)
		case "escape", "esc", "n", "q", "ctrl+c":
			state.confirmingDelete = false
		}
		return m, nil
	}
	switch msg.String() {
	case "escape", "esc", "q", "ctrl+c":
		m.authProfileManager = nil
	case "up", "k":
		state.index = clamp(state.index-1, 0, max(0, len(m.authProfiles)-1))
		state.errorText = ""
	case "down", "j":
		state.index = clamp(state.index+1, 0, max(0, len(m.authProfiles)-1))
		state.errorText = ""
	case "r":
		state.loading = true
		return m, m.loadAuthProfilesCmd()
	case "n":
		state.form = &authProfileFormState{}
	case "e":
		if profile := m.selectedAuthProfile(); profile != nil {
			state.form = &authProfileFormState{profile: *profile}
		}
	case "d", "delete":
		if m.selectedAuthProfile() != nil {
			state.confirmingDelete = true
		}
	case "c":
		if profile := m.selectedAuthProfile(); profile != nil {
			state.secretPrompt = true
			state.secret = ""
		}
	}
	return m, nil
}

func (m *modelState) updateAuthProfileFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.authProfileManager
	form := state.form
	switch msg.String() {
	case "escape", "esc", "ctrl+c":
		state.form = nil
		return m, nil
	case "tab", "down", "enter":
		if form.field < 2 {
			form.field++
			return m, nil
		}
		return m, m.saveAuthProfileCmd(form.profile)
	case "shift+tab", "up":
		form.field = clamp(form.field-1, 0, 2)
		return m, nil
	case "backspace", "ctrl+h":
		value := []rune(authProfileFormValue(form))
		if len(value) > 0 {
			setAuthProfileFormValue(form, string(value[:len(value)-1]))
		}
		return m, nil
	}
	text := msg.Key().Text
	if text != "" && len([]rune(authProfileFormValue(form)))+len([]rune(text)) <= 256 {
		setAuthProfileFormValue(form, authProfileFormValue(form)+text)
	}
	return m, nil
}

func authProfileFormValue(form *authProfileFormState) string {
	switch form.field {
	case 0:
		return form.profile.Name
	case 1:
		return form.profile.User
	default:
		return form.profile.IdentityFile
	}
}

func setAuthProfileFormValue(form *authProfileFormState, value string) {
	switch form.field {
	case 0:
		form.profile.Name = value
	case 1:
		form.profile.User = value
	default:
		form.profile.IdentityFile = value
	}
}

func (m *modelState) saveAuthProfileCmd(profile model.AuthProfile) tea.Cmd {
	catalog, ok := m.catalog.(authProfileCatalog)
	if !ok {
		m.authProfileManager.errorText = "本地存储不支持认证配置档"
		return nil
	}
	state := m.authProfileManager
	state.busy = true
	return func() tea.Msg {
		if profile.Id == "" {
			saved, err := catalog.CreateAuthProfile(context.Background(), profile)
			return authProfileSavedMsg{profile: saved, err: err}
		}
		err := catalog.UpdateAuthProfile(context.Background(), profile)
		return authProfileSavedMsg{profile: profile, err: err}
	}
}

func (m modelState) deleteAuthProfileCmd(id string) tea.Cmd {
	catalog, ok := m.catalog.(authProfileCatalog)
	if !ok {
		return func() tea.Msg {
			return authProfileDeletedMsg{id: id, err: fmt.Errorf("本地存储不支持认证配置档")}
		}
	}
	return func() tea.Msg {
		return authProfileDeletedMsg{id: id, err: catalog.DeleteAuthProfile(context.Background(), id)}
	}
}

func (m *modelState) updateAuthProfileSecretKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.authProfileManager
	profile := m.selectedAuthProfile()
	if profile == nil {
		state.secretPrompt = false
		return m, nil
	}
	if state.confirmingSecretDelete {
		switch msg.String() {
		case "enter", "y":
			state.busy = true
			updated := *profile
			return m, m.deleteAuthProfileCredentialCmd(updated)
		case "escape", "esc", "n", "q", "ctrl+c":
			state.confirmingSecretDelete = false
		}
		return m, nil
	}
	switch msg.String() {
	case "escape", "esc", "ctrl+c":
		state.secretPrompt = false
		state.secret = ""
	case "enter":
		if state.secret == "" {
			state.errorText = "请输入要保存在系统钥匙串中的凭据。"
			return m, nil
		}
		state.busy = true
		updated := *profile
		secret := strings.Clone(state.secret)
		state.secret = ""
		return m, m.saveAuthProfileCredentialCmd(updated, secret)
	case "ctrl+d":
		if profile.CredentialRef != "" {
			state.confirmingSecretDelete = true
		}
	case "backspace", "ctrl+h":
		secret := []rune(state.secret)
		if len(secret) > 0 {
			state.secret = string(secret[:len(secret)-1])
		}
	default:
		if text := msg.Key().Text; text != "" && len([]rune(state.secret))+len([]rune(text)) <= 512 {
			state.secret += text
		}
	}
	return m, nil
}

func (m *modelState) saveAuthProfileCredentialCmd(profile model.AuthProfile, secret string) tea.Cmd {
	catalog, ok := m.catalog.(authProfileCatalog)
	if !ok || m.credentials == nil {
		m.authProfileManager.busy = false
		m.authProfileManager.errorText = "本地存储或系统钥匙串不可用"
		return nil
	}
	oldReference := profile.CredentialRef
	vault := m.credentials
	return func() tea.Msg {
		reference, err := credentialstore.NewReference()
		if err != nil {
			return authProfileCredentialSavedMsg{profile: profile, err: err}
		}
		if err := vault.Set(reference, secret); err != nil {
			return authProfileCredentialSavedMsg{profile: profile, err: err}
		}
		profile.CredentialRef = reference
		if err := catalog.UpdateAuthProfile(context.Background(), profile); err != nil {
			return authProfileCredentialSavedMsg{profile: profile, err: err, cleanupErr: vault.Delete(reference)}
		}
		var cleanupErr error
		if oldReference != "" && oldReference != reference {
			cleanupErr = vault.Delete(oldReference)
		}
		return authProfileCredentialSavedMsg{profile: profile, cleanupErr: cleanupErr}
	}
}

func (m *modelState) deleteAuthProfileCredentialCmd(profile model.AuthProfile) tea.Cmd {
	catalog, ok := m.catalog.(authProfileCatalog)
	if !ok || m.credentials == nil {
		m.authProfileManager.busy = false
		m.authProfileManager.errorText = "本地存储或系统钥匙串不可用"
		return nil
	}
	oldReference := profile.CredentialRef
	profile.CredentialRef = ""
	vault := m.credentials
	return func() tea.Msg {
		if err := catalog.UpdateAuthProfile(context.Background(), profile); err != nil {
			return authProfileCredentialDeletedMsg{profile: profile, err: err}
		}
		if err := vault.Delete(oldReference); err != nil {
			return authProfileCredentialDeletedMsg{profile: profile, referenceCleared: true, err: fmt.Errorf("配置引用已清除，但钥匙串条目删除失败：%w", err)}
		}
		return authProfileCredentialDeletedMsg{profile: profile, referenceCleared: true}
	}
}

func (m *modelState) handleAuthProfileSaved(msg authProfileSavedMsg) {
	state := m.authProfileManager
	if state == nil {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.errorText = "保存认证配置档失败：" + msg.err.Error()
		return
	}
	if index := authProfileIndex(m.authProfiles, msg.profile.Id); index >= 0 {
		m.authProfiles[index] = msg.profile
		state.index = index
	} else {
		m.authProfiles = append(m.authProfiles, msg.profile)
		state.index = len(m.authProfiles) - 1
	}
	state.form = nil
	state.errorText = ""
}

func (m *modelState) handleAuthProfileDeleted(msg authProfileDeletedMsg) {
	state := m.authProfileManager
	if state == nil {
		return
	}
	state.busy = false
	state.confirmingDelete = false
	if msg.err != nil {
		state.errorText = "删除失败：" + msg.err.Error()
		return
	}
	if index := authProfileIndex(m.authProfiles, msg.id); index >= 0 {
		m.authProfiles = append(m.authProfiles[:index], m.authProfiles[index+1:]...)
	}
	state.index = clamp(state.index, 0, max(0, len(m.authProfiles)-1))
	state.errorText = ""
}

func (m *modelState) handleAuthProfileCredentialSaved(msg authProfileCredentialSavedMsg) {
	state := m.authProfileManager
	if state == nil {
		return
	}
	state.busy = false
	if msg.err != nil {
		state.errorText = "保存系统钥匙串凭据失败：" + msg.err.Error()
		if msg.cleanupErr != nil {
			state.errorText += "；清理新条目失败：" + msg.cleanupErr.Error()
		}
		return
	}
	if index := authProfileIndex(m.authProfiles, msg.profile.Id); index >= 0 {
		m.authProfiles[index] = msg.profile
	}
	state.secretPrompt = false
	state.secret = ""
	state.errorText = ""
	if msg.cleanupErr != nil {
		state.errorText = "凭据已更新；旧钥匙串条目清理失败：" + msg.cleanupErr.Error()
	}
}

func (m *modelState) handleAuthProfileCredentialDeleted(msg authProfileCredentialDeletedMsg) {
	state := m.authProfileManager
	if state == nil {
		return
	}
	state.busy = false
	state.confirmingSecretDelete = false
	if index := authProfileIndex(m.authProfiles, msg.profile.Id); index >= 0 && msg.referenceCleared {
		m.authProfiles[index] = msg.profile
	}
	if msg.err != nil {
		state.errorText = "删除系统钥匙串凭据失败：" + msg.err.Error()
		return
	}
	state.secretPrompt = false
	state.secret = ""
	state.errorText = ""
}

func authProfileIndex(profiles []model.AuthProfile, id string) int {
	for index := range profiles {
		if profiles[index].Id == id {
			return index
		}
	}
	return -1
}

func (m modelState) selectedAuthProfile() *model.AuthProfile {
	state := m.authProfileManager
	if state == nil || state.index < 0 || state.index >= len(m.authProfiles) {
		return nil
	}
	return &m.authProfiles[state.index]
}

func (m modelState) authProfileManagerView() string {
	state := m.authProfileManager
	if state.secretPrompt {
		profile := m.selectedAuthProfile()
		name := "认证配置档"
		if profile != nil {
			name = profile.Name
		}
		if state.confirmingSecretDelete {
			return m.modalView("删除认证凭据", []string{"将从系统钥匙串删除此配置档的凭据。", "", mutedStyle.Render("按 Enter / y 确认；按 Esc 取消。")}, 84)
		}
		lines := []string{"配置档：" + name, "凭据：" + strings.Repeat("•", len([]rune(state.secret))), "",
			mutedStyle.Render("Enter 保存到系统钥匙串；Ctrl+D 删除已保存凭据；Esc 返回。")}
		if state.errorText != "" {
			lines = append(lines, "", warningStyle.Render(state.errorText))
		}
		return m.modalView("认证配置凭据", lines, 90)
	}
	if state.form != nil {
		form := state.form
		labels := []string{"名称", "用户名（可留空，沿用主机设置）", "私钥文件（可留空，沿用主机设置）"}
		values := []string{form.profile.Name, form.profile.User, form.profile.IdentityFile}
		lines := []string{mutedStyle.Render("配置档非空的字段会覆盖主机对应设置；空字段继续使用主机设置。"), ""}
		for index, label := range labels {
			style := mutedStyle
			marker := "  "
			if form.field == index {
				style, marker = accentStyle, "› "
			}
			lines = append(lines, style.Render(marker+label), "  "+values[index])
		}
		lines = append(lines, "", mutedStyle.Render("Tab / Enter 移动；最后一项 Enter 保存；Esc 取消。"))
		if state.errorText != "" {
			lines = append(lines, "", warningStyle.Render(state.errorText))
		}
		return m.modalView("认证配置档", lines, 100)
	}
	if state.confirmingDelete {
		profile := m.selectedAuthProfile()
		name := "所选配置档"
		if profile != nil {
			name = profile.Name
		}
		return m.modalView("删除认证配置档", []string{"确定删除「" + name + "」吗？", mutedStyle.Render("仍被主机使用的配置档无法删除。"), "", mutedStyle.Render("按 Enter / y 确认；Esc 取消。")}, 90)
	}
	lines := []string{mutedStyle.Render("复用用户名、钥匙串凭据和私钥路径；主机可单独覆盖空字段。"), ""}
	if state.loading {
		lines = append(lines, accentStyle.Render("◌ 正在读取认证配置档…"))
	} else if len(m.authProfiles) == 0 {
		lines = append(lines, mutedStyle.Render("暂无配置档，按 n 新建。"))
	} else {
		for index, profile := range m.authProfiles {
			marker, style := "  ", mutedStyle
			if index == state.index {
				marker, style = "› ", selectedStyle
			}
			detail := profile.User
			if detail == "" {
				detail = "沿用主机用户名"
			}
			if profile.CredentialRef != "" {
				detail += " · 已保存凭据"
			}
			if profile.IdentityFile != "" {
				detail += " · 私钥 " + profile.IdentityFile
			}
			lines = append(lines, style.Render(marker+profile.Name+"  "+mutedStyle.Render(detail)))
		}
	}
	lines = append(lines, "", mutedStyle.Render("n 新建 · e 编辑 · c 凭据 · d 删除 · r 刷新 · Esc 返回"))
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render(state.errorText))
	}
	return m.modalView("SSH 认证配置档", lines, 112)
}
