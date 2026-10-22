package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
)

func TestSystemClipboardCommandCandidates(t *testing.T) {
	tests := []struct {
		goos string
		want []systemClipboardCommand
	}{
		{goos: "darwin", want: []systemClipboardCommand{{name: "pbcopy"}}},
		{goos: "windows", want: []systemClipboardCommand{{name: "clip"}}},
		{goos: "linux", want: []systemClipboardCommand{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard", "-in"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}},
		{goos: "freebsd", want: nil},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			if got := systemClipboardCommands(test.goos); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("clipboard commands = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestWriteSystemClipboardRetriesAvailableLinuxTools(t *testing.T) {
	var calls []systemClipboardCommand
	err := writeSystemClipboard("linux", "selected text", func(_ context.Context, command systemClipboardCommand, text string) error {
		calls = append(calls, command)
		if text != "selected text" {
			t.Fatalf("clipboard text = %q", text)
		}
		if command.name == "wl-copy" {
			return errors.New("no Wayland clipboard")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writeSystemClipboard returned error: %v", err)
	}
	if len(calls) != 2 || calls[0].name != "wl-copy" || calls[1].name != "xclip" {
		t.Fatalf("clipboard commands tried = %#v, want wl-copy followed by xclip", calls)
	}
}

func TestWriteSystemClipboardReportsMissingNativeTools(t *testing.T) {
	err := writeSystemClipboard("linux", "text", func(context.Context, systemClipboardCommand, string) error {
		return errors.New("command unavailable")
	})
	if err == nil {
		t.Fatal("writeSystemClipboard succeeded when every clipboard tool failed")
	}
	for _, name := range []string{"wl-copy", "xclip", "xsel"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("clipboard error %q does not identify %s", err, name)
		}
	}
}

func TestTerminalClipboardCopyShortcuts(t *testing.T) {
	for _, test := range []struct {
		name string
		key  tea.Key
	}{
		{name: "control shift c", key: tea.Key{Code: 'c', Mod: tea.ModCtrl | tea.ModShift}},
		{name: "command shift c", key: tea.Key{Code: 'c', Mod: tea.ModSuper | tea.ModShift}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &workspaceTestSession{}
			terminal := vt.NewSafeEmulator(20, 4)
			_, _ = terminal.Write([]byte("copy me"))
			pane := &terminalPane{id: "pane-1", session: session, terminal: terminal}
			tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
			state := modelState{width: 20, height: 8, workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}
			state.updateWorkspaceMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift}))
			state.updateWorkspaceMouse(tea.MouseMotionMsg(tea.Mouse{X: 7, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift}))
			state.updateWorkspaceMouse(tea.MouseReleaseMsg(tea.Mouse{X: 7, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift}))

			updated, cmd := state.updateWorkspaceKey(tea.KeyPressMsg(test.key))
			state = unwrapModel(t, updated)
			if cmd == nil || state.statusText != "正在复制选区…" {
				t.Fatalf("copy shortcut did not start clipboard copy: cmd=%v status=%q", cmd != nil, state.statusText)
			}
			if len(session.writes) != 0 {
				t.Fatalf("copy shortcut leaked to terminal: %q", session.writes)
			}
			updated, _ = state.Update(terminalClipboardCopyCompletedMsg{text: "copy me"})
			state = unwrapModel(t, updated)
			if state.statusText != "已复制到系统剪贴板" {
				t.Fatalf("successful native copy status = %q", state.statusText)
			}
		})
	}
}

func TestTerminalClipboardCopyFallsBackToOSC52(t *testing.T) {
	state := modelState{}
	updated, cmd := state.Update(terminalClipboardCopyCompletedMsg{text: "copy me", err: errors.New("pbcopy failed")})
	state = unwrapModel(t, updated)
	if cmd == nil {
		t.Fatal("native clipboard failure did not fall back to OSC 52")
	}
	if state.statusText != "本机剪贴板不可用，已尝试 OSC 52 复制" || state.statusErr {
		t.Fatalf("fallback status = %q, error=%v", state.statusText, state.statusErr)
	}
}

func TestPaneContextMenuOffersCopyForSelection(t *testing.T) {
	terminal := vt.NewSafeEmulator(20, 4)
	_, _ = terminal.Write([]byte("copy me"))
	pane := &terminalPane{id: "pane-1", terminal: terminal, session: &workspaceTestSession{}}
	pane.selection = &terminalSelection{
		rows:   snapshotTerminalViewport(pane, 4),
		anchor: terminalSelectionPoint{row: 0, col: 0},
		focus:  terminalSelectionPoint{row: 0, col: 6},
	}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{width: 80, height: 24, activeTabID: tab.id, tabs: []*workspaceTab{tab}}
	state.openPaneContextMenu(tab.id, pane.id, 10, 5)
	items := state.contextMenuItems()
	if len(items) == 0 || items[0].action != menuCopySelection || items[0].disabled {
		t.Fatalf("first pane menu item = %#v, want enabled copy selection", items)
	}
	updated, cmd := state.executeContextMenuAction(0)
	state = unwrapModel(t, updated)
	if cmd == nil || state.statusText != "正在复制选区…" {
		t.Fatalf("pane menu copy did not request clipboard: cmd=%v status=%q", cmd != nil, state.statusText)
	}
}
