package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestWorkspaceLayoutsCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "layouts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	layout, err := store.SaveWorkspaceLayout(ctx, model.WorkspaceLayout{
		Name: "  Production  ", Data: `{"version":1,"tabs":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Id == "" || layout.Name != "Production" || layout.UpdatedAt.IsZero() {
		t.Fatalf("saved layout = %#v", layout)
	}
	loaded, err := store.GetWorkspaceLayout(ctx, layout.Id)
	if err != nil || loaded.Id != layout.Id || loaded.Name != layout.Name || loaded.Data != layout.Data || !loaded.UpdatedAt.Equal(layout.UpdatedAt) {
		t.Fatalf("loaded layout = %#v, err=%v; want %#v", loaded, err, layout)
	}

	if _, err := store.SaveWorkspaceLayout(ctx, model.WorkspaceLayout{
		Name: "production", Data: `{"version":1,"tabs":[{}]}`,
	}); err == nil {
		t.Fatal("saved a case-insensitive duplicate layout name")
	}

	updatedAt := layout.UpdatedAt
	layout.Name = "Production Servers"
	layout.Data = `{"version":1,"tabs":[{"root":{"host_id":"host-1"}}]}`
	updated, err := store.SaveWorkspaceLayout(ctx, layout)
	if err != nil {
		t.Fatalf("update layout: %v", err)
	}
	if updated.Id != layout.Id || updated.Name != "Production Servers" || !updated.UpdatedAt.After(updatedAt) {
		t.Fatalf("updated layout = %#v, want same ID with newer timestamp", updated)
	}

	listed, err := store.ListWorkspaceLayouts(ctx)
	if err != nil || len(listed) != 1 || listed[0].Id != updated.Id || listed[0].Name != updated.Name || listed[0].Data != updated.Data || !listed[0].UpdatedAt.Equal(updated.UpdatedAt) {
		t.Fatalf("listed layouts = %#v, err=%v", listed, err)
	}

	invalid := []model.WorkspaceLayout{
		{Name: "", Data: `{}`},
		{Name: "bad\nname", Data: `{}`},
		{Name: strings.Repeat("n", 81), Data: `{}`},
		{Name: "bad-json", Data: `{`},
		{Name: "empty-json", Data: ""},
		{Name: "large", Data: `{` + strings.Repeat(" ", maxWorkspaceLayoutBytes) + `}`},
	}
	for _, candidate := range invalid {
		if _, err := store.SaveWorkspaceLayout(ctx, candidate); err == nil {
			t.Errorf("accepted invalid layout %#v", candidate.Name)
		}
	}

	if err := store.DeleteWorkspaceLayout(ctx, layout.Id); err != nil {
		t.Fatalf("delete layout: %v", err)
	}
	if err := store.DeleteWorkspaceLayout(ctx, layout.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing layout error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetWorkspaceLayout(ctx, layout.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted layout error = %v, want ErrNotFound", err)
	}
}

func TestLastWorkspacePreferenceValidationAndMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "last-workspace.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"version":1,"active_tab":0,"tabs":[{"active_pane":0,"root":{"host_id":"host-1"}}]}`
	if err := store.SetLastWorkspaceData(ctx, data); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LastWorkspaceData(ctx); err != nil || got != data {
		t.Fatalf("LastWorkspaceData() = %q, %v; want %q", got, err, data)
	}
	for _, invalid := range []string{"not-json", strings.Repeat("x", maxWorkspaceLayoutBytes+1)} {
		if err := store.SetLastWorkspaceData(ctx, invalid); err == nil {
			t.Errorf("SetLastWorkspaceData accepted invalid value of length %d", len(invalid))
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE app_preferences DROP COLUMN last_workspace_data`); err != nil {
		t.Fatalf("prepare version 22 schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 22`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate version 22 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got, err := store.LastWorkspaceData(ctx); err != nil || got != "" {
		t.Fatalf("migrated LastWorkspaceData() = %q, %v; want empty", got, err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, schemaVersion)
	}
}

func TestVersionEighteenDatabaseMigratesWorkspaceLayouts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v18.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP INDEX workspace_layouts_unique_name`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE workspace_layouts`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 18`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate version 18 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	layouts, err := store.ListWorkspaceLayouts(ctx)
	if err != nil || len(layouts) != 0 {
		t.Fatalf("layouts after migration = %#v, err=%v; want empty", layouts, err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}
