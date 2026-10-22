package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"tui-ssh-workbench/internal/model"
)

func TestCatalogCreateUpdateDelete(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	root, err := store.CreateGroup(ctx, model.Group{Name: "Servers", LocalStartDir: "~/Downloads", RemoteStartDir: "/srv"})
	if err != nil {
		t.Fatalf("create root group: %v", err)
	}
	child, err := store.CreateGroup(ctx, model.Group{Name: "Dev", ParentId: root.Id, LocalStartDir: "~/work", RemoteStartDir: "/srv/dev"})
	if err != nil {
		t.Fatalf("create child group: %v", err)
	}

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP, Name: "dev-box", IP: "127.0.0.1", Port: 22,
		User: "tester", GroupId: root.Id, IdentityFile: "~/.ssh/workbench_ed25519", StartupScriptPath: "~/scripts/dev-login.json",
		TerminalEncoding: "GB18030", TerminalInputEncoding: model.TerminalEncodingUTF8,
		LocalStartDir: "~/Downloads", RemoteStartDir: "/srv/releases",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}

	if err := store.DeleteGroup(ctx, root.Id); !errors.Is(err, ErrGroupNotEmpty) {
		t.Fatalf("delete non-empty group error = %v, want %v", err, ErrGroupNotEmpty)
	}

	host.Name = "dev-box-updated"
	host.GroupId = child.Id
	if err := store.UpdateHost(ctx, host); err != nil {
		t.Fatalf("update host: %v", err)
	}
	gotHost, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("get updated host: %v", err)
	}
	if gotHost.Name != host.Name || gotHost.GroupId != child.Id || gotHost.FileProtocol != model.SSHFileProtocolSCP || gotHost.IdentityFile != "~/.ssh/workbench_ed25519" || gotHost.StartupScriptPath != host.StartupScriptPath || gotHost.TerminalEncoding != "GB18030" || gotHost.TerminalInputEncoding != model.TerminalEncodingUTF8 || gotHost.LocalStartDir != host.LocalStartDir || gotHost.RemoteStartDir != host.RemoteStartDir {
		t.Fatalf("updated host = %#v, want name %q in group %q", gotHost, host.Name, child.Id)
	}

	child.Name = "Development"
	if err := store.UpdateGroup(ctx, child); err != nil {
		t.Fatalf("update group: %v", err)
	}
	gotChild, err := store.GetGroup(ctx, child.Id)
	if err != nil || gotChild.LocalStartDir != "~/work" || gotChild.RemoteStartDir != "/srv/dev" {
		t.Fatalf("updated group start paths = %#v, %v", gotChild, err)
	}
	if err := store.UpdateGroup(ctx, model.Group{Id: root.Id, Name: root.Name, ParentId: child.Id}); err == nil {
		t.Fatal("moving a group below its descendant unexpectedly succeeded")
	}

	if err := store.DeleteHost(ctx, host.Id); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if err := store.DeleteGroup(ctx, child.Id); err != nil {
		t.Fatalf("delete child group: %v", err)
	}
	if err := store.DeleteGroup(ctx, root.Id); err != nil {
		t.Fatalf("delete root group: %v", err)
	}

	groups, err := store.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups after delete: %v", err)
	}
	hosts, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts after delete: %v", err)
	}
	if len(groups) != 0 || len(hosts) != 0 {
		t.Fatalf("remaining catalog entries: groups=%d hosts=%d, want both empty", len(groups), len(hosts))
	}
}

func TestVersionTwentySixMigrationAddsTerminalInputEncoding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "terminal-input-encoding-v26.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.10", Port: 22, TerminalEncoding: "GB18030"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN terminal_input_encoding`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 26`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	got, err := migrated.GetHost(ctx, host.Id)
	if err != nil || got.TerminalEncoding != "GB18030" || got.TerminalInputEncoding != "" {
		t.Fatalf("migrated terminal encodings = %q/%q, err=%v", got.TerminalEncoding, got.TerminalInputEncoding, err)
	}
}

