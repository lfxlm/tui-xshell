package ui

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

type recentTrackingSession struct{}

func (recentTrackingSession) Read([]byte) (int, error) { return 0, io.EOF }
func (recentTrackingSession) Write([]byte) error       { return nil }
func (recentTrackingSession) Resize(int, int) error    { return nil }
func (recentTrackingSession) Close() error             { return nil }

func TestOnlySuccessfulTerminalConnectionsAreRecordedAsRecent(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "test", IP: "192.0.2.8", Port: 22})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	target := connectionTarget{kind: newTabTarget, tabID: "tab-1", paneID: "pane-1", width: 80, height: 20}
	state := modelState{
		catalog: store, hosts: []model.Host{host}, width: 80, height: 24,
		connectingHost: &host, connectionTarget: &target,
	}
	updated, batch := state.Update(terminalConnectedMsg{host: host, session: recentTrackingSession{}, target: target})
	state = unwrapModel(t, updated)
	if batch == nil {
		t.Fatal("successful connection did not schedule terminal and recent-connection work")
	}
	batchMessage := batch()
	commands, ok := batchMessage.(tea.BatchMsg)
	if !ok {
		t.Fatalf("connection work = %T, want Bubble Tea batch", batchMessage)
	}
	var recorded bool
	for _, command := range commands {
		if command == nil {
			continue
		}
		msg := command()
		if _, ok := msg.(hostConnectionRecordedMsg); ok {
			recorded = true
			updated, _ = state.Update(msg)
			state = unwrapModel(t, updated)
		}
	}
	if !recorded {
		t.Fatal("successful connection batch omitted recent-connection recording")
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil || loaded.LastConnectedAt.IsZero() {
		t.Fatalf("successful connection timestamp = %s, err=%v", loaded.LastConnectedAt, err)
	}

	failedTarget := connectionTarget{kind: newTabTarget, tabID: "tab-failed", paneID: "pane-failed"}
	state.connectingHost = &host
	state.connectionTarget = &failedTarget
	updated, _ = state.Update(terminalConnectedMsg{host: host, target: failedTarget, err: errors.New("connection refused")})
	state = unwrapModel(t, updated)
	loaded, err = store.GetHost(ctx, host.Id)
	if err != nil || !loaded.LastConnectedAt.Equal(state.hostByID(host.Id).LastConnectedAt) {
		t.Fatalf("failed connection changed recent timestamp: %#v, err=%v", loaded, err)
	}
}

