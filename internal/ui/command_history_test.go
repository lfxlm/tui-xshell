package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func commandHistoryTestState() (modelState, *terminalPane, *workspaceTestSession) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "test"},
		session: session, terminal: vt.NewSafeEmulator(80, 20),
	}
	pane.terminal.SetScrollbackSize(100)
	state := modelState{
		width: 100, height: 26, workspaceVisible: true, activeTabID: "tab-1",
		tabs: []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	return state, pane, session
}

func typeWorkspaceText(state modelState, text string) modelState {
	for _, value := range text {
		updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: value, Text: string(value)}))
		state = unwrapModelForCommandHistory(updated)
	}
	return state
}

func unwrapModelForCommandHistory(updated tea.Model) modelState {
	if state, ok := updated.(modelState); ok {
		return state
	}
	if state, ok := updated.(*modelState); ok {
		return *state
	}
	panic("unexpected model type")
}

func TestTerminalCommandHistoryRequiresEchoAndTracksEditedInput(t *testing.T) {
	state, pane, session := commandHistoryTestState()
	state = typeWorkspaceText(state, "echo helo")
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	state = unwrapModelForCommandHistory(updated)
	state = typeWorkspaceText(state, "lo")
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 0 || len(pane.pendingCommands) != 1 {
		t.Fatalf("command was stored before output echo: history=%q pending=%+v", pane.commandHistory, pane.pendingCommands)
	}

	updated, _ = state.Update(terminalOutputMsg{session: session, data: []byte("user@host:~$ echo helo\b \blo\r\nhello\r\nuser@host:~$ ")})
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 1 || pane.commandHistory[0] != "echo hello" {
		t.Fatalf("echoed command history = %q, want [echo hello]", pane.commandHistory)
	}
	if len(pane.pendingCommands) != 0 {
		t.Fatalf("echoed command remained pending: %+v", pane.pendingCommands)
	}
	if len(session.writes) == 0 {
		t.Fatal("test input was not forwarded to the shell")
	}
}

func TestTerminalCommandHistoryAcceptsEchoBeforeEnter(t *testing.T) {
	state, pane, session := commandHistoryTestState()
	_, _ = pane.terminal.Write([]byte("user@host:~$ "))
	state = typeWorkspaceText(state, "echo live")
	updated, _ := state.Update(terminalOutputMsg{session: session, data: []byte("echo live")})
	state = unwrapModelForCommandHistory(updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 1 || pane.commandHistory[0] != "echo live" {
		t.Fatalf("live echoed command history = %q, want [echo live]", pane.commandHistory)
	}
	if len(pane.pendingCommands) != 0 {
		t.Fatalf("live echoed command remained pending: %+v", pane.pendingCommands)
	}
}

func TestTerminalCommandHistoryDoesNotTreatCommandOutputAsEcho(t *testing.T) {
	state, pane, session := commandHistoryTestState()
	state = typeWorkspaceText(state, "ls")
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	updated, _ = state.Update(terminalOutputMsg{session: session, data: []byte("ls\r\n")})
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 0 || len(pane.pendingCommands) != 1 {
		t.Fatalf("output identical to the command was mistaken for an echo: history=%q pending=%+v", pane.commandHistory, pane.pendingCommands)
	}
	updated, _ = state.Update(terminalOutputMsg{session: session, data: []byte("user@host:~$ ls\r\n")})
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 1 || pane.commandHistory[0] != "ls" {
		t.Fatalf("prompt-echoed command history = %q, want [ls]", pane.commandHistory)
	}
}

func TestTerminalCommandHistorySkipsHiddenPasswordPromptAndAlternateScreen(t *testing.T) {
	for _, prompt := range []string{"Password: ", "user@host's password: ", "[sudo] password for liufei: ", "Enter passphrase for key: ", "请输入密码："} {
		state, pane, _ := commandHistoryTestState()
		_, _ = pane.terminal.Write([]byte(prompt))
		if !terminalHasSensitiveInputPrompt(pane) {
			t.Errorf("did not recognize sensitive prompt %q", prompt)
			continue
		}
		state = typeWorkspaceText(state, "not-a-command")
		updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		state = unwrapModelForCommandHistory(updated)
		if len(pane.commandHistory) != 0 || len(pane.pendingCommands) != 0 || len(pane.commandInput) != 0 {
			t.Errorf("hidden prompt input %q was retained: history=%q pending=%+v draft=%q", prompt, pane.commandHistory, pane.pendingCommands, string(pane.commandInput))
		}
	}

	state, pane, _ := commandHistoryTestState()
	_, _ = pane.terminal.Write([]byte("\x1b[?1049h"))
	state = typeWorkspaceText(state, "vim-input")
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 0 || len(pane.pendingCommands) != 0 {
		t.Fatalf("alternate-screen input was recorded: history=%q pending=%+v", pane.commandHistory, pane.pendingCommands)
	}
}

func TestTerminalCommandHistoryViewerCopiesAndClearsWithoutShellInput(t *testing.T) {
	state, pane, session := commandHistoryTestState()
	pane.commandHistory = []string{"echo old", "echo newest"}
	initialWrites := len(session.writes)
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModelForCommandHistory(updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	state = unwrapModelForCommandHistory(updated)
	if state.commandHistoryView == nil {
		t.Fatal("Ctrl+G then Y did not open command history")
	}
	if view := state.view(); !strings.Contains(view, "echo newest") || !strings.Contains(view, "明文秘密") {
		t.Fatalf("command history view is missing entries or its secret warning:\n%s", view)
	}

	updated, copyCommand := state.updateTerminalCommandHistoryKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	if copyCommand == nil || state.commandHistoryView != nil {
		t.Fatal("Enter did not request clipboard copy and close history")
	}
	if len(session.writes) != initialWrites {
		t.Fatalf("viewing/copying history wrote to shell: %q", session.writes[initialWrites:])
	}

	state.commandHistoryView = &terminalCommandHistoryViewState{tabID: "tab-1", paneID: pane.id}
	updated, _ = state.updateTerminalCommandHistoryKey(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	state = unwrapModelForCommandHistory(updated)
	if !state.commandHistoryView.confirmingClear {
		t.Fatal("X did not request confirmation before clearing history")
	}
	updated, _ = state.updateTerminalCommandHistoryKey(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	state = unwrapModelForCommandHistory(updated)
	if len(pane.commandHistory) != 0 || len(pane.pendingCommands) != 0 {
		t.Fatalf("confirmed clear left command history: history=%q pending=%+v", pane.commandHistory, pane.pendingCommands)
	}
	if len(session.writes) != initialWrites {
		t.Fatalf("clearing history wrote to shell: %q", session.writes[initialWrites:])
	}
}

func TestTerminalCommandHistoryRejectsShellArrowHistoryEditing(t *testing.T) {
	state, pane, _ := commandHistoryTestState()
	state = typeWorkspaceText(state, "echo first")
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	state = unwrapModelForCommandHistory(updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModelForCommandHistory(updated)
	if len(pane.pendingCommands) != 0 {
		t.Fatalf("incompletely tracked shell-history edit became a candidate: %+v", pane.pendingCommands)
	}
}