func TestVersionTwentySevenMigrationAddsGBKAndPreservesHostReferences(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "terminal-gbk-v27.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.10", Port: 22,
		TerminalEncoding: "GB18030", TerminalInputEncoding: model.TerminalEncodingUTF8,
	})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: host.Id, Kind: model.PortForwardLocalTCP,
		ListenAddress: "127.0.0.1:2200", RemoteAddress: "127.0.0.1:22",
	})
	if err != nil {
		t.Fatal(err)
	}
	var currentSchema string
	if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'hosts'`).Scan(&currentSchema); err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.ReplaceAll(currentSchema, "'UTF-8', 'GBK', 'GB18030'", "'UTF-8', 'GB18030'")
	oldSchema = strings.Replace(oldSchema, "CREATE TABLE hosts (", "CREATE TABLE hosts_v27 (", 1)
	oldSchema = strings.Replace(oldSchema, `CREATE TABLE "hosts" (`, `CREATE TABLE "hosts_v27" (`, 1)
	if oldSchema == currentSchema || (!strings.Contains(oldSchema, "CREATE TABLE hosts_v27 (") && !strings.Contains(oldSchema, `CREATE TABLE "hosts_v27" (`)) {
		t.Fatal("could not prepare a v27 hosts schema")
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		oldSchema,
		`INSERT INTO hosts_v27 SELECT * FROM hosts`,
		`DROP INDEX hosts_by_group`, `DROP INDEX hosts_by_favorite`,
		`DROP INDEX hosts_by_recent_connection`, `DROP INDEX hosts_by_auth_profile`,
		`DROP TABLE hosts`, `ALTER TABLE hosts_v27 RENAME TO hosts`,
		`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_favorite ON hosts(favorite, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_recent_connection ON hosts(last_connected_at DESC) WHERE last_connected_at > 0`,
		`CREATE INDEX hosts_by_auth_profile ON hosts(auth_profile_id)`,
		`PRAGMA user_version = 27`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen v27 database: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if err := migrated.SetHostTerminalEncoding(ctx, host.Id, "GBK"); err != nil {
		t.Fatalf("save migrated GBK encoding: %v", err)
	}
	got, err := migrated.GetHost(ctx, host.Id)
	if err != nil || got.TerminalEncoding != "GBK" || got.TerminalInputEncoding != "GBK" {
		t.Fatalf("migrated GBK host = %q/%q, err=%v", got.TerminalEncoding, got.TerminalInputEncoding, err)
	}
	rules, err := migrated.ListPortForwardRules(ctx, host.Id)
	if err != nil || len(rules) != 1 || rules[0].Id != rule.Id {
		t.Fatalf("port forwarding rows after migration = %#v, err=%v", rules, err)
	}
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestVersionTwentyFourMigrationAddsLocalStartupScriptPath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "host-script-v24.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.10", Port: 22,
		StartupScriptPath: "~/scripts/legacy.json",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN startup_script_path`); err != nil {
		t.Fatalf("prepare v24 hosts table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 24`); err != nil {
		t.Fatalf("set v24 schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close v24 store: %v", err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	got, err := migrated.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("get migrated host: %v", err)
	}
	if got.StartupScriptPath != "" {
		t.Fatalf("migrated startup script path = %q, want empty for old database", got.StartupScriptPath)
	}
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestVersionTwentyFiveMigrationAddsTerminalEncoding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "terminal-encoding-v25.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.10", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN terminal_encoding`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 25`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	got, err := migrated.GetHost(ctx, host.Id)
	if err != nil || got.TerminalEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("migrated terminal encoding = %q, err=%v; want default UTF-8", got.TerminalEncoding, err)
	}
}

func TestHostTerminalEncodingDefaultsAndPersists(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "terminal-encoding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.10", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	if host.TerminalEncoding != model.TerminalEncodingUTF8 {
		t.Fatalf("default terminal encoding = %q", host.TerminalEncoding)
	}
	if err := store.SetHostTerminalEncoding(ctx, host.Id, "GB18030"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetHost(ctx, host.Id)
	if err != nil || got.TerminalEncoding != "GB18030" || got.TerminalInputEncoding != "GB18030" {
		t.Fatalf("saved terminal encodings = %q/%q, err=%v", got.TerminalEncoding, got.TerminalInputEncoding, err)
	}
	if err := store.SetHostTerminalEncoding(ctx, host.Id, "unsupported"); err == nil {
		t.Fatal("unsupported terminal encoding was accepted")
	}
}

func TestSerialHostPersistsAndValidatesConfiguration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "serial.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSerial, Name: "router-console", IP: "/dev/ttyUSB0",
		SerialBaudRate: 115200, SerialDataBits: 7, SerialParity: model.SerialParityEven,
		SerialStopBits: model.SerialStopBitsTwo, SerialFlowControl: model.SerialFlowControlRTSCTS,
		StartupScriptPath: "ignored-local-only-path",
	})
	if err != nil {
		t.Fatalf("create serial host: %v", err)
	}
	if host.Port != 0 || host.SerialBaudRate != 115200 || host.SerialDataBits != 7 ||
		host.SerialParity != model.SerialParityEven || host.SerialStopBits != model.SerialStopBitsTwo || host.SerialFlowControl != model.SerialFlowControlRTSCTS || host.StartupScriptPath != "" {
		t.Fatalf("normalized serial host = %#v", host)
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("load serial host: %v", err)
	}
	if loaded.Kind != model.HostKindSerial || loaded.IP != "/dev/ttyUSB0" || loaded.SerialBaudRate != 115200 ||
		loaded.SerialDataBits != 7 || loaded.SerialParity != model.SerialParityEven || loaded.SerialStopBits != model.SerialStopBitsTwo || loaded.SerialFlowControl != model.SerialFlowControlRTSCTS || loaded.StartupScriptPath != "" {
		t.Fatalf("persisted serial host = %#v", loaded)
	}

	loaded.SerialBaudRate = 57600
	loaded.SerialParity = model.SerialParityOdd
	loaded.SerialFlowControl = model.SerialFlowControlXONXOFF
	if err := store.UpdateHost(ctx, loaded); err != nil {
		t.Fatalf("update serial host: %v", err)
	}
	loaded, err = store.GetHost(ctx, host.Id)
	if err != nil || loaded.SerialBaudRate != 57600 || loaded.SerialParity != model.SerialParityOdd || loaded.SerialFlowControl != model.SerialFlowControlXONXOFF {
		t.Fatalf("updated serial host = %#v, err=%v", loaded, err)
	}

	for name, invalid := range map[string]model.Host{
		"missing device": {Kind: model.HostKindSerial, Name: "missing-device"},
		"invalid baud":   {Kind: model.HostKindSerial, Name: "invalid-baud", IP: "/dev/ttyUSB0", SerialBaudRate: 1},
		"invalid bits":   {Kind: model.HostKindSerial, Name: "invalid-bits", IP: "/dev/ttyUSB0", SerialDataBits: 9},
		"invalid parity": {Kind: model.HostKindSerial, Name: "invalid-parity", IP: "/dev/ttyUSB0", SerialParity: "mark-ish"},
		"invalid stop":   {Kind: model.HostKindSerial, Name: "invalid-stop", IP: "/dev/ttyUSB0", SerialStopBits: "3"},
		"invalid flow":   {Kind: model.HostKindSerial, Name: "invalid-flow", IP: "/dev/ttyUSB0", SerialFlowControl: "software-ish"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateHost(ctx, invalid); err == nil {
				t.Fatal("CreateHost unexpectedly accepted invalid serial host")
			}
		})
	}
}

