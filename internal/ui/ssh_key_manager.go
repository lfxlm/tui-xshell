package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sshkeys"
	"tui-ssh-workbench/internal/model"
)

type sshKeyManagerState struct {
	identities       []sshkeys.ManagedIdentity
	index            int
	loading          bool
	busy             bool
	confirmingDelete bool
	rotation         *sshKeyRotationState
	errorText        string
}

type sshKeyRotationState struct {
	sourcePath   string
	targetCursor int
	confirming   bool
	busy         bool
}

type sshKeysLoadedMsg struct {
	identities []sshkeys.ManagedIdentity
	err        error
}

type sshKeyDeletedMsg struct {
	privatePath string
	err         error
}

type sshIdentityReferencesRotatedMsg struct {
	sourcePath string
	targetPath string
	committed  bool
	hosts      []model.Host
	profiles   []model.AuthProfile
	refreshErr error
	err        error
}

func (m *modelState) openSSHKeyManager() tea.Cmd {
	m.sshKeyManager = &sshKeyManagerState{loading: true}
	return m.loadSSHKeysCmd()
}

func (m modelState) loadSSHKeysCmd() tea.Cmd {
	return func() tea.Msg {
		identities, err := sshkeys.ListManaged()
		return sshKeysLoadedMsg{identities: identities, err: err}
	}
}

func (m *modelState) handleSSHKeysLoaded(msg sshKeysLoadedMsg) {
	state := m.sshKeyManager
	if state == nil {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.errorText = "读取密钥目录失败：" + msg.err.Error()
		return
	}
	selectedPath := ""
	if state.index >= 0 && state.index < len(state.identities) {
		selectedPath = state.identities[state.index].PrivatePath
	}
	state.identities = msg.identities
	state.index = 0
	for index, identity := range state.identities {
		if identity.PrivatePath == selectedPath {
			state.index = index
			break
		}
	}
	state.index = clamp(state.index, 0, max(0, len(state.identities)-1))
	state.errorText = ""
}

func (m *modelState) handleSSHKeyDeleted(msg sshKeyDeletedMsg) tea.Cmd {
	state := m.sshKeyManager
	if state == nil {
		return nil
	}
	state.busy = false
	if msg.err != nil {
		state.confirmingDelete = false
		state.errorText = "删除失败：" + msg.err.Error()
		return nil
	}
	state.confirmingDelete = false
	state.loading = true
	state.errorText = ""
	m.statusText, m.statusErr = "SSH 密钥对已删除", false
	return m.loadSSHKeysCmd()
}

func (m modelState) deleteSSHKeyCmd(privatePath string) tea.Cmd {
	return func() tea.Msg {
		return sshKeyDeletedMsg{privatePath: privatePath, err: sshkeys.DeleteManaged(privatePath)}
	}
}

func (m modelState) rotateSSHIdentityReferencesCmd(sourcePath, targetPath string) tea.Cmd {
	catalog := m.catalog
	return func() tea.Msg {
		if catalog == nil {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: fmt.Errorf("本地会话目录不可用")}
		}
		rotationCatalog, ok := catalog.(identityReferenceRotationCatalog)
		if !ok {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: fmt.Errorf("本地存储不支持密钥引用迁移")}
		}
		if err := validateManagedRotationTarget(targetPath); err != nil {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: err}
		}
		ctx := context.Background()
		hosts, err := catalog.ListHosts(ctx)
		if err != nil {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: fmt.Errorf("读取主机引用失败：%w", err)}
		}
		var profiles []model.AuthProfile
		if profileCatalog, ok := catalog.(authProfileCatalog); ok {
			profiles, err = profileCatalog.ListAuthProfiles(ctx)
			if err != nil {
				return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: fmt.Errorf("读取认证配置档引用失败：%w", err)}
			}
		}
		references := managedIdentityReferenceUpdates(hosts, profiles, sourcePath)
		if len(references) == 0 {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: fmt.Errorf("原密钥已没有可迁移的主机或认证配置档引用")}
		}
		if err := rotationCatalog.ReplaceIdentityFileReferences(ctx, references, targetPath); err != nil {
			return sshIdentityReferencesRotatedMsg{sourcePath: sourcePath, targetPath: targetPath, err: err}
		}

		updatedHosts, hostErr := catalog.ListHosts(ctx)
		updatedProfiles := profiles
		var profileErr error
		if profileCatalog, ok := catalog.(authProfileCatalog); ok {
			updatedProfiles, profileErr = profileCatalog.ListAuthProfiles(ctx)
		}
		refreshErr := errors.Join(hostErr, profileErr)
		return sshIdentityReferencesRotatedMsg{
			sourcePath: sourcePath, targetPath: targetPath, committed: true,
			hosts: updatedHosts, profiles: updatedProfiles, refreshErr: refreshErr,
		}
	}
}

