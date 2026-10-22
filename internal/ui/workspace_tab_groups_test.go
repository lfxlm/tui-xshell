package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func newTabGroupTestTab(id, group, hostName string) *workspaceTab {
	pane := &terminalPane{
		id: id + "-pane", host: model.Host{Id: id, Name: hostName, Kind: model.HostKindLocal},
		terminal: vt.NewSafeEmulator(80, 20),
	}
	return &workspaceTab{id: id, groupName: group, root: &paneNode{pane: pane}, activePaneID: pane.id}
}

func TestWorkspaceTabGroupCreateMoveRenameAndDissolve(t *testing.T) {
	first := newTabGroupTestTab("tab-1", "", "alpha")
	second := newTabGroupTestTab("tab-2", "", "beta")
	state := modelState{
		width: 100, height: 30, tabs: []*workspaceTab{first, second}, activeTabID: first.id, workspaceVisible: true,
	}
	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "n")
	state = updateTabGroupKey(t, state, "开发")
	state = updateTabGroupKey(t, state, "enter")
	if first.groupName != "开发" || state.activeTabID != first.id {
		t.Fatalf("creating a group did not move the active tab: group=%q active=%q", first.groupName, state.activeTabID)
	}
	if len(state.tabs) != 2 || state.tabs[0].id != second.id || state.tabs[1].id != first.id {
		t.Fatalf("tabs were not ordered into group blocks after creation: %q, %q", state.tabs[0].id, state.tabs[1].id)
	}

	state.activeTabID = second.id
	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "down") // select the named group after Unassigned.
	state = updateTabGroupKey(t, state, "m")
	if second.groupName != "开发" || len(state.tabs) != 2 || state.tabs[0].groupName != "开发" || state.tabs[1].groupName != "开发" {
		t.Fatalf("moving the active tab did not join the group: %#v", state.tabs)
	}

	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "r")
	for range []rune("开发") {
		state = updateTabGroupKey(t, state, "backspace")
	}
	state = updateTabGroupKey(t, state, "平台")
	state = updateTabGroupKey(t, state, "enter")
	if first.groupName != "平台" || second.groupName != "平台" {
		t.Fatalf("renaming a group did not update all members: %q, %q", first.groupName, second.groupName)
	}

	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "down")
	state = updateTabGroupKey(t, state, "d")
	if first.groupName != "" || second.groupName != "" || len(state.tabs) != 2 {
		t.Fatalf("dissolving a group should ungroup, not close, its tabs: %#v", state.tabs)
	}
}

func TestWorkspaceTabGroupSwitchAndNewTabInheritance(t *testing.T) {
	first := newTabGroupTestTab("tab-1", "开发", "alpha")
	second := newTabGroupTestTab("tab-2", "运维", "beta")
	state := modelState{width: 100, height: 30, tabs: []*workspaceTab{first, second}, activeTabID: second.id, workspaceVisible: true}
	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "up")
	state = updateTabGroupKey(t, state, "enter")
	if state.activeTabID != first.id {
		t.Fatalf("switching to the selected group activated %q, want %q", state.activeTabID, first.id)
	}
	if target := state.newTabTarget(); target.groupName != "开发" {
		t.Fatalf("new tab target group = %q, want the active tab's group", target.groupName)
	}

	view := state.workspaceTabsView(100)
	if !strings.Contains(view, "开发") || !strings.Contains(view, "运维") {
		t.Fatalf("tab bar did not identify group boundaries:\n%s", view)
	}
}

func TestSFTPWorkspaceTabKeepsIndependentStateWhenSwitching(t *testing.T) {
	terminalTab := newTabGroupTestTab("terminal", "", "dev")
	sftpState := &sftpBrowserState{standalone: true, localPath: "/tmp", path: "/remote", selected: 4}
	sftpTab := &workspaceTab{id: "sftp", sftp: sftpState}
	state := modelState{tabs: []*workspaceTab{terminalTab, sftpTab}, activeTabID: terminalTab.id}

	if !state.activateWorkspaceTab(sftpTab.id) || state.activeSFTPState() != sftpState {
		t.Fatal("activating the SFTP tab did not select its own browser state")
	}
	if !state.activateWorkspaceTab(terminalTab.id) || state.sftp != nil {
		t.Fatal("activating a terminal tab left the SFTP browser bound as active")
	}
	if !state.activateWorkspaceTab(sftpTab.id) || state.activeSFTPState().selected != 4 || state.activeSFTPState().localPath != "/tmp" {
		t.Fatal("switching back to the SFTP tab did not preserve its pane state")
	}
}

func TestWorkspaceTabGroupNamesRejectInvalidAndDuplicateValues(t *testing.T) {
	for _, invalid := range []string{"", "  ", workspaceTabGroupUnassigned, "prod\nblue", strings.Repeat("字", workspaceTabGroupMaxRunes+1)} {
		if _, err := normalizeWorkspaceTabGroupName(invalid); err == nil {
			t.Errorf("normalizeWorkspaceTabGroupName(%q) succeeded", invalid)
		}
	}
	first := newTabGroupTestTab("tab-1", "Dev", "alpha")
	second := newTabGroupTestTab("tab-2", "", "beta")
	state := modelState{width: 100, height: 30, tabs: []*workspaceTab{first, second}, activeTabID: second.id}
	state.openWorkspaceTabGroupManager()
	state = updateTabGroupKey(t, state, "n")
	state = updateTabGroupKey(t, state, "dEV")
	state = updateTabGroupKey(t, state, "enter")
	if state.tabGroupManager == nil || state.tabGroupManager.error == "" || second.groupName != "" {
		t.Fatal("group creation accepted a duplicate name ignoring case")
	}
}

func TestWorkspaceTabGroupEditorHandlesPasteWithoutForwardingToTerminal(t *testing.T) {
	state := modelState{
		width: 100, height: 30,
		tabs:            []*workspaceTab{newTabGroupTestTab("tab-1", "", "alpha")},
		activeTabID:     "tab-1",
		tabGroupManager: &tabGroupManagerState{editing: true, mode: tabGroupCreateMode},
	}
	updated, _ := state.Update(tea.PasteMsg{Content: "开发组"})
	state = unwrapModel(t, updated)
	if state.tabGroupManager == nil || state.tabGroupManager.name != "开发组" {
		t.Fatalf("pasted text was not kept in the group editor: %#v", state.tabGroupManager)
	}
}

func updateTabGroupKey(t *testing.T, state modelState, value string) modelState {
	t.Helper()
	key := keyPress(value)
	switch value {
	case "enter":
		key = tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "up":
		key = tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
	case "down":
		key = tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
	case "escape":
		key = tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "backspace":
		key = tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	}
	updated, _ := state.updateWorkspaceTabGroupManagerKey(key)
	return unwrapModel(t, updated)
}
