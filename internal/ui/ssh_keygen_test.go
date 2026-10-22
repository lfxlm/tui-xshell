package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tui-ssh-workbench/internal/infra/sshkeys"
	"tui-ssh-workbench/internal/model"
)

func TestSSHHostFormOpensKeyGeneratorOnIdentityField(t *testing.T) {
	state := modelState{form: &formState{kind: hostForm, hostKind: model.HostKindSSH, field: 5}}
	updated, _ := state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	got := updated.(modelState)
	if got.sshKeyGeneration == nil {
		t.Fatal("Ctrl+G on the SSH identity field did not open key generation")
	}
	if got.sshKeyGeneration.path != "~/.ssh/workbench_ed25519" || got.sshKeyGeneration.algorithm != sshkeys.Ed25519 {
		t.Fatalf("default key generation state = %#v", got.sshKeyGeneration)
	}

	state.form.field = 4
	updated, _ = state.updateKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	if updated.(modelState).sshKeyGeneration != nil {
		t.Fatal("Ctrl+G outside the identity field unexpectedly opened key generation")
	}
}

func TestGeneratedSSHKeyReturnsPathToHostForm(t *testing.T) {
	state := modelState{
		width: 100, height: 30,
		form:             &formState{kind: hostForm, hostKind: model.HostKindSSH, field: 5},
		sshKeyGeneration: &sshKeyGenerationState{path: "~/keys/dev", algorithm: sshkeys.Ed25519, passphrase: "secret", confirm: "secret"},
	}
	state.handleSSHKeyGenerated(sshKeyGeneratedMsg{
		privatePath: "/home/user/keys/dev",
		publicPath:  "/home/user/keys/dev.pub",
	})
	if state.form.identityFile != "/home/user/keys/dev" {
		t.Fatalf("host identity path = %q, want generated private-key path", state.form.identityFile)
	}
	if state.sshKeyGeneration == nil || !state.sshKeyGeneration.done || state.sshKeyGeneration.passphrase != "" || state.sshKeyGeneration.confirm != "" {
		t.Fatalf("key generation result did not clear the transient passphrase: %#v", state.sshKeyGeneration)
	}
	view := state.sshKeyGenerationView()
	if !strings.Contains(view, "/home/user/keys/dev.pub") || !strings.Contains(view, "authorized_keys") {
		t.Fatalf("success view should show the public-key installation instructions:\n%s", view)
	}
	updated, _ := state.updateSSHKeyGenerationKey(keyPress("enter"))
	if updated.(modelState).sshKeyGeneration != nil {
		t.Fatal("Enter on the completed screen should return to the host form")
	}
}

func TestSSHKeyGeneratorRejectsMismatchedPassphrases(t *testing.T) {
	state := modelState{sshKeyGeneration: &sshKeyGenerationState{
		field: 3, path: "/tmp/key", algorithm: sshkeys.Ed25519,
		passphrase: "first", confirm: "second",
	}}
	updated, command := state.updateSSHKeyGenerationKey(keyPress("enter"))
	got := updated.(modelState)
	if command != nil || got.sshKeyGeneration.saving || got.sshKeyGeneration.errorText == "" {
		t.Fatalf("mismatched passphrases should not start generation: state=%#v command=%v", got.sshKeyGeneration, command)
	}
	if got.sshKeyGeneration.field != 2 {
		t.Fatalf("focus after mismatch = %d, want passphrase field", got.sshKeyGeneration.field)
	}
}
