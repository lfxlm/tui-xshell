package ui

import (
	"context"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestNamedShortcutBarHomeScreenFlow(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "alpha"})
	if err != nil {
		t.Fatalf("create first host: %v", err)
	}
	second, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "beta"})
	if err != nil {
		t.Fatalf("create second host: %v", err)
	}
	state := modelState{
		catalog: store, hosts: []model.Host{first, second}, activePane: groupPane,
		width: 100, height: 30,
	}

	state = updateModelWithKey(t, state, "b")
	if state.form == nil || state.form.kind != shortcutBarForm {
		t.Fatal("b did not open the named connection bar form")
	}
	state.form.name = "Production"
	updated, command := state.submitForm()
	if command == nil {
		t.Fatal("creating a named connection bar did not schedule a save")
	}
	state = unwrapModel(t, updated)
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.form != nil || len(state.shortcutBars) != 1 || state.selectedGroup().name != "Production" {
		t.Fatalf("created connection bar was not selected: form=%#v bars=%#v group=%#v", state.form, state.shortcutBars, state.selectedGroup())
	}

	state = updateModelWithKey(t, state, "e")
	if state.form == nil || state.form.kind != shortcutBarForm || !state.form.editing {
		t.Fatal("e did not open the connection bar rename form")
	}
	state.form.name = "Production EU"
	updated, command = state.submitForm()
	state = unwrapModel(t, updated)
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	barID := state.shortcutBars[0].Id
	if state.shortcutBars[0].Name != "Production EU" {
		t.Fatalf("renamed connection bar = %q", state.shortcutBars[0].Name)
	}

	state.activePane = hostPane
	state.groupIndex, state.hostIndex = 0, 0
	state = updateModelWithKey(t, state, "m")
	if state.shortcutBarPicker == nil || state.shortcutBarPicker.hostID != first.Id {
		t.Fatal("m did not open a picker for the selected host")
	}
	updated, command = state.updateKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("selecting a connection bar did not schedule a membership save")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.shortcutBarPicker != nil || len(state.shortcutBars[0].HostIDs) != 1 || state.shortcutBars[0].HostIDs[0] != first.Id {
		t.Fatalf("first host was not added to the bar: picker=%#v bars=%#v", state.shortcutBarPicker, state.shortcutBars)
	}

	state.groupIndex, state.hostIndex = 0, 1
	state = updateModelWithKey(t, state, "m")
	updated, command = state.updateKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("adding a second host did not schedule a membership save")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if got := state.shortcutBars[0].HostIDs; len(got) != 2 || got[0] != first.Id || got[1] != second.Id {
		t.Fatalf("connection bar members = %#v, want alpha then beta", got)
	}

	state.groupIndex = state.groupIndexForID(barID)
	state.hostIndex = 0
	state.ensureHostSelection()
	updated, command = state.updateKey(keyPress("1"))
	state = unwrapModel(t, updated)
	if command == nil || state.connectingHost == nil || state.connectingHost.Id != first.Id {
		t.Fatalf("1 did not connect the first bar member: host=%#v", state.connectingHost)
	}
	state.connectingHost, state.connectionTarget = nil, nil

	state.hostIndex = 1
	updated, command = state.updateKey(keyPress("["))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("[ did not schedule moving the selected bar member")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if got := state.shortcutBars[0].HostIDs; got[0] != second.Id || got[1] != first.Id {
		t.Fatalf("bar member order = %#v, want beta then alpha", got)
	}
	if selected := state.selectedHost(); selected == nil || selected.Id != second.Id {
		t.Fatalf("reordering changed selected host: %#v", selected)
	}

	updated, command = state.updateKey(keyPress("m"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("m in a connection bar did not schedule member removal")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if got := state.shortcutBars[0].HostIDs; len(got) != 1 || got[0] != first.Id {
		t.Fatalf("removing selected host left members %#v, want only alpha", got)
	}
	if _, err := store.GetHost(ctx, second.Id); err != nil {
		t.Fatalf("removing a host from a bar deleted the host: %v", err)
	}

	state.activePane = groupPane
	state.groupIndex = state.groupIndexForID(barID)
	state = updateModelWithKey(t, state, "d")
	if state.deleteConfirm == nil || state.deleteConfirm.kind != deleteShortcutBar {
		t.Fatal("d did not ask to delete the selected connection bar")
	}
	updated, command = state.updateKey(keyPress("y"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirming connection bar deletion did not schedule storage deletion")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.shortcutBars) != 0 {
		t.Fatalf("deleted connection bar remains in UI state: %#v", state.shortcutBars)
	}
	if _, err := store.GetHost(ctx, first.Id); err != nil {
		t.Fatalf("deleting a connection bar deleted its host: %v", err)
	}
}

func TestDeletingHostRemovesItFromNamedShortcutBar(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "ephemeral"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	bar, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: "Temporary"})
	if err != nil {
		t.Fatalf("create connection bar: %v", err)
	}
	if err := store.SetShortcutBarHosts(ctx, bar.Id, []string{host.Id}); err != nil {
		t.Fatalf("add host to connection bar: %v", err)
	}
	bar.HostIDs = []string{host.Id}
	state := modelState{
		catalog: store, hosts: []model.Host{host}, shortcutBars: []model.ShortcutBar{bar},
		activePane: hostPane, width: 100, height: 30,
	}

	state = updateModelWithKey(t, state, "d")
	updated, command := state.updateKey(keyPress("y"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirming host deletion did not schedule it")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.hosts) != 0 || len(state.shortcutBars[0].HostIDs) != 0 {
		t.Fatalf("host deletion left stale state: hosts=%#v bars=%#v", state.hosts, state.shortcutBars)
	}
	loaded, err := store.ListShortcutBars(ctx)
	if err != nil {
		t.Fatalf("reload connection bars: %v", err)
	}
	if len(loaded) != 1 || len(loaded[0].HostIDs) != 0 {
		t.Fatalf("database retained deleted host membership: %#v", loaded)
	}
}
