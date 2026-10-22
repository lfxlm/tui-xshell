package ui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/credentialstore"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

type memoryCredentialVault struct {
	values map[string]string
}

func (v *memoryCredentialVault) Set(reference, secret string) error {
	if v.values == nil {
		v.values = make(map[string]string)
	}
	v.values[reference] = secret
	return nil
}

func (v *memoryCredentialVault) Get(reference string) (string, error) {
	secret, ok := v.values[reference]
	if !ok {
		return "", credentialstore.ErrNotFound
	}
	return secret, nil
}

func (v *memoryCredentialVault) Delete(reference string) error {
	delete(v.values, reference)
	return nil
}

func TestHostCredentialCanBeSavedAndRemovedWithoutPersistingSecret(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "build", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	vault := &memoryCredentialVault{}
	state := modelState{catalog: store, credentials: vault, hosts: []model.Host{host}, activePane: hostPane}
	state = updateModelWithKey(t, state, "c")
	if state.credentialPrompt == nil {
		t.Fatal("c did not open credential manager")
	}
	state.credentialPrompt.value = "test-secret"
	updated, command := state.updateCredentialPromptKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if command == nil || state.credentialPrompt == nil || !state.credentialPrompt.saving {
		t.Fatal("saving a credential did not start an asynchronous operation")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.credentialPrompt != nil || state.selectedHost().CredentialRef == "" {
		t.Fatalf("credential was not attached to host: prompt=%#v host=%#v", state.credentialPrompt, state.selectedHost())
	}
	savedHost, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("read saved host: %v", err)
	}
	if savedHost.CredentialRef != state.selectedHost().CredentialRef {
		t.Fatalf("SQLite credential reference = %q, UI reference = %q", savedHost.CredentialRef, state.selectedHost().CredentialRef)
	}
	if got := vault.values[savedHost.CredentialRef]; got != "test-secret" {
		t.Fatalf("system vault value = %q, want saved credential", got)
	}
	if savedHost.Password != "" {
		t.Fatalf("SQLite host unexpectedly has transient password %q", savedHost.Password)
	}

	state = updateModelWithKey(t, state, "c")
	updated, command = state.updateCredentialPromptKey(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("Ctrl+D did not schedule credential deletion")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	deletedHost, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("read host after credential deletion: %v", err)
	}
	if deletedHost.CredentialRef != "" {
		t.Fatalf("credential reference after delete = %q, want empty", deletedHost.CredentialRef)
	}
	if len(vault.values) != 0 {
		t.Fatalf("system vault still has values after deletion: %#v", vault.values)
	}
}

func TestSSHConnectLoadsStoredCredentialsForJumpChain(t *testing.T) {
	jump := model.Host{Id: "jump", Kind: model.HostKindSSH, Name: "gateway", CredentialRef: "vault-jump"}
	target := model.Host{
		Id: "target", Kind: model.HostKindSSH, Name: "app", IP: "127.0.0.1", Port: 22,
		CredentialRef: "vault-target", ProxyJumpList: []string{jump.Id},
	}
	vault := &memoryCredentialVault{values: map[string]string{
		"vault-jump":   "jump-password",
		"vault-target": "target-password",
	}}
	state := modelState{hosts: []model.Host{jump, target}, credentials: vault}
	command := state.startHostConnect(target, state.newTabTarget())
	if command == nil {
		t.Fatal("SSH connection with saved credentials did not start a keyring read")
	}
	loaded, ok := command().(sshCredentialsLoadedMsg)
	if !ok {
		t.Fatalf("credential command returned %T, want sshCredentialsLoadedMsg", command())
	}
	if loaded.warning != "" {
		t.Fatalf("credential lookup warning = %q", loaded.warning)
	}
	if got := loaded.passwords[sshclient.HostCredentialKey(jump)]; got != "jump-password" {
		t.Fatalf("jump credential = %q, want stored password", got)
	}
	if got := loaded.passwords[sshclient.HostCredentialKey(target)]; got != "target-password" {
		t.Fatalf("target credential = %q, want stored password", got)
	}
	updated, connectCommand := state.Update(loaded)
	state = unwrapModel(t, updated)
	if connectCommand == nil || state.connectionPasswords[sshclient.HostCredentialKey(jump)] != "jump-password" {
		t.Fatal("loaded jump credential was not passed to the SSH connection flow")
	}
}

