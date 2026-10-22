package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type sftpEditorCommandSavedMsg struct {
	state   *sftpBrowserState
	command string
	err     error
}

func (m *modelState) openSFTPExternalEditorCommandPrompt() {
	if m.sftp == nil || m.sftp.busy {
		return
	}
	m.sftp.prompt = &sftpPrompt{
		action: sftpPromptExternalEditorCommand,
		title:  "设置本地编辑器命令（默认 code --wait；留空使用默认编辑器）",
		value:  m.sftpExternalEditorCommand,
	}
}

func (m *modelState) saveSFTPExternalEditorCommand(command string, state *sftpBrowserState) tea.Cmd {
	catalog, ok := m.catalog.(sftpEditorCommandPreferenceCatalog)
	if !ok {
		state.status, state.statusErr = "当前存储不支持保存本地编辑器设置", true
		return nil
	}
	state.busy = true
	state.status, state.statusErr = "正在保存本地编辑器设置…", false
	return func() tea.Msg {
		return sftpEditorCommandSavedMsg{
			state: state, command: command,
			err: catalog.SetSFTPExternalEditorCommand(context.Background(), command),
		}
	}
}

func (m *modelState) handleSFTPExternalEditorCommandSaved(msg sftpEditorCommandSavedMsg) {
	if msg.state != nil {
		msg.state.busy = false
	}
	if msg.err != nil {
		if msg.state != nil {
			msg.state.prompt = &sftpPrompt{
				action: sftpPromptExternalEditorCommand,
				title:  "保存本地编辑器设置失败：" + msg.err.Error(),
				value:  msg.command,
			}
		}
		return
	}
	m.sftpExternalEditorCommand = strings.TrimSpace(msg.command)
	if msg.state != nil {
		msg.state.status, msg.state.statusErr = "本地编辑器设置已保存", false
	}
}
