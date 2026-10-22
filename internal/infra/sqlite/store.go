package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schemaVersion = 36

var (
	ErrNotFound                  = errors.New("record not found")
	ErrGroupNotEmpty             = errors.New("group is not empty")
	ErrHostIsJump                = errors.New("主机仍被其他连接配置为跳板机")
	ErrHostHasScheduledTransfers = errors.New("主机仍被计划传输任务引用")
	ErrHostNotFavorite           = errors.New("主机尚未收藏")
	ErrHostNotInShortcutBar      = errors.New("主机不在连接栏中")
	ErrAuthProfileInUse          = errors.New("认证配置档仍被主机或分组使用")
)

// Store persists connection entries and their groups in SQLite.
type Store struct {
	db *sql.DB
}

// DefaultPath returns the per-user database path.
func DefaultPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config directory: %w", err)
	}
	return filepath.Join(configDir, "tui-ssh-workbench", "workbench.db"), nil
}

// Open opens the database, creates its parent directory, and applies schema migrations.
// An empty path uses the per-user default database path.
func Open(ctx context.Context, path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := store.initialize(ctx, path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initialize(ctx context.Context, path string) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable SQLite foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("configure SQLite busy timeout: %w", err)
	}

	if path != ":memory:" {
		if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
			return fmt.Errorf("enable SQLite write-ahead log: %w", err)
		}
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("set database file permissions: %w", err)
		}
	}

	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	// Migration 18 replaces hosts to extend its kind constraint. Keep child
	// records intact while the table is swapped, then verify all references.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable SQLite foreign keys for migration: %w", err)
	}
	defer func() { _, _ = s.db.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`) }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin database migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if version == 0 {
		statements := []string{
			`CREATE TABLE groups (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL CHECK (length(trim(name)) > 0),
			parent_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT
		)`,
			`CREATE UNIQUE INDEX groups_unique_sibling_name
			ON groups(COALESCE(parent_id, ''), name COLLATE NOCASE)`,
			`CREATE TABLE hosts (
			id TEXT PRIMARY KEY NOT NULL,
			name TEXT NOT NULL CHECK (length(trim(name)) > 0),
			address TEXT NOT NULL CHECK (length(trim(address)) > 0),
			group_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT,
			description TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
			username TEXT NOT NULL DEFAULT '',
			startup TEXT NOT NULL DEFAULT '',
			credential_ref TEXT NOT NULL DEFAULT '',
			proxy_jump_list TEXT NOT NULL DEFAULT '[]'
		)`,
			`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 1: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
			return fmt.Errorf("record database migration 1: %w", err)
		}
		version = 1
	}
	if version == 1 {
		statements := []string{
			`CREATE TABLE hosts_v2 (
				id TEXT PRIMARY KEY NOT NULL,
				kind TEXT NOT NULL DEFAULT 'ssh' CHECK (kind IN ('ssh', 'local')),
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				address TEXT NOT NULL DEFAULT '',
				group_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT,
				description TEXT NOT NULL DEFAULT '',
				port INTEGER NOT NULL DEFAULT 0,
				username TEXT NOT NULL DEFAULT '',
				startup TEXT NOT NULL DEFAULT '',
				credential_ref TEXT NOT NULL DEFAULT '',
				proxy_jump_list TEXT NOT NULL DEFAULT '[]',
				CHECK (
					(kind = 'ssh' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535)
					OR (kind = 'local' AND address = '' AND port = 0 AND username = '')
				)
			)`,
			`INSERT INTO hosts_v2 (
				id, kind, name, address, group_id, description, port, username,
				startup, credential_ref, proxy_jump_list
			)
			SELECT id, 'ssh', name, address, group_id, description, port, username,
			       startup, credential_ref, proxy_jump_list FROM hosts`,
			`DROP INDEX hosts_by_group`,
			`DROP TABLE hosts`,
			`ALTER TABLE hosts_v2 RENAME TO hosts`,
			`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 2: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
			return fmt.Errorf("record database migration 2: %w", err)
		}
		version = 2
	}
	if version == 2 {
		statements := []string{
			`CREATE TABLE port_forward_rules (
				id TEXT PRIMARY KEY NOT NULL,
				host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('local_tcp', 'remote_tcp', 'socks5')),
				listen_address TEXT NOT NULL CHECK (length(trim(listen_address)) > 0),
				remote_address TEXT NOT NULL DEFAULT '',
				CHECK (
					(kind = 'socks5' AND remote_address = '')
					OR (kind IN ('local_tcp', 'remote_tcp') AND length(trim(remote_address)) > 0)
				)
			)`,
			`CREATE UNIQUE INDEX port_forward_rules_unique
				ON port_forward_rules(host_id, kind, listen_address, remote_address)`,
			`CREATE INDEX port_forward_rules_by_host
				ON port_forward_rules(host_id, kind, listen_address)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 3: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
			return fmt.Errorf("record database migration 3: %w", err)
		}
		version = 3
	}
	if version == 3 {
		statements := []string{
			`CREATE TABLE quick_commands (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				command TEXT NOT NULL CHECK (length(trim(command)) > 0)
			)`,
			`CREATE UNIQUE INDEX quick_commands_unique_name
				ON quick_commands(name COLLATE NOCASE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 4: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
			return fmt.Errorf("record database migration 4: %w", err)
		}
		version = 4
	}
	if version == 4 {
		statements := []string{
			`CREATE TABLE hosts_v5 (
				id TEXT PRIMARY KEY NOT NULL,
				kind TEXT NOT NULL DEFAULT 'ssh' CHECK (kind IN ('ssh', 'local', 'ftp')),
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				address TEXT NOT NULL DEFAULT '',
				group_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT,
				description TEXT NOT NULL DEFAULT '',
				port INTEGER NOT NULL DEFAULT 0,
				username TEXT NOT NULL DEFAULT '',
				startup TEXT NOT NULL DEFAULT '',
				credential_ref TEXT NOT NULL DEFAULT '',
				proxy_jump_list TEXT NOT NULL DEFAULT '[]',
				tls_mode TEXT NOT NULL DEFAULT '',
				CHECK (
					(kind = 'ssh' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535 AND tls_mode = '')
					OR (kind = 'local' AND address = '' AND port = 0 AND username = '' AND tls_mode = '')
					OR (kind = 'ftp' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535
						AND tls_mode IN ('off', 'explicit', 'implicit'))
				)
			)`,
			`INSERT INTO hosts_v5 (
				id, kind, name, address, group_id, description, port, username, startup,
				credential_ref, proxy_jump_list, tls_mode
			)
			SELECT id, kind, name, address, group_id, description, port, username, startup,
			       credential_ref, proxy_jump_list,
			       CASE WHEN kind = 'ftp' THEN 'off' ELSE '' END
			FROM hosts`,
			`CREATE TABLE port_forward_rules_v5 (
				id TEXT PRIMARY KEY NOT NULL,
				host_id TEXT NOT NULL REFERENCES hosts_v5(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('local_tcp', 'remote_tcp', 'socks5')),
				listen_address TEXT NOT NULL CHECK (length(trim(listen_address)) > 0),
				remote_address TEXT NOT NULL DEFAULT '',
				CHECK (
					(kind = 'socks5' AND remote_address = '')
					OR (kind IN ('local_tcp', 'remote_tcp') AND length(trim(remote_address)) > 0)
				)
			)`,
			`INSERT INTO port_forward_rules_v5 (id, host_id, kind, listen_address, remote_address)
				SELECT id, host_id, kind, listen_address, remote_address FROM port_forward_rules`,
			`DROP TABLE port_forward_rules`,
			`DROP INDEX hosts_by_group`,
			`DROP TABLE hosts`,
			`ALTER TABLE hosts_v5 RENAME TO hosts`,
			`ALTER TABLE port_forward_rules_v5 RENAME TO port_forward_rules`,
			`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
			`CREATE UNIQUE INDEX port_forward_rules_unique
				ON port_forward_rules(host_id, kind, listen_address, remote_address)`,
			`CREATE INDEX port_forward_rules_by_host
				ON port_forward_rules(host_id, kind, listen_address)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 5: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 5`); err != nil {
			return fmt.Errorf("record database migration 5: %w", err)
		}
		version = 5
	}
	if version == 5 {
		statements := []string{
			`CREATE TABLE hosts_v6 (
				id TEXT PRIMARY KEY NOT NULL,
				kind TEXT NOT NULL DEFAULT 'ssh' CHECK (kind IN ('ssh', 'local', 'ftp', 'telnet')),
				name TEXT NOT NULL CHECK (length(trim(name)) > 0),
				address TEXT NOT NULL DEFAULT '',
				group_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT,
				description TEXT NOT NULL DEFAULT '',
				port INTEGER NOT NULL DEFAULT 0,
				username TEXT NOT NULL DEFAULT '',
				startup TEXT NOT NULL DEFAULT '',
				credential_ref TEXT NOT NULL DEFAULT '',
				proxy_jump_list TEXT NOT NULL DEFAULT '[]',
				tls_mode TEXT NOT NULL DEFAULT '',
				CHECK (
					(kind = 'ssh' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535 AND tls_mode = '')
					OR (kind = 'local' AND address = '' AND port = 0 AND username = '' AND tls_mode = '')
					OR (kind = 'ftp' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535
						AND tls_mode IN ('off', 'explicit', 'implicit'))
					OR (kind = 'telnet' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535
						AND tls_mode = '' AND startup = '' AND credential_ref = '' AND proxy_jump_list = '[]')
				)
			)`,
			`INSERT INTO hosts_v6 (
				id, kind, name, address, group_id, description, port, username, startup,
				credential_ref, proxy_jump_list, tls_mode
			)
			SELECT id, kind, name, address, group_id, description, port, username, startup,
			       credential_ref, proxy_jump_list, tls_mode FROM hosts`,
			`CREATE TABLE port_forward_rules_v6 (
				id TEXT PRIMARY KEY NOT NULL,
				host_id TEXT NOT NULL REFERENCES hosts_v6(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('local_tcp', 'remote_tcp', 'socks5')),
				listen_address TEXT NOT NULL CHECK (length(trim(listen_address)) > 0),
				remote_address TEXT NOT NULL DEFAULT '',
				CHECK (
					(kind = 'socks5' AND remote_address = '')
					OR (kind IN ('local_tcp', 'remote_tcp') AND length(trim(remote_address)) > 0)
				)
			)`,
			`INSERT INTO port_forward_rules_v6 (id, host_id, kind, listen_address, remote_address)
				SELECT id, host_id, kind, listen_address, remote_address FROM port_forward_rules`,
			`DROP TABLE port_forward_rules`,
			`DROP INDEX hosts_by_group`,
			`DROP TABLE hosts`,
			`ALTER TABLE hosts_v6 RENAME TO hosts`,
			`ALTER TABLE port_forward_rules_v6 RENAME TO port_forward_rules`,
			`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
			`CREATE UNIQUE INDEX port_forward_rules_unique
				ON port_forward_rules(host_id, kind, listen_address, remote_address)`,
			`CREATE INDEX port_forward_rules_by_host
				ON port_forward_rules(host_id, kind, listen_address)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 6: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 6`); err != nil {
			return fmt.Errorf("record database migration 6: %w", err)
		}
		version = 6
	}
	if version == 6 {
		statements := []string{
			`CREATE TABLE sftp_filters (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				expression TEXT NOT NULL CHECK (length(trim(expression)) BETWEEN 1 AND 256)
			)`,
			`CREATE UNIQUE INDEX sftp_filters_unique_name
				ON sftp_filters(name COLLATE NOCASE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 7: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 7`); err != nil {
			return fmt.Errorf("record database migration 7: %w", err)
		}
		version = 7
	}
	if version == 7 {
		statements := []string{
			`CREATE TABLE scheduled_transfers (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
				direction TEXT NOT NULL CHECK (direction IN ('upload', 'download')),
				local_path TEXT NOT NULL CHECK (length(trim(local_path)) > 0),
				remote_path TEXT NOT NULL CHECK (length(trim(remote_path)) > 0),
				cron_spec TEXT NOT NULL CHECK (length(trim(cron_spec)) BETWEEN 1 AND 128),
				enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
				overwrite INTEGER NOT NULL DEFAULT 0 CHECK (overwrite IN (0, 1)),
				next_run_at INTEGER NOT NULL DEFAULT 0,
				last_run_at INTEGER NOT NULL DEFAULT 0,
				last_finish_at INTEGER NOT NULL DEFAULT 0,
				last_status TEXT NOT NULL DEFAULT 'idle' CHECK (last_status IN ('idle', 'running', 'succeeded', 'failed', 'canceled')),
				last_error TEXT NOT NULL DEFAULT '',
				lease_until INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX scheduled_transfers_due
				ON scheduled_transfers(enabled, next_run_at, lease_until)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 8: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 8`); err != nil {
			return fmt.Errorf("record database migration 8: %w", err)
		}
		version = 8
	}
	if version == 8 {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE scheduled_transfers ADD COLUMN lease_token TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("apply database migration 9: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 9`); err != nil {
			return fmt.Errorf("record database migration 9: %w", err)
		}
		version = 9
	}
	if version == 9 {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE scheduled_transfers ADD COLUMN run_requested INTEGER NOT NULL DEFAULT 0 CHECK (run_requested IN (0, 1))`); err != nil {
			return fmt.Errorf("apply database migration 10: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `CREATE INDEX scheduled_transfers_manual_due ON scheduled_transfers(run_requested, enabled, next_run_at)`); err != nil {
			return fmt.Errorf("apply database migration 10: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 10`); err != nil {
			return fmt.Errorf("record database migration 10: %w", err)
		}
		version = 10
	}
	if version == 10 {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE scheduled_transfers ADD COLUMN time_zone TEXT NOT NULL DEFAULT 'Local'`); err != nil {
			return fmt.Errorf("apply database migration 11: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 11`); err != nil {
			return fmt.Errorf("record database migration 11: %w", err)
		}
		version = 11
	}
	if version == 11 {
		statements := []string{
			`ALTER TABLE hosts ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1))`,
			`ALTER TABLE hosts ADD COLUMN last_connected_at INTEGER NOT NULL DEFAULT 0 CHECK (last_connected_at >= 0)`,
			`CREATE INDEX hosts_by_favorite ON hosts(favorite, name COLLATE NOCASE)`,
			`CREATE INDEX hosts_by_recent_connection ON hosts(last_connected_at DESC) WHERE last_connected_at > 0`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 12: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 12`); err != nil {
			return fmt.Errorf("record database migration 12: %w", err)
		}
		version = 12
	}
	if version == 12 {
		var fileProtocolColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'file_protocol'`).Scan(&fileProtocolColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 13: %w", err)
		}
		if fileProtocolColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN file_protocol TEXT NOT NULL DEFAULT '' CHECK (file_protocol IN ('', 'sftp', 'scp'))`); err != nil {
				return fmt.Errorf("apply database migration 13: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 13`); err != nil {
			return fmt.Errorf("record database migration 13: %w", err)
		}
		version = 13
	}
	if version == 13 {
		var identityFileColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'identity_file'`).Scan(&identityFileColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 14: %w", err)
		}
		if identityFileColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN identity_file TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("apply database migration 14: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 14`); err != nil {
			return fmt.Errorf("record database migration 14: %w", err)
		}
		version = 14
	}
	if version == 14 {
		var terminalProtocolColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'terminal_protocol'`).Scan(&terminalProtocolColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 15: %w", err)
		}
		if terminalProtocolColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN terminal_protocol TEXT NOT NULL DEFAULT '' CHECK (terminal_protocol IN ('', 'telnet', 'rlogin'))`); err != nil {
				return fmt.Errorf("apply database migration 15: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 15`); err != nil {
			return fmt.Errorf("record database migration 15: %w", err)
		}
		version = 15
	}
	if version == 15 {
		var x11ForwardingColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'x11_forwarding'`).Scan(&x11ForwardingColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 16: %w", err)
		}
		if x11ForwardingColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN x11_forwarding INTEGER NOT NULL DEFAULT 0 CHECK (x11_forwarding IN (0, 1))`); err != nil {
				return fmt.Errorf("apply database migration 16: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 16`); err != nil {
			return fmt.Errorf("record database migration 16: %w", err)
		}
		version = 16
	}
	if version == 16 {
		var favoriteOrderColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'favorite_order'`).Scan(&favoriteOrderColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 17: %w", err)
		}
		if favoriteOrderColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN favorite_order INTEGER NOT NULL DEFAULT 0 CHECK (favorite_order >= 0)`); err != nil {
				return fmt.Errorf("apply database migration 17: %w", err)
			}
			if _, err = tx.ExecContext(ctx, `
				UPDATE hosts AS target
				SET favorite_order = (
					SELECT COUNT(*) FROM hosts AS sibling
					WHERE sibling.favorite = 1 AND (
						sibling.name COLLATE NOCASE < target.name COLLATE NOCASE OR
						(sibling.name COLLATE NOCASE = target.name COLLATE NOCASE AND sibling.id <= target.id)
					)
				)
				WHERE target.favorite = 1
			`); err != nil {
				return fmt.Errorf("initialize favorite shortcut order in migration 17: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 17`); err != nil {
			return fmt.Errorf("record database migration 17: %w", err)
		}
		version = 17
	}
	if version == 17 {
		if err := migrateSerialHostSchema(ctx, tx); err != nil {
			return err
		}
		version = 18
	}
	if version == 18 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS workspace_layouts (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				data TEXT NOT NULL CHECK (length(data) BETWEEN 2 AND 262144),
				updated_at INTEGER NOT NULL CHECK (updated_at >= 0)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS workspace_layouts_unique_name
				ON workspace_layouts(name COLLATE NOCASE)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 19: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 19`); err != nil {
			return fmt.Errorf("record database migration 19: %w", err)
		}
		version = 19
	}
	if version == 19 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS app_preferences (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				default_host_id TEXT NULL REFERENCES hosts(id) ON DELETE SET NULL
			)`,
			`INSERT OR IGNORE INTO app_preferences (id, default_host_id) VALUES (1, NULL)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 20: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 20`); err != nil {
			return fmt.Errorf("record database migration 20: %w", err)
		}
		version = 20
	}
	if version == 20 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS auth_profiles (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				username TEXT NOT NULL DEFAULT '',
				credential_ref TEXT NOT NULL DEFAULT '',
				identity_file TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS auth_profiles_unique_name
				ON auth_profiles(name COLLATE NOCASE)`,
			`CREATE INDEX IF NOT EXISTS hosts_by_auth_profile ON hosts(auth_profile_id)`,
		}
		for _, statement := range statements[:2] {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 21: %w", err)
			}
		}
		var authProfileColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'auth_profile_id'`).Scan(&authProfileColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 21: %w", err)
		}
		if authProfileColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN auth_profile_id TEXT NULL REFERENCES auth_profiles(id) ON DELETE RESTRICT`); err != nil {
				return fmt.Errorf("apply database migration 21: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, statements[2]); err != nil {
			return fmt.Errorf("apply database migration 21: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 21`); err != nil {
			return fmt.Errorf("record database migration 21: %w", err)
		}
	}
	if version < 22 {
		var flowControlColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'serial_flow_control'`).Scan(&flowControlColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 22: %w", err)
		}
		if flowControlColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN serial_flow_control TEXT NOT NULL DEFAULT '' CHECK (serial_flow_control IN ('', 'none', 'xon_xoff', 'rts_cts'))`); err != nil {
				return fmt.Errorf("apply database migration 22: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hosts SET serial_flow_control = 'none' WHERE kind = 'serial' AND serial_flow_control = ''`); err != nil {
			return fmt.Errorf("initialize serial flow control in migration 22: %w", err)
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 22`); err != nil {
			return fmt.Errorf("record database migration 22: %w", err)
		}
	}
	if version < 23 {
		var lastWorkspaceColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('app_preferences') WHERE name = 'last_workspace_data'`).Scan(&lastWorkspaceColumn); err != nil {
			return fmt.Errorf("inspect app preferences for database migration 23: %w", err)
		}
		if lastWorkspaceColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE app_preferences ADD COLUMN last_workspace_data TEXT NOT NULL DEFAULT '' CHECK (length(last_workspace_data) <= 262144)`); err != nil {
				return fmt.Errorf("apply database migration 23: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 23`); err != nil {
			return fmt.Errorf("record database migration 23: %w", err)
		}
		version = 23
	}
	if version < 24 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS shortcut_bars (
				id TEXT PRIMARY KEY NOT NULL,
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				sort_order INTEGER NOT NULL CHECK (sort_order > 0)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS shortcut_bars_unique_name
				ON shortcut_bars(name COLLATE NOCASE)`,
			`CREATE INDEX IF NOT EXISTS shortcut_bars_by_order
				ON shortcut_bars(sort_order, name COLLATE NOCASE, id)`,
			`CREATE TABLE IF NOT EXISTS shortcut_bar_hosts (
				bar_id TEXT NOT NULL REFERENCES shortcut_bars(id) ON DELETE CASCADE,
				host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
				sort_order INTEGER NOT NULL CHECK (sort_order > 0),
				PRIMARY KEY (bar_id, host_id)
			)`,
			`CREATE INDEX IF NOT EXISTS shortcut_bar_hosts_by_order
				ON shortcut_bar_hosts(bar_id, sort_order, host_id)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 24: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 24`); err != nil {
			return fmt.Errorf("record database migration 24: %w", err)
		}
		version = 24
	}
	if version < 25 {
		var startupScriptColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'startup_script_path'`).Scan(&startupScriptColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 25: %w", err)
		}
		if startupScriptColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN startup_script_path TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("apply database migration 25: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 25`); err != nil {
			return fmt.Errorf("record database migration 25: %w", err)
		}
		version = 25
	}
	if version < 26 {
		var terminalEncodingColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'terminal_encoding'`).Scan(&terminalEncodingColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 26: %w", err)
		}
		if terminalEncodingColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN terminal_encoding TEXT NOT NULL DEFAULT 'UTF-8' CHECK (terminal_encoding IN ('UTF-8', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))`); err != nil {
				return fmt.Errorf("apply database migration 26: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 26`); err != nil {
			return fmt.Errorf("record database migration 26: %w", err)
		}
		version = 26
	}
	if version < 27 {
		var terminalInputEncodingColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'terminal_input_encoding'`).Scan(&terminalInputEncodingColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 27: %w", err)
		}
		if terminalInputEncodingColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN terminal_input_encoding TEXT NOT NULL DEFAULT '' CHECK (terminal_input_encoding IN ('', 'UTF-8', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))`); err != nil {
				return fmt.Errorf("apply database migration 27: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 27`); err != nil {
			return fmt.Errorf("record database migration 27: %w", err)
		}
	}
	if version < 28 {
		if err := migrateTerminalEncodingSchema(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 28`); err != nil {
			return fmt.Errorf("record database migration 28: %w", err)
		}
	}
	if version < 29 {
		var ftpActiveModeColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = 'ftp_active_mode'`).Scan(&ftpActiveModeColumn); err != nil {
			return fmt.Errorf("inspect hosts columns for database migration 29: %w", err)
		}
		if ftpActiveModeColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN ftp_active_mode INTEGER NOT NULL DEFAULT 0 CHECK (ftp_active_mode IN (0, 1))`); err != nil {
				return fmt.Errorf("apply database migration 29: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 29`); err != nil {
			return fmt.Errorf("record database migration 29: %w", err)
		}
	}
	if version < 30 {
		var appearanceSchemeColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('app_preferences') WHERE name = 'appearance_scheme'`).Scan(&appearanceSchemeColumn); err != nil {
			return fmt.Errorf("inspect app preferences for database migration 30: %w", err)
		}
		if appearanceSchemeColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE app_preferences ADD COLUMN appearance_scheme TEXT NOT NULL DEFAULT '' CHECK (appearance_scheme IN ('', 'default', 'nord', 'dracula', 'solarized-dark', 'high-contrast', 'catppuccin-mocha', 'tokyo-night', 'gruvbox-dark', 'one-dark'))`); err != nil {
				return fmt.Errorf("apply database migration 30: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 30`); err != nil {
			return fmt.Errorf("record database migration 30: %w", err)
		}
	}
	if version < 31 {
		for _, column := range []string{"local_start_dir", "remote_start_dir"} {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('hosts') WHERE name = ?`, column).Scan(&count); err != nil {
				return fmt.Errorf("inspect hosts columns for database migration 31: %w", err)
			}
			if count == 0 {
				if _, err = tx.ExecContext(ctx, `ALTER TABLE hosts ADD COLUMN `+column+` TEXT NOT NULL DEFAULT '' CHECK (length(`+column+`) <= 4096)`); err != nil {
					return fmt.Errorf("apply database migration 31: %w", err)
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 31`); err != nil {
			return fmt.Errorf("record database migration 31: %w", err)
		}
	}
	if version < 32 {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS sftp_bookmarks (
				id TEXT PRIMARY KEY NOT NULL,
				scope_key TEXT NOT NULL CHECK (length(trim(scope_key)) BETWEEN 1 AND 128),
				remote INTEGER NOT NULL CHECK (remote IN (0, 1)),
				name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
				path TEXT NOT NULL CHECK (length(trim(path)) BETWEEN 1 AND 4096)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS sftp_bookmarks_unique_name
				ON sftp_bookmarks(scope_key, remote, name COLLATE NOCASE)`,
			`CREATE INDEX IF NOT EXISTS sftp_bookmarks_by_scope
				ON sftp_bookmarks(scope_key, remote, name COLLATE NOCASE, id)`,
		}
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply database migration 32: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 32`); err != nil {
			return fmt.Errorf("record database migration 32: %w", err)
		}
	}
	if version < 33 {
		var groupAuthProfileColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('groups') WHERE name = 'auth_profile_id'`).Scan(&groupAuthProfileColumn); err != nil {
			return fmt.Errorf("inspect groups for database migration 33: %w", err)
		}
		if groupAuthProfileColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE groups ADD COLUMN auth_profile_id TEXT NULL REFERENCES auth_profiles(id) ON DELETE RESTRICT`); err != nil {
				return fmt.Errorf("apply database migration 33: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 33`); err != nil {
			return fmt.Errorf("record database migration 33: %w", err)
		}
	}
	if version < 34 {
		for _, column := range []string{"local_start_dir", "remote_start_dir"} {
			var present int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('groups') WHERE name = ?`, column).Scan(&present); err != nil {
				return fmt.Errorf("inspect groups for database migration 34: %w", err)
			}
			if present == 0 {
				if _, err = tx.ExecContext(ctx, `ALTER TABLE groups ADD COLUMN `+column+` TEXT NOT NULL DEFAULT '' CHECK (length(`+column+`) <= 1024)`); err != nil {
					return fmt.Errorf("apply database migration 34: %w", err)
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 34`); err != nil {
			return fmt.Errorf("record database migration 34: %w", err)
		}
	}
	if version < 35 {
		var editorCommandColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('app_preferences') WHERE name = 'sftp_external_editor_command'`).Scan(&editorCommandColumn); err != nil {
			return fmt.Errorf("inspect app preferences for database migration 35: %w", err)
		}
		if editorCommandColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE app_preferences ADD COLUMN sftp_external_editor_command TEXT NOT NULL DEFAULT '' CHECK (length(sftp_external_editor_command) <= 1024)`); err != nil {
				return fmt.Errorf("apply database migration 35: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 35`); err != nil {
			return fmt.Errorf("record database migration 35: %w", err)
		}
	}
	if version < 36 {
		var footerShortcutsColumn int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('app_preferences') WHERE name = 'footer_shortcuts_visible'`).Scan(&footerShortcutsColumn); err != nil {
			return fmt.Errorf("inspect app preferences for database migration 36: %w", err)
		}
		if footerShortcutsColumn == 0 {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE app_preferences ADD COLUMN footer_shortcuts_visible INTEGER NULL CHECK (footer_shortcuts_visible IS NULL OR footer_shortcuts_visible IN (0, 1))`); err != nil {
				return fmt.Errorf("apply database migration 36: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 36`); err != nil {
			return fmt.Errorf("record database migration 36: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database migration: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("re-enable SQLite foreign keys after migration: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check SQLite foreign keys after migration: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID any
		var parent string
		var foreignKey int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return fmt.Errorf("read SQLite foreign-key check: %w", err)
		}
		return fmt.Errorf("database migration left a foreign-key violation in %s (parent %s, key %d, row %v)", table, parent, foreignKey, rowID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate SQLite foreign-key check: %w", err)
	}
	return nil
}

// migrateTerminalEncodingSchema extends the checked encoding values without
// discarding existing host settings or dependent rows. SQLite cannot alter a
// CHECK constraint in place, so copy the existing table definition and rows.
func migrateTerminalEncodingSchema(ctx context.Context, tx *sql.Tx) error {
	var tableSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'hosts'`).Scan(&tableSQL); err != nil {
		return fmt.Errorf("read hosts table definition for database migration 28: %w", err)
	}
	createPrefix := "CREATE TABLE hosts ("
	if !strings.HasPrefix(tableSQL, createPrefix) {
		createPrefix = `CREATE TABLE "hosts" (`
	}
	if !strings.HasPrefix(tableSQL, createPrefix) {
		return errors.New("database migration 28 could not identify the hosts table definition")
	}
	tableSQL = "CREATE TABLE hosts_v28 (" + strings.TrimPrefix(tableSQL, createPrefix)
	oldOutputConstraint := "CHECK (terminal_encoding IN ('UTF-8', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))"
	newOutputConstraint := "CHECK (terminal_encoding IN ('UTF-8', 'GBK', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))"
	oldInputConstraint := "CHECK (terminal_input_encoding IN ('', 'UTF-8', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))"
	newInputConstraint := "CHECK (terminal_input_encoding IN ('', 'UTF-8', 'GBK', 'GB18030', 'Big5', 'Shift_JIS', 'Windows-1252'))"
	// A database can report an older user_version while already having one of
	// the new constraints (for example, after an interrupted/manual migration
	// or when a test removes only one recently added column). Accept either
	// valid form per column, and upgrade only the old form.
	switch {
	case strings.Contains(tableSQL, oldOutputConstraint):
		tableSQL = strings.Replace(tableSQL, oldOutputConstraint, newOutputConstraint, 1)
	case !strings.Contains(tableSQL, newOutputConstraint):
		return errors.New("database migration 28 found an unexpected terminal encoding constraint")
	}
	switch {
	case strings.Contains(tableSQL, oldInputConstraint):
		tableSQL = strings.Replace(tableSQL, oldInputConstraint, newInputConstraint, 1)
	case !strings.Contains(tableSQL, newInputConstraint):
		return errors.New("database migration 28 found an unexpected terminal input encoding constraint")
	}
	statements := []string{
		tableSQL,
		`INSERT INTO hosts_v28 SELECT * FROM hosts`,
		`DROP INDEX hosts_by_group`,
		`DROP INDEX hosts_by_favorite`,
		`DROP INDEX hosts_by_recent_connection`,
		`DROP INDEX hosts_by_auth_profile`,
		`DROP TABLE hosts`,
		`ALTER TABLE hosts_v28 RENAME TO hosts`,
		`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_favorite ON hosts(favorite, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_recent_connection ON hosts(last_connected_at DESC) WHERE last_connected_at > 0`,
		`CREATE INDEX hosts_by_auth_profile ON hosts(auth_profile_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply database migration 28: %w", err)
		}
	}
	return nil
}

func migrateSerialHostSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE hosts_v18 (
			id TEXT PRIMARY KEY NOT NULL,
			kind TEXT NOT NULL DEFAULT 'ssh' CHECK (kind IN ('ssh', 'local', 'ftp', 'telnet', 'serial')),
			name TEXT NOT NULL CHECK (length(trim(name)) > 0),
			address TEXT NOT NULL DEFAULT '',
			group_id TEXT NULL REFERENCES groups(id) ON DELETE RESTRICT,
			description TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			startup TEXT NOT NULL DEFAULT '',
			credential_ref TEXT NOT NULL DEFAULT '',
			proxy_jump_list TEXT NOT NULL DEFAULT '[]',
			tls_mode TEXT NOT NULL DEFAULT '',
			favorite INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1)),
			last_connected_at INTEGER NOT NULL DEFAULT 0 CHECK (last_connected_at >= 0),
			file_protocol TEXT NOT NULL DEFAULT '' CHECK (file_protocol IN ('', 'sftp', 'scp')),
			identity_file TEXT NOT NULL DEFAULT '',
			terminal_protocol TEXT NOT NULL DEFAULT '' CHECK (terminal_protocol IN ('', 'telnet', 'rlogin')),
			x11_forwarding INTEGER NOT NULL DEFAULT 0 CHECK (x11_forwarding IN (0, 1)),
			favorite_order INTEGER NOT NULL DEFAULT 0 CHECK (favorite_order >= 0),
		serial_baud_rate INTEGER NOT NULL DEFAULT 0 CHECK (serial_baud_rate = 0 OR serial_baud_rate BETWEEN 50 AND 4000000),
		serial_data_bits INTEGER NOT NULL DEFAULT 0 CHECK (serial_data_bits = 0 OR serial_data_bits BETWEEN 5 AND 8),
		serial_parity TEXT NOT NULL DEFAULT '' CHECK (serial_parity IN ('', 'none', 'even', 'odd', 'mark', 'space')),
		serial_stop_bits TEXT NOT NULL DEFAULT '' CHECK (serial_stop_bits IN ('', '1', '1.5', '2')),
			CHECK (
			(kind = 'ssh' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535 AND tls_mode = '' AND serial_baud_rate = 0 AND serial_data_bits = 0 AND serial_parity = '' AND serial_stop_bits = '')
			OR (kind = 'local' AND address = '' AND port = 0 AND username = '' AND tls_mode = '' AND serial_baud_rate = 0 AND serial_data_bits = 0 AND serial_parity = '' AND serial_stop_bits = '')
				OR (kind = 'ftp' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535
				AND tls_mode IN ('off', 'explicit', 'implicit') AND serial_baud_rate = 0 AND serial_data_bits = 0 AND serial_parity = '' AND serial_stop_bits = '')
				OR (kind = 'telnet' AND length(trim(address)) > 0 AND port BETWEEN 1 AND 65535
				AND tls_mode = '' AND startup = '' AND credential_ref = '' AND proxy_jump_list = '[]' AND serial_baud_rate = 0 AND serial_data_bits = 0 AND serial_parity = '' AND serial_stop_bits = '')
				OR (kind = 'serial' AND length(trim(address)) > 0 AND port = 0 AND username = ''
					AND startup = '' AND credential_ref = '' AND proxy_jump_list = '[]' AND tls_mode = ''
				AND serial_baud_rate BETWEEN 50 AND 4000000 AND serial_data_bits BETWEEN 5 AND 8
				AND serial_parity IN ('none', 'even', 'odd', 'mark', 'space') AND serial_stop_bits IN ('1', '1.5', '2'))
			)
		)`,
		`INSERT INTO hosts_v18 (
			id, kind, name, address, group_id, description, port, username, startup,
			credential_ref, proxy_jump_list, tls_mode, favorite, last_connected_at,
			file_protocol, identity_file, terminal_protocol, x11_forwarding, favorite_order
		)
		SELECT id, kind, name, address, group_id, description, port, username, startup,
		       credential_ref, proxy_jump_list, tls_mode, favorite, last_connected_at,
		       file_protocol, identity_file, terminal_protocol, x11_forwarding, favorite_order
		FROM hosts`,
		`DROP INDEX hosts_by_group`,
		`DROP INDEX hosts_by_favorite`,
		`DROP INDEX hosts_by_recent_connection`,
		`DROP TABLE hosts`,
		`ALTER TABLE hosts_v18 RENAME TO hosts`,
		`CREATE INDEX hosts_by_group ON hosts(group_id, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_favorite ON hosts(favorite, name COLLATE NOCASE)`,
		`CREATE INDEX hosts_by_recent_connection ON hosts(last_connected_at DESC) WHERE last_connected_at > 0`,
		`PRAGMA user_version = 18`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply database migration 18: %w", err)
		}
	}
	return nil
}

func newID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func requireAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read affected row count: %w", operation, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	}
	return nil
}
