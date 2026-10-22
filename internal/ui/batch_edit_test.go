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

func TestBatchEditSelectsHostsAndAppliesOnlyEnabledFields(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	group, err := store.CreateGroup(ctx, model.Group{Name: "Production"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	first, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "alpha", IP: "192.0.2.1", Port: 22, User: "old"})
	if err != nil {
		t.Fatalf("create first host: %v", err)
	}
	second, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "beta", IP: "192.0.2.2", Port: 22, User: "old"})
	if err != nil {
		t.Fatalf("create second host: %v", err)
	}
	state := modelState{
		catalog: store, groups: []model.Group{group}, hosts: []model.Host{first, second},
		activePane: hostPane, width: 100, height: 30,
	}

	state = updateModelWithKey(t, state, " ")
	if state.batchSelectedCount() != 1 || !state.batchSelectedHostIDs[first.Id] {
		t.Fatalf("space did not select first host: %#v", state.batchSelectedHostIDs)
	}
	state = updateModelWithKey(t, state, "down")
	state = updateModelWithKey(t, state, " ")
	if state.batchSelectedCount() != 2 {
		t.Fatalf("space did not select second host: %#v", state.batchSelectedHostIDs)
	}
	state = updateModelWithKey(t, state, "B")
	if state.batchEdit == nil || len(state.batchEdit.hostIDs) != 2 {
		t.Fatalf("B did not open batch editor for both hosts: %#v", state.batchEdit)
	}

	// Enable group movement and favorite status through the same keys exposed by
	// the editor, then enable and type into each text field.
	updated, _ := state.updateBatchEditKey(keyPress(" "))
	state = unwrapModel(t, updated)
	updated, _ = state.updateBatchEditKey(keyPress("right"))
	state = unwrapModel(t, updated)
	updated, _ = state.updateBatchEditKey(keyPress("down"))
	state = unwrapModel(t, updated)
	updated, _ = state.updateBatchEditKey(keyPress("right"))
	state = unwrapModel(t, updated)

	ctrlT := tea.KeyPressMsg(tea.Key{Code: 't', Mod: tea.ModCtrl})
	for _, value := range []string{"production note", "deploy", "2222", "~/.ssh/prod"} {
		updated, _ = state.updateBatchEditKey(keyPress("down"))
		state = unwrapModel(t, updated)
		updated, _ = state.updateBatchEditKey(ctrlT)
		state = unwrapModel(t, updated)
		updated, _ = state.updateBatchEditKey(keyPress(value))
		state = unwrapModel(t, updated)
	}
	updated, _ = state.updateBatchEditKey(keyPress("down"))
	state = unwrapModel(t, updated)
	updated, _ = state.updateBatchEditKey(ctrlT)
	state = unwrapModel(t, updated)
	updated, _ = state.updateBatchEditKey(keyPress("prod-{name}-{index}"))
	state = unwrapModel(t, updated)
	if preview := state.batchEditView(); !strings.Contains(preview, "prod-alpha-1") || !strings.Contains(preview, "prod-beta-2") {
		t.Fatalf("batch rename preview does not show generated names:\n%s", preview)
	}
	updated, command := state.updateBatchEditKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil || !state.batchEdit.busy {
		t.Fatal("Enter did not schedule the transaction")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.batchEdit != nil || state.batchSelectedCount() != 0 {
		t.Fatalf("successful batch edit did not close and clear selection: %#v", state.batchEdit)
	}
	for _, id := range []string{first.Id, second.Id} {
		host, err := store.GetHost(ctx, id)
		if err != nil {
			t.Fatalf("get host %q: %v", id, err)
		}
		wantName := "prod-alpha-1"
		if id == second.Id {
			wantName = "prod-beta-2"
		}
		if host.Name != wantName || host.GroupId != group.Id || !host.Favorite || host.Description != "production note" || host.User != "deploy" || host.Port != 2222 || host.IdentityFile != "~/.ssh/prod" {
			t.Fatalf("batch edited host = %#v", host)
		}
	}
	if state.statusText != "已批量更新 2 台主机" {
		t.Fatalf("batch update status = %q", state.statusText)
	}
}

func TestBatchRenameTemplateValidation(t *testing.T) {
	ids := []string{"host-a", "host-b"}
	original := map[string]string{"host-a": "alpha", "host-b": "beta"}
	if _, err := renderBatchRenameNames(ids, original, "prod"); err == nil {
		t.Fatal("constant template for multiple hosts should be rejected")
	}
	if _, err := renderBatchRenameNames(ids, original, " "); err == nil {
		t.Fatal("empty template should be rejected")
	}
	if _, err := renderBatchRenameNames(ids, map[string]string{"host-a": "alpha"}, "{name}-{index}"); err == nil {
		t.Fatal("missing original host name should be rejected")
	}
	one, err := renderBatchRenameNames(ids[:1], original, " production ")
	if err != nil || one["host-a"] != "production" {
		t.Fatalf("single-host rename = %#v, %v; want trimmed exact name", one, err)
	}
}

func TestBatchEditRejectsEmptySelectionAndKeepsSelectionAfterFailure(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "local"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	state := modelState{catalog: store, hosts: []model.Host{host}, activePane: hostPane, width: 80, height: 24}
	state = updateModelWithKey(t, state, "B")
	if state.batchEdit != nil || state.statusText == "" {
		t.Fatal("B opened batch editor with no selected hosts")
	}
	state = updateModelWithKey(t, state, " ")
	state = updateModelWithKey(t, state, "B")
	if state.batchEdit == nil {
		t.Fatal("B did not open batch editor after selecting a host")
	}
	state.batchEdit.identityFileOn = true
	state.batchEdit.identityFile = "~/.ssh/not-supported-for-local"
	updated, command := state.submitBatchEdit()
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("unsupported field validation should be handled by the transaction result")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.batchEdit == nil || state.batchEdit.busy || state.batchEdit.errorText == "" || state.batchSelectedCount() != 1 {
		t.Fatalf("failed batch edit lost recoverable form state: editor=%#v selection=%#v", state.batchEdit, state.batchSelectedHostIDs)
	}
	stored, err := store.GetHost(ctx, host.Id)
	if err != nil || stored.IdentityFile != "" {
		t.Fatalf("failed batch update changed local host: %#v err=%v", stored, err)
	}
}