func TestVersionTwentyOneSerialHostMigrationDefaultsFlowControl(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "serial-v21.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSerial, Name: "legacy-console", IP: "COM4",
		SerialFlowControl: model.SerialFlowControlRTSCTS,
	})
	if err != nil {
		t.Fatalf("create serial host: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN serial_flow_control`); err != nil {
		t.Fatalf("prepare version 21 schema: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 21`); err != nil {
		t.Fatalf("set version 21: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close version 21 store: %v", err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	got, err := migrated.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("load migrated host: %v", err)
	}
	if got.SerialFlowControl != model.SerialFlowControlNone {
		t.Fatalf("migrated flow control = %q, want none", got.SerialFlowControl)
	}
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestHostFavoriteAndRecentConnectionPersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "favorite", IP: "192.0.2.1", Port: 22, Favorite: true,
	})
	if err != nil {
		t.Fatalf("create favorite host: %v", err)
	}
	first := time.Date(2026, time.October, 21, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	if err := store.RecordHostConnection(ctx, host.Id, second); err != nil {
		t.Fatalf("record recent host connection: %v", err)
	}
	if err := store.RecordHostConnection(ctx, host.Id, first); err != nil {
		t.Fatalf("record older host connection: %v", err)
	}
	if err := store.SetHostFavorite(ctx, host.Id, false); err != nil {
		t.Fatalf("remove host favorite: %v", err)
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("load host metadata: %v", err)
	}
	if loaded.Favorite || !loaded.LastConnectedAt.Equal(second) {
		t.Fatalf("loaded favorite/recent metadata = favorite:%v recent:%s; want false and %s", loaded.Favorite, loaded.LastConnectedAt, second)
	}
}

func TestDefaultHostPersistenceReplacementAndDelete(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "default-session.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "first", IP: "192.0.2.1", Port: 22})
	if err != nil {
		t.Fatalf("create first host: %v", err)
	}
	second, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "second", IP: "192.0.2.2", Port: 22})
	if err != nil {
		t.Fatalf("create second host: %v", err)
	}

	if err := store.SetDefaultHostID(ctx, first.Id); err != nil {
		t.Fatalf("set first default host: %v", err)
	}
	if got, err := store.DefaultHostID(ctx); err != nil || got != first.Id {
		t.Fatalf("default host = %q, err=%v; want %q", got, err, first.Id)
	}
	if err := store.SetDefaultHostID(ctx, second.Id); err != nil {
		t.Fatalf("replace default host: %v", err)
	}
	if got, err := store.DefaultHostID(ctx); err != nil || got != second.Id {
		t.Fatalf("replaced default host = %q, err=%v; want %q", got, err, second.Id)
	}
	if err := store.DeleteHost(ctx, second.Id); err != nil {
		t.Fatalf("delete default host: %v", err)
	}
	if got, err := store.DefaultHostID(ctx); err != nil || got != "" {
		t.Fatalf("default after deleting its host = %q, err=%v; want empty", got, err)
	}
	if err := store.SetDefaultHostID(ctx, "missing-host"); err == nil {
		t.Fatal("setting a nonexistent host as default unexpectedly succeeded")
	}
	if err := store.SetDefaultHostID(ctx, ""); err != nil {
		t.Fatalf("clear default session: %v", err)
	}
}

