package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	"tui-ssh-workbench/internal/model"
)

func TestLoadStartupLoginScriptValidatesAndDefaultsTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "login.json")
	data := `{"version":1,"steps":[{"expect":"(?i)password:","send":"{{password}}"},{"expect":"[$#] $","send":"{{user}}"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	script, err := loadStartupLoginScript(path)
	if err != nil {
		t.Fatalf("load script: %v", err)
	}
	if script == nil || len(script.steps) != 2 || script.steps[0].TimeoutSeconds != startupScriptDefaultTimeout || !script.steps[0].matcher.MatchString("Password:") {
		t.Fatalf("loaded script = %#v, want compiled steps with default timeout", script)
	}
}

func TestLoadStartupLoginScriptRejectsUnsafeOrInvalidSteps(t *testing.T) {
	tooManySteps := `{"version":1,"steps":[` + strings.TrimSuffix(strings.Repeat(`{"expect":"prompt","send":"next"},`, maxStartupScriptSteps+1), ",") + `]}`
	for name, content := range map[string]string{
		"unknown placeholder": `{"version":1,"steps":[{"expect":"prompt","send":"{{token}}"}]}`,
		"invalid regex":       `{"version":1,"steps":[{"expect":"[","send":""}]}`,
		"multiline send":      `{"version":1,"steps":[{"expect":"prompt","send":"first\nsecond"}]}`,
		"too many steps":      tooManySteps,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write script: %v", err)
			}
			if _, err := loadStartupLoginScript(path); err == nil {
				t.Fatal("invalid script unexpectedly loaded")
			}
		})
	}
}

func TestANSITextStripperHandlesSplitEscapeSequences(t *testing.T) {
	var stripper ansiTextStripper
	first := stripper.write([]byte("\x1b[31mPass"))
	second := stripper.write([]byte("word:\x1b[0m"))
	if got := first + second; got != "Password:" {
		t.Fatalf("stripped split ANSI text = %q, want %q", got, "Password:")
	}
}

func TestStartupScriptWaitsForPromptAndReadsPasswordFromVault(t *testing.T) {
	session := &workspaceTestSession{}
	vault := &memoryCredentialVault{values: map[string]string{"keychain-ref": "secret-value"}}
	firstStep := startupScriptStep{
		Expect: "Password:", Send: "{{password}}", TimeoutSeconds: 10,
		matcher: regexp.MustCompile(`Password:`),
	}
	secondStep := startupScriptStep{
		Expect: "continue?", Send: "yes", TimeoutSeconds: 10,
		matcher: regexp.MustCompile(`continue\?`),
	}
	runtime := newStartupScriptRuntime(&startupLoginScript{steps: []startupScriptStep{firstStep, secondStep}}, 4)
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Kind: model.HostKindSSH, User: "operator", CredentialRef: "keychain-ref"},
		session: session, startupScript: runtime,
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{credentials: vault, tabs: []*workspaceTab{tab}, activeTabID: tab.id}
	if command := state.matchStartupScriptOutput(pane, []byte("\x1b[32mPass")); command != nil {
		t.Fatal("partial prompt triggered a response")
	}
	command := state.matchStartupScriptOutput(pane, []byte("word:\x1b[0m"))
	if command == nil {
		t.Fatal("complete prompt did not trigger a response")
	}
	if repeated := state.matchStartupScriptOutput(pane, []byte("Password:")); repeated != nil {
		t.Fatal("repeated prompt triggered a second response while send was pending")
	}
	if deferred := state.matchStartupScriptOutput(pane, []byte("continue?")); deferred != nil {
		t.Fatal("next prompt was sent before the previous response completed")
	}
	message, ok := command().(startupScriptStepSentMsg)
	if !ok || message.err != nil || message.generation != 4 || message.stepIndex != 0 {
		t.Fatalf("send result = %#v", message)
	}
	if len(session.writes) != 1 || string(session.writes[0]) != "secret-value\r" {
		t.Fatalf("script writes = %q, want keychain secret followed by CR", session.writes)
	}
	updated, nextCommand := state.Update(message)
	state = unwrapModel(t, updated)
	if pane.startupScript == nil || pane.startupScript.stepIndex != 1 || !pane.startupScript.sendPending || nextCommand == nil {
		t.Fatalf("buffered prompt did not advance safely: runtime=%#v command=%v", pane.startupScript, nextCommand != nil)
	}
	nextMessage, ok := nextCommand().(startupScriptStepSentMsg)
	if !ok || nextMessage.err != nil || len(session.writes) != 2 || string(session.writes[1]) != "yes\r" {
		t.Fatalf("deferred next-step send = %#v writes=%q", nextMessage, session.writes)
	}
}

func TestStartupScriptTimeoutStopsOnlyCurrentStep(t *testing.T) {
	session := &workspaceTestSession{}
	step := startupScriptStep{Expect: ".+", Send: "next", matcher: regexp.MustCompile(`.+`)}
	pane := &terminalPane{
		id: "pane-1", session: session,
		startupScript: newStartupScriptRuntime(&startupLoginScript{steps: []startupScriptStep{step}}, 9),
		terminal:      vt.NewSafeEmulator(80, 24),
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}}
	state := modelState{tabs: []*workspaceTab{tab}, activeTabID: tab.id}
	updated, _ := state.Update(startupScriptTimeoutMsg{paneID: pane.id, generation: 8, stepIndex: 0})
	state = unwrapModel(t, updated)
	if pane.startupScript == nil || strings.Contains(pane.statusText, "超时") {
		t.Fatal("stale timeout stopped the active script")
	}
	updated, _ = state.Update(startupScriptTimeoutMsg{paneID: pane.id, generation: 9, stepIndex: 0})
	state = unwrapModel(t, updated)
	if pane.startupScript != nil || !strings.Contains(pane.statusText, "第 1 步超时") {
		t.Fatalf("current timeout state = script:%v status:%q", pane.startupScript != nil, pane.statusText)
	}
}
