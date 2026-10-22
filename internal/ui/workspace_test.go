package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"tui-ssh-workbench/internal/model"
)

type workspaceTestSession struct {
	writes  [][]byte
	resizes [][2]int
}

func (*workspaceTestSession) Read([]byte) (int, error) { return 0, nil }
func (s *workspaceTestSession) Write(data []byte) error {
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}
func (s *workspaceTestSession) Resize(width, height int) error {
	s.resizes = append(s.resizes, [2]int{width, height})
	return nil
}
func (*workspaceTestSession) Close() error { return nil }

func TestRenderTerminalPaneShowsHistoryAtSelectedOffset(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 5)
	terminal.SetScrollbackSize(100)
	for index := 0; index < 10; index++ {
		_, _ = terminal.Write([]byte("history-row-" + string(rune('0'+index)) + "\r\n"))
	}
	if terminal.ScrollbackLen() == 0 {
		t.Fatal("terminal did not record scrollback lines")
	}
	pane := &terminalPane{
		host:              model.Host{Name: "test"},
		session:           &workspaceTestSession{},
		terminal:          terminal,
		scrollbackViewing: true,
		scrollbackOffset:  terminal.ScrollbackLen(),
	}
	view := (modelState{}).renderTerminalPane(pane, paneRect{width: 24, height: 8})
	if !strings.Contains(view, "history-row-0") {
		t.Fatalf("oldest scrollback content not rendered:\n%s", view)
	}
}

func TestRenderTerminalPaneClipsANSIAtHorizontalViewportOffset(t *testing.T) {
	terminal := vt.NewSafeEmulator(20, 5)
	_, _ = terminal.Write([]byte("\x1b[31m0123456789ABCDEFGHIJ\x1b[0m"))
	pane := &terminalPane{
		host: model.Host{Name: "test"}, session: &workspaceTestSession{}, terminal: terminal,
		horizontalOffset: 5,
	}
	view := (modelState{}).renderTerminalPane(pane, paneRect{width: 8, height: 2})
	rows := strings.Split(ansi.Strip(view), "\n")
	if len(rows) != 2 || strings.TrimSpace(rows[1]) != "56789ABC" {
		t.Fatalf("horizontal viewport rendered rows = %#v, want visible text %q", rows, "56789ABC")
	}
}

func TestRenderTerminalPaneMarksFocusedPaneClearly(t *testing.T) {
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "production"}, session: &workspaceTestSession{},
		terminal: vt.NewSafeEmulator(24, 5),
	}
	state := modelState{
		activeTabID: "tab-1",
		tabs:        []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	view := state.renderTerminalPane(pane, paneRect{width: 32, height: 8})
	rows := strings.Split(ansi.Strip(view), "\n")
	if len(rows) != 8 || !strings.HasPrefix(rows[0], "┌") || !strings.HasPrefix(rows[1], "│▶ production") || !strings.HasPrefix(rows[7], "└") {
		t.Fatalf("focused pane frame = %#v, want a full border and focused title", rows)
	}
	if !strings.Contains(view, "38;2;246;195;67") {
		t.Fatalf("focused pane border does not use the yellow focus color:\n%s", view)
	}
}

