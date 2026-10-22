package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/model"
)

const maxStartupCommandLength = 1024

type terminalStartupSentMsg struct {
	paneID string
	err    error
}

func startupCommandForHost(host model.Host) string {
	if host.Kind != model.HostKindSSH {
		return ""
	}
	return strings.TrimSpace(host.Startup)
}

func sendStartupCommandCmd(session terminalSession, paneID, command, encodingName string) tea.Cmd {
	return func() tea.Msg {
		command = strings.TrimSpace(command)
		if command == "" {
			return terminalStartupSentMsg{paneID: paneID}
		}
		payload, err := encodeTerminalText(encodingName, append([]byte(command), '\r'))
		if err != nil {
			return terminalStartupSentMsg{paneID: paneID, err: err}
		}
		return terminalStartupSentMsg{
			paneID: paneID,
			err:    session.Write(payload),
		}
	}
}