func validateManagedRotationTarget(targetPath string) error {
	target, err := sshkeys.ResolvePath(targetPath)
	if err != nil {
		return fmt.Errorf("解析新密钥路径失败：%w", err)
	}
	identities, err := sshkeys.ListManaged()
	if err != nil {
		return fmt.Errorf("重新检查新密钥失败：%w", err)
	}
	for _, identity := range identities {
		path, pathErr := sshkeys.ResolvePath(identity.PrivatePath)
		if pathErr == nil && filepath.Clean(path) == filepath.Clean(target) && !identity.Invalid && identity.HasPublic && identity.Fingerprint != "" {
			return nil
		}
	}
	return fmt.Errorf("新密钥已不存在或无法验证公钥，请重新选择密钥")
}

func (m *modelState) handleSSHIdentityReferencesRotated(msg sshIdentityReferencesRotatedMsg) tea.Cmd {
	state := m.sshKeyManager
	if state == nil {
		return nil
	}
	if state.rotation != nil {
		state.rotation.busy = false
		state.rotation.confirming = false
	}
	if !msg.committed {
		state.rotation = nil
		state.errorText = "迁移失败：" + msg.err.Error()
		return nil
	}
	state.rotation = nil
	state.errorText = ""
	m.statusText, m.statusErr = "主机和认证配置档已切换到新 SSH 密钥；旧密钥仍保留", false
	if msg.refreshErr != nil {
		state.errorText = "引用已迁移，但刷新列表失败：" + msg.refreshErr.Error()
		return m.loadCatalogCmd()
	}
	m.hosts = msg.hosts
	m.authProfiles = msg.profiles
	state.loading = true
	return m.loadSSHKeysCmd()
}