func TestWorkspaceSplitPanesShareBorderWithoutGap(t *testing.T) {
	first := &terminalPane{id: "first", host: model.Host{Name: "first"}, session: &workspaceTestSession{}, terminal: vt.NewSafeEmulator(20, 2)}
	second := &terminalPane{id: "second", host: model.Host{Name: "second"}, session: &workspaceTestSession{}, terminal: vt.NewSafeEmulator(20, 2)}
	root := &paneNode{axis: splitHorizontal, ratio: 50, first: &paneNode{pane: first}, second: &paneNode{pane: second}}
	tab := &workspaceTab{id: "tab-1", root: root, activePaneID: second.id}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	rows := strings.Split(ansi.Strip(state.renderWorkspaceNode(tab, root, paneRect{width: 24, height: 8})), "\n")
	if len(rows) != 8 {
		t.Fatalf("split pane view has %d rows, want 8:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[3], "─") || strings.Contains(rows[2], "└") || strings.Contains(rows[2], "┘") {
		t.Fatalf("shared horizontal divider has a gap or duplicate border:\n%s", strings.Join(rows, "\n"))
	}
	if strings.HasPrefix(rows[4], "┌") || !strings.Contains(rows[7], "└") {
		t.Fatalf("pane seam or outer bottom border is malformed:\n%s", strings.Join(rows, "\n"))
	}
}

func TestWorkspaceRightPaneBorderReachesRightEdge(t *testing.T) {
	first := &terminalPane{id: "first", host: model.Host{Name: "first"}, session: &workspaceTestSession{}, terminal: vt.NewSafeEmulator(20, 2)}
	second := &terminalPane{id: "second", host: model.Host{Name: "second"}, session: &workspaceTestSession{}, terminal: vt.NewSafeEmulator(20, 2)}
	root := &paneNode{axis: splitVertical, ratio: 50, first: &paneNode{pane: first}, second: &paneNode{pane: second}}
	tab := &workspaceTab{id: "tab-1", root: root, activePaneID: second.id}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	rows := strings.Split(ansi.Strip(state.renderWorkspaceNode(tab, root, paneRect{width: 40, height: 8})), "\n")
	if len(rows) != 8 {
		t.Fatalf("vertical split has %d rows, want 8:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for index, row := range rows {
		want := "│"
		if index == 0 {
			want = "┐"
		} else if index == len(rows)-1 {
			want = "┘"
		}
		got := []rune(row)
		last := ""
		if len(got) > 0 {
			last = string(got[len(got)-1])
		}
		if last != want {
			t.Errorf("right edge row %d ends with %q, want %q: %q", index, last, want, row)
		}
	}
}

func TestWorkspaceBottomBorderDoesNotOverlapFooter(t *testing.T) {
	pane := &terminalPane{id: "pane-1", host: model.Host{Name: "test"}, session: &workspaceTestSession{}, terminal: vt.NewSafeEmulator(30, 4)}
	tab := &workspaceTab{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}
	state := modelState{width: 30, height: 10, activeTabID: tab.id, tabs: []*workspaceTab{tab}, config: model.DefaultConfig()}
	rows := strings.Split(ansi.Strip(state.renderWorkspace()), "\n")
	if len(rows) != state.height {
		t.Fatalf("workspace rendered %d rows for terminal height %d:\n%s", len(rows), state.height, strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[len(rows)-2], "└") || strings.TrimSpace(rows[len(rows)-1]) == "" {
		t.Fatalf("bottom border and footer are not on separate rows:\n%s", strings.Join(rows, "\n"))
	}
}

func TestHomePanesShowWhichListHasFocus(t *testing.T) {
	state := modelState{
		activePane: groupPane,
		hosts:      []model.Host{{Id: "host-1", Name: "build", IP: "127.0.0.1", Port: 22}},
	}
	groupView := ansi.Strip(state.renderGroupPane(30, 8))
	hostView := ansi.Strip(state.renderHostPane(60, 8))
	if !strings.Contains(groupView, "◆ 当前焦点：分组") || strings.Contains(hostView, "全部主机 ◆") {
		t.Fatalf("group focus is not clear: groups=%q hosts=%q", groupView, hostView)
	}
	state.activePane = hostPane
	groupView = ansi.Strip(state.renderGroupPane(30, 8))
	hostView = ansi.Strip(state.renderHostPane(60, 8))
	if strings.Contains(groupView, "◆ 当前焦点：分组") || !strings.Contains(hostView, "全部主机 ◆") {
		t.Fatalf("host focus is not clear: groups=%q hosts=%q", groupView, hostView)
	}
}

func TestRenderGroupTreeKeepsSelectedRootBranchAligned(t *testing.T) {
	state := modelState{
		groups: []model.Group{
			{Id: "alpha", Name: "alpha"},
			{Id: "beta", Name: "beta"},
		},
		activePane: groupPane,
		groupIndex: 3, // the first real group follows All, Favorites, and Recent
	}
	rows := strings.Split(ansi.Strip(state.renderGroupPane(40, 8)), "\n")
	if len(rows) < 8 {
		t.Fatalf("group pane rendered only %d rows", len(rows))
	}
	selectedRowWithoutMarker := strings.Replace(rows[6], "› ", "  ", 1)
	selectedBranch := strings.Index(selectedRowWithoutMarker, "├─")
	otherBranch := strings.Index(rows[7], "└─")
	if selectedBranch < 0 || otherBranch < 0 || selectedBranch != otherBranch {
		t.Fatalf("root group branches are misaligned: selected=%q sibling=%q", rows[6], rows[7])
	}
}

func TestWorkspaceFooterUsesConfiguredStatusBarOrderAndVisibility(t *testing.T) {
	config := model.DefaultConfig()
	config.Workspace.StatusBarItems = []string{"quick_command", "help", "split"}
	state := modelState{config: config}
	footer := ansi.Strip(state.workspaceFooter(200))
	quickCommand := strings.Index(footer, "Q 快速命令")
	help := strings.Index(footer, "? 快捷键")
	split := strings.Index(footer, "V/H 分屏")
	if quickCommand < 0 || help < 0 || split < 0 || !(quickCommand < help && help < split) {
		t.Fatalf("custom status bar order = %q, want quick command, help, then split", footer)
	}
	if strings.Contains(footer, "Ctrl+G 前缀") || strings.Contains(footer, "Y 命令历史") || strings.Contains(footer, "F 远程文件") {
		t.Fatalf("hidden status bar items remain visible: %q", footer)
	}
}

func TestWorkspaceFooterCanShowActiveSessionAndTraffic(t *testing.T) {
	traffic := &terminalTraffic{}
	traffic.received.Add(2048)
	traffic.sent.Add(4)
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "production"}, session: &workspaceTestSession{},
		terminal: vt.NewSafeEmulator(80, 5), traffic: traffic,
	}
	config := model.DefaultConfig()
	config.Workspace.StatusBarItems = []string{"session", "traffic"}
	state := modelState{
		config: config, width: 120, height: 24, activeTabID: "tab-1",
		tabs: []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	footer := ansi.Strip(state.workspaceFooter(120))
	if !strings.Contains(footer, "会话 production") || !strings.Contains(footer, "↓2.0K ↑4B") {
		t.Fatalf("custom session/traffic footer = %q", footer)
	}
}

func TestFixedTerminalSizeResizesPTYAndViewportCanScroll(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "test"}, session: session,
		terminal: vt.NewSafeEmulator(20, 5),
	}
	state := modelState{
		width: 40, height: 14, workspaceVisible: true, activeTabID: "tab-1",
		config: model.DefaultConfig(),
		tabs:   []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	state.config.Terminal.Columns, state.config.Terminal.Rows = 100, 30
	commands := state.resizeActiveWorkspace()
	if pane.terminal.Width() != 100 || pane.terminal.Height() != 30 {
		t.Fatalf("VT emulator size = %dx%d, want 100x30", pane.terminal.Width(), pane.terminal.Height())
	}
	if len(commands) != 1 {
		t.Fatalf("resize command count = %d, want 1", len(commands))
	}
	commands[0]()
	if len(session.resizes) != 1 || session.resizes[0] != [2]int{100, 30} {
		t.Fatalf("PTY resize calls = %#v, want [[100 30]]", session.resizes)
	}
	state.scrollActiveTerminalViewport(1, 1)
	if pane.horizontalOffset == 0 || pane.verticalOffset == 0 {
		t.Fatalf("viewport offsets after scrolling = (%d,%d), want both axes to move", pane.horizontalOffset, pane.verticalOffset)
	}
	if footer := state.workspaceFooter(120); !strings.Contains(footer, "终端 100×30") {
		t.Fatalf("fixed-size terminal state missing from footer: %q", footer)
	}
}

func TestWorkspaceScrollbackKeysDoNotReachRemoteShell(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 5)
	for index := 0; index < 10; index++ {
		_, _ = terminal.Write([]byte("line\r\n"))
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id:       "pane-1",
		host:     model.Host{Name: "test"},
		session:  session,
		terminal: terminal,
	}
	state := modelState{
		height:           20,
		workspaceVisible: true,
		activeTabID:      "tab-1",
		tabs:             []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 's', Text: "s"}))
	state = unwrapModel(t, updated)
	if !pane.scrollbackViewing {
		t.Fatal("Ctrl+G then S did not enter terminal history")
	}
	pane.scrollbackOffset = 0
	before := pane.scrollbackOffset
	_, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if pane.scrollbackOffset <= before {
		t.Fatalf("PageUp offset = %d, want > %d", pane.scrollbackOffset, before)
	}
	if len(session.writes) != 0 {
		t.Fatalf("scrollback key leaked to remote shell: %q", session.writes)
	}
	_, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if pane.scrollbackViewing || pane.scrollbackOffset != 0 {
		t.Fatalf("End did not return to live terminal: viewing=%v offset=%d", pane.scrollbackViewing, pane.scrollbackOffset)
	}
}

