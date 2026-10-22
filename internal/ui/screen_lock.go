package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/crypto/scrypt"
	"tui-ssh-workbench/internal/infra/credentialstore"
)

const (
	screenLockCredentialRef = "security:screen-lock:v1"
	screenLockMinRunes      = 8
	screenLockMaxRunes      = 256
	screenLockScryptN       = 1 << 15
	screenLockScryptR       = 8
	screenLockScryptP       = 1
	screenLockSaltBytes     = 16
	screenLockKeyBytes      = 32
)

type screenLockPhase uint8

const (
	screenLockLoading screenLockPhase = iota + 1
	screenLockLocked
	screenLockVerifyPassword
	screenLockNewPassword
	screenLockConfirmPassword
	screenLockBusy
)

type screenLockPurpose uint8

const (
	screenLockPurposeLock screenLockPurpose = iota + 1
	screenLockPurposeChange
	screenLockPurposeAutoLoad
)

type screenLockState struct {
	requestID   uint64
	phase       screenLockPhase
	purpose     screenLockPurpose
	input       string
	first       string
	storedHash  string
	errorText   string
	lockOnSave  bool
	busyContext screenLockPhase
}

type screenLockLoadedMsg struct {
	requestID uint64
	purpose   screenLockPurpose
	hash      string
	err       error
}

type screenLockVerifiedMsg struct {
	requestID uint64
	phase     screenLockPhase
	valid     bool
	err       error
}

type screenLockSavedMsg struct {
	requestID  uint64
	hash       string
	lockOnSave bool
	err        error
}

type screenLockDeletedMsg struct {
	requestID uint64
	err       error
}

type screenLockIdleTickMsg struct{}

func (m *modelState) beginScreenLock(purpose screenLockPurpose) tea.Cmd {
	if m.screenLock != nil {
		return nil
	}
	m.nextScreenLockID++
	requestID := m.nextScreenLockID
	state := &screenLockState{requestID: requestID, purpose: purpose}
	if m.screenLockLoaded {
		state.storedHash = m.screenLockHash
		if state.storedHash == "" {
			state.phase = screenLockNewPassword
			state.lockOnSave = purpose == screenLockPurposeLock
		} else if purpose == screenLockPurposeChange {
			state.phase = screenLockVerifyPassword
		} else {
			state.phase = screenLockLocked
		}
		m.screenLock = state
		return nil
	}
	state.phase = screenLockLoading
	m.screenLock = state
	vault := m.credentials
	return func() tea.Msg {
		if vault == nil {
			return screenLockLoadedMsg{requestID: requestID, purpose: purpose, err: errors.New("系统钥匙串不可用")}
		}
		hash, err := vault.Get(screenLockCredentialRef)
		return screenLockLoadedMsg{requestID: requestID, purpose: purpose, hash: hash, err: err}
	}
}

func (m *modelState) loadAutoScreenLock() tea.Cmd {
	if m.config.Security.AutoLockMinutes <= 0 || m.screenLockLoaded {
		return nil
	}
	m.nextScreenLockID++
	requestID := m.nextScreenLockID
	vault := m.credentials
	return func() tea.Msg {
		if vault == nil {
			return screenLockLoadedMsg{requestID: requestID, purpose: screenLockPurposeAutoLoad, err: errors.New("系统钥匙串不可用")}
		}
		hash, err := vault.Get(screenLockCredentialRef)
		return screenLockLoadedMsg{requestID: requestID, purpose: screenLockPurposeAutoLoad, hash: hash, err: err}
	}
}

func (m *modelState) handleScreenLockLoaded(msg screenLockLoadedMsg) {
	if msg.purpose == screenLockPurposeAutoLoad {
		if msg.err == nil {
			if _, _, err := parseScreenLockVerifier(msg.hash); err != nil {
				m.screenLockLoaded = true
				m.statusText = "锁屏密码校验值无效；自动锁屏未启用，请重新设置锁屏密码"
				m.statusErr = true
				return
			}
			m.screenLockLoaded = true
			m.screenLockHash = msg.hash
			return
		}
		if errors.Is(msg.err, credentialstore.ErrNotFound) {
			m.screenLockLoaded = true
			m.screenLockHash = ""
			if m.config.Security.AutoLockMinutes > 0 {
				m.statusText = "自动锁屏尚未启用：请先设置锁屏密码，再使用 " + m.workspaceLeaderChord("screen_lock")
				m.statusErr = false
			}
			return
		}
		m.screenLockLoaded = false
		m.screenLockHash = ""
		m.statusText = "读取锁屏密码失败：" + msg.err.Error()
		m.statusErr = true
		return
	}
	state := m.screenLock
	if state == nil || state.requestID != msg.requestID || state.phase != screenLockLoading {
		return
	}
	if msg.err != nil && !errors.Is(msg.err, credentialstore.ErrNotFound) {
		m.screenLock = nil
		m.statusText = "读取锁屏密码失败：" + msg.err.Error()
		m.statusErr = true
		return
	}
	if msg.err == nil {
		if _, _, err := parseScreenLockVerifier(msg.hash); err != nil {
			m.screenLock = nil
			m.statusText = "锁屏密码校验值无效；请通过系统钥匙串修复或移除该条目：" + err.Error()
			m.statusErr = true
			return
		}
	}
	m.screenLockLoaded = true
	m.screenLockHash = msg.hash
	state.storedHash = msg.hash
	if msg.err == nil {
		if state.purpose == screenLockPurposeChange {
			state.phase = screenLockVerifyPassword
		} else {
			state.phase = screenLockLocked
		}
		return
	}
	state.phase = screenLockNewPassword
	state.lockOnSave = state.purpose == screenLockPurposeLock
}

