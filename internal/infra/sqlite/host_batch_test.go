package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestBatchUpdateHostsAppliesOnlySelectedFields(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	group, err := store.CreateGroup(ctx, model.Group{Name: "Production"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	first, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "one", IP: "192.0.2.1", Port: 22, User: "old"})
	if err != nil {
		t.Fatalf("create first host: %v", err)
	}
	second, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "two", IP: "192.0.2.2", Port: 22, User: "old"})
	if err != nil {
		t.Fatalf("create second host: %v", err)
	}
	patch := model.HostBatchPatch{
		GroupID: stringPointer(group.Id), Description: stringPointer("  managed  "),
		User: stringPointer(" deploy "), Port: intPointer(2222),
		IdentityFile: stringPointer(" ~/.ssh/prod "), Favorite: boolPointer(true),
		Names: map[string]string{first.Id: " prod-one ", second.Id: " prod-two "},
	}
	if err := store.BatchUpdateHosts(ctx, []string{first.Id, second.Id}, patch); err != nil {
		t.Fatalf("batch update hosts: %v", err)
	}
	for _, id := range []string{first.Id, second.Id} {
		host, err := store.GetHost(ctx, id)
		if err != nil {
			t.Fatalf("get host %q: %v", id, err)
		}
		wantName := "prod-one"
		if id == second.Id {
			wantName = "prod-two"
		}
		if host.Name != wantName || host.GroupId != group.Id || host.Description != "managed" || host.User != "deploy" || host.Port != 2222 || host.IdentityFile != "~/.ssh/prod" || !host.Favorite || host.FavoriteOrder <= 0 {
			t.Fatalf("batch updated host = %#v", host)
		}
	}

	unfavorite := false
	if err := store.BatchUpdateHosts(ctx, []string{first.Id}, model.HostBatchPatch{Favorite: &unfavorite}); err != nil {
		t.Fatalf("batch unfavorite host: %v", err)
	}
	first, err = store.GetHost(ctx, first.Id)
	if err != nil || first.Favorite || first.FavoriteOrder != 0 {
		t.Fatalf("unfavorited host = %#v, err=%v", first, err)
	}
}

func TestBatchUpdateHostsValidationAndRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	group, err := store.CreateGroup(ctx, model.Group{Name: "Before"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	sshHost, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "ssh", IP: "192.0.2.1", Port: 22})
	if err != nil {
		t.Fatalf("create SSH host: %v", err)
	}
	localHost, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "local"})
	if err != nil {
		t.Fatalf("create local host: %v", err)
	}
	newGroupID := "missing-group"
	keyPath := "~/.ssh/key"
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id, localHost.Id}, model.HostBatchPatch{
		GroupID: &newGroupID, IdentityFile: &keyPath,
		Names: map[string]string{sshHost.Id: "renamed-ssh", localHost.Id: "renamed-local"},
	}); err == nil {
		t.Fatal("batch update unexpectedly accepted a private key field for a local host")
	}
	for _, id := range []string{sshHost.Id, localHost.Id} {
		host, err := store.GetHost(ctx, id)
		wantName := "ssh"
		if id == localHost.Id {
			wantName = "local"
		}
		if err != nil || host.Name != wantName || host.GroupId != "" || host.IdentityFile != "" {
			t.Fatalf("rejected batch update partially changed %q: host=%#v err=%v", id, host, err)
		}
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id}, model.HostBatchPatch{Port: intPointer(0)}); err == nil {
		t.Fatal("batch update accepted an invalid port")
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id, localHost.Id}, model.HostBatchPatch{
		GroupID: stringPointer(group.Id), Names: map[string]string{sshHost.Id: "renamed", localHost.Id: " "},
	}); err == nil {
		t.Fatal("batch update accepted an empty replacement name")
	}
	for id, wantName := range map[string]string{sshHost.Id: "ssh", localHost.Id: "local"} {
		host, err := store.GetHost(ctx, id)
		if err != nil || host.Name != wantName || host.GroupId != "" {
			t.Fatalf("invalid rename partially changed host %q: %#v err=%v", id, host, err)
		}
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id, localHost.Id}, model.HostBatchPatch{
		GroupID: stringPointer(group.Id), Names: map[string]string{sshHost.Id: "renamed", "unexpected": "outside"},
	}); err == nil {
		t.Fatal("batch update accepted a name for an unselected host")
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id, sshHost.Id}, model.HostBatchPatch{GroupID: &group.Id}); err == nil {
		t.Fatal("batch update accepted duplicate host IDs")
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id, localHost.Id}, model.HostBatchPatch{Names: map[string]string{sshHost.Id: "only-one"}}); err == nil {
		t.Fatal("batch update accepted a partial rename map")
	}
	if err := store.BatchUpdateHosts(ctx, []string{sshHost.Id}, model.HostBatchPatch{}); err == nil {
		t.Fatal("batch update accepted an empty patch")
	}
	if err := store.BatchUpdateHosts(ctx, []string{"missing-host"}, model.HostBatchPatch{GroupID: &group.Id}); err == nil {
		t.Fatal("batch update accepted a missing host")
	}
}

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }
func boolPointer(value bool) *bool       { return &value }