func TestWorkspaceClearScreenClearsLocalVTWithoutWritingToShell(t *testing.T) {
	terminal := vt.NewSafeEmulator(40, 6)
	terminal.SetScrollbackSize(100)
	for index := 0; index < 12; index++ {
		_, _ = terminal.Write([]byte("old-output-" + string(rune('0'+index)) + "\r\n"))
	}
	if terminal.ScrollbackLen() == 0 {
		t.Fatal("test terminal did not create scrollback")
	}
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Name: "test"}, session: session, terminal: terminal,
		selection: &terminalSelection{}, scrollbackViewing: true, scrollbackOffset: 3,
		search: &terminalSearchState{query: "old-output", matches: []terminalSearchMatch{{row: 0, startCell: 0, endCell: 10}}},
	}
	state := modelState{
		width: 100, height: 20, workspaceVisible: true, activeTabID: "tab-1",
		tabs: []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}

	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	state = unwrapModel(t, updated)

	if terminal.ScrollbackLen() != 0 {
		t.Fatalf("scrollback length after clear = %d, want 0", terminal.ScrollbackLen())
	}
	if pane.scrollbackViewing || pane.scrollbackOffset != 0 || pane.selection != nil || pane.search != nil {
		t.Fatalf("clear did not reset local view state: viewing=%v offset=%d selection=%v search=%v", pane.scrollbackViewing, pane.scrollbackOffset, pane.selection, pane.search)
	}
	if rendered := terminal.Render(); strings.Contains(rendered, "old-output-") {
		t.Fatalf("old screen content remained after clear:\n%s", rendered)
	}
	if len(session.writes) != 0 {
		t.Fatalf("clear-screen shortcut leaked to shell: %q", session.writes)
	}
	if pane.session != session {
		t.Fatal("clearing the screen changed the active session")
	}

	_, _ = terminal.Write([]byte("new-output"))
	if rendered := terminal.Render(); !strings.Contains(rendered, "new-output") {
		t.Fatalf("terminal did not accept output after clear:\n%s", rendered)
	}
}

