package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestHostEditAndDeleteFlow(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "before", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	state := modelState{catalog: store, hosts: []model.Host{host}, activePane: hostPane}
	state = updateModelWithKey(t, state, "e")
	if state.form == nil || !state.form.editing || state.form.id != host.Id {
		t.Fatalf("pressing e did not open host edit form: %#v", state.form)
	}
	state.form.name = "after"
	updated, command := state.submitForm()
	if command == nil {
		t.Fatal("editing host did not schedule a save")
	}
	state = updated.(modelState)
	updated, _ = state.Update(command())
	state = updated.(modelState)
	if state.form != nil || len(state.hosts) != 1 || state.hosts[0].Name != "after" {
		t.Fatalf("host edit was not applied to the UI model: form=%#v hosts=%#v", state.form, state.hosts)
	}

	state = updateModelWithKey(t, state, "d")
	if state.deleteConfirm == nil || state.deleteConfirm.id != host.Id {
		t.Fatal("pressing d did not ask for deletion confirmation")
	}
	updated, command = state.updateKey(keyPress("y"))
	state = unwrapModel(t, updated)
	if state.deleteConfirm == nil || !state.deleteConfirm.deleting {
		t.Fatal("confirming deletion did not start the delete operation")
	}
	if _, duplicate := state.updateCatalogDeleteConfirm("enter"); duplicate != nil {
		t.Fatal("repeated confirmation scheduled a duplicate delete")
	}
	// Resolve the asynchronous command through the model's normal message path.
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.hosts) != 0 || state.deleteConfirm != nil {
		t.Fatalf("host deletion did not update UI state: %#v", state)
	}
	if _, err := store.GetHost(ctx, host.Id); err == nil {
		t.Fatal("host still exists in SQLite after confirmed deletion")
	}
}

func TestSerialHostFormSavesParametersAndConnectErrorsAreLabeled(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "serial-ui.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	form := newHostForm("")
	form.hostKind = model.HostKindSerial
	form.name = "router-console"
	form.address = "/dev/ttyUSB0"
	form.serialBaudRate = "115200"
	form.serialDataBits = "7"
	form.serialParity = model.SerialParityEven
	form.serialStopBits = model.SerialStopBitsTwo
	state := modelState{catalog: store, form: form}
	updated, command := state.submitForm()
	if command == nil {
		t.Fatalf("serial form was rejected: %s", form.errorText)
	}
	state = updated.(modelState)
	updated, _ = state.Update(command())
	state = updated.(modelState)
	if len(state.hosts) != 1 || state.hosts[0].Kind != model.HostKindSerial || state.hosts[0].SerialBaudRate != 115200 ||
		state.hosts[0].SerialDataBits != 7 || state.hosts[0].SerialParity != model.SerialParityEven || state.hosts[0].SerialStopBits != model.SerialStopBitsTwo {
		t.Fatalf("saved serial host state = %#v", state.hosts)
	}

	badDevice := state.hosts[0]
	badDevice.Id = "missing-device"
	badDevice.IP = "/dev/codex-serial-device-does-not-exist"
	state = modelState{}
	command = state.startHostConnect(badDevice, connectionTarget{kind: newTabTarget})
	updated, _ = state.Update(command())
	state = updated.(modelState)
	if !strings.HasPrefix(state.statusText, "串口连接失败：") {
		t.Fatalf("serial connection error status = %q", state.statusText)
	}
}

