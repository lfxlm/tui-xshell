package ui

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/x/vt"

	"tui-ssh-workbench/internal/model"
)

func TestStartupCommandIsOnlyEnabledForSSHHosts(t *testing.T) {
	if got := startupCommandForHost(model.Host{Kind: model.HostKindSSH, Startup: "  cd /srv/app  "}); got != "cd /srv/app" {
		t.Fatalf("SSH startup command = %q, want trimmed command", got)
	}
	if got := startupCommandForHost(model.Host{Kind: model.HostKindLocal, Startup: "echo unsafe"}); got != "" {
		t.Fatalf("local startup command = %q, want empty", got)
	}
}

func TestStartupCommandUsesSelectedTerminalEncoding(t *testing.T) {
	session := &workspaceTestSession{}
	message, ok := sendStartupCommandCmd(session, "pane-1", "echo 中文", "GB18030")().(terminalStartupSentMsg)
	if !ok || message.err != nil {
		t.Fatalf("startup command result = %#v", message)
	}
	want, err := encodeTerminalInput(&terminalPane{terminalEncoding: "GB18030"}, []byte("echo 中文\r"))
	if err != nil {
		t.Fatal(err)
	}
	if len(session.writes) != 1 || string(session.writes[0]) != string(want) {
		t.Fatalf("startup command bytes = %x, want %x", session.writes, want)
	}
}

func TestSendStartupCommandWritesOneCarriageReturn(t *testing.T) {
	session := &workspaceTestSession{}
	message, ok := sendStartupCommandCmd(session, "pane-1", "cd /srv/app", model.TerminalEncodingUTF8)().(terminalStartupSentMsg)
	if !ok || message.err != nil || message.paneID != "pane-1" {
		t.Fatalf("startup send result = %#v", message)
	}
	if len(session.writes) != 1 || !bytes.Equal(session.writes[0], []byte("cd /srv/app\r")) {
		t.Fatalf("startup writes = %q, want one command followed by CR", session.writes)
	}
}

func TestTerminalOutputConsumesPendingStartupCommandOnlyOnce(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id:             "pane-1",
		host:           model.Host{Kind: model.HostKindSSH, Name: "remote"},
		session:        session,
		terminal:       vt.NewSafeEmulator(80, 24),
		startupPending: "cd /srv/app",
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{width: 80, height: 24, activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true}

	updated, command := state.Update(terminalOutputMsg{session: session, data: []byte("Welcome\r\n")})
	state = unwrapModel(t, updated)
	if pane.startupPending != "" || command == nil {
		t.Fatalf("first terminal output did not consume startup command: pending=%q command=%v", pane.startupPending, command != nil)
	}
	updated, _ = state.Update(terminalOutputMsg{session: session, data: []byte("remote$ ")})
	state = unwrapModel(t, updated)
	if pane.startupPending != "" {
		t.Fatalf("subsequent output restored startup command: %q", pane.startupPending)
	}
}