func TestTerminalVisualBellIsShownInFooterAndCanBeDisabled(t *testing.T) {
	state, pane, _ := zmodemTestState()
	state.config = model.DefaultConfig()
	updated, _ := state.Update(terminalOutputMsg{session: pane.session, data: []byte("\a")})
	state = unwrapModel(t, updated)
	if !pane.bellUntil.After(time.Now()) {
		t.Fatal("BEL did not activate the visual bell")
	}
	if footer := state.workspaceFooter(80); !strings.Contains(footer, "终端响铃") {
		t.Fatalf("visual bell footer = %q, want bell notice", footer)
	}
	pane.bellUntil = time.Now().Add(-time.Second)
	updated, _ = state.Update(terminalBellExpiredMsg{paneID: pane.id})
	state = unwrapModel(t, updated)
	if !pane.bellUntil.IsZero() {
		t.Fatal("expired visual bell was not cleared")
	}

	state.config.Terminal.Bell = model.TerminalBellOff
	updated, _ = state.Update(terminalOutputMsg{session: pane.session, data: []byte("\a")})
	state = unwrapModel(t, updated)
	if !pane.bellUntil.IsZero() {
		t.Fatal("BEL activated a visual bell while bell mode was off")
	}
	if footer := state.workspaceFooter(80); strings.Contains(footer, "终端响铃") {
		t.Fatalf("disabled visual bell appeared in footer: %q", footer)
	}
}

