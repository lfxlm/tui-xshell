package ui

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/crypto/ssh"

	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestSSHHostKeyManagerListsAndRemovesTrustedEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := sshclient.TrustHost(model.Host{Kind: model.HostKindSSH, Name: "build", IP: "build.example", Port: 22}, signer.PublicKey()); err != nil {
		t.Fatalf("trust host: %v", err)
	}

	state := modelState{width: 120, height: 40}
	key := tea.KeyPressMsg(tea.Key{Code: 'h', Mod: tea.ModCtrl | tea.ModShift})
	updated, command := state.updateKey(key)
	state = unwrapModel(t, updated)
	if state.sshHostKeyManager == nil || !state.sshHostKeyManager.loading || command == nil {
		t.Fatal("Ctrl+Shift+H did not open the host key manager")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if len(state.sshHostKeyManager.entries) != 1 || !strings.Contains(state.View().Content, "build.example") {
		t.Fatalf("trusted host key was not displayed: %#v\n%s", state.sshHostKeyManager, state.View().Content)
	}
	updated, command = state.updateSSHHostKeyManagerKey(keyPress("d"))
	state = unwrapModel(t, updated)
	if command != nil || !state.sshHostKeyManager.confirmingDelete {
		t.Fatal("host key removal skipped confirmation")
	}
	updated, command = state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	if command == nil || !state.sshHostKeyManager.busy {
		t.Fatal("confirmed host key removal did not start")
	}
	updated, refresh := state.Update(command())
	state = unwrapModel(t, updated)
	if refresh == nil || !state.sshHostKeyManager.loading {
		t.Fatal("host key removal did not refresh the list")
	}
	updated, _ = state.Update(refresh())
	state = unwrapModel(t, updated)
	if len(state.sshHostKeyManager.entries) != 0 {
		t.Fatalf("known hosts after removal = %#v", state.sshHostKeyManager.entries)
	}
	entries, err := sshclient.ListKnownHostEntries()
	if err != nil || len(entries) != 0 {
		t.Fatalf("persisted known hosts = %#v, err=%v", entries, err)
	}
}

func TestSSHHostKeyManagerImportsWithPreviewAndExportsWithoutOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, existingPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	existingSigner, err := ssh.NewSignerFromKey(existingPrivate)
	if err != nil {
		t.Fatal(err)
	}
	if err := sshclient.TrustHost(model.Host{Kind: model.HostKindSSH, Name: "existing", IP: "existing.example", Port: 22}, existingSigner.PublicKey()); err != nil {
		t.Fatalf("trust existing host: %v", err)
	}

	_, importedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	importedSigner, err := ssh.NewSignerFromKey(importedPrivate)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(home, "known hosts to import")
	line := fmt.Sprintf("imported.example %s\n", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(importedSigner.PublicKey()))))
	if err := os.WriteFile(sourcePath, []byte(line), 0o600); err != nil {
		t.Fatalf("write import file: %v", err)
	}
	exportPath := filepath.Join(home, "known hosts export")

	state := modelState{width: 120, height: 40}
	state.sshHostKeyManager = &sshHostKeyManagerState{}
	updated, _ := state.updateSSHHostKeyManagerKey(keyPress("i"))
	state = unwrapModel(t, updated)
	state, _ = sshHostKeyManagerInput(t, state, sourcePath)
	updated, command := state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	if command == nil || !state.sshHostKeyManager.busy {
		t.Fatal("import preview did not start")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if !state.sshHostKeyManager.confirmingImport || state.sshHostKeyManager.importPlan == nil {
		t.Fatalf("import did not show a confirmation preview: %#v", state.sshHostKeyManager)
	}
	if state.sshHostKeyManager.importPlan.TotalRecords != 1 || state.sshHostKeyManager.importPlan.NewRecords != 1 {
		t.Fatalf("unexpected import preview: %#v", state.sshHostKeyManager.importPlan)
	}
	if !strings.Contains(state.View().Content, filepath.Base(sourcePath)) {
		t.Fatalf("preview does not show its source file:\n%s", state.View().Content)
	}
	updated, command = state.updateSSHHostKeyManagerKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if command != nil || state.sshHostKeyManager.confirmingImport {
		t.Fatal("Esc did not cancel import preview")
	}
	entries, err := sshclient.ListKnownHostEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("cancelled preview changed trust store: entries=%#v err=%v", entries, err)
	}

	updated, _ = state.updateSSHHostKeyManagerKey(keyPress("i"))
	state = unwrapModel(t, updated)
	state, _ = sshHostKeyManagerInput(t, state, sourcePath)
	updated, command = state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	updated, command = state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	if command == nil || !state.sshHostKeyManager.busy {
		t.Fatal("confirmed import did not start")
	}
	updated, refresh := state.Update(command())
	state = unwrapModel(t, updated)
	if refresh == nil || !state.sshHostKeyManager.loading {
		t.Fatal("successful import did not refresh trusted keys")
	}
	updated, _ = state.Update(refresh())
	state = unwrapModel(t, updated)
	if len(state.sshHostKeyManager.entries) != 2 {
		t.Fatalf("imported trust entries = %#v; want 2", state.sshHostKeyManager.entries)
	}

	updated, _ = state.updateSSHHostKeyManagerKey(keyPress("e"))
	state = unwrapModel(t, updated)
	state, _ = sshHostKeyManagerInput(t, state, exportPath)
	updated, command = state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("export did not start")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.sshHostKeyManager.errorText != "" || !strings.Contains(state.statusText, exportPath) {
		t.Fatalf("export did not report success: error=%q status=%q", state.sshHostKeyManager.errorText, state.statusText)
	}
	info, err := os.Stat(exportPath)
	if err != nil {
		t.Fatalf("stat exported file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exported file mode = %04o; want 0600", info.Mode().Perm())
	}
	exported, err := os.ReadFile(exportPath)
	if err != nil || !strings.Contains(string(exported), "imported.example") || !strings.Contains(string(exported), "existing.example") {
		t.Fatalf("exported known_hosts = %q, err=%v", exported, err)
	}

	updated, _ = state.updateSSHHostKeyManagerKey(keyPress("e"))
	state = unwrapModel(t, updated)
	state, _ = sshHostKeyManagerInput(t, state, exportPath)
	updated, command = state.updateSSHHostKeyManagerKey(sshHostKeyEnterKey())
	state = unwrapModel(t, updated)
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if !strings.Contains(state.sshHostKeyManager.errorText, "避免覆盖") {
		t.Fatalf("existing export target was not rejected: %q", state.sshHostKeyManager.errorText)
	}
}

