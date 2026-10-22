package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sshkeys"
	"tui-ssh-workbench/internal/model"
)

type sshKeyGenerationState struct {
	field      int
	path       string
	algorithm  sshkeys.Algorithm
	passphrase string
	confirm    string
	saving     bool
	done       bool
	publicPath string
	errorText  string
}

type sshKeyGeneratedMsg struct {
	privatePath string
	publicPath  string
	err         error
}

func (m modelState) updateSSHKeyGenerationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sshKeyGeneration
	if state == nil || state.saving {
		return m, nil
	}
	if state.done {
		switch msg.String() {
		case "enter", "esc", "escape", "ctrl+c":
			m.sshKeyGeneration = nil
			if m.sshKeyManager != nil {
				m.sshKeyManager.loading = true
				return m, m.loadSSHKeysCmd()
			}
			return m, nil
		}
		return m, nil
	}

	switch msg.String() {
	case "esc", "escape", "ctrl+c":
		state.passphrase, state.confirm = "", ""
		m.sshKeyGeneration = nil
		return m, nil
	case "tab", "down":
		state.field = (state.field + 1) % 4
		return m, nil
	case "shift+tab", "up":
		state.field = (state.field + 3) % 4
		return m, nil
	case "left", "right":
		if state.field == 1 {
			if state.algorithm == sshkeys.Ed25519 {
				state.algorithm = sshkeys.RSA3072
			} else {
				state.algorithm = sshkeys.Ed25519
			}
		}
		return m, nil
	case "enter":
		if state.field < 3 {
			state.field++
			return m, nil
		}
		if strings.TrimSpace(state.path) == "" {
			state.errorText = "私钥保存路径不能为空。"
			state.field = 0
			return m, nil
		}
		if state.passphrase != state.confirm {
			state.errorText = "两次输入的密钥口令不一致。"
			state.field = 2
			return m, nil
		}
		state.saving = true
		state.errorText = ""
		path, algorithm, passphrase := state.path, state.algorithm, state.passphrase
		return m, func() tea.Msg {
			privatePath, publicPath, err := sshkeys.Generate(path, algorithm, passphrase)
			return sshKeyGeneratedMsg{privatePath: privatePath, publicPath: publicPath, err: err}
		}
	case "backspace", "ctrl+h":
		if state.field != 1 {
			value := []rune(state.text())
			if len(value) > 0 {
				state.setText(string(value[:len(value)-1]))
			}
			state.errorText = ""
		}
		return m, nil
	case "ctrl+u":
		if state.field != 1 {
			state.setText("")
			state.errorText = ""
		}
		return m, nil
	}
	if state.field == 1 {
		return m, nil
	}
	text := msg.Key().Text
	if text != "" && len([]rune(state.text()))+len([]rune(text)) <= 1024 {
		state.setText(state.text() + text)
		state.errorText = ""
	}
	return m, nil
}

func (s *sshKeyGenerationState) text() string {
	switch s.field {
	case 0:
		return s.path
	case 2:
		return s.passphrase
	case 3:
		return s.confirm
	default:
		return ""
	}
}

func (s *sshKeyGenerationState) setText(value string) {
	switch s.field {
	case 0:
		s.path = value
	case 2:
		s.passphrase = value
	case 3:
		s.confirm = value
	}
}

func (m *modelState) handleSSHKeyGenerated(msg sshKeyGeneratedMsg) {
	state := m.sshKeyGeneration
	if state == nil {
		return
	}
	state.saving = false
	state.passphrase, state.confirm = "", ""
	if msg.err != nil {
		state.errorText = "生成失败：" + msg.err.Error()
		return
	}
	if m.form == nil || m.form.kind != hostForm || m.form.hostKind != model.HostKindSSH {
		if m.sshKeyManager != nil {
			state.path, state.publicPath = msg.privatePath, msg.publicPath
			state.done = true
			return
		}
		state.errorText = "密钥已生成，但主机表单已关闭；请手动填写私钥路径。"
		state.path, state.publicPath = msg.privatePath, msg.publicPath
		state.done = true
		return
	}
	m.form.identityFile = msg.privatePath
	state.path, state.publicPath = msg.privatePath, msg.publicPath
	state.done = true
}

func (m modelState) sshKeyGenerationView() string {
	state := m.sshKeyGeneration
	if state == nil {
		return ""
	}
	lines := []string{
		"为当前主机创建本机 SSH 密钥对。已有同名文件会被保留，不会覆盖。",
		"",
	}
	if state.done {
		successText := "✓ 密钥对已生成，私钥路径已填入主机表单。"
		returnLabel := "主机表单"
		if m.sshKeyManager != nil {
			successText = "✓ 密钥对已生成，可在密钥列表中查看。"
			returnLabel = "密钥管理"
		}
		lines = append(lines,
			accentStyle.Render(successText),
			"私钥："+state.path,
			"公钥："+state.publicPath,
			"",
			mutedStyle.Render("连接前请将 .pub 公钥内容追加到服务器的 ~/.ssh/authorized_keys。"),
			mutedStyle.Render("加密私钥的口令不会保存；连接时需要再次输入或存入系统钥匙串。"),
			"",
			mutedStyle.Render("Enter / Esc 返回"+returnLabel),
		)
		return m.modalView("生成 SSH 密钥", lines, 100)
	}

	values := []string{state.path, string(state.algorithm), maskSecret(state.passphrase), maskSecret(state.confirm)}
	labels := []string{"私钥路径", "算法", "密钥口令（可留空）", "确认口令"}
	for index, label := range labels {
		value := values[index]
		if index == 1 {
			value += "  ←/→ 切换"
		} else if index == state.field && index != 1 {
			value += "▏"
		} else if value == "" {
			value = mutedStyle.Render("留空")
		}
		line := "  " + sectionStyle.Render(label) + "  " + value
		if state.field == index {
			line = selectedStyle.Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	if state.saving {
		lines = append(lines, accentStyle.Render("◌ 正在安全写入密钥文件…"), "")
	}
	if state.errorText != "" {
		lines = append(lines, warningStyle.Render("! "+state.errorText), "")
	}
	lines = append(lines,
		mutedStyle.Render("Ed25519 适合大多数新部署；RSA 使用 3072 位密钥。"),
		mutedStyle.Render("Tab / ↑↓ 切换字段  ·  ←/→ 选择算法  ·  Ctrl+U 清空当前文字"),
		mutedStyle.Render("Enter 下一项/生成  ·  Esc 取消"),
	)
	return m.modalView("生成 SSH 密钥", lines, 100)
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	return strings.Repeat("•", len([]rune(value)))
}