func TestWorkspaceUsesConfiguredLeaderAndCustomShortcut(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", host: model.Host{Id: "ssh-1", Kind: model.HostKindSSH, Name: "test"},
		session: session, terminal: vt.NewSafeEmulator(40, 10),
	}
	state := modelState{
		config: model.Config{Workspace: model.WorkspaceConfig{
			Leader: "ctrl+u", Bindings: map[string]string{"split_vertical": "j"},
		}},
		width: 100, height: 28, workspaceVisible: true, activeTabID: "tab-1",
		tabs:  []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
		hosts: []model.Host{{Id: "ssh-2", Kind: model.HostKindSSH, Name: "other", IP: "127.0.0.1", Port: 22}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if state.workspaceLeaderPending {
		t.Fatal("default Ctrl+G still opened the prefix after leader was changed")
	}
	if len(session.writes) != 1 {
		t.Fatalf("unconfigured Ctrl+G was not forwarded to the terminal: %q", session.writes)
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	if !state.workspaceLeaderPending {
		t.Fatal("configured Ctrl+U did not activate the leader")
	}
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	state = unwrapModel(t, updated)
	if state.hostPicker == nil || state.hostPicker.target.axis != splitVertical {
		t.Fatal("custom j binding did not start a vertical split host picker")
	}
	if !strings.Contains(state.workspaceLeaderSummary(), "Ctrl+U") || !strings.Contains(state.workspaceLeaderSummary(), "J 左右分屏") || !strings.Contains(state.workspaceLeaderSummary(), "U 清屏") || !strings.Contains(state.workspaceLeaderSummary(), "Y 命令历史") {
		t.Fatalf("configured keymap is missing from help text: %s", state.workspaceLeaderSummary())
	}
}

func TestWorkspaceShortcutHelpDisplaysConfiguredBindingsAndBlocksPaste(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminal: vt.NewSafeEmulator(40, 10)}
	state := modelState{
		config: model.Config{Workspace: model.WorkspaceConfig{
			Leader: "ctrl+u", Bindings: map[string]string{"split_vertical": "j"},
		}},
		width: 100, height: 16, workspaceVisible: true, activeTabID: "tab-1",
		tabs: []*workspaceTab{{id: "tab-1", root: &paneNode{pane: pane}, activePaneID: pane.id}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	state = unwrapModel(t, updated)
	if !state.workspaceHelp {
		t.Fatal("configured help binding did not open the workspace keymap")
	}
	if view := state.view(); !strings.Contains(view, "Ctrl+U") || !strings.Contains(view, "Ctrl+U 后按 J") {
		t.Fatalf("workspace keymap does not show configured shortcuts:\n%s", view)
	}
	updated, _ = state.Update(tea.PasteMsg{Content: "must-not-reach-shell"})
	state = unwrapModel(t, updated)
	if len(session.writes) != 0 {
		t.Fatalf("paste leaked through the shortcut help screen: %q", session.writes)
	}
	updated, _ = state.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.workspaceHelp {
		t.Fatal("Escape did not close the workspace shortcut help")
	}
}

func TestWorkspaceSyncInputBroadcastsOnlyWithinActiveTab(t *testing.T) {
	firstSession := &workspaceTestSession{}
	secondSession := &workspaceTestSession{}
	otherTabSession := &workspaceTestSession{}
	first := &terminalPane{id: "pane-1", session: firstSession, host: model.Host{Name: "one"}}
	second := &terminalPane{id: "pane-2", session: secondSession, host: model.Host{Name: "two"}}
	otherTabPane := &terminalPane{id: "pane-3", session: otherTabSession, host: model.Host{Name: "other"}}
	activeTab := &workspaceTab{
		id: "tab-1", activePaneID: first.id,
		root: &paneNode{axis: splitVertical, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{
		activeTabID: "tab-1", workspaceVisible: true,
		tabs: []*workspaceTab{
			activeTab,
			{id: "tab-2", activePaneID: otherTabPane.id, root: &paneNode{pane: otherTabPane}},
		},
	}

	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'i', Text: "i"}))
	state = unwrapModel(t, updated)
	if !activeTab.syncInput {
		t.Fatal("Ctrl+G then I did not enable synchronized input")
	}

	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	state = unwrapModel(t, updated)
	if len(firstSession.writes) != 1 || string(firstSession.writes[0]) != "x" {
		t.Fatalf("active pane writes = %q, want [x]", firstSession.writes)
	}
	if len(secondSession.writes) != 1 || string(secondSession.writes[0]) != "x" {
		t.Fatalf("sibling pane writes = %q, want [x]", secondSession.writes)
	}
	if len(otherTabSession.writes) != 0 {
		t.Fatalf("input leaked to another tab: %q", otherTabSession.writes)
	}
	if !strings.Contains(state.workspaceFooter(200), "同步输入已开启") {
		t.Fatal("synchronized-input state is not visible in the workspace footer")
	}

	activeTab.syncInput = false
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	state = unwrapModel(t, updated)
	if len(firstSession.writes) != 2 || string(firstSession.writes[1]) != "y" {
		t.Fatalf("active pane writes after disabling sync = %q, want [x y]", firstSession.writes)
	}
	if len(secondSession.writes) != 1 {
		t.Fatalf("sibling received input after sync was disabled: %q", secondSession.writes)
	}
}

func TestWorkspaceSyncInputAppliesBracketedPastePerPane(t *testing.T) {
	firstSession := &workspaceTestSession{}
	secondSession := &workspaceTestSession{}
	first := &terminalPane{id: "pane-1", session: firstSession, bracketedPaste: true}
	second := &terminalPane{id: "pane-2", session: secondSession}
	tab := &workspaceTab{
		id: "tab-paste", activePaneID: first.id, syncInput: true,
		root: &paneNode{axis: splitVertical, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true}

	updated, _ := state.Update(tea.PasteMsg{Content: "pwd\n"})
	state = unwrapModel(t, updated)
	if got, want := string(firstSession.writes[0]), "\x1b[200~pwd\n\x1b[201~"; got != want {
		t.Fatalf("bracketed pane paste = %q, want %q", got, want)
	}
	if got, want := string(secondSession.writes[0]), "pwd\n"; got != want {
		t.Fatalf("plain pane paste = %q, want %q", got, want)
	}
}

func TestTerminalClipboardPasteRespectsPaneAndBracketedModes(t *testing.T) {
	firstSession := &workspaceTestSession{}
	secondSession := &workspaceTestSession{}
	otherSession := &workspaceTestSession{}
	first := &terminalPane{id: "pane-1", session: firstSession, bracketedPaste: true}
	second := &terminalPane{id: "pane-2", session: secondSession}
	other := &terminalPane{id: "pane-3", session: otherSession}
	activeTab := &workspaceTab{
		id: "tab-1", activePaneID: first.id, syncInput: true,
		root: &paneNode{axis: splitVertical, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{
		activeTabID: activeTab.id, workspaceVisible: true,
		tabs: []*workspaceTab{activeTab, {id: "tab-2", activePaneID: other.id, root: &paneNode{pane: other}}},
	}

	updated, cmd := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl | tea.ModShift}))
	state = unwrapModel(t, updated)
	if cmd == nil || state.clipboardPasteRequest == nil {
		t.Fatal("Ctrl+Shift+V did not request the system clipboard")
	}
	updated, _ = state.Update(tea.ClipboardMsg{Content: "echo one\necho two"})
	state = unwrapModel(t, updated)
	if got, want := string(firstSession.writes[0]), "\x1b[200~echo one\necho two\x1b[201~"; got != want {
		t.Fatalf("bracketed clipboard paste = %q, want %q", got, want)
	}
	if got, want := string(secondSession.writes[0]), "echo one\necho two"; got != want {
		t.Fatalf("plain clipboard paste = %q, want %q", got, want)
	}
	if len(otherSession.writes) != 0 {
		t.Fatalf("clipboard paste leaked to another tab: %q", otherSession.writes)
	}
}

func TestClipboardMessageWithoutPendingPasteIsIgnored(t *testing.T) {
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	updated, _ := state.Update(tea.ClipboardMsg{Content: "should not be sent"})
	state = unwrapModel(t, updated)
	if len(session.writes) != 0 {
		t.Fatalf("unsolicited clipboard response was sent to the terminal: %q", session.writes)
	}
}

func TestShiftDragSelectsVisibleTerminalCells(t *testing.T) {
	terminal := vt.NewSafeEmulator(12, 4)
	_, _ = terminal.Write([]byte("hello world"))
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminal: terminal, host: model.Host{Name: "test"}}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		width: 12, height: 8, workspaceVisible: true,
		activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}

	state.updateWorkspaceMouse(tea.MouseClickMsg(tea.Mouse{
		X: 1, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift,
	}))
	if pane.selection == nil || !pane.selection.dragging {
		t.Fatal("Shift+click did not begin a terminal selection")
	}
	state.updateWorkspaceMouse(tea.MouseMotionMsg(tea.Mouse{
		X: 5, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift,
	}))
	state.updateWorkspaceMouse(tea.MouseReleaseMsg(tea.Mouse{
		X: 5, Y: 3, Button: tea.MouseLeft, Mod: tea.ModShift,
	}))
	if pane.selection.dragging {
		t.Fatal("mouse release did not finish the selection")
	}
	if got, want := pane.selection.Text(), "hello"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
	if len(session.writes) != 0 {
		t.Fatalf("selection mouse events leaked to the terminal: %q", session.writes)
	}
}