func (m *modelState) updateScreenLockKey(msg tea.KeyPressMsg) tea.Cmd {
	state := m.screenLock
	if state == nil || state.phase == screenLockLoading || state.phase == screenLockBusy {
		return nil
	}
	if state.phase != screenLockLocked {
		m.lastActivity = time.Now()
	}
	key := msg.Key()
	keyName := msg.String()
	if keyName == "ctrl+d" && state.phase == screenLockNewPassword && state.purpose == screenLockPurposeChange {
		state.phase = screenLockBusy
		requestID := state.requestID
		vault := m.credentials
		return func() tea.Msg {
			if vault == nil {
				return screenLockDeletedMsg{requestID: requestID, err: errors.New("系统钥匙串不可用")}
			}
			return screenLockDeletedMsg{requestID: requestID, err: vault.Delete(screenLockCredentialRef)}
		}
	}
	if key.Code == tea.KeyEscape {
		if state.phase != screenLockLocked {
			clearScreenLockState(state)
			m.screenLock = nil
			m.lastActivity = time.Now()
		}
		return nil
	}
	if key.Code == tea.KeyBackspace || keyName == "ctrl+h" {
		state.input = removeLastRune(state.input)
		state.errorText = ""
		return nil
	}
	if key.Code == tea.KeyEnter {
		return m.submitScreenLockInput()
	}
	if key.Mod == 0 && key.Text != "" {
		appendScreenLockInput(state, key.Text)
	}
	return nil
}

func (m *modelState) updateScreenLockPaste(text string) {
	if m.screenLock == nil || m.screenLock.phase == screenLockLoading || m.screenLock.phase == screenLockBusy {
		return
	}
	appendScreenLockInput(m.screenLock, text)
	if m.screenLock.phase != screenLockLocked {
		m.lastActivity = time.Now()
	}
}

func appendScreenLockInput(state *screenLockState, text string) {
	if state == nil {
		return
	}
	for _, character := range text {
		if unicode.IsControl(character) || !utf8.ValidRune(character) {
			continue
		}
		if utf8.RuneCountInString(state.input) >= screenLockMaxRunes {
			break
		}
		state.input += string(character)
	}
	state.errorText = ""
}

func (m *modelState) submitScreenLockInput() tea.Cmd {
	state := m.screenLock
	if state == nil {
		return nil
	}
	switch state.phase {
	case screenLockLocked, screenLockVerifyPassword:
		if state.input == "" {
			state.errorText = "请输入锁屏密码。"
			return nil
		}
		password := []byte(state.input)
		state.input = ""
		state.busyContext = state.phase
		state.phase = screenLockBusy
		requestID, storedHash, phase := state.requestID, state.storedHash, state.busyContext
		return func() tea.Msg {
			valid, err := verifyScreenLockPassword(storedHash, password)
			return screenLockVerifiedMsg{requestID: requestID, phase: phase, valid: valid, err: err}
		}
	case screenLockNewPassword:
		if utf8.RuneCountInString(state.input) < screenLockMinRunes {
			state.errorText = fmt.Sprintf("密码至少需要 %d 个字符。", screenLockMinRunes)
			return nil
		}
		state.first, state.input = state.input, ""
		state.phase = screenLockConfirmPassword
		state.errorText = ""
		return nil
	case screenLockConfirmPassword:
		if state.input != state.first {
			state.input = ""
			state.first = ""
			state.phase = screenLockNewPassword
			state.errorText = "两次输入不一致，请重新设置。"
			return nil
		}
		password := []byte(state.input)
		state.input, state.first = "", ""
		state.phase = screenLockBusy
		requestID, lockOnSave := state.requestID, state.lockOnSave
		vault := m.credentials
		return func() tea.Msg {
			hash, err := hashScreenLockPassword(password)
			if err != nil {
				return screenLockSavedMsg{requestID: requestID, lockOnSave: lockOnSave, err: err}
			}
			if vault == nil {
				return screenLockSavedMsg{requestID: requestID, lockOnSave: lockOnSave, err: errors.New("系统钥匙串不可用")}
			}
			if err := vault.Set(screenLockCredentialRef, hash); err != nil {
				return screenLockSavedMsg{requestID: requestID, lockOnSave: lockOnSave, err: err}
			}
			return screenLockSavedMsg{requestID: requestID, hash: hash, lockOnSave: lockOnSave}
		}
	}
	return nil
}

