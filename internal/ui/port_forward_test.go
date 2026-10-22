package ui

import (
	"context"
	"net"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sshclient"
	"tui-ssh-workbench/internal/model"
)

func TestPortForwardCanStartFromTrackedSSHSession(t *testing.T) {
	sshSession := &sshclient.Session{}
	pane := &terminalPane{
		id:      "pane-1",
		host:    model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "staging"},
		session: trackTerminalSession(sshSession),
	}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{
		activeTabID: tab.id,
		tabs:        []*workspaceTab{tab},
		portForwardManager: &portForwardManagerState{
			paneID: "pane-1", localAddress: "127.0.0.1:8080", remote: "127.0.0.1:80",
		},
	}

	command := state.startPortForward()
	if command == nil || !state.portForwardManager.starting {
		t.Fatal("port forwarding did not accept the tracked SSH session")
	}
}

func TestPortForwardManagerCanEditAddressesAndRender(t *testing.T) {
	pane := &terminalPane{id: "pane-1", host: model.Host{Kind: model.HostKindSSH, Name: "staging"}}
	state := modelState{
		width:            100,
		height:           28,
		workspaceVisible: true,
		tabs: []*workspaceTab{{
			id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id,
		}},
		activeTabID:        "tab-1",
		portForwardManager: &portForwardManagerState{paneID: pane.id},
	}

	if view := state.portForwardManagerView(); !strings.Contains(view, "staging") || !strings.Contains(view, "当前没有运行中的转发") || !strings.Contains(view, "活动通道 · 0") {
		t.Fatalf("port forward manager view is missing its host or empty state:\n%s", view)
	}
	state = updateModelWithKey(t, state, "n")
	if !state.portForwardManager.editing || state.portForwardManager.localAddress != "127.0.0.1:8080" {
		t.Fatalf("N did not open the new-forward form: %#v", state.portForwardManager)
	}
	updated, _ := state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	state = unwrapModel(t, updated)
	if state.portForwardManager.field != 1 {
		t.Fatal("Tab did not move focus to the remote destination")
	}
	state = updateModelWithKey(t, state, "9")
	if state.portForwardManager.remote != "127.0.0.1:809" {
		t.Fatalf("remote address input = %q, want appended digit", state.portForwardManager.remote)
	}
	updated, _ = state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.portForwardManager == nil || state.portForwardManager.editing {
		t.Fatal("Esc did not leave address editing")
	}
	updated, _ = state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.portForwardManager != nil {
		t.Fatal("Esc did not close the port forward manager")
	}
}

func TestPortForwardStatsRefreshStopsWhenManagerCloses(t *testing.T) {
	state := modelState{portForwardManager: &portForwardManagerState{paneID: "pane-1"}}
	updated, command := state.Update(portForwardStatsTickMsg{paneID: "pane-1"})
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("open port-forward manager did not schedule another stats refresh")
	}
	state.portForwardManager = nil
	updated, command = state.Update(portForwardStatsTickMsg{paneID: "pane-1"})
	state = unwrapModel(t, updated)
	if command != nil || state.portForwardManager != nil {
		t.Fatal("stats refresh continued after its manager closed")
	}
}

func TestPortForwardManagerCanConfigureSOCKS5Proxy(t *testing.T) {
	pane := &terminalPane{id: "pane-1", host: model.Host{Kind: model.HostKindSSH, Name: "gateway"}}
	state := modelState{
		width: 100, height: 28,
		tabs:               []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}}},
		activeTabID:        "tab-1",
		portForwardManager: &portForwardManagerState{paneID: pane.id},
	}
	state = updateModelWithKey(t, state, "s")
	if manager := state.portForwardManager; !manager.editing || manager.kind != sshclient.ForwardSOCKS5 || manager.localAddress != "127.0.0.1:1080" {
		t.Fatalf("S did not open the SOCKS5 setup form: %#v", manager)
	}
	view := state.portForwardManagerView()
	if !strings.Contains(view, "SOCKS5 动态转发") || !strings.Contains(view, "127.0.0.1:1080") {
		t.Fatalf("SOCKS5 form does not show its listener configuration:\n%s", view)
	}
	updated, _ := state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.portForwardManager == nil || state.portForwardManager.editing {
		t.Fatal("Esc did not leave the SOCKS5 setup form")
	}
}

func TestPortForwardManagerCanConfigureRemoteForward(t *testing.T) {
	pane := &terminalPane{id: "pane-1", host: model.Host{Kind: model.HostKindSSH, Name: "gateway"}}
	state := modelState{
		width: 100, height: 28,
		tabs:               []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}}},
		activeTabID:        "tab-1",
		portForwardManager: &portForwardManagerState{paneID: pane.id},
	}
	state = updateModelWithKey(t, state, "r")
	if manager := state.portForwardManager; !manager.editing || manager.kind != sshclient.ForwardRemoteTCP || manager.localAddress != "127.0.0.1:8080" || manager.remote != "127.0.0.1:22" {
		t.Fatalf("R did not open the remote-forward form: %#v", manager)
	}
	view := state.portForwardManagerView()
	if !strings.Contains(view, "新建远程端口转发") || !strings.Contains(view, "远程监听") || !strings.Contains(view, "本地目标") {
		t.Fatalf("remote-forward form does not explain both endpoints:\n%s", view)
	}
}

