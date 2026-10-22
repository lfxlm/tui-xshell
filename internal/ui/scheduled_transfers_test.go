package ui

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

type scheduledTransferCatalogStub struct {
	Catalog
	transfers []model.ScheduledTransfer
	nextID    int
}

func (catalog *scheduledTransferCatalogStub) ListScheduledTransfers(context.Context) ([]model.ScheduledTransfer, error) {
	return append([]model.ScheduledTransfer(nil), catalog.transfers...), nil
}

func (catalog *scheduledTransferCatalogStub) SaveScheduledTransfer(_ context.Context, transfer model.ScheduledTransfer) (model.ScheduledTransfer, error) {
	if transfer.Id == "" {
		catalog.nextID++
		transfer.Id = "scheduled-" + strconv.Itoa(catalog.nextID)
	}
	if index := scheduledTransferIndex(catalog.transfers, transfer.Id); index >= 0 {
		catalog.transfers[index] = transfer
	} else {
		catalog.transfers = append(catalog.transfers, transfer)
	}
	return transfer, nil
}

func (catalog *scheduledTransferCatalogStub) DeleteScheduledTransfer(_ context.Context, id string) error {
	if index := scheduledTransferIndex(catalog.transfers, id); index >= 0 {
		catalog.transfers = append(catalog.transfers[:index], catalog.transfers[index+1:]...)
	}
	return nil
}

func (catalog *scheduledTransferCatalogStub) RequestScheduledTransferNow(_ context.Context, id string) error {
	if index := scheduledTransferIndex(catalog.transfers, id); index >= 0 {
		catalog.transfers[index].RunRequested = true
		return nil
	}
	return nil
}

