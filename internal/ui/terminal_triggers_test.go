package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestTerminalTriggerMatchesANSIAndSplitOutputOnce(t *testing.T) {
	runtime := newTerminalTriggerRuntime(compileTerminalTriggers([]model.TerminalTriggerRule{{
		Name: "password", Pattern: "password:", IgnoreCase: true, Action: model.TerminalTriggerMessage, Text: "检测到密码提示",
	}}), 3)
	now := time.Unix(100, 0)
	if matches := runtime.match([]byte("\x1b[32mPass"), now); len(matches) != 0 {
		t.Fatalf("partial prompt triggered a rule: %#v", matches)
	}
	matches := runtime.match([]byte("word:\x1b[0m"), now)
	if len(matches) != 1 || matches[0].rule.name != "password" {
		t.Fatalf("split ANSI prompt matches = %#v, want one password trigger", matches)
	}
	if repeated := runtime.match([]byte("\r\n"), now); len(repeated) != 0 {
		t.Fatalf("existing match fired again on unrelated output: %#v", repeated)
	}
	if repeated := runtime.match([]byte("Password:"), now.Add(time.Second)); len(repeated) != 0 {
		t.Fatalf("cooldown did not suppress an immediate repeated trigger: %#v", repeated)
	}
	if later := runtime.match([]byte("Password:"), now.Add(3*time.Second)); len(later) != 1 {
		t.Fatalf("trigger did not resume after cooldown: %#v", later)
	}
}

func TestTerminalTriggerSendWritesEncodedTextAndOptionalEnter(t *testing.T) {
	session := &workspaceTestSession{}
	rule := model.TerminalTriggerRule{
		Name: "confirm", Pattern: `continue\?`, IgnoreCase: true, Action: model.TerminalTriggerSend, Text: "继续", SendEnter: true,
	}
	runtime := newTerminalTriggerRuntime(compileTerminalTriggers([]model.TerminalTriggerRule{rule}), 7)
	matches := runtime.match([]byte("Continue?"), time.Unix(100, 0))
	if len(matches) != 1 {
		t.Fatalf("trigger matches = %#v, want one", matches)
	}
	command := terminalTriggerActionsCmd(session, "pane-1", runtime, model.TerminalEncodingUTF8, matches)
	if command == nil {
		t.Fatal("send trigger did not return a command")
	}
	message, ok := command().(terminalTriggerSentMsg)
	if !ok || message.err != nil || message.paneID != "pane-1" || message.generation != 7 {
		t.Fatalf("send result = %#v", message)
	}
	if len(session.writes) != 1 || string(session.writes[0]) != "继续\r" {
		t.Fatalf("trigger writes = %q, want UTF-8 text followed by CR", session.writes)
	}
}

func TestTerminalTriggerDoesNotInterleaveWithStartupScript(t *testing.T) {
	session := &workspaceTestSession{}
	runtime := newTerminalTriggerRuntime(compileTerminalTriggers([]model.TerminalTriggerRule{{
		Name: "confirm", Pattern: "continue", Action: model.TerminalTriggerSend, Text: "yes", SendEnter: true,
	}}), 1)
	pane := &terminalPane{
		id: "pane-1", session: session, startupScript: &startupScriptRuntime{}, triggerRuntime: runtime,
		terminalInputEncoding: model.TerminalEncodingUTF8,
	}
	state := modelState{config: model.DefaultConfig()}
	matches := runtime.match([]byte("continue"), time.Unix(100, 0))
	if command := state.handleTerminalTriggerMatches(pane, matches, []byte("continue")); command != nil {
		t.Fatal("startup script suppression unexpectedly returned a send command")
	}
	if len(session.writes) != 0 {
		t.Fatalf("trigger interleaved with startup script: writes = %q", session.writes)
	}
}

func TestTerminalTriggerGreedyMatchDoesNotRefireAsItGrows(t *testing.T) {
	rule := compiledTerminalTrigger{
		name: "greedy", pattern: regexp.MustCompile(`start.*end`), action: model.TerminalTriggerMessage, text: "done",
	}
	runtime := newTerminalTriggerRuntime([]compiledTerminalTrigger{rule}, 1)
	now := time.Unix(100, 0)
	if got := runtime.match([]byte("start one end"), now); len(got) != 1 {
		t.Fatalf("initial greedy match count = %d, want 1", len(got))
	}
	if got := runtime.match([]byte(" then end"), now.Add(3*time.Second)); len(got) != 0 {
		t.Fatalf("growing greedy match fired again: %#v", got)
	}
}

func TestTerminalTriggerScriptLoadsAndRunsPromptSteps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "confirm.json")
	content := `{"version":1,"steps":[{"expect":"Continue\\?","send":"yes","timeout_seconds":10}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, ok := terminalTriggerLoadScriptCmd("pane-1", 9, "confirm", path)().(terminalTriggerScriptLoadedMsg)
	if !ok || loaded.err != nil || loaded.script == nil {
		t.Fatalf("loaded trigger script = %#v", loaded)
	}
	session := &workspaceTestSession{}
	runtime := newTerminalTriggerRuntime(compileTerminalTriggers([]model.TerminalTriggerRule{{
		Name: "confirm", Pattern: "Are you ready", Action: model.TerminalTriggerScript, ScriptPath: path,
	}}), 9)
	pane := &terminalPane{
		id: "pane-1", session: session, triggerRuntime: runtime, triggerScriptLoading: true,
		triggerScriptOutput: []byte("Continue?"), terminalInputEncoding: model.TerminalEncodingUTF8,
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{config: model.DefaultConfig(), tabs: []*workspaceTab{tab}, activeTabID: tab.id}
	updated, timeout := state.Update(loaded)
	state = unwrapModel(t, updated)
	if pane.startupScript == nil || pane.startupScript.stepIndex != 0 || !pane.startupScript.sendPending || timeout == nil || pane.triggerScriptLoading {
		t.Fatalf("trigger script was not started: runtime=%#v loading=%t timeout=%t", pane.startupScript, pane.triggerScriptLoading, timeout != nil)
	}
	send := sendStartupScriptStepCmd(pane.session, pane.id, pane.startupScript, pane.host.User, pane.host.CredentialRef, pane.terminalInputEncoding, state.credentials)
	sent, ok := send().(startupScriptStepSentMsg)
	if !ok || sent.err != nil || string(session.writes[0]) != "yes\r" {
		t.Fatalf("script send result = %#v; writes = %q", sent, session.writes)
	}
	updated, _ = state.Update(sent)
	state = unwrapModel(t, updated)
	if pane.startupScript != nil || pane.statusText != "提示符登录脚本已完成" {
		t.Fatalf("trigger script completion state = %#v, status=%q", pane.startupScript, pane.statusText)
	}
}
