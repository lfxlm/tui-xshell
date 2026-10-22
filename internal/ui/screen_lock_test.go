package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func TestScreenLockHashesAndConstantTimeVerifiesPassword(t *testing.T) {
	password := []byte("correct horse battery")
	hash, err := hashScreenLockPassword(password)
	if err != nil {
		t.Fatalf("hash screen lock password: %v", err)
	}
	if string(password) != strings.Repeat("\x00", len(password)) {
		t.Fatal("password byte buffer was not cleared after hashing")
	}
	valid, err := verifyScreenLockPassword(hash, []byte("correct horse battery"))
	if err != nil || !valid {
		t.Fatalf("verify correct password = %v, %v", valid, err)
	}
	valid, err = verifyScreenLockPassword(hash, []byte("incorrect password"))
	if err != nil || valid {
		t.Fatalf("verify incorrect password = %v, %v; want false", valid, err)
	}
	if _, err := hashScreenLockPassword([]byte("short")); err == nil {
		t.Fatal("short screen lock password was accepted")
	}
	if _, err := verifyScreenLockPassword("invalid", []byte("correct horse battery")); err == nil {
		t.Fatal("malformed keyring value was accepted")
	}
}

func TestScreenLockSetupLocksAndBlocksTerminalInputUntilUnlock(t *testing.T) {
	password := "correct horse battery"
	vault := &memoryCredentialVault{}
	session := &workspaceTestSession{}
	terminal := vt.NewSafeEmulator(40, 8)
	_, _ = terminal.Write([]byte("secret remote output"))
	pane := &terminalPane{id: "pane", session: session, terminal: terminal}
	tab := &workspaceTab{id: "tab", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{
		credentials: vault, config: model.DefaultConfig(), width: 80, height: 24,
		workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}
	command := state.beginScreenLock(screenLockPurposeLock)
	if command == nil {
		t.Fatal("setting up the first screen lock did not read the keyring")
	}
	loaded, ok := command().(screenLockLoadedMsg)
	if !ok {
		t.Fatalf("screen lock load message = %#v", loaded)
	}
	state.handleScreenLockLoaded(loaded)
	if state.screenLock == nil || state.screenLock.phase != screenLockNewPassword || !state.screenLock.lockOnSave {
		t.Fatalf("initial lock state = %#v, want password setup", state.screenLock)
	}
	for _, character := range password {
		state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	saveCommand := state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if saveCommand != nil || state.screenLock.phase != screenLockConfirmPassword {
		t.Fatal("password confirmation step did not open")
	}
	for _, character := range password {
		state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	saveCommand = state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if saveCommand == nil {
		t.Fatal("confirmed password did not start keyring save")
	}
	saved, ok := saveCommand().(screenLockSavedMsg)
	if !ok {
		t.Fatalf("screen lock save message = %#v", saved)
	}
	state.handleScreenLockSaved(saved)
	if state.screenLock == nil || state.screenLock.phase != screenLockLocked {
		t.Fatalf("screen lock after save = %#v, want locked", state.screenLock)
	}
	if vault.values[screenLockCredentialRef] == password || vault.values[screenLockCredentialRef] == "" {
		t.Fatal("screen lock password was not stored as a derived verifier")
	}
	if view := state.view(); strings.Contains(view, "secret remote output") {
		t.Fatalf("locked screen exposed remote terminal output:\n%s", view)
	}
	state.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	state.Update(tea.PasteMsg{Content: "exit\r"})
	state.Update(tea.ClipboardMsg{Content: "clipboard"})
	if len(session.writes) != 0 || state.screenLock == nil || state.screenLock.phase != screenLockLocked {
		t.Fatalf("locked input reached terminal or unlocked it: writes=%q state=%#v", session.writes, state.screenLock)
	}
	state.screenLock.input = ""
	for _, character := range password {
		state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	verifyCommand := state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if verifyCommand == nil {
		t.Fatal("correct unlock password did not start verification")
	}
	verified, ok := verifyCommand().(screenLockVerifiedMsg)
	if !ok || verified.err != nil || !verified.valid {
		t.Fatalf("screen lock verification = %#v", verified)
	}
	state.handleScreenLockVerified(verified)
	if state.screenLock != nil || state.statusText != "会话屏幕已解锁" {
		t.Fatalf("screen lock state after unlock = %#v, status=%q", state.screenLock, state.statusText)
	}
}

func TestScreenLockAutomaticExpiryHonorsRecentActivity(t *testing.T) {
	state := modelState{
		config:         model.Config{Security: model.SecurityConfig{AutoLockMinutes: 1}},
		screenLockHash: "scrypt-v1$stored$hash", lastActivity: time.Now().Add(-2 * time.Minute),
	}
	state.handleScreenLockIdleTick()
	if state.screenLock == nil || state.screenLock.phase != screenLockLocked {
		t.Fatalf("idle expiry state = %#v, want locked", state.screenLock)
	}
	state.screenLock = nil
	state.screenLockHash = "stored-hash"
	state.screenLockLoaded = true
	state.lastActivity = time.Now()
	state.handleScreenLockIdleTick()
	if state.screenLock != nil {
		t.Fatal("recent activity did not prevent automatic screen lock")
	}
}

func TestScreenLockPasswordCanBeVerifiedAndRemoved(t *testing.T) {
	const password = "correct horse battery"
	hash, err := hashScreenLockPassword([]byte(password))
	if err != nil {
		t.Fatal(err)
	}
	vault := &memoryCredentialVault{values: map[string]string{screenLockCredentialRef: hash}}
	state := modelState{credentials: vault, config: model.DefaultConfig(), screenLockLoaded: true, screenLockHash: hash}
	state.beginScreenLock(screenLockPurposeChange)
	if state.screenLock.phase != screenLockVerifyPassword {
		t.Fatalf("password change phase = %d, want verify current password", state.screenLock.phase)
	}
	for _, character := range password {
		state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
	}
	verifyCommand := state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if verifyCommand == nil {
		t.Fatal("current password verification did not start")
	}
	verified, ok := verifyCommand().(screenLockVerifiedMsg)
	if !ok || verified.err != nil || !verified.valid {
		t.Fatalf("current password verification = %#v", verified)
	}
	state.handleScreenLockVerified(verified)
	if state.screenLock.phase != screenLockNewPassword {
		t.Fatalf("phase after verifying current password = %d, want new password", state.screenLock.phase)
	}
	deleteCommand := state.updateScreenLockKey(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
	if deleteCommand == nil || state.screenLock.phase != screenLockBusy {
		t.Fatal("Ctrl+D did not start lock password removal")
	}
	deleted, ok := deleteCommand().(screenLockDeletedMsg)
	if !ok || deleted.err != nil {
		t.Fatalf("screen lock delete message = %#v", deleted)
	}
	state.handleScreenLockDeleted(deleted)
	if state.screenLock != nil || state.screenLockHash != "" {
		t.Fatalf("state after removing lock password: lock=%#v hash=%q", state.screenLock, state.screenLockHash)
	}
	if _, exists := vault.values[screenLockCredentialRef]; exists {
		t.Fatal("screen lock verifier remained in the keyring after removal")
	}
}

func TestScreenLockIsAvailableFromWorkspaceShortcutAndPaneMenu(t *testing.T) {
	config := model.DefaultConfig()
	if got := config.Workspace.Bindings["screen_lock"]; got != "ctrl+l" {
		t.Fatalf("default screen lock binding = %q, want ctrl+l", got)
	}
	if got := config.Workspace.Bindings["screen_lock_password"]; got != "ctrl+p" {
		t.Fatalf("default screen lock password binding = %q, want ctrl+p", got)
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane", session: session}
	tab := &workspaceTab{id: "tab", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{
		credentials: &memoryCredentialVault{}, config: config,
		workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, loadCommand := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'l', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if loadCommand == nil || state.screenLock == nil || state.screenLock.phase != screenLockLoading {
		t.Fatal("Ctrl+G then Ctrl+L did not start the screen lock flow")
	}
	state.screenLock = nil
	state.contextMenu = &contextMenuState{target: paneMenuTarget, tabID: tab.id, paneID: pane.id}
	labels := make(map[string]bool)
	for _, item := range state.contextMenuItems() {
		labels[item.label] = true
	}
	if !labels["锁定工作区屏幕  Ctrl+G 后按 Ctrl+L"] || !labels["锁屏密码设置  Ctrl+G 后按 Ctrl+P"] {
		t.Fatalf("pane menu is missing screen lock actions: %#v", labels)
	}
}
