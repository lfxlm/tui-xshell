package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestWorkspaceLayoutSnapshotRoundTripAndValidation(t *testing.T) {
	host1 := model.Host{Id: "host-1", Name: "dev", Kind: model.HostKindSSH}
	host2 := model.Host{Id: "host-2", Name: "local", Kind: model.HostKindLocal}
	host3 := model.Host{Id: "host-3", Name: "staging", Kind: model.HostKindSSH}
	leaf := func(id string, host model.Host) *paneNode {
		return &paneNode{pane: &terminalPane{id: id, host: host, terminal: vt.NewSafeEmulator(80, 20)}}
	}
	state := modelState{
		activeTabID: "tab-2",
		tabs: []*workspaceTab{
			{
				id: "tab-1", groupName: "开发", root: &paneNode{
					axis: splitVertical, ratio: 60,
					first: leaf("pane-1", host1),
					second: &paneNode{
						axis: splitHorizontal, ratio: 40,
						first: leaf("pane-2", host2), second: leaf("pane-3", host3),
					},
				}, activePaneID: "pane-3", syncInput: true,
			},
			{id: "tab-2", groupName: "运维", root: leaf("pane-4", host2), activePaneID: "pane-4"},
		},
	}

	data, err := encodeWorkspaceLayout(state)
	if err != nil {
		t.Fatalf("encode layout: %v", err)
	}
	snapshot, err := decodeWorkspaceLayout(data)
	if err != nil {
		t.Fatalf("decode layout: %v", err)
	}
	if snapshot.Version != workspaceLayoutVersion || snapshot.ActiveTab != 1 || len(snapshot.Tabs) != 2 {
		t.Fatalf("active tab / tabs = %d / %d", snapshot.ActiveTab, len(snapshot.Tabs))
	}
	first := snapshot.Tabs[0]
	if first.GroupName != "开发" || first.ActivePane != 2 || !first.Synchronized || first.Root.Axis != "vertical" || first.Root.Ratio != 60 || first.Root.Second.Axis != "horizontal" {
		t.Fatalf("split, focus or sync state was not preserved: %#v", first)
	}
	if snapshot.Tabs[1].GroupName != "运维" {
		t.Fatalf("second tab group was not preserved: %#v", snapshot.Tabs[1])
	}
	if strings.Contains(data, "password") || strings.Contains(data, "terminal output") {
		t.Fatalf("layout unexpectedly contains credentials or terminal output: %s", data)
	}

	invalid := []string{
		`{"version":3,"active_tab":0,"tabs":[{"active_pane":0,"root":{"host_id":"host-1"}}]}`,
		`{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"axis":"vertical","ratio":100,"first":{"host_id":"host-1"},"second":{"host_id":"host-2"}}}]}`,
		`{"version":1,"active_tab":1,"tabs":[{"active_pane":0,"root":{"host_id":"host-1"}}]}`,
	}
	for _, candidate := range invalid {
		if _, err := decodeWorkspaceLayout(candidate); err == nil {
			t.Errorf("decodeWorkspaceLayout(%s) succeeded; want validation error", candidate)
		}
	}
}

func TestWorkspaceLayoutEncodingSkipsSFTPTabs(t *testing.T) {
	first := newTabGroupTestTab("first", "", "dev")
	second := newTabGroupTestTab("second", "ops", "prod")
	state := modelState{
		activeTabID: "second",
		tabs: []*workspaceTab{
			{id: "sftp", sftp: &sftpBrowserState{standalone: true}},
			first,
			second,
		},
	}
	data, err := encodeWorkspaceLayout(state)
	if err != nil {
		t.Fatalf("encode layout with an open SFTP tab: %v", err)
	}
	snapshot, err := decodeWorkspaceLayout(data)
	if err != nil {
		t.Fatalf("decode layout with an open SFTP tab: %v", err)
	}
	if len(snapshot.Tabs) != 2 || snapshot.ActiveTab != 1 {
		t.Fatalf("saved terminal tabs=%d active=%d; want 2 terminal tabs and active index 1", len(snapshot.Tabs), snapshot.ActiveTab)
	}
}

func TestWorkspaceLayoutVersionOneRemainsReadableAndGroupNamesAreValidated(t *testing.T) {
	legacy := `{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"host_id":"host-1"}}]}`
	snapshot, err := decodeWorkspaceLayout(legacy)
	if err != nil || snapshot.Tabs[0].GroupName != "" {
		t.Fatalf("decode legacy v1 layout = %#v, %v; want no tab group", snapshot, err)
	}
	invalidGroup := `{"version":2,"active_tab":0,"tabs":[{"active_pane":0,"group":"\nprod","root":{"host_id":"host-1"}}]}`
	if _, err := decodeWorkspaceLayout(invalidGroup); err == nil {
		t.Fatal("layout accepted a tab group name containing a control character")
	}
}