func TestMoveFavoriteHostPersistsCustomShortcutOrder(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hosts := make([]model.Host, 0, 4)
	for _, name := range []string{"delta", "alpha", "charlie", "bravo"} {
		host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: name, IP: "192.0.2.1", Port: 22})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := store.SetHostFavorite(ctx, host.Id, true); err != nil {
			t.Fatalf("favorite %s: %v", name, err)
		}
		hosts = append(hosts, host)
	}

	// Move charlie up from its creation-order slot 3 to slot 2.
	ordered, err := store.MoveFavoriteHost(ctx, hosts[2].Id, -1)
	if err != nil {
		t.Fatalf("move favorite up: %v", err)
	}
	want := []string{hosts[0].Id, hosts[2].Id, hosts[1].Id, hosts[3].Id}
	if !reflect.DeepEqual(ordered, want) {
		t.Fatalf("favorite order = %#v, want %#v", ordered, want)
	}

	loaded, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	for index, hostID := range want {
		found := false
		for _, host := range loaded {
			if host.Id == hostID {
				found = true
				if host.FavoriteOrder != index+1 {
					t.Fatalf("host %q favorite order = %d, want %d", host.Name, host.FavoriteOrder, index+1)
				}
			}
		}
		if !found {
			t.Fatalf("moved host %q missing from list", hostID)
		}
	}

	if _, err := store.MoveFavoriteHost(ctx, hosts[0].Id, 0); err == nil {
		t.Fatal("invalid move direction was accepted")
	}
	nonFavorite, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "ordinary", IP: "192.0.2.2", Port: 22})
	if err != nil {
		t.Fatalf("create ordinary host: %v", err)
	}
	if _, err := store.MoveFavoriteHost(ctx, nonFavorite.Id, -1); !errors.Is(err, ErrHostNotFavorite) {
		t.Fatalf("moving a non-favorite returned %v, want ErrHostNotFavorite", err)
	}
}

