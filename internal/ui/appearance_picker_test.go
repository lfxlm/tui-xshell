package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/infra/sqlite"
	"tui-ssh-workbench/internal/model"
)

func TestAppearancePickerSavesSelectedScheme(t *testing.T) {
	t.Cleanup(func() { applyAppearance(model.DefaultConfig().Appearance) })
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "appearance.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	state := modelState{catalog: store, config: model.DefaultConfig(), width: 100, height: 32}
	updated, _ := state.updateKey(keyPress("T"))
	state = unwrapModel(t, updated)
	if state.appearancePicker == nil {
		t.Fatal("T did not open the appearance picker")
	}
	if view := state.appearancePickerView(); !strings.Contains(view, "Nord") || !strings.Contains(view, "Catppuccin Mocha") {
		t.Fatalf("appearance picker omitted built-in palettes:\n%s", view)
	}
	updated, _ = state.updateAppearancePickerKey(keyPress("down"))
	state = unwrapModel(t, updated)
	updated, saveCommand := state.updateAppearancePickerKey(keyPress("enter"))
	state = unwrapModel(t, updated)
	if saveCommand == nil || !state.appearancePicker.saving || state.config.Appearance.Scheme != model.AppearanceNord {
		t.Fatalf("selected palette was not applied and queued for saving: %#v", state.appearancePicker)
	}
	updated, _ = state.Update(saveCommand())
	state = unwrapModel(t, updated)
	if state.appearancePicker != nil || state.statusText != "工作台配色已保存" {
		t.Fatalf("saved palette flow did not finish: picker=%#v status=%q", state.appearancePicker, state.statusText)
	}
	if scheme, err := store.AppearanceScheme(ctx); err != nil || scheme != model.AppearanceNord {
		t.Fatalf("persisted palette = %q, err=%v", scheme, err)
	}
}

func TestAppearancePickerCancelKeepsCurrentScheme(t *testing.T) {
	state := modelState{config: model.Config{Appearance: model.AppearanceConfig{Scheme: model.AppearanceDracula}}}
	state.openAppearancePicker()
	updated, _ := state.updateAppearancePickerKey(keyPress("esc"))
	state = unwrapModel(t, updated)
	if state.appearancePicker != nil || state.config.Appearance.Scheme != model.AppearanceDracula {
		t.Fatalf("cancel changed the current palette: picker=%#v scheme=%q", state.appearancePicker, state.config.Appearance.Scheme)
	}
}
