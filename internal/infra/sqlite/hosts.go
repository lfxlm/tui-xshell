package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"tui-ssh-workbench/internal/model"
)

// CreateHost stores a host and returns it with a generated ID when needed.
// Password and connection status are runtime-only and are never persisted.
func (s *Store) CreateHost(ctx context.Context, host model.Host) (model.Host, error) {
	if err := normalizeHost(&host); err != nil {
		return model.Host{}, err
	}
	proxyJumps, err := marshalProxyJumps(host.ProxyJumpList)
	if err != nil {
		return model.Host{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO hosts (
			id, kind, name, address, group_id, description, port, username, startup,
			credential_ref, proxy_jump_list, tls_mode, ftp_active_mode, favorite, file_protocol, identity_file, terminal_protocol, x11_forwarding,
			serial_baud_rate, serial_data_bits, serial_parity, serial_stop_bits, serial_flow_control, auth_profile_id, startup_script_path, terminal_encoding, terminal_input_encoding, local_start_dir, remote_start_dir
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, host.Id, host.Kind, host.Name, host.IP, nullableID(host.GroupId), host.Description,
		host.Port, host.User, host.Startup, host.CredentialRef, proxyJumps, host.TLSMode, host.FTPActiveMode, host.Favorite, host.FileProtocol, host.IdentityFile, host.TerminalProtocol, host.X11Forwarding,
		host.SerialBaudRate, host.SerialDataBits, host.SerialParity, host.SerialStopBits, host.SerialFlowControl, nullableID(host.AuthProfileID), host.StartupScriptPath, host.TerminalEncoding, host.TerminalInputEncoding, host.LocalStartDir, host.RemoteStartDir)
	if err != nil {
		return model.Host{}, fmt.Errorf("create host: %w", err)
	}
	return host, nil
}

// GetHost retrieves a host by ID. Runtime-only fields are left at their zero values.
func (s *Store) GetHost(ctx context.Context, id string) (model.Host, error) {
	var host model.Host
	if err := scanHost(s.db.QueryRowContext(ctx, hostSelect+` WHERE id = ?`, id), &host); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Host{}, ErrNotFound
		}
		return model.Host{}, fmt.Errorf("get host: %w", err)
	}
	return host, nil
}

// ListHosts returns all hosts ordered by name.
func (s *Store) ListHosts(ctx context.Context) ([]model.Host, error) {
	return s.listHosts(ctx, hostSelect+` ORDER BY name COLLATE NOCASE, id`)
}

// DefaultHostID returns the saved host selected for automatic connection at
// application startup. An empty ID means no default session is configured.
func (s *Store) DefaultHostID(ctx context.Context) (string, error) {
	var hostID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT default_host_id FROM app_preferences WHERE id = 1`).Scan(&hostID); err != nil {
		return "", fmt.Errorf("read default session: %w", err)
	}
	return hostID.String, nil
}

// SetDefaultHostID selects a saved host for automatic connection at startup.
// Passing an empty ID clears the default. The foreign key rejects unknown IDs.
func (s *Store) SetDefaultHostID(ctx context.Context, hostID string) error {
	hostID = strings.TrimSpace(hostID)
	var value any
	if hostID != "" {
		value = hostID
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_preferences SET default_host_id = ? WHERE id = 1`, value)
	if err != nil {
		return fmt.Errorf("save default session: %w", err)
	}
	return requireAffected(result, "save default session")
}

// ListHostsByGroup returns the hosts in a group. An empty group ID selects
// ungrouped hosts.
func (s *Store) ListHostsByGroup(ctx context.Context, groupID string) ([]model.Host, error) {
	if groupID == "" {
		return s.listHosts(ctx, hostSelect+` WHERE group_id IS NULL ORDER BY name COLLATE NOCASE, id`)
	}
	return s.listHosts(ctx, hostSelect+` WHERE group_id = ? ORDER BY name COLLATE NOCASE, id`, groupID)
}