func (m modelState) updateSSHKeyManagerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sshKeyManager
	if state == nil {
		return m, nil
	}
	if state.rotation != nil {
		return m.updateSSHKeyRotationKey(msg)
	}
	if state.busy || state.loading {
		return m, nil
	}
	if state.confirmingDelete {
		switch msg.String() {
		case "enter", "y":
			identity := m.selectedManagedIdentity()
			if identity == nil {
				state.confirmingDelete = false
				return m, nil
			}
			if references := managedIdentityReferences(m.hosts, m.authProfiles, identity.PrivatePath); len(references) > 0 {
				state.confirmingDelete = false
				state.errorText = "无法删除：仍被主机或认证配置档引用（" + strings.Join(references, "、") + "）。请先解除这些私钥路径。"
				return m, nil
			}
			state.busy = true
			state.errorText = ""
			return m, m.deleteSSHKeyCmd(identity.PrivatePath)
		case "esc", "escape", "n", "q", "ctrl+c":
			state.confirmingDelete = false
			return m, nil
		default:
			return m, nil
		}
	}
	switch msg.String() {
	case "esc", "escape", "q", "ctrl+c":
		m.sshKeyManager = nil
	case "up", "k":
		state.index = clamp(state.index-1, 0, max(0, len(state.identities)-1))
		state.errorText = ""
	case "down", "j":
		state.index = clamp(state.index+1, 0, max(0, len(state.identities)-1))
		state.errorText = ""
	case "r":
		state.loading = true
		state.errorText = ""
		return m, m.loadSSHKeysCmd()
	case "n":
		path, err := sshkeys.NextManagedPath(sshkeys.Ed25519)
		if err != nil {
			state.errorText = "无法选择密钥路径：" + err.Error()
			return m, nil
		}
		m.sshKeyGeneration = &sshKeyGenerationState{path: path, algorithm: sshkeys.Ed25519}
	case "d", "delete":
		identity := m.selectedManagedIdentity()
		if identity == nil {
			state.errorText = "没有可删除的密钥。"
			return m, nil
		}
		if references := managedIdentityReferences(m.hosts, m.authProfiles, identity.PrivatePath); len(references) > 0 {
			state.errorText = "无法删除：仍被主机或认证配置档引用（" + strings.Join(references, "、") + "）。请先解除这些私钥路径。"
			return m, nil
		}
		state.confirmingDelete = true
		state.errorText = ""
	case "m":
		identity := m.selectedManagedIdentity()
		if identity == nil {
			state.errorText = "没有可迁移的密钥。"
			return m, nil
		}
		if refs := managedIdentityReferences(m.hosts, m.authProfiles, identity.PrivatePath); len(refs) == 0 {
			state.errorText = "当前密钥未被主机或认证配置档引用，无需迁移。"
			return m, nil
		}
		candidates := sshKeyRotationCandidates(state.identities, identity.PrivatePath)
		if len(candidates) == 0 {
			state.errorText = "没有可用的新密钥。请先按 n 创建密钥，并将其公钥部署到相关服务器。"
			return m, nil
		}
		state.rotation = &sshKeyRotationState{sourcePath: identity.PrivatePath}
		state.errorText = ""
	}
	return m, nil
}

func (m modelState) updateSSHKeyRotationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	manager := m.sshKeyManager
	rotation := manager.rotation
	if rotation == nil || rotation.busy {
		return m, nil
	}
	if rotation.confirming {
		switch msg.String() {
		case "enter", "y":
			target := m.selectedSSHKeyRotationTarget()
			if target == nil {
				rotation.confirming = false
				manager.errorText = "请选择有效的新密钥。"
				return m, nil
			}
			rotation.busy = true
			manager.errorText = ""
			return m, m.rotateSSHIdentityReferencesCmd(rotation.sourcePath, target.PrivatePath)
		case "esc", "escape", "n", "q", "ctrl+c":
			manager.rotation = nil
			return m, nil
		default:
			return m, nil
		}
	}
	candidates := sshKeyRotationCandidates(manager.identities, rotation.sourcePath)
	if len(candidates) == 0 {
		manager.rotation = nil
		manager.errorText = "没有可用的新密钥。"
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape", "q", "ctrl+c":
		manager.rotation = nil
	case "up", "k", "left":
		rotation.targetCursor = (rotation.targetCursor + len(candidates) - 1) % len(candidates)
	case "down", "j", "right":
		rotation.targetCursor = (rotation.targetCursor + 1) % len(candidates)
	case "enter":
		rotation.confirming = true
		manager.errorText = ""
	}
	return m, nil
}

func (m modelState) selectedSSHKeyRotationTarget() *sshkeys.ManagedIdentity {
	manager := m.sshKeyManager
	if manager == nil || manager.rotation == nil {
		return nil
	}
	candidates := sshKeyRotationCandidates(manager.identities, manager.rotation.sourcePath)
	if len(candidates) == 0 {
		return nil
	}
	manager.rotation.targetCursor = clamp(manager.rotation.targetCursor, 0, len(candidates)-1)
	return &manager.identities[candidates[manager.rotation.targetCursor]]
}

func sshKeyRotationCandidates(identities []sshkeys.ManagedIdentity, sourcePath string) []int {
	resolvedSource, err := sshkeys.ResolvePath(sourcePath)
	if err != nil {
		return nil
	}
	candidates := make([]int, 0, len(identities))
	for index, identity := range identities {
		resolved, err := sshkeys.ResolvePath(identity.PrivatePath)
		if err != nil || filepath.Clean(resolved) == filepath.Clean(resolvedSource) || identity.Invalid || !identity.HasPublic || identity.Fingerprint == "" {
			continue
		}
		candidates = append(candidates, index)
	}
	return candidates
}