func TestPlainDragSelectsVisibleTerminalCells(t *testing.T) {
	terminal := vt.NewSafeEmulator(12, 4)
	_, _ = terminal.Write([]byte("hello world"))
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminal: terminal, host: model.Host{Name: "test"}}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{width: 12, height: 8, workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	state.updateWorkspaceMouse(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 3, Button: tea.MouseLeft}))
	if pane.selection != nil {
		t.Fatal("a plain click should not leave a one-cell selection")
	}
	state.updateWorkspaceMouse(tea.MouseMotionMsg(tea.Mouse{X: 6, Y: 3, Button: tea.MouseLeft}))
	state.updateWorkspaceMouse(tea.MouseReleaseMsg(tea.Mouse{X: 6, Y: 3, Button: tea.MouseLeft}))
	if pane.selection == nil {
		t.Fatal("a plain drag did not create a terminal selection")
	}
	if got, want := pane.selection.Text(), "hello"; got != want {
		t.Fatalf("selected text = %q, want %q", got, want)
	}
	if len(session.writes) != 0 {
		t.Fatalf("plain selection events leaked to the shell: %q", session.writes)
	}
}

func TestCommandClickOpensTerminalURL(t *testing.T) {
	terminal := vt.NewSafeEmulator(40, 4)
	_, _ = terminal.Write([]byte("see https://example.com/path"))
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminal: terminal}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{width: 40, height: 8, workspaceVisible: true, activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	command := state.updateWorkspaceMouse(tea.MouseClickMsg(tea.Mouse{
		X: 12, Y: 3, Button: tea.MouseLeft, Mod: tea.ModSuper,
	}))
	if command == nil {
		t.Fatal("Cmd+click on a terminal URL did not request opening it")
	}
	if len(session.writes) != 0 {
		t.Fatalf("Cmd+click was sent to the shell: %q", session.writes)
	}
}

