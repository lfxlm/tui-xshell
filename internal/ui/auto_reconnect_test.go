package ui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

type autoReconnectTestSession struct{}

func (*autoReconnectTestSession) Read([]byte) (int, error) { return 0, nil }
func (*autoReconnectTestSession) Write([]byte) error       { return nil }
func (*autoReconnectTestSession) Resize(int, int) error    { return nil }
func (*autoReconnectTestSession) Close() error             { return nil }

func TestAutoReconnectBackoffIsBounded(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for attempt, delay := range want {
		if got := autoReconnectDelay(attempt + 1); got != delay {
			t.Fatalf("attempt %d delay = %s, want %s", attempt+1, got, delay)
		}
	}
	if got := autoReconnectDelay(0); got != 0 {
		t.Fatalf("zero attempt delay = %s, want zero", got)
	}
	if got := autoReconnectDelay(maxAutoReconnectAttempts + 1); got != 0 {
		t.Fatalf("attempt past limit delay = %s, want zero", got)
	}
}

func TestToggleAutoReconnectArmsSSHPaneAndInvalidatesOldMessage(t *testing.T) {
	pane := &terminalPane{
		id:   "pane-1",
		host: model.Host{Id: "ssh-1", Kind: model.HostKindSSH, Name: "staging"},
	}
	state := modelState{
		width: 100, height: 28, workspaceVisible: true,
		tabs:        []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
		activeTabID: "tab-1",
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, command := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'z', Text: "z"}))
	state = unwrapModel(t, updated)
	if command != nil || !pane.autoReconnect || pane.reconnectAttempts != 0 {
		t.Fatalf("auto reconnect toggle: enabled=%v attempts=%d command=%v", pane.autoReconnect, pane.reconnectAttempts, command != nil)
	}
	if !strings.Contains(pane.statusText, "请按 Ctrl+G 后按 R") {
		t.Fatalf("status = %q, want manual connect hint for an already-disconnected pane", pane.statusText)
	}
	oldMessage := autoReconnectMsg{tabID: "tab-1", paneID: pane.id, generation: pane.reconnectGeneration}

	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'z', Text: "z"}))
	state = unwrapModel(t, updated)
	if pane.autoReconnect {
		t.Fatal("second toggle did not turn auto reconnect off")
	}
	_, staleCommand := state.Update(oldMessage)
	if staleCommand != nil {
		t.Fatal("stale retry message was not invalidated after disabling auto reconnect")
	}
}

func TestTransportLossSchedulesRetryOnlyForEnabledSSHPane(t *testing.T) {
	tests := []struct {
		name         string
		hostKind     model.HostKind
		auto         bool
		err          error
		wantAttempts int
	}{
		{name: "enabled SSH transport loss", hostKind: model.HostKindSSH, auto: true, err: sshclient.ErrTransportLost, wantAttempts: 1},
		{name: "disabled SSH transport loss", hostKind: model.HostKindSSH, err: sshclient.ErrTransportLost},
		{name: "clean shell exit", hostKind: model.HostKindSSH, auto: true, err: io.EOF},
		{name: "local shell failure", hostKind: model.HostKindLocal, auto: true, err: sshclient.ErrTransportLost},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &autoReconnectTestSession{}
			pane := &terminalPane{
				id: "pane-1", host: model.Host{Id: "host-1", Kind: test.hostKind, Name: "test"},
				session: session, terminal: vt.NewSafeEmulator(40, 10), autoReconnect: test.auto,
			}
			tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
			state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true}
			updated, command := state.Update(terminalOutputMsg{session: session, err: test.err})
			state = unwrapModel(t, updated)
			if pane.reconnectAttempts != test.wantAttempts {
				t.Fatalf("retry attempts = %d, want %d", pane.reconnectAttempts, test.wantAttempts)
			}
			if (command != nil) != (test.wantAttempts > 0) {
				t.Fatalf("retry command present = %v, want %v", command != nil, test.wantAttempts > 0)
			}
		})
	}
}

func TestAutoReconnectStopsAfterMaximumAttempts(t *testing.T) {
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Kind: model.HostKindSSH}, autoReconnect: true,
		reconnectAttempts: maxAutoReconnectAttempts,
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}}
	state := modelState{}
	if command := state.schedulePaneAutoReconnect(tab, pane); command != nil {
		t.Fatal("retry scheduled after reaching the maximum attempt count")
	}
	if !strings.Contains(pane.statusText, "5 次上限") {
		t.Fatalf("limit status = %q, want attempt limit message", pane.statusText)
	}
}