func TestFTPConnectLoadsStoredCredentialBeforePrompting(t *testing.T) {
	host := model.Host{Id: "ftp", Kind: model.HostKindFTP, Name: "archive", IP: "127.0.0.1", Port: 21, CredentialRef: "vault-ftp"}
	vault := &memoryCredentialVault{values: map[string]string{"vault-ftp": "ftp-password"}}
	state := modelState{credentials: vault}
	target := state.newSFTPTarget()
	command := state.startHostConnect(host, target)
	if command == nil {
		t.Fatal("FTP connection with saved credential did not start keyring read")
	}
	loaded, ok := command().(ftpCredentialLoadedMsg)
	if !ok || loaded.err != nil || loaded.password != "ftp-password" {
		t.Fatalf("FTP credential load = %#v, want saved password", loaded)
	}
	updated, connectCommand := state.Update(loaded)
	state = unwrapModel(t, updated)
	if connectCommand == nil || state.lastFTPPassword != "ftp-password" {
		t.Fatal("saved FTP password was not passed to the FTP connection flow")
	}
}

func TestSSHCredentialChainDoesNotLoadUnrelatedSecrets(t *testing.T) {
	jump := model.Host{Id: "jump", Kind: model.HostKindSSH, ProxyJumpList: []string{"upstream"}}
	upstream := model.Host{Id: "upstream", Kind: model.HostKindSSH}
	target := model.Host{Id: "target", Kind: model.HostKindSSH, ProxyJumpList: []string{jump.Id}}
	unrelated := model.Host{Id: "unrelated", Kind: model.HostKindSSH, CredentialRef: "should-not-be-read"}
	chain := sshCredentialHosts(target, []model.Host{jump, upstream, target, unrelated})
	if len(chain) != 3 || chain[0].Id != "upstream" || chain[1].Id != "jump" || chain[2].Id != "target" {
		t.Fatalf("SSH credential chain = %#v", chain)
	}
	if hostHasSavedCredential(target, []model.Host{jump, upstream, target, unrelated}) {
		t.Fatal("unrelated host credential caused target connection to read the keyring")
	}
}

func TestDeletingHostAlsoDeletesItsKeyringCredential(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "temporary", IP: "127.0.0.1", Port: 22, User: "tester", CredentialRef: "vault-entry",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	vault := &memoryCredentialVault{values: map[string]string{"vault-entry": "secret"}}
	state := modelState{catalog: store, credentials: vault, hosts: []model.Host{host}, activePane: hostPane}
	state = updateModelWithKey(t, state, "d")
	updated, command := state.updateCatalogDeleteConfirm("enter")
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("confirming host deletion did not schedule deletion")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.hosts) != 0 || len(vault.values) != 0 {
		t.Fatalf("host deletion left host or credential behind: hosts=%#v vault=%#v", state.hosts, vault.values)
	}
	if _, err := store.GetHost(ctx, host.Id); err == nil {
		t.Fatal("host still exists after confirmed deletion")
	}
}

func TestCredentialSaveCompensatesWhenHostUpdateFails(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "temporary", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	vault := &memoryCredentialVault{}
	state := modelState{catalog: store, credentials: vault}
	prompt := &credentialPromptState{host: host, value: "new-secret"}
	command := state.saveHostCredential(prompt)
	if err := store.DeleteHost(ctx, host.Id); err != nil {
		t.Fatalf("delete host before asynchronous update: %v", err)
	}
	result, ok := command().(credentialSavedMsg)
	if !ok || result.err == nil {
		t.Fatalf("credential save result = %#v, want database update error", result)
	}
	if len(vault.values) != 0 {
		t.Fatalf("failed host update left orphaned keyring entry: %#v", vault.values)
	}
}

type failingCredentialVault struct {
	memoryCredentialVault
	getErr error
}

func (v *failingCredentialVault) Get(reference string) (string, error) {
	if v.getErr != nil {
		return "", v.getErr
	}
	return v.memoryCredentialVault.Get(reference)
}

func TestSSHKeyringFailureFallsBackToInteractiveAuthentication(t *testing.T) {
	host := model.Host{Id: "ssh", Kind: model.HostKindSSH, Name: "target", CredentialRef: "locked"}
	state := modelState{hosts: []model.Host{host}, credentials: &failingCredentialVault{getErr: errors.New("keychain is locked")}}
	command := state.startHostConnect(host, state.newTabTarget())
	loaded, ok := command().(sshCredentialsLoadedMsg)
	if !ok || loaded.warning == "" {
		t.Fatalf("keyring failure result = %#v, want interactive-auth warning", loaded)
	}
	updated, connectCommand := state.Update(loaded)
	state = unwrapModel(t, updated)
	if connectCommand == nil || state.connectingHost == nil {
		t.Fatal("SSH connection did not continue to interactive authentication")
	}
}