func TestWorkspaceLayoutRestoreQueuesConnectionsAndReportsFailures(t *testing.T) {
	host1 := model.Host{Id: "local-1", Name: "Local A", Kind: model.HostKindLocal}
	host2 := model.Host{Id: "local-2", Name: "Local B", Kind: model.HostKindLocal}
	data := `{"version":2,"active_tab":0,"tabs":[{"active_pane":1,"group":"开发","synchronized_input":true,"root":{"axis":"vertical","ratio":50,"first":{"host_id":"local-1","host_name":"Local A"},"second":{"host_id":"local-2","host_name":"Local B"}}}]}`
	state := modelState{
		width: 100, height: 40,
		hosts:            []model.Host{host1, host2},
		workspaceLayouts: &workspaceLayoutManagerState{},
	}
	command := state.restoreWorkspaceLayout(model.WorkspaceLayout{Name: "开发环境", Data: data})
	if command == nil || state.layoutRestore == nil || state.connectionTarget == nil {
		t.Fatal("restore did not start the first queued connection")
	}
	if len(state.tabs) != 1 || state.tabs[0].groupName != "开发" {
		t.Fatalf("restore did not retain the tab group: %#v", state.tabs)
	}
	if len(state.tabs) != 1 || state.layoutRestore.current == nil || state.layoutRestore.current.hostID != host1.Id || len(state.layoutRestore.remaining) != 1 {
		t.Fatalf("restore queue state = %#v; tabs=%d", state.layoutRestore, len(state.tabs))
	}
	if state.tabs[0].activePaneID != findPaneByHostID(state.tabs[0].root, host2.Id).id || !state.tabs[0].syncInput {
		t.Fatal("restore did not preserve active pane and synchronized input")
	}

	firstTarget := *state.connectionTarget
	updated, command := state.Update(terminalConnectedMsg{host: host1, session: &workspaceTestSession{}, target: firstTarget})
	state = unwrapModel(t, updated)
	if command == nil || state.connectionTarget == nil || state.connectionTarget.layoutRestoreID == 0 || state.layoutRestore.current == nil || state.layoutRestore.current.hostID != host2.Id {
		t.Fatal("successful first connection did not advance to the next pane")
	}
	if findPaneByHostID(state.tabs[0].root, host1.Id).session == nil {
		t.Fatal("first restored connection was not attached to its pane")
	}
	secondTarget := *state.connectionTarget
	updated, _ = state.Update(terminalConnectedMsg{host: host2, target: secondTarget, err: errors.New("connection refused")})
	state = unwrapModel(t, updated)
	if state.layoutRestore != nil || !state.statusErr || !strings.Contains(state.statusText, "1/2") {
		t.Fatalf("restore completion status = %q, err=%v, state=%#v", state.statusText, state.statusErr, state.layoutRestore)
	}
}

func TestWorkspaceLayoutRestoreRejectsMissingHostsBeforeMutation(t *testing.T) {
	existing := model.Host{Id: "existing", Name: "Existing", Kind: model.HostKindLocal}
	root := &paneNode{pane: &terminalPane{id: "old-pane", host: existing}}
	state := modelState{
		width: 100, height: 40, hosts: []model.Host{existing},
		activeTabID: "old-tab", tabs: []*workspaceTab{{id: "old-tab", root: root, activePaneID: "old-pane"}},
		workspaceLayouts: &workspaceLayoutManagerState{},
	}
	data := `{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"host_id":"deleted-host","host_name":"Deleted"}}]}`
	command := state.restoreWorkspaceLayout(model.WorkspaceLayout{Name: "broken", Data: data})
	if command != nil || len(state.tabs) != 1 || state.layoutRestore != nil || !strings.Contains(state.workspaceLayouts.errorText, "已不在本地主机目录") {
		t.Fatalf("missing host was not rejected atomically: tabs=%d restore=%#v error=%q", len(state.tabs), state.layoutRestore, state.workspaceLayouts.errorText)
	}
}