func TestFavoriteAndRecentViewsPersistAndSortHosts(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	older := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	hostA, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "alpha", IP: "192.0.2.1", Port: 22})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	hostB, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "beta", IP: "192.0.2.2", Port: 22, Favorite: true})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if err := store.RecordHostConnection(ctx, hostB.Id, older); err != nil {
		t.Fatalf("record older connection: %v", err)
	}
	loadedHosts, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	state := modelState{catalog: store, hosts: loadedHosts, activePane: hostPane, width: 100, height: 30}

	entries := buildGroupEntries(nil, state.hosts)
	if len(entries) < 3 || entries[1].name != "收藏" || entries[1].hostCount != 1 || entries[2].name != "最近连接" || entries[2].hostCount != 1 {
		t.Fatalf("system views = %#v, want favorite/recent counts of one", entries)
	}
	state.groupIndex = 1
	if visible := state.visibleHosts(); len(visible) != 1 || visible[0].Id != hostB.Id {
		t.Fatalf("favorite view = %#v, want beta only", visible)
	}

	state.groupIndex = 0
	state.hostIndex = 0
	updated, command := state.updateKey(keyPress("v"))
	state = unwrapModel(t, updated)
	if command == nil || state.favoriteUpdatingID != hostA.Id {
		t.Fatalf("favorite toggle command/state = %v / %q", command != nil, state.favoriteUpdatingID)
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if got := state.hostByID(hostA.Id); got == nil || !got.Favorite {
		t.Fatalf("alpha was not added to favorites: %#v", got)
	}
	storedA, err := store.GetHost(ctx, hostA.Id)
	if err != nil || !storedA.Favorite {
		t.Fatalf("persisted alpha favorite = %v, err=%v", storedA.Favorite, err)
	}

	command = state.recordRecentHostConnection(hostA)
	if command == nil {
		t.Fatal("recording a recent connection returned no command")
	}
	updatedModel, _ := state.Update(command())
	state = unwrapModel(t, updatedModel)
	state.groupIndex = 2
	recent := state.visibleHosts()
	if len(recent) != 2 || recent[0].Id != hostA.Id || recent[1].Id != hostB.Id {
		t.Fatalf("recent hosts = %#v, want alpha then beta", recent)
	}
	storedA, err = store.GetHost(ctx, hostA.Id)
	if err != nil || storedA.LastConnectedAt.IsZero() {
		t.Fatalf("alpha recent connection was not persisted: %#v, err=%v", storedA, err)
	}

	state.form = newHostEditForm(*state.hostByID(hostA.Id))
	state.form.name = "alpha-renamed"
	updated, command = state.submitForm()
	state = unwrapModel(t, updated)
	if command == nil {
		t.Fatal("editing the host with local metadata returned no save command")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	storedA, err = store.GetHost(ctx, hostA.Id)
	if err != nil || !storedA.Favorite || storedA.LastConnectedAt.IsZero() || storedA.Name != "alpha-renamed" {
		t.Fatalf("host edit lost favorite/recent metadata: %#v, err=%v", storedA, err)
	}
}

func TestDefaultSessionCanBeSetClearedAndAutoConnectOnStartup(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "default-session-ui.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindFTP, Name: "default-ftp", IP: "127.0.0.1", Port: 21,
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	state := modelState{catalog: store, hosts: []model.Host{host}, activePane: hostPane}
	updated, command := state.updateKey(keyPress("s"))
	state = unwrapModel(t, updated)
	if command == nil || !state.defaultSessionUpdating {
		t.Fatal("setting the selected host as default did not start persistence")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.defaultHostID != host.Id || state.defaultSessionUpdating {
		t.Fatalf("default session state after save = %q, updating=%v", state.defaultHostID, state.defaultSessionUpdating)
	}
	if stored, err := store.DefaultHostID(ctx); err != nil || stored != host.Id {
		t.Fatalf("persisted default session = %q, err=%v", stored, err)
	}
	card, _ := hostCardLines(host, false, 0, true, 80)
	if !strings.Contains(card, "默认") {
		t.Fatalf("default host card does not show its status: %q", card)
	}
	updated, command = state.updateKey(keyPress("s"))
	state = unwrapModel(t, updated)
	if command == nil || !state.defaultSessionUpdating {
		t.Fatal("clearing the selected default host did not start persistence")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.defaultHostID != "" {
		t.Fatalf("cleared default session remains %q", state.defaultHostID)
	}
	if stored, err := store.DefaultHostID(ctx); err != nil || stored != "" {
		t.Fatalf("cleared persisted default session = %q, err=%v", stored, err)
	}
	if err := store.SetDefaultHostID(ctx, host.Id); err != nil {
		t.Fatalf("restore default host for startup test: %v", err)
	}

	// The default host is opened after the catalog loads. FTP prompts for a
	// password before networking, so this verifies startup selection without
	// requiring a live server or storing a credential in the test.
	startup := modelState{catalog: store}
	loadedMessage, ok := startup.loadCatalogCmd()().(catalogLoadedMsg)
	if !ok || loadedMessage.err != nil || loadedMessage.defaultHostID != host.Id {
		t.Fatalf("catalog load did not include default session: %#v", loadedMessage)
	}
	updated, _ = startup.Update(loadedMessage)
	startup = unwrapModel(t, updated)
	if !startup.defaultSessionStarted || startup.ftpPendingHost == nil || startup.ftpPendingHost.Id != host.Id || !startup.ftpPasswordPrompt {
		t.Fatalf("startup did not begin default host connection: started=%v host=%#v ftpPrompt=%v", startup.defaultSessionStarted, startup.ftpPendingHost, startup.ftpPasswordPrompt)
	}

	// A repeated catalog refresh must not launch another automatic connection.
	updated, _ = startup.Update(loadedMessage)
	startup = unwrapModel(t, updated)
	if !startup.ftpPasswordPrompt || startup.ftpPendingHost == nil || startup.ftpPendingHost.Id != host.Id {
		t.Fatal("reloading the catalog replaced or duplicated the pending default connection")
	}
}

func TestFavoriteNumberShortcutsUseStableFallbackOrder(t *testing.T) {
	hosts := []model.Host{
		{Id: "c", Kind: model.HostKindLocal, Name: "Charlie", Favorite: true},
		{Id: "b", Kind: model.HostKindLocal, Name: "beta", Favorite: true},
		{Id: "a", Kind: model.HostKindLocal, Name: "Alpha", Favorite: true},
		{Id: "ignored", Kind: model.HostKindLocal, Name: "Not favorite"},
	}
	state := modelState{hosts: hosts, activePane: hostPane, width: 80, height: 24}

	shortcuts := state.favoriteShortcutNumbers()
	if shortcuts["a"] != 1 || shortcuts["b"] != 2 || shortcuts["c"] != 3 {
		t.Fatalf("favorite shortcut map = %#v, want alphabetical 1..3", shortcuts)
	}
	first, _ := hostCardLines(hosts[2], false, shortcuts["a"], false, 50)
	if !strings.Contains(first, "1★") {
		t.Fatalf("favorite card does not display its shortcut number: %q", first)
	}

	updated, command := state.updateKey(keyPress("2"))
	state = unwrapModel(t, updated)
	if command == nil || state.connectingHost == nil || state.connectingHost.Id != "b" {
		t.Fatalf("2 shortcut opened %#v, want beta", state.connectingHost)
	}
	if state.connectionTarget == nil || state.connectionTarget.kind != newTabTarget {
		t.Fatalf("favorite shortcut target = %#v, want new tab", state.connectionTarget)
	}

	many := modelState{}
	for index := 0; index < 12; index++ {
		name := string(rune('a' + index))
		many.hosts = append(many.hosts, model.Host{Id: name, Name: name, Favorite: true, FavoriteOrder: index + 1})
	}
	if got := many.favoriteShortcutHosts(); len(got) != 9 || got[8].FavoriteOrder != 9 {
		t.Fatalf("favorite shortcut hosts = %d, last order %d; want only the first nine", len(got), got[8].FavoriteOrder)
	}
}

func TestFavoriteNumberShortcutsRespectHomeScreenState(t *testing.T) {
	host := model.Host{Id: "favorite", Kind: model.HostKindLocal, Name: "favorite", Favorite: true}

	searching := modelState{hosts: []model.Host{host}, activePane: hostPane, searching: true}
	updated, command := searching.updateKey(keyPress("1"))
	searching = unwrapModel(t, updated)
	if command != nil || searching.query != "1" || searching.connectingHost != nil {
		t.Fatalf("number while filtering should be text: command=%v query=%q host=%#v", command != nil, searching.query, searching.connectingHost)
	}

	groupsFocused := modelState{hosts: []model.Host{host}, activePane: groupPane}
	updated, command = groupsFocused.updateKey(keyPress("1"))
	groupsFocused = unwrapModel(t, updated)
	if command != nil || groupsFocused.connectingHost != nil {
		t.Fatalf("number shortcut connected while group pane was focused: command=%v host=%#v", command != nil, groupsFocused.connectingHost)
	}
}

func TestFavoriteShortcutOrderCanBeAdjustedAndKeepsSelection(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var hosts []model.Host
	for _, name := range []string{"alpha", "beta", "gamma"} {
		host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := store.SetHostFavorite(ctx, host.Id, true); err != nil {
			t.Fatalf("favorite %s: %v", name, err)
		}
		hosts = append(hosts, host)
	}
	loaded, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	state := modelState{catalog: store, hosts: loaded, activePane: hostPane, groupIndex: 1, hostIndex: 2, width: 100, height: 30}

	updated, command := state.updateKey(keyPress("["))
	state = unwrapModel(t, updated)
	if command == nil || !state.favoriteOrderUpdating {
		t.Fatal("moving the selected favorite up did not start a save")
	}
	updated, _ = state.Update(command())
	state = unwrapModel(t, updated)
	if state.favoriteOrderUpdating {
		t.Fatal("favorite order save was not completed")
	}
	ordered := state.favoriteShortcutHosts()
	if len(ordered) != 3 || ordered[0].Name != "alpha" || ordered[1].Name != "gamma" || ordered[2].Name != "beta" {
		t.Fatalf("favorite shortcut order = %#v, want alpha, gamma, beta", ordered)
	}
	if selected := state.selectedHost(); selected == nil || selected.Name != "gamma" || state.hostIndex != 1 {
		t.Fatalf("selection after reorder = %#v at %d, want gamma at slot 2", selected, state.hostIndex)
	}
}
