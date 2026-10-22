package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposePaneEditsMultilineTextAtCursor(t *testing.T) {
	state := &composePaneState{lines: []string{"cmd", "tail"}, row: 0, column: 1}
	state.insertText("X\r\nY")
	if got, want := strings.Join(state.lines, "|"), "cX|Ymd|tail"; got != want {
		t.Fatalf("pasted text = %q, want %q", got, want)
	}
	if state.row != 1 || state.column != 1 {
		t.Fatalf("cursor after multiline insertion = row %d, column %d; want row 1, column 1", state.row, state.column)
	}

	model := modelState{composePane: state, height: 24}
	updated, _ := model.updateComposePaneKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	model = unwrapModel(t, updated)
	if got := strings.Join(model.composePane.lines, "|"); got != "cX|md|tail" {
		t.Fatalf("backspace before cursor = %q, want %q", got, "cX|md|tail")
	}
	if model.composePane.row != 1 || model.composePane.column != 0 {
		t.Fatalf("cursor after backspace = row %d, column %d; want row 1, column 0", model.composePane.row, model.composePane.column)
	}
	updated, _ = model.updateComposePaneKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	model = unwrapModel(t, updated)
	if got := strings.Join(model.composePane.lines, "|"); got != "cXmd|tail" {
		t.Fatalf("backspace at line start = %q, want %q", got, "cXmd|tail")
	}
	if model.composePane.row != 0 || model.composePane.column != 2 {
		t.Fatalf("cursor after joining lines = row %d, column %d; want row 0, column 2", model.composePane.row, model.composePane.column)
	}
}

func TestComposePaneLocksSynchronizedTargetsAndSendsBracketedPaste(t *testing.T) {
	firstSession := &workspaceTestSession{}
	secondSession := &workspaceTestSession{}
	otherTabSession := &workspaceTestSession{}
	first := &terminalPane{id: "first", session: firstSession, bracketedPaste: true}
	second := &terminalPane{id: "second", session: secondSession}
	other := &terminalPane{id: "other", session: otherTabSession}
	tab := &workspaceTab{
		id: "tab", activePaneID: first.id, syncInput: true,
		root: &paneNode{axis: splitVertical, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{
		width: 120, height: 30, activeTabID: tab.id, workspaceVisible: true,
		tabs: []*workspaceTab{
			tab,
			{id: "other-tab", activePaneID: other.id, root: &paneNode{pane: other}},
		},
	}

	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	state = unwrapModel(t, updated)
	if state.composePane == nil || !state.composePane.syncInput || len(state.composePane.targetPaneIDs) != 2 {
		t.Fatalf("Compose targets = %#v, want two frozen targets in sync mode", state.composePane)
	}
	if !strings.Contains(state.composePaneView(), "2 个已连接 pane") {
		t.Fatal("Compose Pane does not display its target scope")
	}

	updated, _ = state.Update(tea.PasteMsg{Content: "echo one\r\necho two"})
	state = unwrapModel(t, updated)
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	wantFirst := "\x1b[200~echo one\r\necho two\x1b[201~"
	if len(firstSession.writes) != 1 || string(firstSession.writes[0]) != wantFirst {
		t.Fatalf("bracketed target received %q, want %q", firstSession.writes, wantFirst)
	}
	if len(secondSession.writes) != 1 || string(secondSession.writes[0]) != "echo one\r\necho two" {
		t.Fatalf("plain target received %q", secondSession.writes)
	}
	if len(otherTabSession.writes) != 0 {
		t.Fatalf("Compose text leaked into another tab: %q", otherTabSession.writes)
	}
	if state.composePane != nil || !strings.Contains(state.statusText, "没有额外补回车") {
		t.Fatalf("Compose completion state = %#v, status=%q", state.composePane, state.statusText)
	}
}

func TestComposePaneCancelAndEmptySendDoNotWrite(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane", session: session}
	tab := &workspaceTab{id: "tab", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true}

	state.openComposePane()
	updated, _ := state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if state.composePane == nil || state.composePane.errorText == "" {
		t.Fatal("empty send should keep the panel open with an explanation")
	}
	if len(session.writes) != 0 {
		t.Fatalf("empty send wrote to terminal: %q", session.writes)
	}

	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.composePane != nil || len(session.writes) != 0 {
		t.Fatalf("cancel state = composer %#v, writes %q", state.composePane, session.writes)
	}
}