func TestWorkspaceDoubleClickSelectsTerminalWord(t *testing.T) {
	terminal := vt.NewSafeEmulator(40, 5)
	_, _ = terminal.Write([]byte("alpha_beta gamma"))
	session := &workspaceTestSession{}
	pane := &terminalPane{id: "pane-1", session: session, terminal: terminal}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		width: 40, height: 10, workspaceVisible: true,
		activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}
	click := tea.MouseClickMsg(tea.Mouse{X: 8, Y: 3, Button: tea.MouseLeft})
	state.updateWorkspaceMouse(click)
	state.updateWorkspaceMouse(click)

	if pane.selection == nil {
		t.Fatal("double-click did not create a terminal selection")
	}
	if got, want := pane.selection.Text(), "alpha_beta"; got != want {
		t.Fatalf("selected word = %q, want %q", got, want)
	}
	if len(session.writes) != 0 {
		t.Fatalf("local word selection leaked mouse events to shell: %q", session.writes)
	}
}

func TestSelectTerminalWordHandlesUnicodeAndDelimiters(t *testing.T) {
	for _, test := range []struct {
		name string
		line string
		col  int
		want string
	}{
		{name: "unicode word", line: "你好 世界", col: 3, want: "你好"},
		{name: "underscore and digits", line: "file_name2.txt", col: 8, want: "file_name2"},
		{name: "delimiter is isolated", line: "foo-bar", col: 3, want: "-"},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := vt.NewSafeEmulator(40, 4)
			_, _ = terminal.Write([]byte(test.line))
			pane := &terminalPane{terminal: terminal}
			if !selectTerminalWord(pane, 4, 0, test.col) {
				t.Fatal("word selection failed")
			}
			if got := pane.selection.Text(); got != test.want {
				t.Fatalf("selected text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDoubleClickKeepsMouseEventsForTerminalApplications(t *testing.T) {
	terminal := vt.NewSafeEmulator(40, 5)
	_, _ = terminal.Write([]byte("vim buffer"))
	session := &workspaceTestSession{}
	pane := &terminalPane{
		id: "pane-1", session: session, terminal: terminal,
		mouse: terminalMouseTracking{click: true, sgr: true},
	}
	tab := &workspaceTab{id: "tab-1", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{
		width: 40, height: 10, workspaceVisible: true,
		activeTabID: tab.id, tabs: []*workspaceTab{tab},
	}
	click := tea.MouseClickMsg(tea.Mouse{X: 3, Y: 3, Button: tea.MouseLeft})
	state.updateWorkspaceMouse(click)
	state.updateWorkspaceMouse(click)

	if pane.selection != nil {
		t.Fatalf("ordinary double-click was intercepted while application mouse tracking is enabled: %q", pane.selection.Text())
	}
	if len(session.writes) != 2 {
		t.Fatalf("application received %d mouse events, want 2", len(session.writes))
	}
}

func TestWorkspaceSyncInputRequiresTwoConnectedPanes(t *testing.T) {
	pane := &terminalPane{id: "pane-1", session: &workspaceTestSession{}}
	tab := &workspaceTab{id: "tab-one", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{activeTabID: tab.id, tabs: []*workspaceTab{tab}}

	state.toggleWorkspaceSyncInput()
	if tab.syncInput {
		t.Fatal("synchronized input enabled with only one connected pane")
	}
	if !state.statusErr || !strings.Contains(state.statusText, "至少有两个") {
		t.Fatalf("missing explanatory status: err=%v text=%q", state.statusErr, state.statusText)
	}
}

func TestMouseWheelScrollbackReturnsToLiveScreen(t *testing.T) {
	terminal := vt.NewSafeEmulator(24, 5)
	for index := 0; index < 10; index++ {
		_, _ = terminal.Write([]byte("line\r\n"))
	}
	pane := &terminalPane{terminal: terminal}
	scrollTerminalPane(pane, 1, 3, terminal.Height())
	if !pane.scrollbackViewing || pane.scrollbackOffset != 3 {
		t.Fatalf("scroll up state = viewing %v, offset %d", pane.scrollbackViewing, pane.scrollbackOffset)
	}
	scrollTerminalPane(pane, -1, 3, terminal.Height())
	if pane.scrollbackViewing || pane.scrollbackOffset != 0 {
		t.Fatalf("scroll down to bottom state = viewing %v, offset %d", pane.scrollbackViewing, pane.scrollbackOffset)
	}
}