func TestScheduledTransferManagerCreatesTogglesAndDeletesPlan(t *testing.T) {
	catalog := &scheduledTransferCatalogStub{}
	host := model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "build", IP: "192.0.2.10", Port: 22, User: "tester"}
	state := modelState{
		catalog: catalog,
		hosts:   []model.Host{host},
		width:   120,
		height:  40,
		scheduledTransfers: &scheduledTransferManagerState{
			hosts: []model.Host{host},
		},
	}
	state.beginScheduledTransferEdit(nil)
	typeScheduledText(t, &state, "nightly archive")
	advanceScheduledField(t, &state) // host
	advanceScheduledField(t, &state) // direction
	advanceScheduledField(t, &state) // local path
	typeScheduledText(t, &state, "/tmp/archive.tar")
	advanceScheduledField(t, &state) // remote path
	typeScheduledText(t, &state, "/incoming/archive.tar")
	advanceScheduledField(t, &state) // cron
	advanceScheduledField(t, &state) // timezone
	typeScheduledText(t, &state, "Asia/Shanghai")

	updated, saveCmd := state.updateScheduledTransferEditorKey(tea.Key{Code: 's', Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if saveCmd == nil || !state.scheduledTransfers.busy {
		t.Fatalf("Ctrl+S did not start saving the scheduled transfer: %#v", state.scheduledTransfers)
	}
	updated, _ = state.Update(saveCmd())
	state = unwrapModel(t, updated)
	if state.scheduledTransfers.editing || len(state.scheduledTransfers.transfers) != 1 {
		t.Fatalf("saved manager state = %#v", state.scheduledTransfers)
	}
	transfer := state.scheduledTransfers.transfers[0]
	if transfer.Name != "nightly archive" || transfer.HostId != host.Id || transfer.Direction != model.ScheduledTransferUpload || transfer.TimeZone != "Asia/Shanghai" {
		t.Fatalf("saved transfer = %#v", transfer)
	}
	if transfer.LocalPath != filepath.Clean("/tmp/archive.tar") || !transfer.Enabled || transfer.NextRunAt.Before(time.Now()) {
		t.Fatalf("saved transfer path/schedule = %#v", transfer)
	}

	updated, toggleCmd := state.updateScheduledTransferKey(tea.KeyPressMsg(tea.Key{Text: "t"}))
	state = unwrapModel(t, updated)
	if toggleCmd == nil {
		t.Fatal("t did not start the enable/disable update")
	}
	updated, _ = state.Update(toggleCmd())
	state = unwrapModel(t, updated)
	if state.scheduledTransfers.transfers[0].Enabled {
		t.Fatal("t did not disable the scheduled transfer")
	}
	updated, runCmd := state.updateScheduledTransferKey(tea.KeyPressMsg(tea.Key{Text: "i"}))
	state = unwrapModel(t, updated)
	if runCmd == nil {
		t.Fatal("i did not queue the disabled transfer for immediate execution")
	}
	updated, _ = state.Update(runCmd())
	state = unwrapModel(t, updated)
	if !state.scheduledTransfers.transfers[0].RunRequested || catalog.transfers[0].Enabled {
		t.Fatal("immediate run request changed the recurring schedule or was not recorded")
	}
	if view := state.scheduledTransferManagerView(); !strings.Contains(view, "待立即执行") {
		t.Fatalf("queued transfer status is not visible in the manager: %q", view)
	}

	updated, _ = state.updateScheduledTransferKey(tea.KeyPressMsg(tea.Key{Text: "d"}))
	state = unwrapModel(t, updated)
	updated, deleteCmd := state.updateScheduledTransferKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if deleteCmd == nil {
		t.Fatal("confirmed delete did not start the delete request")
	}
	updated, _ = state.Update(deleteCmd())
	state = unwrapModel(t, updated)
	if len(state.scheduledTransfers.transfers) != 0 || len(catalog.transfers) != 0 {
		t.Fatalf("plans after delete = %#v, catalog = %#v", state.scheduledTransfers.transfers, catalog.transfers)
	}
}

func TestScheduledTransferManagerOpensFromHomeAndWorkspaceLeader(t *testing.T) {
	catalog := &scheduledTransferCatalogStub{}
	host := model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "build", IP: "192.0.2.10", Port: 22, User: "tester"}
	home := modelState{catalog: catalog, hosts: []model.Host{host}, width: 100, height: 30}
	updated, loadCmd := home.updateKey(tea.KeyPressMsg(tea.Key{Text: "t"}))
	home = unwrapModel(t, updated)
	if home.scheduledTransfers == nil || loadCmd == nil {
		t.Fatal("home shortcut t did not open the scheduled transfer manager")
	}
	updated, _ = home.Update(loadCmd())
	home = unwrapModel(t, updated)
	if home.scheduledTransfers.loading || !strings.Contains(home.scheduledTransferManagerView(), "--run-scheduled") {
		t.Fatal("scheduled transfer manager did not load and explain its runner mode")
	}

	pane := &terminalPane{id: "pane", session: &workspaceTestSession{}}
	tab := &workspaceTab{id: "tab", activePaneID: pane.id, root: &paneNode{pane: pane}}
	workspace := modelState{
		catalog: catalog, hosts: []model.Host{host}, width: 100, height: 30,
		config: model.DefaultConfig(), activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true,
	}
	updated, _ = workspace.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	workspace = unwrapModel(t, updated)
	updated, loadCmd = workspace.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Text: "d"}))
	workspace = unwrapModel(t, updated)
	if workspace.scheduledTransfers == nil || loadCmd == nil {
		t.Fatal("Ctrl+G then d did not open the scheduled transfer manager")
	}
}

func typeScheduledText(t *testing.T, state *modelState, text string) {
	t.Helper()
	for _, character := range text {
		updated, _ := state.updateScheduledTransferEditorKey(tea.Key{Code: character, Text: string(character)})
		*state = unwrapModel(t, updated)
	}
}

func advanceScheduledField(t *testing.T, state *modelState) {
	t.Helper()
	updated, _ := state.updateScheduledTransferEditorKey(tea.Key{Code: tea.KeyTab})
	*state = unwrapModel(t, updated)
}
