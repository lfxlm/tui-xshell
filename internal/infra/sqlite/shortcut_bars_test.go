package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestShortcutBarsPersistMembersAndOrder(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hosts := make([]model.Host, 3)
	for index, name := range []string{"alpha", "bravo", "charlie"} {
		hosts[index], err = store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: name, IP: "192.0.2.1", Port: 22})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	bar, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: "  Production  "})
	if err != nil {
		t.Fatalf("create shortcut bar: %v", err)
	}
	if bar.Name != "Production" || bar.SortOrder != 1 || len(bar.HostIDs) != 0 {
		t.Fatalf("created bar = %#v", bar)
	}
	second, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: "Local"})
	if err != nil {
		t.Fatalf("create second shortcut bar: %v", err)
	}
	if second.SortOrder != 2 {
		t.Fatalf("second bar sort order = %d, want 2", second.SortOrder)
	}
	if _, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: "production"}); err == nil {
		t.Fatal("case-insensitive duplicate bar name was accepted")
	}

	want := []string{hosts[2].Id, hosts[0].Id, hosts[1].Id}
	if err := store.SetShortcutBarHosts(ctx, bar.Id, want); err != nil {
		t.Fatalf("set shortcut bar members: %v", err)
	}
	ordered, err := store.MoveShortcutBarHost(ctx, bar.Id, hosts[0].Id, -1)
	if err != nil {
		t.Fatalf("move shortcut host up: %v", err)
	}
	want = []string{hosts[0].Id, hosts[2].Id, hosts[1].Id}
	if !reflect.DeepEqual(ordered, want) {
		t.Fatalf("moved host order = %#v, want %#v", ordered, want)
	}
	if _, err := store.MoveShortcutBarHost(ctx, bar.Id, hosts[0].Id, -1); err != nil {
		t.Fatalf("move first shortcut host up at boundary: %v", err)
	}
	if _, err := store.MoveShortcutBarHost(ctx, bar.Id, hosts[0].Id, 0); err == nil {
		t.Fatal("invalid direction was accepted")
	}
	if _, err := store.MoveShortcutBarHost(ctx, bar.Id, "outside", 1); !errors.Is(err, ErrHostNotInShortcutBar) {
		t.Fatalf("moving non-member returned %v, want ErrHostNotInShortcutBar", err)
	}

	bar.Name = "Production Servers"
	bar, err = store.SaveShortcutBar(ctx, bar)
	if err != nil {
		t.Fatalf("rename shortcut bar: %v", err)
	}
	loaded, err := store.ListShortcutBars(ctx)
	if err != nil {
		t.Fatalf("list shortcut bars: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Id != bar.Id || loaded[0].Name != "Production Servers" || !reflect.DeepEqual(loaded[0].HostIDs, want) {
		t.Fatalf("loaded bars = %#v", loaded)
	}
	if loaded[1].Id != second.Id || loaded[1].SortOrder != 2 || len(loaded[1].HostIDs) != 0 {
		t.Fatalf("second loaded bar = %#v", loaded[1])
	}

	if err := store.DeleteHost(ctx, hosts[2].Id); err != nil {
		t.Fatalf("delete member host: %v", err)
	}
	loaded, err = store.ListShortcutBars(ctx)
	if err != nil {
		t.Fatalf("list bars after deleting host: %v", err)
	}
	if !reflect.DeepEqual(loaded[0].HostIDs, []string{hosts[0].Id, hosts[1].Id}) {
		t.Fatalf("host deletion left stale membership: %#v", loaded[0].HostIDs)
	}
	if err := store.DeleteShortcutBar(ctx, bar.Id); err != nil {
		t.Fatalf("delete shortcut bar: %v", err)
	}
	if _, err := store.GetHost(ctx, hosts[0].Id); err != nil {
		t.Fatalf("deleting shortcut bar removed host: %v", err)
	}
}

func TestShortcutBarValidationAndAtomicMembershipReplacement(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for name, value := range map[string]string{
		"empty": "  ", "too long": strings.Repeat("x", 81), "control": "line\nbreak",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: value}); err == nil {
				t.Fatalf("invalid shortcut bar name %q was accepted", value)
			}
		})
	}
	bar, err := store.SaveShortcutBar(ctx, model.ShortcutBar{Name: "servers"})
	if err != nil {
		t.Fatalf("create bar: %v", err)
	}
	first, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "first", IP: "192.0.2.1", Port: 22})
	if err != nil {
		t.Fatalf("create first host: %v", err)
	}
	second, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "second", IP: "192.0.2.2", Port: 22})
	if err != nil {
		t.Fatalf("create second host: %v", err)
	}
	if err := store.SetShortcutBarHosts(ctx, bar.Id, []string{first.Id, second.Id}); err != nil {
		t.Fatalf("set initial members: %v", err)
	}
	if err := store.SetShortcutBarHosts(ctx, bar.Id, []string{second.Id, "missing-host"}); err == nil {
		t.Fatal("unknown host was accepted")
	}
	loaded, err := store.ListShortcutBars(ctx)
	if err != nil {
		t.Fatalf("list bars after failed replacement: %v", err)
	}
	if !reflect.DeepEqual(loaded[0].HostIDs, []string{first.Id, second.Id}) {
		t.Fatalf("failed replacement was not atomic: %#v", loaded[0].HostIDs)
	}
	if err := store.SetShortcutBarHosts(ctx, bar.Id, []string{first.Id, first.Id}); err == nil {
		t.Fatal("duplicate host membership was accepted")
	}
	if err := store.SetShortcutBarHosts(ctx, "missing-bar", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown bar returned %v, want ErrNotFound", err)
	}
	if err := store.DeleteShortcutBar(ctx, "missing-bar"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting unknown bar returned %v, want ErrNotFound", err)
	}
}

func TestVersionTwentyThreeMigratesNamedShortcutBars(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "v23.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE shortcut_bar_hosts; DROP TABLE shortcut_bars; PRAGMA user_version = 23`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare v23 database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}
	store, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate v23 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	if bars, err := store.ListShortcutBars(ctx); err != nil || len(bars) != 0 {
		t.Fatalf("list bars after migration = %#v, err=%v", bars, err)
	}
}
