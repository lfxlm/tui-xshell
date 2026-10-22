package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type terminalScriptRecording struct {
	steps                []startupScriptStep
	skippedSensitive     bool
	skippedUnknownPrompt bool
	skippedLongCommand   bool
	paused               bool
	startedAt            time.Time
}

func recordTerminalScriptCommand(pane *terminalPane, command string) bool {
	if pane == nil || pane.scriptRecording == nil {
		return false
	}
	recording := pane.scriptRecording
	if recording.paused {
		return false
	}
	if terminalHasSensitiveInputPrompt(pane) {
		recording.skippedSensitive = true
		return false
	}
	prompt := strings.TrimSpace(pane.commandEchoPrompt)
	if prompt == "" {
		recording.skippedUnknownPrompt = true
		return false
	}
	if command == "" || len([]rune(command)) > maxStartupScriptSend || containsControlCharacter(command) {
		recording.skippedLongCommand = true
		return false
	}
	if len(recording.steps) >= maxStartupScriptSteps {
		return false
	}
	recording.steps = append(recording.steps, startupScriptStep{
		Expect:         regexp.QuoteMeta(prompt),
		Send:           command,
		TimeoutSeconds: startupScriptDefaultTimeout,
	})
	return true
}

func (m *modelState) toggleActiveTerminalScriptRecording() {
	pane := m.activeTerminalPane()
	if pane == nil {
		m.statusText, m.statusErr = "没有可录制的终端 pane", true
		return
	}
	m.toggleTerminalScriptRecording(pane)
}

func (m *modelState) toggleTerminalScriptRecording(pane *terminalPane) {
	if pane == nil {
		m.statusText, m.statusErr = "没有可录制的终端 pane", true
		return
	}
	if pane.scriptRecording != nil {
		path, recording, err := stopTerminalScriptRecording(pane)
		if recording == nil {
			return
		}
		if err != nil {
			pane.statusText = "保存录制脚本失败：" + err.Error()
			pane.statusErr = true
			return
		}
		if path == "" {
			pane.statusText = "脚本录制已停止；没有识别到可保存的 Shell 命令"
			pane.statusErr = false
			return
		}
		pane.statusText = fmt.Sprintf("脚本录制已保存：%s（%d 条命令）", path, len(recording.steps))
		if recording.skippedSensitive {
			pane.statusText += "；敏感提示中的输入未录制"
		}
		if recording.skippedUnknownPrompt || recording.skippedLongCommand || len(recording.steps) >= maxStartupScriptSteps {
			pane.statusText += "；有未录入内容，请检查脚本"
		}
		pane.statusErr = false
		return
	}
	if pane.session == nil || pane.terminal == nil {
		pane.statusText, pane.statusErr = "终端未连接，无法开始脚本录制", true
		return
	}
	if pane.startupPending != "" || pane.startupScript != nil || pane.triggerScriptLoading {
		pane.statusText, pane.statusErr = "自动登录或触发脚本正在运行，暂不能开始录制", true
		return
	}
	if terminalHasSensitiveInputPrompt(pane) {
		pane.statusText, pane.statusErr = "当前是敏感输入提示，无法开始脚本录制", true
		return
	}
	if strings.TrimSpace(pane.commandEchoPrompt) == "" {
		pane.statusText, pane.statusErr = "尚未识别到 Shell 提示符；等待命令提示符出现后再开始录制", true
		return
	}
	pane.scriptRecording = &terminalScriptRecording{startedAt: time.Now()}
	pane.statusText = "正在录制 Shell 命令；命令参数可能包含敏感内容，敏感提示输入会跳过。按 " + m.workspaceLeaderChord("toggle_script_recording") + " 停止并保存"
	pane.statusErr = false
}

func (m *modelState) saveTerminalScriptRecordingsBeforeQuit() error {
	for _, tab := range m.tabs {
		for _, pane := range collectPanes(tab.root) {
			if pane.scriptRecording == nil {
				continue
			}
			if _, _, err := stopTerminalScriptRecording(pane); err != nil {
				return fmt.Errorf("%s：%w", pane.host.Name, err)
			}
		}
	}
	return nil
}

func stopTerminalScriptRecording(pane *terminalPane) (string, *terminalScriptRecording, error) {
	if pane == nil || pane.scriptRecording == nil {
		return "", nil, nil
	}
	recording := pane.scriptRecording
	if len(recording.steps) == 0 {
		pane.scriptRecording = nil
		return "", recording, nil
	}
	path, err := saveTerminalScriptRecording(pane.host.Name, recording)
	if err != nil {
		return "", recording, err
	}
	pane.scriptRecording = nil
	return path, recording, err
}

func saveTerminalScriptRecording(hostName string, recording *terminalScriptRecording) (string, error) {
	if recording == nil || len(recording.steps) == 0 {
		return "", fmt.Errorf("录制内容为空")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("获取用户配置目录：%w", err)
	}
	directory := filepath.Join(configDir, "tui-ssh-workbench", "scripts", "recordings")
	return saveTerminalScriptRecordingToDir(directory, hostName, recording, time.Now())
}

func saveTerminalScriptRecordingToDir(directory, hostName string, recording *terminalScriptRecording, now time.Time) (string, error) {
	if recording == nil || len(recording.steps) == 0 {
		return "", fmt.Errorf("录制内容为空")
	}
	data, err := json.MarshalIndent(startupScriptDocument{Version: 1, Steps: recording.steps}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("编码 JSON 脚本：%w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("创建脚本目录：%w", err)
	}
	baseName := terminalScriptFileBase(hostName)
	stamp := now.Format("20060102-150405")
	for suffix := 0; suffix < 1000; suffix++ {
		name := fmt.Sprintf("%s-%s.json", baseName, stamp)
		if suffix > 0 {
			name = fmt.Sprintf("%s-%s-%02d.json", baseName, stamp, suffix+1)
		}
		path := filepath.Join(directory, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("创建脚本文件：%w", err)
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("写入脚本文件：%w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("无法分配唯一脚本文件名")
}

func terminalScriptFileBase(hostName string) string {
	var result strings.Builder
	separator := false
	for _, char := range hostName {
		if unicode.IsLetter(char) || unicode.IsNumber(char) || char == '-' || char == '_' || char == '.' {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			separator = false
			result.WriteRune(char)
		} else {
			separator = true
		}
	}
	name := strings.Trim(result.String(), "-.")
	if name == "" {
		return "session"
	}
	return name
}