func (m modelState) selectedManagedIdentity() *sshkeys.ManagedIdentity {
	state := m.sshKeyManager
	if state == nil || state.index < 0 || state.index >= len(state.identities) {
		return nil
	}
	return &state.identities[state.index]
}

func managedIdentityReferences(hosts []model.Host, profiles []model.AuthProfile, privatePath string) []string {
	references := make([]string, 0)
	for _, host := range hosts {
		if identityPathsMatch(host.IdentityFile, privatePath) {
			references = append(references, host.Name)
		}
	}
	for _, profile := range profiles {
		if identityPathsMatch(profile.IdentityFile, privatePath) {
			references = append(references, "认证配置档："+profile.Name)
		}
	}
	return references
}

func managedIdentityReferenceUpdates(hosts []model.Host, profiles []model.AuthProfile, privatePath string) []model.IdentityFileReference {
	references := make([]model.IdentityFileReference, 0)
	for _, host := range hosts {
		if identityPathsMatch(host.IdentityFile, privatePath) {
			references = append(references, model.IdentityFileReference{HostID: host.Id, CurrentPath: host.IdentityFile})
		}
	}
	for _, profile := range profiles {
		if identityPathsMatch(profile.IdentityFile, privatePath) {
			references = append(references, model.IdentityFileReference{AuthProfileID: profile.Id, CurrentPath: profile.IdentityFile})
		}
	}
	return references
}

func identityPathsMatch(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	resolvedLeft, err := sshkeys.ResolvePath(left)
	if err != nil {
		return false
	}
	resolvedRight, err := sshkeys.ResolvePath(right)
	return err == nil && filepath.Clean(resolvedLeft) == filepath.Clean(resolvedRight)
}

func (m modelState) sshKeyManagerView() string {
	state := m.sshKeyManager
	if state == nil {
		return ""
	}
	if state.confirmingDelete {
		identity := m.selectedManagedIdentity()
		if identity == nil {
			return m.modalView("删除 SSH 密钥", []string{"没有选择密钥。", "", mutedStyle.Render("Esc 返回")}, 80)
		}
		lines := []string{
			warningStyle.Render("此操作会删除本机私钥及其 .pub 公钥文件，无法撤销。"),
			"",
			"私钥：" + identity.PrivatePath,
			"指纹：" + managedIdentityFingerprint(*identity),
			"",
			mutedStyle.Render("按 Enter / y 删除；按 Esc 取消。"),
		}
		return m.modalView("删除 SSH 密钥", lines, 100)
	}
	if state.rotation != nil {
		return m.sshKeyRotationView()
	}
	lines := []string{
		mutedStyle.Render("仅管理 ~/.ssh/workbench_* 密钥。其它私钥可继续通过主机配置中的路径使用。"),
		mutedStyle.Render("删除只允许作用于未被主机或认证配置档引用的密钥；创建时不会覆盖已有文件。"),
		mutedStyle.Render("轮换步骤：按 n 创建新密钥、先部署其 .pub 公钥，再选中旧密钥按 m 迁移本机引用。"),
		"",
	}
	if state.loading {
		lines = append(lines, accentStyle.Render("◌ 正在读取本机密钥…"))
	} else if len(state.identities) == 0 {
		lines = append(lines, mutedStyle.Render("没有由本应用管理的密钥。按 n 创建 Ed25519 密钥对。"))
	} else {
		visible := min(len(state.identities), 8)
		start := clamp(state.index-visible+1, 0, max(0, len(state.identities)-visible))
		for index := start; index < start+visible; index++ {
			identity := state.identities[index]
			marker, style := "  ", mutedStyle
			if index == state.index {
				marker, style = "› ", selectedStyle
			}
			filename := filepath.Base(identity.PrivatePath)
			detail := identity.Algorithm
			if identity.Invalid {
				detail = "无法解析"
			} else if identity.Encrypted {
				detail += " · 已加密"
			} else {
				detail += " · 未加密"
			}
			if !identity.HasPublic {
				detail += " · 缺少 .pub"
			}
			lines = append(lines, style.Render(marker+filename+"  "+mutedStyle.Render(detail)))
		}
		if identity := m.selectedManagedIdentity(); identity != nil {
			lines = append(lines, "", sectionStyle.Render("当前密钥"), "路径："+identity.PrivatePath,
				"指纹："+managedIdentityFingerprint(*identity))
			if refs := managedIdentityReferences(m.hosts, m.authProfiles, identity.PrivatePath); len(refs) > 0 {
				lines = append(lines, warningStyle.Render("被引用："+strings.Join(refs, "、")))
			} else {
				lines = append(lines, mutedStyle.Render("当前未被主机或认证配置档引用，可安全删除。"))
			}
		}
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render("! "+state.errorText))
	}
	lines = append(lines, "", helpKeyStyle.Render("n ")+"新建  "+helpKeyStyle.Render("m ")+"迁移引用  "+helpKeyStyle.Render("d ")+"删除  "+helpKeyStyle.Render("r ")+"刷新  "+helpKeyStyle.Render("Esc ")+"返回")
	return m.modalView("SSH 密钥管理", lines, 108)
}

