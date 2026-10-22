package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestReplaceIdentityFileReferencesUpdatesHostsAndProfilesAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "identity-rotation.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "production", IdentityFile: "~/.ssh/workbench_old"})
	if err != nil {
		t.Fatalf("create auth profile: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "prod", IP: "example.test", Port: 22,
		IdentityFile: "~/.ssh/workbench_old", AuthProfileID: profile.Id,
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	if err := store.ReplaceIdentityFileReferences(ctx, []model.IdentityFileReference{
		{HostID: host.Id, CurrentPath: "~/.ssh/workbench_old"},
		{AuthProfileID: profile.Id, CurrentPath: "~/.ssh/workbench_old"},
	}, "/Users/test/.ssh/workbench_new"); err != nil {
		t.Fatalf("replace identity references: %v", err)
	}
	loadedHost, err := store.GetHost(ctx, host.Id)
	if err != nil || loadedHost.IdentityFile != "/Users/test/.ssh/workbench_new" {
		t.Fatalf("host identity path = %q, err=%v", loadedHost.IdentityFile, err)
	}
	loadedProfile, err := store.GetAuthProfile(ctx, profile.Id)
	if err != nil || loadedProfile.IdentityFile != "/Users/test/.ssh/workbench_new" {
		t.Fatalf("profile identity path = %q, err=%v", loadedProfile.IdentityFile, err)
	}
}

func TestReplaceIdentityFileReferencesRollsBackOnStaleReference(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "identity-rotation-rollback.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "prod", IP: "example.test", Port: 22,
		IdentityFile: "/keys/old",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	err = store.ReplaceIdentityFileReferences(ctx, []model.IdentityFileReference{
		{HostID: host.Id, CurrentPath: "/keys/old"},
		{AuthProfileID: "deleted-profile", CurrentPath: "/keys/old"},
	}, "/keys/new")
	if !errors.Is(err, ErrIdentityReferenceChanged) {
		t.Fatalf("stale reference error = %v, want %v", err, ErrIdentityReferenceChanged)
	}
	loadedHost, err := store.GetHost(ctx, host.Id)
	if err != nil || loadedHost.IdentityFile != "/keys/old" {
		t.Fatalf("failed rotation partially changed host reference: path=%q err=%v", loadedHost.IdentityFile, err)
	}
}
