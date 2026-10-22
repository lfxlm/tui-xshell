package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshkeys"
	"tui-ssh-workbench/internal/model"
)

func TestSSHKeyManagerLoadsManagedKeysAndOpensFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	privatePath, _, err := sshkeys.Generate(filepath.Join(home, ".ssh", "workbench_test"), sshkeys.Ed25519, "")
	if err != nil {
		t.Fatal(err)
	}

	state := modelState{width: 120, height: 40}
	updated, command := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'k', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if state.sshKeyManager == nil || !state.sshKeyManager.loading || command == nil {
		t.Fatal("Ctrl+K did not asynchronously open the SSH key manager")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.sshKeyManager.loading || len(state.sshKeyManager.identities) != 1 || state.sshKeyManager.identities[0].PrivatePath != privatePath {
		t.Fatalf("loaded SSH key inventory = %#v", state.sshKeyManager)
	}
	view := state.View().Content
	if !strings.Contains(view, "workbench_test") || !strings.Contains(view, "SHA256:") {
		t.Fatalf("key manager view omits identity or fingerprint:\n%s", view)
	}
}

func TestSSHKeyManagerBlocksDeletingIdentityReferencedByHost(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "workbench_referenced")
	state := modelState{
		hosts:         []model.Host{{Name: "build", Kind: model.HostKindSSH, IdentityFile: privatePath}},
		sshKeyManager: &sshKeyManagerState{identities: []sshkeys.ManagedIdentity{{PrivatePath: privatePath}}, index: 0},
	}
	updated, command := state.updateSSHKeyManagerKey(keyPress("d"))
	state = unwrapModel(t, updated)
	if command != nil || state.sshKeyManager.confirmingDelete || !strings.Contains(state.sshKeyManager.errorText, "build") {
		t.Fatalf("referenced identity was not protected: state=%#v command=%v", state.sshKeyManager, command)
	}
}

func TestSSHKeyManagerBlocksDeletingIdentityReferencedByAuthProfile(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "workbench_profile_key")
	state := modelState{
		authProfiles:  []model.AuthProfile{{Id: "profile-1", Name: "production", IdentityFile: privatePath}},
		sshKeyManager: &sshKeyManagerState{identities: []sshkeys.ManagedIdentity{{PrivatePath: privatePath}}, index: 0},
	}
	updated, command := state.updateSSHKeyManagerKey(keyPress("d"))
	state = unwrapModel(t, updated)
	if command != nil || state.sshKeyManager.confirmingDelete || !strings.Contains(state.sshKeyManager.errorText, "production") {
		t.Fatalf("profile-referenced identity was not protected: state=%#v command=%v", state.sshKeyManager, command)
	}
}

func TestSSHKeyManagerDeletesUnusedManagedKeyAndRefreshesInventory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	privatePath := filepath.Join(home, ".ssh", "workbench_remove")
	publicPath, err := generateManagedPublicPair(t, privatePath)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := sshkeys.ListManaged()
	if err != nil {
		t.Fatal(err)
	}
	state := modelState{width: 120, height: 40, sshKeyManager: &sshKeyManagerState{identities: identities}}
	updated, _ := state.updateSSHKeyManagerKey(keyPress("d"))
	state = unwrapModel(t, updated)
	if !state.sshKeyManager.confirmingDelete {
		t.Fatal("deleting an unused identity did not require confirmation")
	}
	updated, command := state.updateSSHKeyManagerKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil || !state.sshKeyManager.busy {
		t.Fatal("confirmed deletion did not run asynchronously")
	}
	updated, refresh := state.Update(command())
	state = unwrapModel(t, updated)
	if refresh == nil || !state.sshKeyManager.loading {
		t.Fatal("successful deletion did not refresh the inventory")
	}
	updated, _ = state.Update(refresh())
	state = unwrapModel(t, updated)
	if state.sshKeyManager.loading || len(state.sshKeyManager.identities) != 0 {
		t.Fatalf("inventory after deletion = %#v", state.sshKeyManager.identities)
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted identity path %q still exists (err=%v)", path, err)
		}
	}
}