func (m modelState) sshKeyRotationView() string {
	state := m.sshKeyManager
	rotation := state.rotation
	source := rotation.sourcePath
	target := m.selectedSSHKeyRotationTarget()
	references := managedIdentityReferences(m.hosts, m.authProfiles, source)
	title := "迁移 SSH 密钥引用"
	lines := []string{
		"原密钥：" + source,
		"将更新以下主机和认证配置档的私钥路径：",
	}
	for _, reference := range references {
		lines = append(lines, "  · "+reference)
	}
	lines = append(lines, "")
	if rotation.confirming {
		if target == nil {
			lines = append(lines, warningStyle.Render("所选新密钥已不可用，请返回并重新选择。"))
			return m.modalView(title, lines, 112)
		}
		lines = append(lines,
			warningStyle.Render("请先将新公钥追加到上述每台主机对应用户的 ~/.ssh/authorized_keys。"),
			warningStyle.Render("确认后，新建连接会使用新密钥；正在运行的连接不受影响。旧密钥不会删除。"),
			"",
			"新密钥："+target.PrivatePath,
			"指纹："+managedIdentityFingerprint(*target),
			"",
			mutedStyle.Render("按 Enter / y 原子迁移全部引用；按 Esc 返回选择。"),
		)
	} else {
		lines = append(lines, "选择已创建且已部署公钥的新密钥：", "")
		candidates := sshKeyRotationCandidates(state.identities, rotation.sourcePath)
		for cursor, index := range candidates {
			identity := state.identities[index]
			marker, style := "  ", mutedStyle
			if cursor == rotation.targetCursor {
				marker, style = "› ", selectedStyle
			}
			lines = append(lines, style.Render(marker+filepath.Base(identity.PrivatePath)+"  "+mutedStyle.Render(identity.Algorithm+" · "+managedIdentityFingerprint(identity))))
		}
		lines = append(lines, "", mutedStyle.Render("公钥尚未部署前不要迁移，否则相应主机的新连接会认证失败。"), mutedStyle.Render("↑/↓ 选择  ·  Enter 确认目标  ·  Esc 取消"))
	}
	if rotation.busy {
		lines = append(lines, "", accentStyle.Render("◌ 正在事务式迁移密钥引用…"))
	}
	if state.errorText != "" {
		lines = append(lines, "", warningStyle.Render("! "+state.errorText))
	}
	return m.modalView(title, lines, 112)
}

func managedIdentityFingerprint(identity sshkeys.ManagedIdentity) string {
	if identity.Fingerprint != "" {
		return identity.Fingerprint
	}
	if identity.Encrypted {
		return "已加密，缺少可读取的公钥指纹"
	}
	if identity.Invalid {
		return "密钥文件无法解析"
	}
	return "未生成"
}