func TestChangedHostKeyRequiresConfirmationThenReplacesAndReconnects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, oldPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldSigner, err := ssh.NewSignerFromKey(oldPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, newPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newSigner, err := ssh.NewSignerFromKey(newPrivate)
	if err != nil {
		t.Fatal(err)
	}
	host := model.Host{Id: "device", Kind: model.HostKindSSH, Name: "device", IP: "192.0.2.20", Port: 33333}
	if err := sshclient.TrustHost(host, oldSigner.PublicKey()); err != nil {
		t.Fatalf("trust old host key: %v", err)
	}
	entries, err := sshclient.ListKnownHostEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("list trusted host key = %#v, err=%v", entries, err)
	}
	oldFingerprint := ssh.FingerprintSHA256(oldSigner.PublicKey())
	newFingerprint := ssh.FingerprintSHA256(newSigner.PublicKey())
	prompt := &sshclient.ChangedHostKeyError{
		Address: "192.0.2.20:33333", PreviousFingerprints: []string{oldFingerprint},
		Fingerprint: newFingerprint, Key: newSigner.PublicKey(), Host: host, CanReplace: true,
		References: []sshclient.KnownHostKeyReference{{LineNumber: entries[0].LineNumber, Fingerprint: oldFingerprint}},
	}

	cancelled := modelState{
		width: 120, height: 40, connectingHost: &host,
		connectionTarget: &connectionTarget{kind: newTabTarget}, hostKeyChangePrompt: prompt,
	}
	if !strings.Contains(cancelled.View().Content, oldFingerprint) || !strings.Contains(cancelled.View().Content, newFingerprint) {
		t.Fatalf("change prompt does not show both fingerprints:\n%s", cancelled.View().Content)
	}
	updated, command := cancelled.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	cancelled = unwrapModel(t, updated)
	if command != nil || cancelled.hostKeyChangePrompt != nil || cancelled.connectingHost != nil {
		t.Fatal("Esc did not cancel the changed-key connection")
	}
	entries, err = sshclient.ListKnownHostEntries()
	if err != nil || len(entries) != 1 || entries[0].Fingerprint != oldFingerprint {
		t.Fatalf("cancelling changed-key prompt modified trust: %#v, err=%v", entries, err)
	}

	state := modelState{
		width: 120, height: 40, connectingHost: &host,
		connectionTarget: &connectionTarget{kind: newTabTarget}, hostKeyChangePrompt: prompt,
	}
	updated, command = state.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if command == nil || !state.trustingHost || !state.replacingHostKey || state.hostKeyChangePrompt != nil {
		t.Fatal("Enter did not start a confirmed fingerprint replacement")
	}
	updated, reconnect := state.Update(command())
	state = unwrapModel(t, updated)
	if reconnect == nil || state.trustingHost || state.replacingHostKey {
		t.Fatal("successful replacement did not return to SSH reconnect flow")
	}
	entries, err = sshclient.ListKnownHostEntries()
	if err != nil || len(entries) != 1 || entries[0].Fingerprint != newFingerprint {
		t.Fatalf("replacement trust entry = %#v, err=%v", entries, err)
	}
}

func sshHostKeyManagerInput(t *testing.T, state modelState, value string) (modelState, tea.Cmd) {
	t.Helper()
	runes := []rune(value)
	if len(runes) == 0 {
		t.Fatal("test input is empty")
	}
	updated, command := state.updateSSHHostKeyManagerKey(tea.KeyPressMsg(tea.Key{Code: runes[0], Text: value}))
	return unwrapModel(t, updated), command
}

func sshHostKeyEnterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
}
