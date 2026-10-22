package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestSFTPBookmarksAreScopedAndPersisted(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	local, err := store.SaveSFTPBookmark(ctx, model.SFTPBookmark{
		ScopeKey: "local", Name: "Projects", Path: "/Users/tester/projects",
	})
	if err != nil || local.Id == "" || local.Remote {
		t.Fatalf("save local bookmark = %#v, %v", local, err)
	}
	remote, err := store.SaveSFTPBookmark(ctx, model.SFTPBookmark{
		ScopeKey: "host:server-1", Name: "Logs", Path: "/var/log", Remote: true,
	})
	if err != nil || remote.Id == "" || !remote.Remote {
		t.Fatalf("save remote bookmark = %#v, %v", remote, err)
	}
	if _, err := store.SaveSFTPBookmark(ctx, model.SFTPBookmark{
		ScopeKey: "host:server-1", Name: "logs", Path: "/tmp", Remote: true,
	}); err == nil {
		t.Fatal("saved a case-insensitive duplicate bookmark name in the same scope")
	}
	if _, err := store.SaveSFTPBookmark(ctx, model.SFTPBookmark{
		ScopeKey: "host:server-2", Name: "Logs", Path: "/tmp", Remote: true,
	}); err != nil {
		t.Fatalf("same bookmark name in a different host scope: %v", err)
	}
	if _, err := store.SaveSFTPBookmark(ctx, model.SFTPBookmark{
		ScopeKey: "local", Name: "Bad\nName", Path: "/tmp",
	}); err == nil {
		t.Fatal("saved a bookmark with a control character")
	}

	got, err := store.ListSFTPBookmarks(ctx, "host:server-1", true)
	if err != nil || len(got) != 1 || got[0].Id != remote.Id || got[0].Name != "Logs" || got[0].Path != "/var/log" {
		t.Fatalf("remote bookmarks = %#v, %v", got, err)
	}
	loaded, err := store.GetSFTPBookmark(ctx, remote.Id)
	if err != nil || loaded != remote {
		t.Fatalf("loaded bookmark = %#v, %v; want %#v", loaded, err, remote)
	}
	if err := store.DeleteSFTPBookmark(ctx, remote.Id); err != nil {
		t.Fatalf("delete remote bookmark: %v", err)
	}
	if err := store.DeleteSFTPBookmark(ctx, remote.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing bookmark error = %v, want ErrNotFound", err)
	}
}

func TestVersionThirtyOneDatabaseMigratesSFTPBookmarks(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before migration fixture: %v", err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open migration fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `DROP TABLE sftp_bookmarks; PRAGMA user_version = 31`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare schema version 31 fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close migration fixture: %v", err)
	}

	migrated, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate schema version 31: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, schemaVersion)
	}
	bookmarks, err := migrated.ListSFTPBookmarks(ctx, "local", false)
	if err != nil || len(bookmarks) != 0 {
		t.Fatalf("migrated bookmarks = %#v, %v; want empty list", bookmarks, err)
	}
}
