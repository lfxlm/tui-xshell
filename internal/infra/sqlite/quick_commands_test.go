package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestQuickCommandsCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "commands.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	command, err := store.SaveQuickCommand(ctx, model.QuickCommand{Name: "  Inspect  ", Command: "uname -a"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Id == "" || command.Name != "Inspect" {
		t.Fatalf("saved command = %#v, want generated ID and trimmed name", command)
	}

	listed, err := store.ListQuickCommands(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != command {
		t.Fatalf("listed commands = %#v, want [%#v]", listed, command)
	}

	command.Command = "id && whoami"
	if _, err := store.SaveQuickCommand(ctx, command); err != nil {
		t.Fatalf("update quick command: %v", err)
	}
	loaded, err := store.GetQuickCommand(ctx, command.Id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Command != "id && whoami" {
		t.Fatalf("updated command = %q, want %q", loaded.Command, "id && whoami")
	}

	if _, err := store.SaveQuickCommand(ctx, model.QuickCommand{Name: "inspect", Command: "pwd"}); err == nil {
		t.Fatal("case-insensitive duplicate command name was accepted")
	}
	if _, err := store.SaveQuickCommand(ctx, model.QuickCommand{Name: "Empty", Command: "  "}); err == nil {
		t.Fatal("empty command text was accepted")
	}
	if _, err := store.SaveQuickCommand(ctx, model.QuickCommand{Name: "Unsafe", Command: "echo ok\x1b[2J"}); err == nil {
		t.Fatal("terminal control character in command was accepted")
	}

	if err := store.DeleteQuickCommand(ctx, command.Id); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteQuickCommand(ctx, command.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting missing command error = %v, want ErrNotFound", err)
	}
	listed, err = store.ListQuickCommands(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("commands after delete = %#v, want empty", listed)
	}
}

func TestVersionThreeDatabaseMigratesQuickCommands(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3.db")
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
	if _, err := db.ExecContext(ctx, `DROP TABLE quick_commands`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE sftp_filters`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE scheduled_transfers`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade v3 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	commands, err := store.ListQuickCommands(ctx)
	if err != nil {
		t.Fatalf("list commands after migration: %v", err)
	}
	if len(commands) != 0 {
		t.Fatalf("commands after empty v3 migration = %#v, want empty", commands)
	}
}
