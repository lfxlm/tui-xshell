package ui

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/model"
)

func TestTerminalKeyBytesUseConfiguredFunctionAndEditingSequences(t *testing.T) {
	keyboard := model.TerminalKeyboardConfig{
		FunctionKeys:      map[string]string{"f1": "\x1b[99~", "f20": ""},
		BackspaceSequence: "\x15",
		DeleteSequence:    "\x1b[3;5~",
		MetaKeyEmulation:  model.TerminalMetaOff,
	}
	tests := []struct {
		name string
		key  tea.Key
		want []byte
	}{
		{name: "mapped function key", key: tea.Key{Code: tea.KeyF1}, want: []byte("\x1b[99~")},
		{name: "unmapped function key keeps xterm default", key: tea.Key{Code: tea.KeyF2}, want: []byte("\x1bOQ")},
		{name: "F20 can be disabled", key: tea.Key{Code: tea.KeyF20}, want: nil},
		{name: "custom backspace", key: tea.Key{Code: tea.KeyBackspace}, want: []byte("\x15")},
		{name: "custom delete", key: tea.Key{Code: tea.KeyDelete}, want: []byte("\x1b[3;5~")},
		{name: "Meta can be disabled", key: tea.Key{Code: 'x', Text: "x", Mod: tea.ModAlt}, want: []byte("x")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalKeyBytesWithKeyboard(tea.KeyPressMsg(test.key), false, keyboard); !bytes.Equal(got, test.want) {
				t.Fatalf("terminal key bytes = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalMetaPrefixRemainsEnabledByDefault(t *testing.T) {
	got := terminalKeyBytesWithKeyboard(
		tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x", Mod: tea.ModAlt}),
		false,
		model.TerminalKeyboardConfig{},
	)
	if !bytes.Equal(got, []byte("\x1bx")) {
		t.Fatalf("default Alt/Meta bytes = %q, want %q", got, "\x1bx")
	}
}

func TestWorkspaceUsesConfiguredTerminalKeySequences(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "server", Kind: model.HostKindSSH},
		terminalEncoding: "GB18030", session: session,
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{
		config: model.DefaultConfig(), width: 80, height: 20,
		workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}
	state.config.Terminal.Keyboard.FunctionKeys["f13"] = "echo 中文\r"
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF13}))
	state = unwrapModel(t, updated)
	want, err := encodeTerminalText("GB18030", []byte("echo 中文\r"))
	if err != nil {
		t.Fatal(err)
	}
	if len(session.writes) != 1 || !bytes.Equal(session.writes[0], want) {
		t.Fatalf("workspace wrote %#v for configured F13; want %q", session.writes, want)
	}
}
