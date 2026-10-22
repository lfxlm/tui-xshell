package ui

import (
	"context"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestHostFormSelectsReusableAuthProfile(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "auth-profile-ui.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profile, err := store.CreateAuthProfile(ctx, model.AuthProfile{
		Name: "Development", User: "profile-user", IdentityFile: "~/.ssh/dev_key",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "dev", IP: "127.0.0.1", Port: 22, User: "host-user",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	state := modelState{
		catalog: store, hosts: []model.Host{host}, authProfiles: []model.AuthProfile{profile},
		form: newHostEditForm(host),
	}
	state.form.field = 10
	updated, _ := state.updateFormKey(keyPress("right"))
	state = unwrapModel(t, updated)
	if state.form.authProfileID != profile.Id || state.form.authProfileName != profile.Name {
		t.Fatalf("selected auth profile = %q/%q, want %q/%q", state.form.authProfileID, state.form.authProfileName, profile.Id, profile.Name)
	}
	updated, command := state.submitForm()
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("submitting host form did not create a save command")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	saved, err := store.GetHost(ctx, host.Id)
	if err != nil || saved.AuthProfileID != profile.Id {
		t.Fatalf("saved host profile = %q, err=%v; want %q", saved.AuthProfileID, err, profile.Id)
	}
	resolved := state.hostWithAuthProfile(saved)
	if resolved.User != profile.User || resolved.IdentityFile != profile.IdentityFile {
		t.Fatalf("resolved host auth = user %q key %q; want %q / %q", resolved.User, resolved.IdentityFile, profile.User, profile.IdentityFile)
	}
}

func TestGroupAuthProfileIsInheritedAndCanBeEdited(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "group-auth-profile-ui.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile, err := store.CreateAuthProfile(ctx, model.AuthProfile{
		Name: "Engineering", User: "deploy", CredentialRef: "keychain-ref", IdentityFile: "~/.ssh/engineering",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	root, err := store.CreateGroup(ctx, model.Group{Name: "Engineering", AuthProfileID: profile.Id})
	if err != nil {
		t.Fatalf("create profile group: %v", err)
	}
	child, err := store.CreateGroup(ctx, model.Group{Name: "Backend", ParentId: root.Id})
	if err != nil {
		t.Fatalf("create child group: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "api", IP: "127.0.0.1", Port: 22, GroupId: child.Id,
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	state := modelState{
		catalog: store, groups: []model.Group{root, child}, hosts: []model.Host{host},
		authProfiles: []model.AuthProfile{profile},
	}
	resolved := state.hostWithAuthProfile(host)
	if resolved.User != "deploy" || resolved.CredentialRef != "keychain-ref" || resolved.IdentityFile != "~/.ssh/engineering" {
		t.Fatalf("resolved inherited host = %#v, want profile values", resolved)
	}
	if state.effectiveAuthProfileID(host) != profile.Id {
		t.Fatalf("effective profile ID = %q, want %q", state.effectiveAuthProfileID(host), profile.Id)
	}

	state.form = newGroupEditForm(child)
	state.form.field = 2
	updated, _ := state.updateFormKey(keyPress("right"))
	state = unwrapModel(t, updated)
	if state.form.authProfileID != profile.Id || state.form.authProfileName != profile.Name {
		t.Fatalf("selected inherited profile = %q/%q, want %q/%q", state.form.authProfileID, state.form.authProfileName, profile.Id, profile.Name)
	}
	updated, command := state.submitForm()
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("saving group profile did not create a storage command")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	saved, err := store.GetGroup(ctx, child.Id)
	if err != nil || saved.AuthProfileID != profile.Id {
		t.Fatalf("saved group profile = %q, err=%v; want %q", saved.AuthProfileID, err, profile.Id)
	}
}

func TestAuthProfileManagerCreatesProfile(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "auth-profile-manager.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state := modelState{catalog: store}

	command := state.openAuthProfileManager()
	if command == nil {
		t.Fatal("opening auth profile manager did not load profiles")
	}
	updated, _ := state.Update(command())
	state = unwrapModel(t, updated)
	updated, _ = state.updateAuthProfileManagerKey(keyPress("n"))
	state = unwrapModel(t, updated)
	for _, key := range []string{"Development", "tab", "deploy", "tab", "~/.ssh/deploy", "enter"} {
		updated, command = state.updateAuthProfileFormKey(keyPress(key))
		state = unwrapModel(t, updated)
		if command != nil {
			break
		}
	}
	if command == nil {
		t.Fatal("saving auth profile did not issue a storage command")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.authProfiles) != 1 || state.authProfiles[0].Name != "Development" ||
		state.authProfiles[0].User != "deploy" || state.authProfiles[0].IdentityFile != "~/.ssh/deploy" {
		t.Fatalf("profiles after creation = %#v", state.authProfiles)
	}
}