// UpdateHost updates a host's saved connection settings. Password and
// connection status are ignored.
func (s *Store) UpdateHost(ctx context.Context, host model.Host) error {
	if host.Id == "" {
		return errors.New("host ID is required")
	}
	if err := normalizeHost(&host); err != nil {
		return err
	}
	proxyJumps, err := marshalProxyJumps(host.ProxyJumpList)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE hosts SET
			kind = ?, name = ?, address = ?, group_id = ?, description = ?, port = ?, username = ?,
			startup = ?, credential_ref = ?, proxy_jump_list = ?, tls_mode = ?, ftp_active_mode = ?, file_protocol = ?, identity_file = ?,
			terminal_protocol = ?, x11_forwarding = ?, serial_baud_rate = ?, serial_data_bits = ?, serial_parity = ?, serial_stop_bits = ?, serial_flow_control = ?, auth_profile_id = ?, startup_script_path = ?, terminal_encoding = ?, terminal_input_encoding = ?, local_start_dir = ?, remote_start_dir = ?
		WHERE id = ?
	`, host.Kind, host.Name, host.IP, nullableID(host.GroupId), host.Description, host.Port,
		host.User, host.Startup, host.CredentialRef, proxyJumps, host.TLSMode, host.FTPActiveMode, host.FileProtocol, host.IdentityFile, host.TerminalProtocol, host.X11Forwarding,
		host.SerialBaudRate, host.SerialDataBits, host.SerialParity, host.SerialStopBits, host.SerialFlowControl, nullableID(host.AuthProfileID), host.StartupScriptPath, host.TerminalEncoding, host.TerminalInputEncoding, host.LocalStartDir, host.RemoteStartDir, host.Id)
	if err != nil {
		return fmt.Errorf("update host: %w", err)
	}
	return requireAffected(result, "update host")
}

// DeleteHost removes a host by ID.
func (s *Store) DeleteHost(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete host: begin transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT name, proxy_jump_list FROM hosts WHERE id <> ?`, id)
	if err != nil {
		return fmt.Errorf("delete host: check jump references: %w", err)
	}
	for rows.Next() {
		var name, encodedJumps string
		if err := rows.Scan(&name, &encodedJumps); err != nil {
			_ = rows.Close()
			return fmt.Errorf("delete host: read jump references: %w", err)
		}
		var jumps []string
		if err := json.Unmarshal([]byte(encodedJumps), &jumps); err != nil {
			_ = rows.Close()
			return fmt.Errorf("delete host: decode jump references for %q: %w", name, err)
		}
		for _, jumpID := range jumps {
			if jumpID == id {
				_ = rows.Close()
				return fmt.Errorf("%w：%s", ErrHostIsJump, name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("delete host: iterate jump references: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("delete host: close jump reference query: %w", err)
	}
	var scheduledCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_transfers WHERE host_id = ?`, id).Scan(&scheduledCount); err != nil {
		return fmt.Errorf("delete host: check scheduled transfers: %w", err)
	}
	if scheduledCount > 0 {
		return fmt.Errorf("%w：%d 个任务", ErrHostHasScheduledTransfers, scheduledCount)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete host: %w", err)
	}
	if err := requireAffected(result, "delete host"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete host: commit transaction: %w", err)
	}
	return nil
}

const hostSelect = `
	SELECT id, kind, name, address, group_id, description, port, username, startup,
		       credential_ref, proxy_jump_list, tls_mode, favorite, last_connected_at, file_protocol, identity_file, terminal_protocol, x11_forwarding,
	       favorite_order, serial_baud_rate, serial_data_bits, serial_parity, serial_stop_bits, serial_flow_control, COALESCE(auth_profile_id, ''), startup_script_path, terminal_encoding, terminal_input_encoding, ftp_active_mode, local_start_dir, remote_start_dir
	FROM hosts
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanHost(row rowScanner, host *model.Host) error {
	var groupID sql.NullString
	var proxyJumps string
	var lastConnectedAt int64
	if err := row.Scan(
		&host.Id, &host.Kind, &host.Name, &host.IP, &groupID, &host.Description, &host.Port,
		&host.User, &host.Startup, &host.CredentialRef, &proxyJumps, &host.TLSMode, &host.Favorite, &lastConnectedAt, &host.FileProtocol,
		&host.IdentityFile, &host.TerminalProtocol, &host.X11Forwarding, &host.FavoriteOrder,
		&host.SerialBaudRate, &host.SerialDataBits, &host.SerialParity, &host.SerialStopBits, &host.SerialFlowControl, &host.AuthProfileID, &host.StartupScriptPath, &host.TerminalEncoding, &host.TerminalInputEncoding, &host.FTPActiveMode, &host.LocalStartDir, &host.RemoteStartDir,
	); err != nil {
		return err
	}
	host.GroupId = groupID.String
	if host.Kind == model.HostKindSSH && host.FileProtocol == "" {
		// Existing databases migrated with the neutral empty value retain the
		// pre-protocol behavior: SSH file operations use SFTP.
		host.FileProtocol = model.SSHFileProtocolSFTP
	}
	if host.Kind == model.HostKindTelnet && host.TerminalProtocol == "" {
		host.TerminalProtocol = model.TerminalProtocolTelnet
	}
	if lastConnectedAt > 0 {
		host.LastConnectedAt = time.Unix(0, lastConnectedAt).UTC()
	}
	if err := json.Unmarshal([]byte(proxyJumps), &host.ProxyJumpList); err != nil {
		return fmt.Errorf("decode proxy jump list: %w", err)
	}
	if host.ProxyJumpList == nil {
		host.ProxyJumpList = []string{}
	}
	return nil
}

// SetHostFavorite updates only the favorite flag, preserving all other settings.
func (s *Store) SetHostFavorite(ctx context.Context, id string, favorite bool) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE hosts
		SET favorite_order = CASE WHEN ? THEN (
			SELECT COALESCE(MAX(favorite_order), 0) + 1 FROM hosts WHERE favorite = 1 AND id <> ?
		) ELSE 0 END,
		favorite = ?
		WHERE id = ?
	`, favorite, id, favorite, id)
	if err != nil {
		return fmt.Errorf("set host favorite: %w", err)
	}
	return requireAffected(result, "set host favorite")
}

// SetHostTerminalEncoding updates the visible-output and keyboard-input codecs
// together for a saved host, preserving the existing one-setting workflow.
func (s *Store) SetHostTerminalEncoding(ctx context.Context, id, encoding string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("host ID is required")
	}
	if !model.IsTerminalEncodingSupported(encoding) {
		return errors.New("unsupported terminal encoding")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE hosts SET terminal_encoding = ?, terminal_input_encoding = ? WHERE id = ?`, encoding, encoding, id)
	if err != nil {
		return fmt.Errorf("set host terminal encoding: %w", err)
	}
	return requireAffected(result, "set host terminal encoding")
}

// MoveFavoriteHost moves a saved favorite one slot in the link bar and returns
// the normalized favorite IDs in their new display order. Direction must be
// -1 or +1. Non-favorite hosts cannot occupy link-bar slots.
func (s *Store) MoveFavoriteHost(ctx context.Context, id string, direction int) ([]string, error) {
	if direction != -1 && direction != 1 {
		return nil, errors.New("favorite shortcut direction must be -1 or 1")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("move favorite shortcut: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var favorite bool
	if err := tx.QueryRowContext(ctx, `SELECT favorite FROM hosts WHERE id = ?`, id).Scan(&favorite); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("move favorite shortcut: read host: %w", err)
	}
	if !favorite {
		return nil, ErrHostNotFavorite
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM hosts
		WHERE favorite = 1
		ORDER BY CASE WHEN favorite_order > 0 THEN 0 ELSE 1 END,
		         favorite_order, name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("move favorite shortcut: list hosts: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var hostID string
		if err := rows.Scan(&hostID); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("move favorite shortcut: read host ID: %w", err)
		}
		ids = append(ids, hostID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("move favorite shortcut: iterate hosts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("move favorite shortcut: close host list: %w", err)
	}

	index := -1
	for candidate, hostID := range ids {
		if hostID == id {
			index = candidate
			break
		}
	}
	if index < 0 {
		return nil, ErrHostNotFavorite
	}
	neighbor := index + direction
	if neighbor >= 0 && neighbor < len(ids) {
		ids[index], ids[neighbor] = ids[neighbor], ids[index]
	}
	for index, hostID := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET favorite_order = ? WHERE id = ? AND favorite = 1`, index+1, hostID); err != nil {
			return nil, fmt.Errorf("move favorite shortcut: save order: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("move favorite shortcut: commit: %w", err)
	}
	return ids, nil
}

// RecordHostConnection stores the last successful connection time for a host.
func (s *Store) RecordHostConnection(ctx context.Context, id string, connectedAt time.Time) error {
	if id == "" {
		return errors.New("host ID is required")
	}
	if connectedAt.IsZero() || connectedAt.UnixNano() <= 0 {
		return errors.New("connection time must be after the Unix epoch")
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET last_connected_at = MAX(last_connected_at, ?) WHERE id = ?`, connectedAt.UTC().UnixNano(), id)
	if err != nil {
		return fmt.Errorf("record host connection: %w", err)
	}
	return requireAffected(result, "record host connection")
}

func (s *Store) listHosts(ctx context.Context, query string, args ...any) ([]model.Host, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()

	hosts := make([]model.Host, 0)
	for rows.Next() {
		var host model.Host
		if err := scanHost(rows, &host); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hosts: %w", err)
	}
	return hosts, nil
}

func normalizeHost(host *model.Host) error {
	host.Name = strings.TrimSpace(host.Name)
	host.IP = strings.TrimSpace(host.IP)
	host.LocalStartDir = strings.TrimSpace(host.LocalStartDir)
	host.RemoteStartDir = strings.TrimSpace(host.RemoteStartDir)
	if host.Name == "" {
		return errors.New("host name is required")
	}
	if host.Kind == "" {
		host.Kind = model.HostKindSSH
	}
	if host.Kind != model.HostKindSSH {
		host.StartupScriptPath = ""
	}
	if host.Kind != model.HostKindFTP {
		host.FTPActiveMode = false
	}
	for name, value := range map[string]string{"local starting directory": host.LocalStartDir, "remote starting directory": host.RemoteStartDir} {
		if len([]rune(value)) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must be at most 1024 characters and contain no control characters", name)
		}
	}
	if host.Kind != model.HostKindSSH && host.Kind != model.HostKindFTP {
		host.LocalStartDir = ""
		host.RemoteStartDir = ""
	}
	if strings.TrimSpace(host.TerminalEncoding) == "" {
		host.TerminalEncoding = model.TerminalEncodingUTF8
	}
	if !model.IsTerminalEncodingSupported(host.TerminalEncoding) {
		return errors.New("unsupported terminal encoding")
	}
	if host.TerminalInputEncoding != "" && !model.IsTerminalEncodingSupported(host.TerminalInputEncoding) {
		return errors.New("unsupported terminal input encoding")
	}
	if host.Kind == model.HostKindSerial {
		host.Port = 0
		host.User = ""
		host.IdentityFile = ""
		host.TLSMode = ""
		host.Startup = ""
		host.CredentialRef = ""
		host.AuthProfileID = ""
		host.ProxyJumpList = nil
		host.FileProtocol = ""
		host.TerminalProtocol = ""
		host.X11Forwarding = false
		if host.SerialBaudRate == 0 {
			host.SerialBaudRate = 9600
		}
		if host.SerialBaudRate < 50 || host.SerialBaudRate > 4_000_000 {
			return errors.New("serial baud rate must be between 50 and 4000000")
		}
		if host.SerialDataBits == 0 {
			host.SerialDataBits = 8
		}
		if host.SerialDataBits < 5 || host.SerialDataBits > 8 {
			return errors.New("serial data bits must be 5, 6, 7, or 8")
		}
		if host.SerialParity == "" {
			host.SerialParity = model.SerialParityNone
		}
		switch host.SerialParity {
		case model.SerialParityNone, model.SerialParityEven, model.SerialParityOdd, model.SerialParityMark, model.SerialParitySpace:
		default:
			return errors.New("invalid serial parity")
		}
		if host.SerialStopBits == "" {
			host.SerialStopBits = model.SerialStopBitsOne
		}
		switch host.SerialStopBits {
		case model.SerialStopBitsOne, model.SerialStopBitsOne5, model.SerialStopBitsTwo:
		default:
			return errors.New("invalid serial stop bits")
		}
		if host.SerialFlowControl == "" {
			host.SerialFlowControl = model.SerialFlowControlNone
		}
		switch host.SerialFlowControl {
		case model.SerialFlowControlNone, model.SerialFlowControlXONXOFF, model.SerialFlowControlRTSCTS:
		default:
			return errors.New("invalid serial flow control")
		}
		if host.IP == "" {
			return errors.New("serial device path is required")
		}
	} else if host.Kind == model.HostKindLocal {
		host.SerialBaudRate = 0
		host.SerialDataBits = 0
		host.SerialParity = ""
		host.SerialStopBits = ""
		host.SerialFlowControl = ""
		host.IP = ""
		host.Port = 0
		host.User = ""
		host.IdentityFile = ""
		host.TLSMode = ""
		host.Startup = ""
		host.CredentialRef = ""
		host.AuthProfileID = ""
		host.ProxyJumpList = nil
		host.FileProtocol = ""
		host.TerminalProtocol = ""
		host.X11Forwarding = false
	} else {
		if host.Kind == model.HostKindSSH {
			host.TLSMode = ""
			host.TerminalProtocol = ""
			host.IdentityFile = strings.TrimSpace(host.IdentityFile)
			host.AuthProfileID = strings.TrimSpace(host.AuthProfileID)
			if host.FileProtocol == "" {
				host.FileProtocol = model.SSHFileProtocolSFTP
			}
			if host.FileProtocol != model.SSHFileProtocolSFTP && host.FileProtocol != model.SSHFileProtocolSCP {
				return errors.New("SSH file protocol must be sftp or scp")
			}
		} else if host.Kind == model.HostKindFTP {
			host.IdentityFile = ""
			host.TerminalProtocol = ""
			host.X11Forwarding = false
			host.FileProtocol = ""
			host.Startup = ""
			host.CredentialRef = ""
			host.AuthProfileID = ""
			host.ProxyJumpList = nil
			if host.TLSMode == "" {
				host.TLSMode = model.FTPTLSOff
			}
			if host.TLSMode != model.FTPTLSOff && host.TLSMode != model.FTPTLSExplicit && host.TLSMode != model.FTPTLSImplicit {
				return errors.New("invalid FTP TLS mode")
			}
		} else if host.Kind == model.HostKindTelnet {
			host.IdentityFile = ""
			host.FileProtocol = ""
			host.TLSMode = ""
			host.X11Forwarding = false
			if host.TerminalProtocol == "" {
				host.TerminalProtocol = model.TerminalProtocolTelnet
			}
			if host.TerminalProtocol != model.TerminalProtocolTelnet && host.TerminalProtocol != model.TerminalProtocolRlogin {
				return errors.New("terminal protocol must be telnet or rlogin")
			}
			host.Startup = ""
			host.CredentialRef = ""
			host.AuthProfileID = ""
			host.ProxyJumpList = nil
		} else {
			return errors.New("host kind must be ssh, local, ftp, telnet, or serial")
		}
		host.SerialBaudRate = 0
		host.SerialDataBits = 0
		host.SerialParity = ""
		host.SerialStopBits = ""
		host.SerialFlowControl = ""
		if host.IP == "" {
			return errors.New("host address is required")
		}
		if host.Port == 0 {
			host.Port = 22
			if host.Kind == model.HostKindFTP {
				host.Port = 21
				if host.TLSMode == model.FTPTLSImplicit {
					host.Port = 990
				}
			} else if host.Kind == model.HostKindTelnet {
				if host.TerminalProtocol == model.TerminalProtocolRlogin {
					host.Port = 513
				} else {
					host.Port = 23
				}
			}
		}
		if host.Port < 1 || host.Port > 65535 {
			return errors.New("host port must be between 1 and 65535")
		}
	}
	if host.Id == "" {
		id, err := newID()
		if err != nil {
			return fmt.Errorf("generate host ID: %w", err)
		}
		host.Id = id
	}
	return nil
}

func marshalProxyJumps(proxyJumps []string) (string, error) {
	if proxyJumps == nil {
		proxyJumps = []string{}
	}
	data, err := json.Marshal(proxyJumps)
	if err != nil {
		return "", fmt.Errorf("encode proxy jump list: %w", err)
	}
	return string(data), nil
}
