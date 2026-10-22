package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestSFTPFiltersCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	filter, err := store.SaveSFTPFilter(ctx, model.SFTPFilter{Name: "  Go files  ", Expression: " *.go, !*_test.go, type:file "})
	if err != nil {
		t.Fatal(err)
	}
	if filter.Id == "" || filter.Name != "Go files" || filter.Expression != "*.go, !*_test.go, type:file" {
		t.Fatalf("saved filter = %#v, want generated ID and trimmed fields", filter)
	}
	loaded, err := store.GetSFTPFilter(ctx, filter.Id)
	if err != nil || loaded != filter {
		t.Fatalf("loaded filter = %#v, err=%v; want %#v", loaded, err, filter)
	}
	if _, err := store.SaveSFTPFilter(ctx, model.SFTPFilter{Name: "go FILES", Expression: "*.mod"}); err == nil {
		t.Fatal("saved a duplicate filter name with different casing")
	}

	filter.Expression = "*.mod"
	if _, err := store.SaveSFTPFilter(ctx, filter); err != nil {
		t.Fatalf("update filter: %v", err)
	}
	if _, err := store.SaveSFTPFilter(ctx, model.SFTPFilter{Name: "Bad\nName", Expression: "*.go"}); err == nil {
		t.Fatal("accepted a filter name with a control character")
	}
	if _, err := store.SaveSFTPFilter(ctx, model.SFTPFilter{Name: "Empty"}); err == nil {
		t.Fatal("accepted an empty filter expression")
	}

	listed, err := store.ListSFTPFilters(ctx)
	if err != nil || len(listed) != 1 || listed[0].Expression != "*.mod" {
		t.Fatalf("listed filters = %#v, err=%v", listed, err)
	}
	if err := store.DeleteSFTPFilter(ctx, filter.Id); err != nil {
		t.Fatalf("delete filter: %v", err)
	}
	if err := store.DeleteSFTPFilter(ctx, filter.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing filter error = %v, want ErrNotFound", err)
	}
}

func TestVersionSixDatabaseMigratesSavedSFTPFilters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE sftp_filters`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE scheduled_transfers`); err != nil {
		t.Fatal(err)
	}
	dropRecentHostMetadataForLegacyFixture(t, ctx, db)
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v6 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	filters, err := store.ListSFTPFilters(ctx)
	if err != nil || len(filters) != 0 {
		t.Fatalf("filters after empty migration = %#v, err=%v", filters, err)
	}
}