func TestCancelWorkspaceLayoutRestoreStopsRemainingConnections(t *testing.T) {
	host1 := model.Host{Id: "local-1", Name: "Local A", Kind: model.HostKindLocal}
	host2 := model.Host{Id: "local-2", Name: "Local B", Kind: model.HostKindLocal}
	data := `{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"axis":"vertical","ratio":50,"first":{"host_id":"local-1"},"second":{"host_id":"local-2"}}}]}`
	state := modelState{width: 100, height: 40, hosts: []model.Host{host1, host2}, workspaceLayouts: &workspaceLayoutManagerState{}}
	if command := state.restoreWorkspaceLayout(model.WorkspaceLayout{Name: "cancel-test", Data: data}); command == nil || state.layoutRestore == nil {
		t.Fatal("restore did not start")
	}
	state.cancelSSHConnect()
	if state.layoutRestore != nil || state.connectionTarget != nil || state.connectingHost != nil {
		t.Fatal("cancel did not clear restore and connection state")
	}
	if pane := findPaneByHostID(state.tabs[0].root, host2.Id); pane == nil || !pane.statusErr || pane.statusText != "布局恢复已取消" {
		t.Fatalf("pending pane was not marked canceled: %#v", pane)
	}
}

func TestWorkspaceLayoutManagerSaveAndRestoreKeyFlow(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "layouts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "本地终端"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	pane := &terminalPane{id: "pane-1", host: host, terminal: vt.NewSafeEmulator(80, 20)}
	state := modelState{
		catalog: store, width: 100, height: 40, activePane: hostPane,
		activeTabID: "tab-1", workspaceVisible: false,
		tabs:  []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
		hosts: []model.Host{host},
	}
	updated, command := state.updateKey(keyPress("l"))
	state = unwrapModel(t, updated)
	if state.workspaceLayouts == nil || command == nil {
		t.Fatal("l did not open the saved layout manager")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	state.workspaceLayouts.name = "本地开发"
	updated, command = state.updateWorkspaceLayoutManagerKey(keyPress("s"))
	state = unwrapModel(t, updated)
	if !state.workspaceLayouts.editingName {
		t.Fatal("s did not enter layout name input")
	}
	state.workspaceLayouts.name = "本地开发"
	updated, command = state.updateWorkspaceLayoutManagerKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil || !state.workspaceLayouts.busy {
		t.Fatal("Enter did not save the named layout")
	}
	updated, refresh := state.Update(command())
	state = unwrapModel(t, updated)
	if refresh == nil {
		t.Fatal("successful save did not refresh the saved layouts")
	}
	updated, _ = state.Update(refresh())
	state = unwrapModel(t, updated)
	if len(state.workspaceLayouts.layouts) != 1 || state.workspaceLayouts.layouts[0].Name != "本地开发" {
		t.Fatalf("saved layout list = %#v", state.workspaceLayouts.layouts)
	}

	updated, command = state.updateWorkspaceLayoutManagerKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if state.workspaceLayouts != nil || state.layoutRestore == nil || len(state.tabs) != 2 || command == nil {
		t.Fatalf("Enter did not restore the selected layout into a new tab: manager=%#v restore=%#v tabs=%d command=%v status=%q dimensions=%dx%d pane=%#v", state.workspaceLayouts, state.layoutRestore, len(state.tabs), command != nil, state.statusText, state.width, state.height, findPaneByHostID(state.tabs[len(state.tabs)-1].root, host.Id))
	}
}

func TestLastWorkspaceSavesOnExitAndRestoresOnDemand(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "last-session.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "本地终端"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	pane := &terminalPane{
		id: "pane-last-session", host: host, session: &workspaceTestSession{},
		terminal: vt.NewSafeEmulator(80, 20),
	}
	pane.host.CredentialRef = "private-credential-reference"
	_, _ = pane.terminal.Write([]byte("private terminal output"))
	state := modelState{
		catalog: store, width: 100, height: 40, activeTabID: "tab-last-session",
		tabs: []*workspaceTab{{id: "tab-last-session", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	if err := state.Close(); err != nil {
		t.Fatalf("save workspace during close: %v", err)
	}
	data, err := store.LastWorkspaceData(ctx)
	if err != nil || data == "" {
		t.Fatalf("saved last workspace = %q, %v", data, err)
	}
	if strings.Contains(data, "private-credential-reference") || strings.Contains(data, "private terminal output") {
		t.Fatalf("last workspace snapshot leaked credentials or terminal output: %s", data)
	}

	home := modelState{
		catalog: store, hosts: []model.Host{host}, lastWorkspaceData: data,
		width: 100, height: 40,
	}
	updated, command := home.Update(keyPress("R"))
	home = unwrapModel(t, updated)
	if command == nil || !home.workspaceVisible || home.layoutRestore == nil || len(home.tabs) != 1 {
		t.Fatalf("R did not start restoring last workspace: command=%v workspace=%v restore=%#v tabs=%d status=%q", command != nil, home.workspaceVisible, home.layoutRestore, len(home.tabs), home.statusText)
	}
	if home.connectionTarget == nil || home.connectionTarget.layoutRestoreID == 0 {
		t.Fatal("restore did not start reconnecting the saved host")
	}
	if err := home.Close(); err != nil {
		t.Fatalf("close restored placeholder session: %v", err)
	}
}

func TestCatalogStartupRestoresLastWorkspaceBeforeDefaultSession(t *testing.T) {
	savedHost := model.Host{Id: "saved-local", Name: "Saved local", Kind: model.HostKindLocal}
	defaultHost := model.Host{Id: "default-ssh", Name: "Default SSH", Kind: model.HostKindSSH}
	data, err := encodeWorkspaceLayout(modelState{
		activeTabID: "saved-tab",
		tabs: []*workspaceTab{{
			id: "saved-tab", root: &paneNode{pane: &terminalPane{id: "saved-pane", host: savedHost}}, activePaneID: "saved-pane",
		}},
	})
	if err != nil {
		t.Fatalf("encode saved workspace: %v", err)
	}
	config := model.DefaultConfig()
	config.Workspace.RestoreLastOnStartup = true
	state := modelState{config: config, width: 100, height: 40}
	updated, command := state.Update(catalogLoadedMsg{
		hosts: []model.Host{savedHost, defaultHost}, defaultHostID: defaultHost.Id,
		lastWorkspaceData: data,
	})
	state = unwrapModel(t, updated)
	if command == nil || !state.workspaceVisible || state.layoutRestore == nil || len(state.tabs) != 1 {
		t.Fatalf("startup did not begin restoring the last workspace: command=%v visible=%v restore=%#v tabs=%d status=%q", command != nil, state.workspaceVisible, state.layoutRestore, len(state.tabs), state.statusText)
	}
	if state.layoutRestore.current == nil || state.layoutRestore.current.hostID != savedHost.Id {
		restoredHostID := ""
		if state.layoutRestore.current != nil {
			restoredHostID = state.layoutRestore.current.hostID
		}
		t.Fatalf("startup restored %q, want saved workspace host %q (default session %q)", restoredHostID, savedHost.Id, defaultHost.Id)
	}
}

func TestCatalogStartupFallsBackToDefaultSessionWhenNoWorkspaceIsSaved(t *testing.T) {
	defaultHost := model.Host{Id: "default-ssh", Name: "Default SSH", Kind: model.HostKindSSH}
	config := model.DefaultConfig()
	config.Workspace.RestoreLastOnStartup = true
	state := modelState{config: config, width: 100, height: 40}
	updated, command := state.Update(catalogLoadedMsg{
		hosts: []model.Host{defaultHost}, defaultHostID: defaultHost.Id,
	})
	state = unwrapModel(t, updated)
	if command == nil || state.connectingHost == nil || state.connectingHost.Id != defaultHost.Id {
		t.Fatalf("startup did not fall back to default session: command=%v connecting=%#v status=%q", command != nil, state.connectingHost, state.statusText)
	}
	if state.layoutRestore != nil || state.workspaceVisible {
		t.Fatalf("empty last workspace unexpectedly started a restore: restore=%#v visible=%v", state.layoutRestore, state.workspaceVisible)
	}
}

func TestLastWorkspaceRestoreRejectsStaleHostsAndExistingTabs(t *testing.T) {
	staleData := `{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"host_id":"deleted-host"}}]}`
	state := modelState{lastWorkspaceData: staleData, width: 100, height: 40}
	updated, command := state.Update(keyPress("R"))
	state = unwrapModel(t, updated)
	if command != nil || len(state.tabs) != 0 || state.layoutRestore != nil || !state.statusErr {
		t.Fatalf("stale host restore result: command=%v tabs=%d restore=%#v error=%v status=%q", command != nil, len(state.tabs), state.layoutRestore, state.statusErr, state.statusText)
	}

	state = modelState{lastWorkspaceData: staleData, tabs: []*workspaceTab{{id: "current"}}}
	updated, command = state.Update(keyPress("R"))
	state = unwrapModel(t, updated)
	if command != nil || len(state.tabs) != 1 || !state.statusErr || !strings.Contains(state.statusText, "关闭当前会话") {
		t.Fatalf("existing session was not protected: command=%v tabs=%d error=%v status=%q", command != nil, len(state.tabs), state.statusErr, state.statusText)
	}
}

func findPaneByHostID(root *paneNode, hostID string) *terminalPane {
	for _, pane := range collectPanes(root) {
		if pane.host.Id == hostID {
			return pane
		}
	}
	return nil
}

var _ tea.Model = modelState{}