func TestPortForwardManagerShowsAndSelectsSavedRules(t *testing.T) {
	pane := &terminalPane{
		id:   "pane-1",
		host: model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "gateway"},
	}
	state := modelState{
		width: 100, height: 30,
		tabs:        []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}}},
		activeTabID: "tab-1",
		portForwardManager: &portForwardManagerState{
			paneID: pane.id,
			savedRules: []model.PortForwardRule{{
				Id: "rule-1", HostId: pane.host.Id, Kind: model.PortForwardLocalTCP,
				ListenAddress: "127.0.0.1:8080", RemoteAddress: "10.0.0.8:80",
			}},
		},
	}
	view := state.portForwardManagerView()
	if !strings.Contains(view, "已保存 · SSH 连接后自动恢复") || !strings.Contains(view, "10.0.0.8:80") {
		t.Fatalf("saved forwarding rule missing from manager view:\n%s", view)
	}

	updated, _ := state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	state = unwrapModel(t, updated)
	if !state.portForwardManager.focusChannels || state.portForwardManager.focusSaved {
		t.Fatal("first Tab did not switch focus to active channels")
	}
	updated, _ = state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	state = unwrapModel(t, updated)
	if !state.portForwardManager.focusSaved {
		t.Fatal("second Tab did not switch focus to the saved-rules list")
	}
	updated, command := state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Text: "x"}))
	state = unwrapModel(t, updated)
	if command != nil || state.portForwardManager.confirmDeleteRuleID != "rule-1" {
		t.Fatal("deleting a saved rule should ask for confirmation before starting storage I/O")
	}
	updated, _ = state.updatePortForwardKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.portForwardManager == nil || state.portForwardManager.confirmDeleteRuleID != "" {
		t.Fatal("Esc should cancel rule-deletion confirmation without closing the manager")
	}
}

func TestSavedPortForwardRulesRestoreIntoTheOwningPane(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	listenAddress := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release reserved local port: %v", err)
	}

	host := model.Host{Id: "host-1", Kind: model.HostKindSSH, Name: "gateway"}
	rule := model.PortForwardRule{
		Id: "rule-1", HostId: host.Id, Kind: model.PortForwardLocalTCP,
		ListenAddress: listenAddress, RemoteAddress: "127.0.0.1:22",
	}
	session := &sshclient.Session{}
	pane := &terminalPane{id: "pane-1", host: host, session: session}
	state := modelState{
		catalog: &portForwardCatalogStub{rules: []model.PortForwardRule{rule}},
		tabs:    []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}}},
	}
	command := state.restorePortForwardRules(pane.id, host.Id, session)
	if command == nil {
		t.Fatal("saved port-forward rules were not scheduled for restore")
	}
	rawMessage := command()
	message, ok := rawMessage.(portForwardRulesRestoredMsg)
	if !ok {
		t.Fatalf("restore command returned %T, want portForwardRulesRestoredMsg", rawMessage)
	}
	if message.err != nil || len(message.results) != 1 || message.results[0].err != nil {
		t.Fatalf("restore result = %#v", message)
	}
	updated, _ := state.Update(message)
	state = unwrapModel(t, updated)
	if len(pane.forwards) != 1 || !pane.forwards[0].Active() {
		t.Fatalf("restored forward was not attached to its pane: %#v", pane.forwards)
	}
	if !strings.Contains(pane.statusText, "已恢复 1 条") {
		t.Fatalf("pane status = %q, want a successful restore notice", pane.statusText)
	}
	if err := pane.forwards[0].Close(); err != nil {
		t.Errorf("close restored listener: %v", err)
	}
}

type portForwardCatalogStub struct {
	Catalog
	rules []model.PortForwardRule
}

func (s *portForwardCatalogStub) ListPortForwardRules(context.Context, string) ([]model.PortForwardRule, error) {
	return append([]model.PortForwardRule(nil), s.rules...), nil
}

func (*portForwardCatalogStub) SavePortForwardRule(context.Context, model.PortForwardRule) (model.PortForwardRule, error) {
	return model.PortForwardRule{}, nil
}

func (*portForwardCatalogStub) DeletePortForwardRule(context.Context, string) error {
	return nil
}

func TestDisconnectedPaneReconnectUsesSamePane(t *testing.T) {
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Id: "ssh-1", Kind: model.HostKindSSH, Name: "staging"},
	}
	state := modelState{
		width: 100, height: 28, workspaceVisible: true,
		tabs:        []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
		activeTabID: "tab-1",
	}
	updated, command := state.updateWorkspaceLeader(keyPress("r"))
	state = unwrapModel(t, updated)
	if command == nil || state.connectingHost == nil || state.connectionTarget == nil {
		t.Fatal("Ctrl+G then R did not begin reconnection")
	}
	if state.connectionTarget.kind != reconnectPaneTarget || state.connectionTarget.paneID != pane.id || state.connectionTarget.tabID != "tab-1" {
		t.Fatalf("reconnect target would replace the wrong workspace item: %#v", state.connectionTarget)
	}
	state.cancelSSHConnect()
}
