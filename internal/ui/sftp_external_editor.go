package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sftpclient"
)

func (m *modelState) openSFTPExternalEditor(autoSave bool) tea.Cmd {
	state := m.sftp
	if state == nil || state.editor == nil || state.busy {
		return nil
	}
	editor := state.editor
	rawCommand := strings.TrimSpace(m.sftpExternalEditorCommand)
	if rawCommand == "" {
		rawCommand = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if rawCommand == "" {
		rawCommand = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if rawCommand == "" {
		rawCommand = "code --wait"
	}
	commandParts, err := parseEditorCommand(rawCommand)
	if err != nil {
		state.status, state.statusErr = "无法启动外部编辑器："+err.Error(), true
		return nil
	}
	commandParts, err = resolveExternalEditorCommand(commandParts, true)
	if err != nil {
		state.status, state.statusErr = "无法启动外部编辑器："+err.Error(), true
		return nil
	}
	tempPath, err := writeExternalEditorBuffer(editor)
	if err != nil {
		state.status, state.statusErr = "无法准备外部编辑器文件："+err.Error(), true
		return nil
	}
	command := exec.Command(commandParts[0], append(commandParts[1:], tempPath)...)
	state.requestID++
	requestID, generation := state.requestID, state.generation
	state.busy = true
	state.status = "正在外部编辑器中打开临时副本…"
	if autoSave {
		state.status = "正在外部编辑器中打开副本；退出后会自动保存…"
	}
	if isGUIEditorCommand(command.Path) {
		state.status = "外部编辑器已打开；TUI 保持显示，关闭文件后自动回存…"
	}
	state.statusErr = false
	return runExternalEditor(command, func(err error) tea.Msg {
		return sftpExternalEditorFinishedMsg{
			requestID: requestID, generation: generation, tempPath: tempPath, autoSave: autoSave, err: err,
		}
	})
}

func parseEditorCommand(raw string) ([]string, error) {
	return parseEditorCommandForPlatform(raw, runtime.GOOS == "windows")
}

func parseEditorCommandForPlatform(raw string, windows bool) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("请按 E 设置本地编辑器，或设置 VISUAL / EDITOR 环境变量")
	}
	var args []string
	var current strings.Builder
	var quote rune
	escaped, started := false, false
	for _, character := range raw {
		if escaped {
			current.WriteRune(character)
			escaped, started = false, true
			continue
		}
		if character == '\\' && quote != '\'' && !windows {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote, started = character, true
			continue
		}
		if character == ' ' || character == '\t' || character == '\n' {
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
			continue
		}
		current.WriteRune(character)
		started = true
	}
	if escaped {
		return nil, errors.New("编辑器命令末尾不能是转义符")
	}
	if quote != 0 {
		return nil, errors.New("编辑器命令包含未闭合的引号")
	}
	if started {
		args = append(args, current.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, errors.New("编辑器命令不能为空")
	}
	return args, nil
}

func writeExternalEditorBuffer(editor *sftpEditorState) (string, error) {
	if editor == nil {
		return "", errors.New("编辑缓冲区不可用")
	}
	content := serializeSFTPText(editor.lines, editor.lineEndings)
	if int64(len(content)) > sftpclient.MaxEditableFileSize {
		return "", fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(sftpclient.MaxEditableFileSize))
	}
	suffix := filepath.Ext(editor.name)
	if strings.ContainsAny(suffix, `/\\`) {
		suffix = ""
	}
	file, err := os.CreateTemp("", "tui-ssh-workbench-*.edit"+suffix)
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败：%w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("写入临时文件失败：%w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("关闭临时文件失败：%w", err)
	}
	return path, nil
}

func readExternalEditorBuffer(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("读取临时文件信息失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("外部编辑器临时文件已被替换为非普通文件")
	}
	if info.Size() > sftpclient.MaxEditableFileSize {
		return nil, fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(sftpclient.MaxEditableFileSize))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开临时文件失败：%w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取临时文件状态失败：%w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return nil, errors.New("外部编辑器临时文件在读取时发生变化")
	}
	content, err := io.ReadAll(io.LimitReader(file, sftpclient.MaxEditableFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取临时文件失败：%w", err)
	}
	if int64(len(content)) > sftpclient.MaxEditableFileSize {
		return nil, fmt.Errorf("文件大小超过限制（最大 %s）", formatByteCount(sftpclient.MaxEditableFileSize))
	}
	return content, nil
}

func (m *modelState) handleSFTPExternalEditorFinished(msg sftpExternalEditorFinishedMsg) tea.Cmd {
	defer os.Remove(msg.tempPath)
	state := m.sftp
	if state == nil || state.generation != msg.generation || state.requestID != msg.requestID || state.editor == nil {
		return nil
	}
	state.busy = false
	if msg.err != nil {
		state.status, state.statusErr = "外部编辑器退出失败："+msg.err.Error()+"；当前缓冲区未改变", true
		m.discardExternalOnlySFTPTextEditor()
		return nil
	}
	content, err := readExternalEditorBuffer(msg.tempPath)
	if err != nil {
		state.status, state.statusErr = "读取外部编辑结果失败："+err.Error()+"；当前缓冲区未改变", true
		m.discardExternalOnlySFTPTextEditor()
		return nil
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		state.status, state.statusErr = "外部编辑结果不是 UTF-8 文本；当前缓冲区未改变", true
		m.discardExternalOnlySFTPTextEditor()
		return nil
	}
	editor := state.editor
	updated := string(content)
	if updated == serializeSFTPText(editor.lines, editor.lineEndings) {
		if msg.autoSave && editor.dirty {
			state.status, state.statusErr = "外部编辑器已退出；正在保存当前修改…", false
			return m.saveExternalOnlySFTPTextEditor()
		}
		state.status, state.statusErr = "外部编辑器未修改文件", false
		m.discardExternalOnlySFTPTextEditor()
		return nil
	}
	editor.lines, editor.lineEndings = splitSFTPText(updated)
	editor.newline = preferredSFTPNewline(editor.lineEndings)
	editor.row = min(editor.row, len(editor.lines)-1)
	editor.column = min(editor.column, len([]rune(editor.lines[editor.row])))
	editor.dirty = updated != editor.original
	refreshSFTPTextHighlight(editor)
	keepSFTPTextCursorVisible(editor, m.height)
	if msg.autoSave && editor.dirty {
		state.status, state.statusErr = "外部编辑结果已载入；正在保存到文件…", false
		return m.saveExternalOnlySFTPTextEditor()
	}
	state.status, state.statusErr = "外部编辑结果已载入缓冲区；按 Ctrl+S 保存到文件", false
	return nil
}

func (m *modelState) saveExternalOnlySFTPTextEditor() tea.Cmd {
	state := m.sftp
	if state == nil || state.editor == nil {
		return nil
	}
	state.editor.closeAfterSave = true
	command := m.saveSFTPTextEditor()
	if command == nil && state.editor != nil && !state.busy {
		// A synchronous save error has no completion message to reveal the buffer.
		state.externalEditorOnly = false
	}
	if state.editor == nil {
		state.externalEditorOnly = false
	}
	return command
}

func (m *modelState) discardExternalOnlySFTPTextEditor() {
	if m.sftp == nil || !m.sftp.externalEditorOnly {
		return
	}
	m.sftp.editor = nil
	m.sftp.externalEditorOnly = false
}