func TestVersionSixteenDatabaseMigratesFavoriteShortcutOrder(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "v16.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: name, IP: "192.0.2.1", Port: 22})
		if err != nil {
			_ = store.Close()
			t.Fatalf("create %s: %v", name, err)
		}
		if err := store.SetHostFavorite(ctx, host.Id, true); err != nil {
			_ = store.Close()
			t.Fatalf("favorite %s: %v", name, err)
		}
	}
	legacyHosts, err := store.ListHosts(ctx)
	if err != nil || len(legacyHosts) == 0 {
		t.Fatalf("list migration hosts: %#v, %v", legacyHosts, err)
	}
	if _, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		Id: "migration-forward", HostId: legacyHosts[0].Id,
		Kind: model.PortForwardLocalTCP, ListenAddress: "127.0.0.1:18080", RemoteAddress: "127.0.0.1:80",
	}); err != nil {
		t.Fatalf("create forward rule before migration: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close version 17 store: %v", err)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN favorite_order`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare version 16 schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 16`); err != nil {
		_ = database.Close()
		t.Fatalf("set version 16: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close raw SQLite database: %v", err)
	}

	store, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate version 16 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hosts, err := store.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list migrated hosts: %v", err)
	}
	for _, host := range hosts {
		wantOrder := 2
		if host.Name == "alpha" {
			wantOrder = 1
		}
		if host.FavoriteOrder != wantOrder {
			t.Errorf("migrated %s favorite order = %d, want %d", host.Name, host.FavoriteOrder, wantOrder)
		}
	}
	rules, err := store.ListPortForwardRules(ctx, legacyHosts[0].Id)
	if err != nil || len(rules) != 1 || rules[0].Id != "migration-forward" {
		t.Fatalf("port-forward rules after migration = %#v, err=%v", rules, err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestVersionTwelveDatabaseMigratesSSHFileProtocol(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "v12.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	legacyHost, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.40", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN file_protocol`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare a version 12 schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN identity_file`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare a version 12 schema without identity files: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 12`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate version 12 database: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetHost(ctx, legacyHost.Id)
	if err != nil || loaded.FileProtocol != model.SSHFileProtocolSFTP {
		t.Fatalf("migrated legacy SSH file protocol = %q, err=%v; want SFTP", loaded.FileProtocol, err)
	}
	if loaded.IdentityFile != "" {
		t.Fatalf("migrated legacy identity file = %q, want empty", loaded.IdentityFile)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
	scpHost, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, FileProtocol: model.SSHFileProtocolSCP,
		Name: "scp", IP: "192.0.2.41", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetHost(ctx, scpHost.Id)
	if err != nil || loaded.FileProtocol != model.SSHFileProtocolSCP {
		t.Fatalf("persisted SCP file protocol = %q, err=%v", loaded.FileProtocol, err)
	}
}

func TestVersionThirteenDatabaseMigratesIdentityFile(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "v13.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.42", Port: 22, User: "tester",
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN identity_file`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare a version 13 schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN terminal_protocol`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare a version 13 schema without terminal protocol: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN x11_forwarding`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare a version 13 schema without X11 forwarding: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 13`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate version 13 database: %v", err)
	}
	defer store.Close()
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil || loaded.IdentityFile != "" {
		t.Fatalf("migrated identity file = %q, err=%v; want empty", loaded.IdentityFile, err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestFTPHostPersistenceDefaultsAndTLSValidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindFTP, Name: "secure-files", IP: "files.example.test",
		TLSMode: model.FTPTLSImplicit, FTPActiveMode: true, Password: "never-persist-this", IdentityFile: "/private/key",
	})
	if err != nil {
		t.Fatalf("create FTPS host: %v", err)
	}
	if host.Port != 990 || host.TLSMode != model.FTPTLSImplicit || !host.FTPActiveMode {
		t.Fatalf("normalized FTPS host = %#v, want implicit TLS port 990", host)
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("get FTPS host: %v", err)
	}
	if loaded.TLSMode != model.FTPTLSImplicit || loaded.Port != 990 || !loaded.FTPActiveMode || loaded.Password != "" || loaded.IdentityFile != "" {
		t.Fatalf("stored FTPS host = %#v, want TLS mode, default port, and no password", loaded)
	}
	loaded.FTPActiveMode = false
	if err := store.UpdateHost(ctx, loaded); err != nil {
		t.Fatalf("update FTP data mode: %v", err)
	}
	loaded, err = store.GetHost(ctx, host.Id)
	if err != nil || loaded.FTPActiveMode {
		t.Fatalf("updated FTP data mode = %t, %v; want passive", loaded.FTPActiveMode, err)
	}

	if _, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindFTP, Name: "bad-mode", IP: "files.example.test", TLSMode: "starttls",
	}); err == nil {
		t.Fatal("created an FTP host with an unsupported TLS mode")
	}
}

func TestVersionTwentyEightDatabaseMigratesFTPActiveMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindFTP, Name: "archive", IP: "ftp.example.test"}); err != nil {
		t.Fatalf("create FTP host: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before migration fixture: %v", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open migration fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN ftp_active_mode`); err != nil {
		_ = database.Close()
		t.Fatalf("remove post-v28 fixture column: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 28`); err != nil {
		_ = database.Close()
		t.Fatalf("mark fixture as schema version 28: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close migration fixture: %v", err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate schema version 28: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, schemaVersion)
	}
	hosts, err := migrated.ListHosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].FTPActiveMode {
		t.Fatalf("migrated FTP settings = %#v, %v; want passive default", hosts, err)
	}
}

func TestVersionThirtyDatabaseMigratesStartingDirectories(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "legacy", IP: "192.0.2.5", Port: 22}); err != nil {
		t.Fatalf("create migration fixture host: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store before migration fixture: %v", err)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open migration fixture: %v", err)
	}
	if _, err := database.ExecContext(ctx, `ALTER TABLE hosts DROP COLUMN remote_start_dir; ALTER TABLE hosts DROP COLUMN local_start_dir; PRAGMA user_version = 30`); err != nil {
		_ = database.Close()
		t.Fatalf("prepare schema version 30 fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close migration fixture: %v", err)
	}

	migrated, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("migrate schema version 30: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, schemaVersion)
	}
	hosts, err := migrated.ListHosts(ctx)
	if err != nil || len(hosts) != 1 || hosts[0].LocalStartDir != "" || hosts[0].RemoteStartDir != "" {
		t.Fatalf("migrated host starting directories = %#v, %v; want empty defaults", hosts, err)
	}
}

