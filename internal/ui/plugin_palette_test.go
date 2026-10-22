package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"tui-ssh-workbench/internal/plugin"
)

type paletteTestPlugin struct{}

func (paletteTestPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "test", Name: "Test", Version: "1.0.0", APIVersion: plugin.APIVersion}
}

func (paletteTestPlugin) Register(registrar plugin.Registrar) error {
	return registrar.RegisterCommand("echo", "return a safe result", func(context.Context) (string, error) {
		return "plugin result", nil
	})
}

func TestPluginPaletteFiltersRunsAndDisplaysCommandOutput(t *testing.T) {
	registry, err := plugin.NewRegistry(paletteTestPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	state := modelState{pluginRegistry: registry, width: 120, height: 32}
	state.openPluginPalette()

	updated, _ := state.updatePluginPaletteKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	state = unwrapModel(t, updated)
	if matches := state.pluginPaletteMatches(); len(matches) != 1 || matches[0].FullName != "test.echo" {
		t.Fatalf("filtered commands = %#v", matches)
	}
	updated, cmd := state.updatePluginPaletteKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if cmd == nil || state.pluginPalette.running != "test.echo" {
		t.Fatal("Enter did not start the selected command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("plugin command returned %T, want two batched commands", cmd())
	}
	message, ok := batch[0]().(pluginCommandCompletedMsg)
	if !ok {
		t.Fatalf("plugin runner returned %T, want pluginCommandCompletedMsg", batch[0]())
	}
	updated, _ = state.Update(message)
	state = unwrapModel(t, updated)
	if state.pluginPalette.running != "" || state.pluginPalette.output != "plugin result" {
		t.Fatalf("palette state after run = %#v", state.pluginPalette)
	}
	if !strings.Contains(state.pluginPaletteView(), "plugin result") {
		t.Fatal("plugin output is missing from the command palette")
	}
	updated, _ = state.updatePluginPaletteKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	state = unwrapModel(t, updated)
	if state.pluginPalette != nil {
		t.Fatal("Escape did not close the plugin palette")
	}
}

func TestWorkspaceLeaderOpensPluginPalette(t *testing.T) {
	registry, err := plugin.NewRegistry(paletteTestPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	state := modelState{
		pluginRegistry: registry, workspaceVisible: true,
		activeTabID: "tab-1", tabs: []*workspaceTab{{id: "tab-1"}},
	}
	updated, _ := state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	state = unwrapModel(t, updated)
	updated, _ = state.updateWorkspaceKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	state = unwrapModel(t, updated)
	if state.pluginPalette == nil || len(state.pluginPalette.commands) != 1 {
		t.Fatal("Ctrl+G then X did not open the registered plugin command palette")
	}
}

func TestPluginPaletteTimeoutReleasesRunningState(t *testing.T) {
	registry, err := plugin.NewRegistry(paletteTestPlugin{})
	if err != nil {
		t.Fatal(err)
	}
	state := modelState{pluginRegistry: registry, width: 120, height: 32}
	state.openPluginPalette()
	updated, _ := state.updatePluginPaletteKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	state = unwrapModel(t, updated)
	if state.pluginPalette.running != "test.echo" {
		t.Fatal("command did not enter running state")
	}
	generation := state.pluginPalette.generation
	updated, _ = state.Update(pluginCommandTimeoutMsg{generation: generation, fullName: "test.echo"})
	state = unwrapModel(t, updated)
	if state.pluginPalette.running != "" || state.pluginPalette.runCancel != nil || !strings.Contains(state.pluginPalette.errorText, "超过 30 秒") {
		t.Fatalf("timeout did not release palette state: %#v", state.pluginPalette)
	}
}

func TestPluginPaletteSanitizesTerminalControlSequences(t *testing.T) {
	output := sanitizePluginText("safe\x1b[31m red\x1b[0m\x07")
	if strings.ContainsRune(output, '\x1b') || strings.ContainsRune(output, '\x07') {
		t.Fatalf("plugin output retained terminal control bytes: %q", output)
	}
	if !strings.Contains(output, "safe") || !strings.Contains(output, "red") {
		t.Fatalf("sanitized output lost visible text: %q", output)
	}
}