func TestSSHKeyManagerStartsNewKeyGenerationWithUnusedManagedPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, _, err := sshkeys.Generate(filepath.Join(home, ".ssh", "workbench_ed25519"), sshkeys.Ed25519, ""); err != nil {
		t.Fatal(err)
	}
	state := modelState{width: 120, height: 40, sshKeyManager: &sshKeyManagerState{}}
	updated, _ := state.updateSSHKeyManagerKey(keyPress("n"))
	state = unwrapModel(t, updated)
	if state.sshKeyGeneration == nil || state.sshKeyGeneration.path != filepath.Join(home, ".ssh", "workbench_ed25519_2") {
		t.Fatalf("new managed key generation state = %#v", state.sshKeyGeneration)
	}
	state.handleSSHKeyGenerated(sshKeyGeneratedMsg{privatePath: filepath.Join(home, ".ssh", "workbench_ed25519_2"), publicPath: filepath.Join(home, ".ssh", "workbench_ed25519_2.pub")})
	if state.sshKeyGeneration == nil || !state.sshKeyGeneration.done || !strings.Contains(state.sshKeyGenerationView(), "密钥管理") {
		t.Fatalf("key-generation result did not return to manager context: %#v", state.sshKeyGeneration)
	}
}

func TestSSHKeyManagerRotatesHostAndProfileReferencesAfterConfirmation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	oldPath := filepath.Join(home, ".ssh", "workbench_old")
	newPath := filepath.Join(home, ".ssh", "workbench_new")
	if _, _, err := sshkeys.Generate(oldPath, sshkeys.Ed25519, ""); err != nil {
		t.Fatalf("generate old key: %v", err)
	}
	if _, _, err := sshkeys.Generate(newPath, sshkeys.Ed25519, ""); err != nil {
		t.Fatalf("generate replacement key: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rotation-ui.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profile, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "production", IdentityFile: oldPath})
	if err != nil {
		t.Fatalf("create auth profile: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "prod", IP: "example.test", Port: 22,
		IdentityFile: oldPath, AuthProfileID: profile.Id,
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	identities, err := sshkeys.ListManaged()
	if err != nil {
		t.Fatalf("list managed keys: %v", err)
	}
	state := modelState{
		catalog: store, hosts: []model.Host{host}, authProfiles: []model.AuthProfile{profile},
		sshKeyManager: &sshKeyManagerState{identities: identities}, width: 120, height: 40,
	}
	for index, identity := range identities {
		if identity.PrivatePath == oldPath {
			state.sshKeyManager.index = index
		}
	}

	updated, _ := state.updateSSHKeyManagerKey(keyPress("m"))
	state = unwrapModel(t, updated)
	if state.sshKeyManager.rotation == nil || mSelectedRotationTarget(state) == nil {
		t.Fatal("rotation did not offer the replacement identity")
	}
	updated, _ = state.updateSSHKeyRotationKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if !state.sshKeyManager.rotation.confirming || !strings.Contains(state.sshKeyManagerView(), "authorized_keys") {
		t.Fatal("rotation confirmation did not explain remote public-key installation")
	}
	updated, command := state.updateSSHKeyRotationKey(keyPress("y"))
	state = unwrapModel(t, updated)
	if command == nil || !state.sshKeyManager.rotation.busy {
		t.Fatal("confirmed rotation did not start the asynchronous migration")
	}
	updated, refresh := state.Update(command())
	state = unwrapModel(t, updated)
	if refresh == nil || state.sshKeyManager == nil || !state.sshKeyManager.loading {
		t.Fatal("successful rotation did not refresh managed keys")
	}
	if len(state.hosts) != 1 || state.hosts[0].IdentityFile != newPath {
		t.Fatalf("host identity after rotation = %#v", state.hosts)
	}
	if len(state.authProfiles) != 1 || state.authProfiles[0].IdentityFile != newPath {
		t.Fatalf("profile identity after rotation = %#v", state.authProfiles)
	}
	updated, _ = state.Update(refresh())
	state = unwrapModel(t, updated)
	if len(managedIdentityReferences(state.hosts, state.authProfiles, oldPath)) != 0 {
		t.Fatal("old private key still has saved references after rotation")
	}
}

func mSelectedRotationTarget(state modelState) *sshkeys.ManagedIdentity {
	return state.selectedSSHKeyRotationTarget()
}

func generateManagedPublicPair(t *testing.T, privatePath string) (string, error) {
	t.Helper()
	_, publicPath, err := sshkeys.Generate(privatePath, sshkeys.Ed25519, "")
	return publicPath, err
}