func (m *modelState) handleScreenLockVerified(msg screenLockVerifiedMsg) {
	state := m.screenLock
	if state == nil || state.requestID != msg.requestID || state.phase != screenLockBusy || state.busyContext != msg.phase {
		return
	}
	if msg.err != nil {
		state.phase = msg.phase
		state.errorText = "锁屏密码校验失败：" + msg.err.Error()
		return
	}
	if !msg.valid {
		state.phase = msg.phase
		state.errorText = "密码不正确，请重试。"
		return
	}
	state.errorText = ""
	if msg.phase == screenLockLocked {
		clearScreenLockState(state)
		m.screenLock = nil
		m.statusText = "会话屏幕已解锁"
		m.statusErr = false
		m.lastActivity = time.Now()
		return
	}
	state.phase = screenLockNewPassword
	state.input = ""
	state.first = ""
}

func (m *modelState) handleScreenLockSaved(msg screenLockSavedMsg) {
	state := m.screenLock
	if state == nil || state.requestID != msg.requestID || state.phase != screenLockBusy {
		return
	}
	if msg.err != nil {
		state.phase = screenLockNewPassword
		state.errorText = "保存锁屏密码失败：" + msg.err.Error()
		return
	}
	m.screenLockHash = msg.hash
	m.screenLockLoaded = true
	if msg.lockOnSave {
		state.phase = screenLockLocked
		state.storedHash = msg.hash
		state.purpose = screenLockPurposeLock
		state.lockOnSave = false
		m.statusText = "锁屏密码已保存，工作区已锁定"
	} else {
		clearScreenLockState(state)
		m.screenLock = nil
		m.statusText = "锁屏密码已更新"
	}
	m.statusErr = false
	m.lastActivity = time.Now()
}

func (m *modelState) handleScreenLockDeleted(msg screenLockDeletedMsg) {
	state := m.screenLock
	if state == nil || state.requestID != msg.requestID || state.phase != screenLockBusy {
		return
	}
	if msg.err != nil {
		state.phase = screenLockNewPassword
		state.errorText = "移除锁屏密码失败：" + msg.err.Error()
		return
	}
	m.screenLockHash = ""
	m.screenLockLoaded = true
	clearScreenLockState(state)
	m.screenLock = nil
	if m.config.Security.AutoLockMinutes > 0 {
		m.statusText = "锁屏密码已移除；自动锁屏需要重新设置密码"
	} else {
		m.statusText = "锁屏密码已移除"
	}
	m.statusErr = false
	m.lastActivity = time.Now()
}

func (m *modelState) screenLockIdleTickCmd() tea.Cmd {
	if m.config.Security.AutoLockMinutes <= 0 {
		return nil
	}
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return screenLockIdleTickMsg{} })
}

func (m *modelState) handleScreenLockIdleTick() tea.Cmd {
	if m.screenLock == nil && m.screenLockHash != "" && m.config.Security.AutoLockMinutes > 0 &&
		time.Since(m.lastActivity) >= time.Duration(m.config.Security.AutoLockMinutes)*time.Minute {
		m.screenLock = &screenLockState{phase: screenLockLocked, purpose: screenLockPurposeLock, storedHash: m.screenLockHash}
		m.statusText = ""
		m.statusErr = false
	}
	return m.screenLockIdleTickCmd()
}

