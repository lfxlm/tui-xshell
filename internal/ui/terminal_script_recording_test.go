package ui

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

func TestRecordTerminalScriptCommandQuotesPromptAndSkipsSensitiveInput(t *testing.T) {
	pane := &terminalPane{
		host:              model.Host{Name: "dev"},
		terminal:          vt.NewSafeEmulator(80, 10),
		commandEchoPrompt: "user@host:~/work$ ",
		scriptRecording:   &terminalScriptRecording{},
	}
	if !recordTerminalScriptCommand(pane, "echo [hello]") {
		t.Fatal("ordinary shell command was not recorded")
	}
	step := pane.scriptRecording.steps[0]
	if step.Send != "echo [hello]" || step.Expect != regexp.QuoteMeta("user@host:~/work$") {
		t.Fatalf("recorded step = %#v, want a literal shell prompt and command", step)
	}
	if !regexp.MustCompile(step.Expect).MatchString("user@host:~/work$") {
		t.Fatalf("recorded expect %q does not match the captured prompt", step.Expect)
	}

	secretPane := &terminalPane{
		terminal:          vt.NewSafeEmulator(80, 10),
		commandEchoPrompt: "user@host:~$ ",
		scriptRecording:   &terminalScriptRecording{},
	}
	_, _ = secretPane.terminal.Write([]byte("Password: "))
	if recordTerminalScriptCommand(secretPane, "not-a-command") {
		t.Fatal("input at a password prompt was recorded")
	}
	if !secretPane.scriptRecording.skippedSensitive || len(secretPane.scriptRecording.steps) != 0 {
		t.Fatalf("sensitive prompt state = %#v, want skipped flag and no steps", secretPane.scriptRecording)
	}
}

func TestSaveTerminalScriptRecordingCreatesPrivateReusableUniqueFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "recordings")
	now := time.Date(2026, time.October, 21, 12, 34, 56, 0, time.UTC)
	recording := &terminalScriptRecording{steps: []startupScriptStep{{
		Expect: regexp.QuoteMeta("user@host:~$"), Send: "pwd", TimeoutSeconds: startupScriptDefaultTimeout,
	}}}
	first, err := saveTerminalScriptRecordingToDir(directory, "../../production host", recording, now)
	if err != nil {
		t.Fatalf("save first recording: %v", err)
	}
	second, err := saveTerminalScriptRecordingToDir(directory, "../../production host", recording, now)
	if err != nil {
		t.Fatalf("save second recording: %v", err)
	}
	if filepath.Dir(first) != directory || filepath.Dir(second) != directory || strings.Contains(filepath.Base(first), "..") {
		t.Fatalf("recording paths escaped or used unsafe names: %q, %q", first, second)
	}
	if filepath.Base(first) == filepath.Base(second) {
		t.Fatalf("same-second recordings collided: %q", first)
	}
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat saved recording: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("recording mode = %04o, want 0600", info.Mode().Perm())
		}
		if _, err := loadStartupLoginScript(path); err != nil {
			t.Errorf("saved recording is not a valid login script: %v", err)
		}
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o700 {
		if err != nil {
			t.Fatalf("stat recording directory: %v", err)
		}
		t.Errorf("recording directory mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestStopTerminalScriptRecordingKeepsRecordingWhenSaveFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("get temporary config directory: %v", err)
	}
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatalf("create temporary config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "tui-ssh-workbench"), []byte("block directory"), 0o600); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	pane := &terminalPane{
		host:            model.Host{Name: "dev"},
		scriptRecording: &terminalScriptRecording{steps: []startupScriptStep{{Expect: "prompt", Send: "pwd"}}},
	}
	if _, _, err := stopTerminalScriptRecording(pane); err == nil {
		t.Fatal("stop unexpectedly succeeded despite an unwritable recording path")
	}
	if pane.scriptRecording == nil || len(pane.scriptRecording.steps) != 1 {
		t.Fatalf("recording was lost after save failure: %#v", pane.scriptRecording)
	}
}

func TestCloseActivePaneSavesScriptRecording(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "dev"}, session: &workspaceTestSession{},
		scriptRecording: &terminalScriptRecording{steps: []startupScriptStep{{Expect: "prompt", Send: "pwd"}}},
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{tabs: []*workspaceTab{tab}, activeTabID: tab.id, workspaceVisible: true}
	_ = state.closeActivePane()
	if len(state.tabs) != 0 {
		t.Fatalf("closed pane's tab remains: %d tabs", len(state.tabs))
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("get temporary config directory: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(configDirectory, "tui-ssh-workbench", "scripts", "recordings"))
	if err != nil {
		t.Fatalf("read saved recordings: %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".json") {
		t.Fatalf("recordings after closing pane = %#v, want one JSON file", entries)
	}
}

func TestDisconnectSavesScriptRecording(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "dev", Kind: model.HostKindSSH},
		session: session, terminal: vt.NewSafeEmulator(80, 10),
		scriptRecording: &terminalScriptRecording{steps: []startupScriptStep{{Expect: "prompt", Send: "pwd"}}},
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{tabs: []*workspaceTab{tab}, activeTabID: tab.id}
	updated, _ := state.Update(terminalOutputMsg{session: session, err: io.EOF})
	_ = unwrapModel(t, updated)
	if pane.scriptRecording != nil {
		t.Fatal("script recording remained active after successful disconnect save")
	}
	if !strings.Contains(pane.statusText, "脚本已保存：") {
		t.Fatalf("disconnect status = %q, want saved script path", pane.statusText)
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("get temporary config directory: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(configDirectory, "tui-ssh-workbench", "scripts", "recordings"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("saved recordings = %d entries, err=%v; want one", len(entries), err)
	}
}