func TestTelnetHostPersistenceDefaultsAndIgnoresSSHSecrets(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindTelnet, Name: "legacy-console", IP: "192.0.2.44",
		User: "operator", Startup: "must-not-run", CredentialRef: "must-not-keep",
		ProxyJumpList: []string{"ssh-jump"}, TLSMode: "explicit",
	})
	if err != nil {
		t.Fatalf("create Telnet host: %v", err)
	}
	if host.Port != 23 || host.Startup != "" || host.CredentialRef != "" || len(host.ProxyJumpList) != 0 || host.TLSMode != "" {
		t.Fatalf("normalized Telnet host = %#v, want default port and no SSH/FTP-only settings", host)
	}
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("get Telnet host: %v", err)
	}
	if loaded.Kind != model.HostKindTelnet || loaded.Port != 23 || loaded.User != "operator" || loaded.Password != "" {
		t.Fatalf("stored Telnet host = %#v, want Telnet type, login hint, and no password", loaded)
	}
}

func TestVersionFourMigrationPreservesHostsAndPortForwardRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "migration-gateway", IP: "192.0.2.11", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	wantRule, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: host.Id, Kind: model.PortForwardLocalTCP,
		ListenAddress: "127.0.0.1:8080", RemoteAddress: "10.0.0.8:80",
	})
	if err != nil {
		t.Fatalf("create port forwarding rule: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE sftp_filters`); err != nil {
		t.Fatalf("remove v7 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE scheduled_transfers`); err != nil {
		t.Fatalf("remove v8 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatalf("mark database as v4: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw SQLite database: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v4 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gotHost, err := store.GetHost(ctx, host.Id)
	if err != nil || gotHost.Name != host.Name {
		t.Fatalf("host after migration = %#v, err=%v; want name %q", gotHost, err, host.Name)
	}
	rules, err := store.ListPortForwardRules(ctx, host.Id)
	if err != nil {
		t.Fatalf("list port forwarding rules after migration: %v", err)
	}
	if len(rules) != 1 || rules[0].Id != wantRule.Id || rules[0].RemoteAddress != wantRule.RemoteAddress {
		t.Fatalf("rules after migration = %#v, want preserved rule %#v", rules, wantRule)
	}

	var brokenForeignKeyCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&brokenForeignKeyCount); err != nil {
		t.Fatalf("check migrated foreign keys: %v", err)
	}
	if brokenForeignKeyCount != 0 {
		t.Fatalf("migration left %d broken foreign key(s)", brokenForeignKeyCount)
	}
	if err := store.DeleteHost(ctx, host.Id); err != nil {
		t.Fatalf("delete migrated host: %v", err)
	}
	rules, err = store.ListPortForwardRules(ctx, host.Id)
	if err != nil {
		t.Fatalf("list rules after host deletion: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("host deletion left migrated forwarding rules: %#v", rules)
	}
}

func TestPortForwardRulePersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "gateway", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	want := []model.PortForwardRule{
		{HostId: host.Id, Kind: model.PortForwardLocalTCP, ListenAddress: "127.0.0.1:8080", RemoteAddress: "10.0.0.5:80"},
		{HostId: host.Id, Kind: model.PortForwardRemoteTCP, ListenAddress: "127.0.0.1:9000", RemoteAddress: "127.0.0.1:22"},
		{HostId: host.Id, Kind: model.PortForwardSOCKS5, ListenAddress: "127.0.0.1:1080"},
	}
	for index := range want {
		saved, err := store.SavePortForwardRule(ctx, want[index])
		if err != nil {
			t.Fatalf("save rule %d: %v", index, err)
		}
		if saved.Id == "" {
			t.Fatalf("rule %d has no ID: %#v", index, saved)
		}
		want[index] = saved
	}
	duplicate, err := store.SavePortForwardRule(ctx, want[0])
	if err != nil {
		t.Fatalf("save duplicate rule: %v", err)
	}
	if duplicate.Id != want[0].Id {
		t.Fatalf("duplicate rule ID = %q, want existing ID %q", duplicate.Id, want[0].Id)
	}

	got, err := store.ListPortForwardRules(ctx, host.Id)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d rules, want %d: %#v", len(got), len(want), got)
	}
	for _, expected := range want {
		found := false
		for _, actual := range got {
			if actual.Id == expected.Id && actual.HostId == expected.HostId && actual.Kind == expected.Kind &&
				actual.ListenAddress == expected.ListenAddress && actual.RemoteAddress == expected.RemoteAddress {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("listed rules do not contain %#v", expected)
		}
	}

	localHost, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindLocal, Name: "local"})
	if err != nil {
		t.Fatalf("create local host: %v", err)
	}
	if _, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: localHost.Id, Kind: model.PortForwardSOCKS5, ListenAddress: "127.0.0.1:1081",
	}); err == nil {
		t.Fatal("saved a port forwarding rule for a local terminal host")
	}
	if _, err := store.SavePortForwardRule(ctx, model.PortForwardRule{
		HostId: host.Id, Kind: model.PortForwardSOCKS5, ListenAddress: "0.0.0.0:1082",
	}); err == nil {
		t.Fatal("saved an unauthenticated SOCKS5 listener outside loopback")
	}
	if err := store.DeletePortForwardRule(ctx, want[1].Id); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	if err := store.DeleteHost(ctx, host.Id); err != nil {
		t.Fatalf("delete SSH host: %v", err)
	}
	got, err = store.ListPortForwardRules(ctx, host.Id)
	if err != nil {
		t.Fatalf("list rules after host delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rules remained after host deletion: %#v", got)
	}
}