func (m modelState) screenLockView() string {
	width, height := max(1, m.width), max(1, m.height)
	phase := screenLockLoading
	state := m.screenLock
	if state != nil {
		phase = state.phase
	}
	title := "会话屏幕已锁定"
	message := "终端连接仍保持活动，收到的输出会继续更新。"
	inputLabel := "锁屏密码"
	switch phase {
	case screenLockLoading:
		title, message = "正在读取锁屏设置", "请稍候…"
	case screenLockVerifyPassword:
		title, message = "更改锁屏密码", "请输入当前密码以继续。"
	case screenLockNewPassword:
		title, message = "设置锁屏密码", "密码至少 8 个字符；密码校验材料保存在系统钥匙串。"
		inputLabel = "新密码"
	case screenLockConfirmPassword:
		title, message = "确认锁屏密码", "再次输入新密码。"
		inputLabel = "确认密码"
	case screenLockBusy:
		title, message = "正在处理锁屏设置", "请稍候…"
		if state != nil && state.busyContext == screenLockLocked {
			title, message = "正在验证锁屏密码", "终端连接仍保持活动。"
		} else if state != nil && state.busyContext == screenLockVerifyPassword {
			title, message = "正在验证当前密码", "请稍候…"
		}
	}
	mask := ""
	if state != nil {
		mask = strings.Repeat("•", min(32, utf8.RuneCountInString(state.input)))
	}
	cardWidth := min(72, max(1, width-4))
	contentWidth := max(1, cardWidth-6)
	cardStyle := lipgloss.NewStyle().Width(contentWidth).Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(palette.accent).Background(palette.canvas)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(palette.accent).Render("◆  " + title),
		"",
		mutedStyle.Render(fitText(message, contentWidth)),
	}
	if phase != screenLockLoading && phase != screenLockBusy {
		lines = append(lines, "", mutedStyle.Render(inputLabel), selectedStyle.Width(contentWidth).Render(mask+"▏"))
	}
	if state != nil && state.errorText != "" {
		lines = append(lines, "", warningStyle.Render("! "+fitText(state.errorText, max(1, contentWidth-2))))
	}
	hint := "Enter 验证 · Esc 退出程序仍不可用"
	switch phase {
	case screenLockLocked:
		hint = "输入密码后按 Enter 解锁；Esc 和退出快捷键在锁定期间不可用"
	case screenLockVerifyPassword:
		hint = "Enter 验证 · Esc 取消"
	case screenLockNewPassword:
		hint = "Enter 下一步 · Esc 取消"
		if state != nil && state.purpose == screenLockPurposeChange {
			hint += " · Ctrl+D 移除密码"
		}
	case screenLockConfirmPassword:
		hint = "Enter 保存 · Esc 取消"
	case screenLockLoading, screenLockBusy:
		hint = "请稍候…"
	}
	lines = append(lines, "", mutedStyle.Render(fitText(hint, contentWidth)))
	card := cardStyle.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, card)
}

func hashScreenLockPassword(password []byte) (string, error) {
	defer clear(password)
	if utf8.RuneCount(password) < screenLockMinRunes || utf8.RuneCount(password) > screenLockMaxRunes {
		return "", fmt.Errorf("锁屏密码长度必须为 %d 到 %d 个字符", screenLockMinRunes, screenLockMaxRunes)
	}
	salt := make([]byte, screenLockSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成锁屏密码盐值：%w", err)
	}
	derived, err := scrypt.Key(password, salt, screenLockScryptN, screenLockScryptR, screenLockScryptP, screenLockKeyBytes)
	if err != nil {
		return "", fmt.Errorf("派生锁屏密码校验值：%w", err)
	}
	encoded := "scrypt-v1$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(derived)
	clear(derived)
	return encoded, nil
}

func verifyScreenLockPassword(encoded string, password []byte) (bool, error) {
	defer clear(password)
	salt, expected, err := parseScreenLockVerifier(encoded)
	if err != nil {
		return false, err
	}
	derived, err := scrypt.Key(password, salt, screenLockScryptN, screenLockScryptR, screenLockScryptP, screenLockKeyBytes)
	if err != nil {
		return false, fmt.Errorf("派生锁屏密码校验值：%w", err)
	}
	valid := subtle.ConstantTimeCompare(derived, expected) == 1
	clear(derived)
	return valid, nil
}

func parseScreenLockVerifier(encoded string) ([]byte, []byte, error) {
	if len(encoded) > 128 {
		return nil, nil, errors.New("钥匙串中的锁屏校验值过长")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "scrypt-v1" {
		return nil, nil, errors.New("钥匙串中的锁屏校验值格式无效")
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil || len(salt) != screenLockSaltBytes {
		return nil, nil, errors.New("钥匙串中的锁屏盐值无效")
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil || len(expected) != screenLockKeyBytes {
		return nil, nil, errors.New("钥匙串中的锁屏校验值无效")
	}
	return salt, expected, nil
}

func clearScreenLockState(state *screenLockState) {
	if state == nil {
		return
	}
	state.input = ""
	state.first = ""
	state.errorText = ""
}
