package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const (
	maxStartupScriptPathLength  = 1024
	maxStartupScriptBytes       = 128 << 10
	maxStartupScriptSteps       = 64
	maxStartupScriptExpect      = 512
	maxStartupScriptSend        = 1024
	maxStartupScriptTimeout     = 300
	startupScriptDefaultTimeout = 30
	startupScriptMatchBuffer    = 8 << 10
)

var startupScriptPlaceholderRE = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

type startupScriptDocument struct {
	Version int                 `json:"version"`
	Steps   []startupScriptStep `json:"steps"`
}

type startupScriptStep struct {
	Expect         string `json:"expect"`
	Send           string `json:"send"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	matcher        *regexp.Regexp
}

type startupLoginScript struct {
	steps []startupScriptStep
}

type startupScriptRuntime struct {
	script       *startupLoginScript
	stepIndex    int
	matchText    string
	deferredText string
	stripper     ansiTextStripper
	generation   uint64
	sendPending  bool
}

type startupScriptStepSentMsg struct {
	paneID     string
	generation uint64
	stepIndex  int
	err        error
}

type startupScriptTimeoutMsg struct {
	paneID     string
	generation uint64
	stepIndex  int
}

type startupScriptConfigurationError struct{ err error }

func (e *startupScriptConfigurationError) Error() string { return e.err.Error() }
func (e *startupScriptConfigurationError) Unwrap() error { return e.err }

func newStartupScriptRuntime(script *startupLoginScript, generation uint64) *startupScriptRuntime {
	if script == nil {
		return nil
	}
	return &startupScriptRuntime{script: script, generation: generation}
}

func startupScriptWaitingStatus(runtime *startupScriptRuntime) string {
	if runtime == nil || runtime.script == nil {
		return ""
	}
	return fmt.Sprintf("提示符登录脚本：等待第 %d/%d 步", runtime.stepIndex+1, len(runtime.script.steps))
}

func (m *modelState) matchStartupScriptOutput(pane *terminalPane, data []byte) tea.Cmd {
	runtime := pane.startupScript
	if runtime == nil || runtime.script == nil || runtime.stepIndex >= len(runtime.script.steps) {
		return nil
	}
	text := runtime.stripper.write(data)
	if runtime.sendPending {
		runtime.deferredText = appendStartupScriptMatchText(runtime.deferredText, text)
		return nil
	}
	runtime.matchText = appendStartupScriptMatchText(runtime.matchText, text)
	if !runtime.script.steps[runtime.stepIndex].matcher.MatchString(runtime.matchText) {
		return nil
	}
	runtime.matchText = ""
	runtime.sendPending = true
	return sendStartupScriptStepCmd(pane.session, pane.id, runtime, pane.host.User, pane.host.CredentialRef, pane.terminalEncoding, m.credentials)
}

// loadStartupLoginScript reads and validates a machine-local JSON send/expect
// script. Relative paths are resolved against the app's current directory.
func loadStartupLoginScript(path string) (*startupLoginScript, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	resolved, err := resolveStartupScriptPath(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("打开提示符登录脚本：%w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxStartupScriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取提示符登录脚本：%w", err)
	}
	if len(data) > maxStartupScriptBytes {
		return nil, fmt.Errorf("提示符登录脚本不能超过 %d 字节", maxStartupScriptBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document startupScriptDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("解析提示符登录脚本：%w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("提示符登录脚本只能包含一个 JSON 对象")
		}
		return nil, fmt.Errorf("解析提示符登录脚本尾部：%w", err)
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("提示符登录脚本版本 %d 不受支持（当前支持 1）", document.Version)
	}
	if len(document.Steps) == 0 || len(document.Steps) > maxStartupScriptSteps {
		return nil, fmt.Errorf("提示符登录脚本步骤数必须为 1 到 %d", maxStartupScriptSteps)
	}
	for index := range document.Steps {
		step := &document.Steps[index]
		if strings.TrimSpace(step.Expect) == "" || len([]rune(step.Expect)) > maxStartupScriptExpect {
			return nil, fmt.Errorf("第 %d 步 expect 必须填写，且不超过 %d 个字符", index+1, maxStartupScriptExpect)
		}
		matcher, err := regexp.Compile(step.Expect)
		if err != nil {
			return nil, fmt.Errorf("第 %d 步 expect 正则无效：%w", index+1, err)
		}
		step.matcher = matcher
		if len([]rune(step.Send)) > maxStartupScriptSend || strings.ContainsAny(step.Send, "\r\n") {
			return nil, fmt.Errorf("第 %d 步 send 必须是单行且不超过 %d 个字符", index+1, maxStartupScriptSend)
		}
		for _, char := range step.Send {
			if unicode.IsControl(char) {
				return nil, fmt.Errorf("第 %d 步 send 不能包含控制字符", index+1)
			}
		}
		for _, match := range startupScriptPlaceholderRE.FindAllStringSubmatch(step.Send, -1) {
			if match[1] != "user" && match[1] != "password" {
				return nil, fmt.Errorf("第 %d 步包含不支持的占位符 {{%s}}", index+1, match[1])
			}
		}
		if step.TimeoutSeconds == 0 {
			step.TimeoutSeconds = startupScriptDefaultTimeout
		}
		if step.TimeoutSeconds < 1 || step.TimeoutSeconds > maxStartupScriptTimeout {
			return nil, fmt.Errorf("第 %d 步等待时间必须为 1 到 %d 秒", index+1, maxStartupScriptTimeout)
		}
	}
	return &startupLoginScript{steps: document.Steps}, nil
}

func resolveStartupScriptPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("定位用户目录：%w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析提示符登录脚本路径：%w", err)
	}
	return resolved, nil
}

func startupScriptTimeoutCmd(paneID string, runtime *startupScriptRuntime) tea.Cmd {
	if runtime == nil || runtime.script == nil || runtime.stepIndex >= len(runtime.script.steps) {
		return nil
	}
	message := startupScriptTimeoutMsg{paneID: paneID, generation: runtime.generation, stepIndex: runtime.stepIndex}
	delay := time.Duration(runtime.script.steps[runtime.stepIndex].TimeoutSeconds) * time.Second
	return tea.Tick(delay, func(time.Time) tea.Msg { return message })
}

func sendStartupScriptStepCmd(session terminalSession, paneID string, runtime *startupScriptRuntime, username, credentialRef, encodingName string, vault credentialVault) tea.Cmd {
	if runtime == nil || runtime.script == nil || runtime.stepIndex >= len(runtime.script.steps) {
		return nil
	}
	stepIndex := runtime.stepIndex
	step := runtime.script.steps[stepIndex]
	generation := runtime.generation
	return func() tea.Msg {
		var password string
		passwordLoaded := false
		var resolveErr error
		send := startupScriptPlaceholderRE.ReplaceAllStringFunc(step.Send, func(placeholder string) string {
			if resolveErr != nil {
				return ""
			}
			name := strings.TrimSuffix(strings.TrimPrefix(placeholder, "{{"), "}}")
			var value string
			switch name {
			case "user":
				value = username
				if value == "" {
					resolveErr = errors.New("此步骤使用了 {{user}}，但 SSH 用户名为空")
				}
			case "password":
				if !passwordLoaded {
					passwordLoaded = true
					if credentialRef == "" || vault == nil {
						resolveErr = errors.New("此步骤需要钥匙串中已保存的 SSH 凭据")
						return ""
					}
					password, resolveErr = vault.Get(credentialRef)
					if resolveErr != nil {
						resolveErr = fmt.Errorf("读取 SSH 钥匙串凭据失败：%w", resolveErr)
					}
				}
				value = password
			}
			if resolveErr == nil && containsControlCharacter(value) {
				resolveErr = errors.New("自动登录占位符展开后包含控制字符，已拒绝发送")
			}
			return value
		})
		password = ""
		if resolveErr != nil {
			return startupScriptStepSentMsg{paneID: paneID, generation: generation, stepIndex: stepIndex, err: resolveErr}
		}
		if len([]rune(send)) > maxStartupScriptSend {
			return startupScriptStepSentMsg{paneID: paneID, generation: generation, stepIndex: stepIndex, err: fmt.Errorf("占位符展开后的发送内容超过 %d 个字符", maxStartupScriptSend)}
		}
		textPayload := append([]byte(send), '\r')
		send = ""
		payload, encodeErr := encodeTerminalText(encodingName, textPayload)
		for index := range textPayload {
			textPayload[index] = 0
		}
		err := encodeErr
		if err == nil {
			err = session.Write(payload)
		}
		for index := range payload {
			payload[index] = 0
		}
		return startupScriptStepSentMsg{paneID: paneID, generation: generation, stepIndex: stepIndex, err: err}
	}
}

func containsControlCharacter(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

// ansiTextStripper preserves partial escape sequences between SSH reads so
// expect patterns see prompt text rather than terminal styling bytes.
type ansiTextStripper struct {
	state uint8
}

func (s *ansiTextStripper) write(data []byte) string {
	const (
		plain uint8 = iota
		escape
		csi
		osc
		oscEscape
		stringSequence
		stringEscape
	)
	var output strings.Builder
	for _, char := range data {
		switch s.state {
		case plain:
			switch char {
			case 0x1b:
				s.state = escape
			case '\r', '\n':
				output.WriteByte('\n')
			case '\t':
				output.WriteByte('\t')
			default:
				if char >= 0x20 && char != 0x7f || char >= 0x80 {
					output.WriteByte(char)
				}
			}
		case escape:
			switch char {
			case '[':
				s.state = csi
			case ']':
				s.state = osc
			case 'P', '^', '_':
				s.state = stringSequence
			default:
				s.state = plain
			}
		case csi:
			if char >= 0x40 && char <= 0x7e {
				s.state = plain
			}
		case osc:
			if char == 0x07 {
				s.state = plain
			} else if char == 0x1b {
				s.state = oscEscape
			}
		case oscEscape:
			if char == '\\' {
				s.state = plain
			} else if char != 0x1b {
				s.state = osc
			}
		case stringSequence:
			if char == 0x1b {
				s.state = stringEscape
			}
		case stringEscape:
			if char == '\\' {
				s.state = plain
			} else if char != 0x1b {
				s.state = stringSequence
			}
		}
	}
	return output.String()
}

func appendStartupScriptMatchText(current, addition string) string {
	current += addition
	if len(current) <= startupScriptMatchBuffer {
		return current
	}
	return current[len(current)-startupScriptMatchBuffer:]
}
