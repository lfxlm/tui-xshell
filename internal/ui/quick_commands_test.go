package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"tui-ssh-workbench/internal/model"
)

type quickCommandCatalogStub struct {
	Catalog
	commands []model.QuickCommand
}

func (s *quickCommandCatalogStub) ListQuickCommands(context.Context) ([]model.QuickCommand, error) {
	return append([]model.QuickCommand(nil), s.commands...), nil
}

func (s *quickCommandCatalogStub) SaveQuickCommand(_ context.Context, command model.QuickCommand) (model.QuickCommand, error) {
	if command.Id == "" {
		command.Id = "quick-command-id"
	}
	if index := quickCommandIndex(s.commands, command.Id); index >= 0 {
		s.commands[index] = command
	} else {
		s.commands = append(s.commands, command)
	}
	return command, nil
}

func (s *quickCommandCatalogStub) DeleteQuickCommand(_ context.Context, id string) error {
	if index := quickCommandIndex(s.commands, id); index >= 0 {
		s.commands = append(s.commands[:index], s.commands[index+1:]...)
	}
	return nil
}

func TestQuickCommandManagerCreatesMultilineCommand(t *testing.T) {
	catalog := &quickCommandCatalogStub{}
	state := modelState{catalog: catalog, quickCommands: &quickCommandManagerState{}}
	state.beginQuickCommandEdit(nil)
	for _, character := range "Disk usage" {
		updated, _ := state.updateQuickCommandEditorKey(tea.Key{Code: character, Text: string(character)})
		state = unwrapModel(t, updated)
	}
	updated, _ := state.updateQuickCommandEditorKey(tea.Key{Code: tea.KeyEnter})
	state = unwrapModel(t, updated)
	state.appendQuickCommandPaste("df -h\r\ndu -sh /var/log")

	updated, saveCmd := state.updateQuickCommandEditorKey(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	state = unwrapModel(t, updated)
	if saveCmd == nil || !state.quickCommands.saving {
		t.Fatal("Ctrl+Enter did not start saving the quick command")
	}
	updated, _ = state.Update(saveCmd())
	state = unwrapModel(t, updated)
	if state.quickCommands.editing || len(state.quickCommands.commands) != 1 {
		t.Fatalf("saved command state = %#v", state.quickCommands)
	}
	if got, want := state.quickCommands.commands[0].Command, "df -h\ndu -sh /var/log"; got != want {
		t.Fatalf("saved multiline command = %q, want %q", got, want)
	}
}

func TestQuickCommandOpensFromWorkspaceLeader(t *testing.T) {
	catalog := &quickCommandCatalogStub{commands: []model.QuickCommand{{Id: "id", Name: "Show current dir", Command: "pwd"}}}
	pane := &terminalPane{id: "pane", session: &workspaceTestSession{}}
	tab := &workspaceTab{id: "tab", activePaneID: pane.id, root: &paneNode{pane: pane}}
	state := modelState{catalog: catalog, activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true}

	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, loadCmd := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	state = unwrapModel(t, updated)
	if state.quickCommands == nil || !state.quickCommands.loading || loadCmd == nil {
		t.Fatal("Ctrl+G then Q did not open and load the quick-command manager")
	}
	updated, _ = state.Update(loadCmd())
	state = unwrapModel(t, updated)
	if state.quickCommands.loading || len(state.quickCommands.commands) != 1 || state.quickCommands.commands[0].Id != "id" {
		t.Fatalf("loaded quick-command manager = %#v", state.quickCommands)
	}
}

func TestQuickCommandRunsToCurrentPaneOrSynchronizedTab(t *testing.T) {
	firstSession := &workspaceTestSession{}
	secondSession := &workspaceTestSession{}
	first := &terminalPane{id: "first", session: firstSession, bracketedPaste: true}
	second := &terminalPane{id: "second", session: secondSession}
	tab := &workspaceTab{
		id: "tab", activePaneID: first.id, syncInput: true,
		root: &paneNode{axis: splitVertical, first: &paneNode{pane: first}, second: &paneNode{pane: second}},
	}
	state := modelState{
		activeTabID: tab.id, tabs: []*workspaceTab{tab}, workspaceVisible: true,
		quickCommands: &quickCommandManagerState{commands: []model.QuickCommand{{Id: "cmd", Name: "Inspect", Command: "df -h\ndu -sh /var/log"}}},
	}

	updated, _ := state.updateQuickCommandKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	wantFirst := "\x1b[200~df -h\x1b[201~\r\x1b[200~du -sh /var/log\x1b[201~\r"
	if len(firstSession.writes) != 1 || string(firstSession.writes[0]) != wantFirst {
		t.Fatalf("active pane command = %q, want %q", firstSession.writes, wantFirst)
	}
	wantSecond := "df -h\rdu -sh /var/log\r"
	if len(secondSession.writes) != 1 || string(secondSession.writes[0]) != wantSecond {
		t.Fatalf("sibling pane command = %q, want %q", secondSession.writes, wantSecond)
	}
	if state.quickCommands != nil || state.statusText != "快速命令已发送" {
		t.Fatalf("manager state after execution = %#v, status=%q", state.quickCommands, state.statusText)
	}
}

func TestQuickCommandViewShowsExecutionScopeAndSanitizesControlCharacters(t *testing.T) {
	pane := &terminalPane{id: "pane", session: &workspaceTestSession{}}
	tab := &workspaceTab{id: "tab", activePaneID: pane.id, syncInput: true, root: &paneNode{pane: pane}}
	state := modelState{
		width: 120, height: 30, activeTabID: tab.id, tabs: []*workspaceTab{tab},
		quickCommands: &quickCommandManagerState{commands: []model.QuickCommand{{Id: "cmd", Name: "Inspect", Command: "printf '\x1b[2J'"}}},
	}
	view := state.quickCommandView()
	if !strings.Contains(view, "当前标签页 1 个连接 pane") {
		t.Fatalf("quick command view does not show sync execution scope:\n%s", view)
	}
	if strings.Contains(view, "\x1b[2J") {
		t.Fatalf("raw terminal control sequence leaked into command preview:\n%s", view)
	}
}

func TestQuickCommandEditorKeepsLongTextCursorVisible(t *testing.T) {
	value := strings.Repeat("主机", 80)
	view := quickCommandEditorField(value, len([]rune(value)), true, 24)
	if !strings.Contains(view, "▏") {
		t.Fatalf("long command editor hid its cursor: %q", view)
	}
	if width := lipgloss.Width(view); width > 24 {
		t.Fatalf("long command editor width = %d, want <= 24: %q", width, view)
	}
}