func TestVersionTwoDatabaseMigratesPortForwardRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workbench.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "legacy-host", IP: "192.0.2.10", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw SQLite database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE port_forward_rules`); err != nil {
		t.Fatalf("remove v3 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE quick_commands`); err != nil {
		t.Fatalf("remove v4 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE sftp_filters`); err != nil {
		t.Fatalf("remove v7 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE scheduled_transfers`); err != nil {
		t.Fatalf("remove v8 table for migration fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
		t.Fatalf("mark database as v2: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw SQLite database: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v2 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	gotHost, err := store.GetHost(ctx, host.Id)
	if err != nil {
		t.Fatalf("read host after migration: %v", err)
	}
	if gotHost.Name != host.Name {
		t.Fatalf("migrated host name = %q, want %q", gotHost.Name, host.Name)
	}
	rules, err := store.ListPortForwardRules(ctx, host.Id)
	if err != nil {
		t.Fatalf("read migrated forwarding rules: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("new migration table should start empty, got %#v", rules)
	}
}

func dropRecentHostMetadataForLegacyFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP INDEX IF EXISTS hosts_by_favorite`,
		`DROP INDEX IF EXISTS hosts_by_recent_connection`,
		`ALTER TABLE hosts DROP COLUMN last_connected_at`,
		`ALTER TABLE hosts DROP COLUMN favorite`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare legacy host metadata fixture with %q: %v", statement, err)
		}
	}
}

func TestDeleteHostRejectsReferencedJumpHost(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "workbench.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	jump, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "gateway", IP: "127.0.0.1", Port: 22, User: "tester",
	})
	if err != nil {
		t.Fatalf("create jump host: %v", err)
	}
	if _, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "application", IP: "10.0.0.20", Port: 22, User: "tester",
		ProxyJumpList: []string{jump.Id},
	}); err != nil {
		t.Fatalf("create host that uses jump: %v", err)
	}
	if err := store.DeleteHost(ctx, jump.Id); !errors.Is(err, ErrHostIsJump) {
		t.Fatalf("delete referenced jump host error = %v, want %v", err, ErrHostIsJump)
	}
	if _, err := store.GetHost(ctx, jump.Id); err != nil {
		t.Fatalf("jump host was deleted despite reference: %v", err)
	}
}