func TestGroupEditAndDeleteFlow(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	group, err := store.CreateGroup(ctx, model.Group{Name: "Alpha"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	parent, err := store.CreateGroup(ctx, model.Group{Name: "Target"})
	if err != nil {
		t.Fatalf("create parent group: %v", err)
	}

	state := modelState{catalog: store, groups: []model.Group{group, parent}, activePane: groupPane}
	state.groupIndex = state.groupIndexForID(group.Id)
	state = updateModelWithKey(t, state, "e")
	if state.form == nil || !state.form.editing || state.form.id != group.Id {
		t.Fatalf("pressing e did not open group edit form: %#v", state.form)
	}
	state.form.name = "Renamed"
	state.form.groupID = parent.Id
	updated, command := state.submitForm()
	if command == nil {
		t.Fatal("editing group did not schedule a save")
	}
	state = unwrapModel(t, updated)
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	gotGroup := state.groupByID(group.Id)
	if state.form != nil || gotGroup == nil || gotGroup.Name != "Renamed" || gotGroup.ParentId != parent.Id {
		t.Fatalf("group edit was not applied: form=%#v group=%#v", state.form, gotGroup)
	}

	state = updateModelWithKey(t, state, "d")
	if state.deleteConfirm == nil || state.deleteConfirm.id != group.Id {
		t.Fatal("pressing d did not ask for group deletion confirmation")
	}
	updated, command = state.updateKey(keyPress("y"))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirmed group deletion did not schedule storage operation")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.groupByID(group.Id) != nil || len(state.groups) != 1 {
		t.Fatalf("group deletion did not update UI state: %#v", state.groups)
	}
	if _, err := store.GetGroup(ctx, group.Id); err == nil {
		t.Fatal("group still exists in SQLite after confirmed deletion")
	}
}

func TestHostEditCanSaveProxyJumpAndRejectCycles(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jump, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "gateway", IP: "127.0.0.1", Port: 22, User: "jump-user",
	})
	if err != nil {
		t.Fatalf("create jump host: %v", err)
	}
	target, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "application", IP: "10.0.0.20", Port: 22, User: "app-user",
	})
	if err != nil {
		t.Fatalf("create target host: %v", err)
	}

	state := modelState{
		catalog: store,
		hosts:   []model.Host{jump, target},
		form:    newHostEditForm(target),
	}
	state.form.proxyJumpID = jump.Id
	state.form.identityFile = "~/.ssh/application_key"
	state.form.startup = "cd /srv/application"
	if !state.form.isChoiceFieldAt(6) || state.form.groupFieldIndex() != 7 {
		t.Fatalf("SSH form fields do not expose jump selection before group: %#v", state.form)
	}
	updated, command := state.submitForm()
	if command == nil {
		t.Fatalf("submitting host form produced no command: %#v", updated)
	}
	message, ok := command().(hostUpdatedMsg)
	if !ok || message.err != nil {
		t.Fatalf("save host with jump host: %#v", message)
	}
	saved, err := store.GetHost(ctx, target.Id)
	if err != nil {
		t.Fatalf("read saved target: %v", err)
	}
	if len(saved.ProxyJumpList) != 1 || saved.ProxyJumpList[0] != jump.Id || saved.IdentityFile != "~/.ssh/application_key" || saved.Startup != "cd /srv/application" {
		t.Fatalf("saved jump hosts, identity file, and startup command = %v, %q, %q", saved.ProxyJumpList, saved.IdentityFile, saved.Startup)
	}

	state.hosts[0].ProxyJumpList = []string{target.Id}
	state.form = newHostEditForm(target)
	if !state.proxyJumpCreatesCycle(target.Id, jump.Id) {
		t.Fatal("jump host whose chain returns to target was not recognized as a cycle")
	}
	if got := state.cycleProxyJumpID("", 1); got != "" {
		t.Fatalf("cycle-producing jump host remained selectable: %q", got)
	}
}

func updateModelWithKey(t *testing.T, state modelState, value string) modelState {
	t.Helper()
	updated, _ := state.updateKey(keyPress(value))
	return unwrapModel(t, updated)
}

func unwrapModel(t *testing.T, value tea.Model) modelState {
	t.Helper()
	switch result := value.(type) {
	case modelState:
		return result
	case *modelState:
		return *result
	default:
		t.Fatalf("Update returned unexpected model type %T", value)
		return modelState{}
	}
}

func keyPress(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: value, Code: []rune(value)[0]})
}
