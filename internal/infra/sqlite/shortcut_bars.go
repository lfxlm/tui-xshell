package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"tui-ssh-workbench/internal/model"
)

// ListShortcutBars returns named connection bars and their hosts in display
// order. Missing rows are never inferred from favorite state.
func (s *Store) ListShortcutBars(ctx context.Context) ([]model.ShortcutBar, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.name, b.sort_order, h.host_id
		FROM shortcut_bars b
	LEFT JOIN shortcut_bar_hosts h ON h.bar_id = b.id
		ORDER BY b.sort_order, b.name COLLATE NOCASE, b.id, h.sort_order, h.host_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list shortcut bars: %w", err)
	}
	defer rows.Close()

	bars := make([]model.ShortcutBar, 0)
	indices := make(map[string]int)
	for rows.Next() {
		var bar model.ShortcutBar
		var hostID sql.NullString
		if err := rows.Scan(&bar.Id, &bar.Name, &bar.SortOrder, &hostID); err != nil {
			return nil, fmt.Errorf("scan shortcut bar: %w", err)
		}
		index, exists := indices[bar.Id]
		if !exists {
			bar.HostIDs = []string{}
			bars = append(bars, bar)
			index = len(bars) - 1
			indices[bar.Id] = index
		}
		if hostID.Valid {
			bars[index].HostIDs = append(bars[index].HostIDs, hostID.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shortcut bars: %w", err)
	}
	return bars, nil
}

// SaveShortcutBar creates or renames one bar. Names are unique without regard
// to case; saving an existing bar preserves its order and membership.
func (s *Store) SaveShortcutBar(ctx context.Context, bar model.ShortcutBar) (model.ShortcutBar, error) {
	bar.Name = strings.TrimSpace(bar.Name)
	if bar.Name == "" || len([]rune(bar.Name)) > 80 {
		return model.ShortcutBar{}, errors.New("连接栏名称不能为空且不能超过 80 个字符")
	}
	for _, character := range bar.Name {
		if unicode.IsControl(character) {
			return model.ShortcutBar{}, errors.New("连接栏名称不能包含控制字符")
		}
	}
	if bar.Id == "" {
		id, err := newID()
		if err != nil {
			return model.ShortcutBar{}, fmt.Errorf("generate shortcut bar ID: %w", err)
		}
		bar.Id = id
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM shortcut_bars`).Scan(&bar.SortOrder); err != nil {
			return model.ShortcutBar{}, fmt.Errorf("choose shortcut bar order: %w", err)
		}
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO shortcut_bars(id, name, sort_order) VALUES (?, ?, ?)`,
			bar.Id, bar.Name, bar.SortOrder)
		if err != nil {
			return model.ShortcutBar{}, fmt.Errorf("create shortcut bar: %w", err)
		}
		bar.HostIDs = []string{}
		return bar, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE shortcut_bars SET name = ? WHERE id = ?`, bar.Name, bar.Id)
	if err != nil {
		return model.ShortcutBar{}, fmt.Errorf("rename shortcut bar: %w", err)
	}
	if err := requireAffected(result, "rename shortcut bar"); err != nil {
		return model.ShortcutBar{}, err
	}
	return bar, nil
}

// SetShortcutBarHosts replaces a bar's host list atomically and stores the
// requested order. Duplicate host IDs are rejected rather than silently moved.
func (s *Store) SetShortcutBarHosts(ctx context.Context, barID string, hostIDs []string) error {
	barID = strings.TrimSpace(barID)
	if barID == "" {
		return errors.New("shortcut bar ID is required")
	}
	seen := make(map[string]bool, len(hostIDs))
	for _, hostID := range hostIDs {
		if strings.TrimSpace(hostID) == "" {
			return errors.New("host ID is required")
		}
		if seen[hostID] {
			return errors.New("shortcut bar contains a duplicate host")
		}
		seen[hostID] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set shortcut bar hosts: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM shortcut_bars WHERE id = ?`, barID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("set shortcut bar hosts: read bar: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shortcut_bar_hosts WHERE bar_id = ?`, barID); err != nil {
		return fmt.Errorf("set shortcut bar hosts: clear old hosts: %w", err)
	}
	for index, hostID := range hostIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO shortcut_bar_hosts(bar_id, host_id, sort_order) VALUES (?, ?, ?)`,
			barID, hostID, index+1); err != nil {
			return fmt.Errorf("set shortcut bar hosts: add host %q: %w", hostID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set shortcut bar hosts: commit: %w", err)
	}
	return nil
}

// MoveShortcutBarHost shifts one member by one position and returns the
// normalized order. A host not in the requested bar is rejected.
func (s *Store) MoveShortcutBarHost(ctx context.Context, barID, hostID string, direction int) ([]string, error) {
	if direction != -1 && direction != 1 {
		return nil, errors.New("shortcut bar direction must be -1 or 1")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("move shortcut bar host: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM shortcut_bars WHERE id = ?`, barID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("move shortcut bar host: read bar: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT host_id FROM shortcut_bar_hosts
		WHERE bar_id = ? ORDER BY sort_order, host_id
	`, barID)
	if err != nil {
		return nil, fmt.Errorf("move shortcut bar host: list members: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("move shortcut bar host: scan member: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("move shortcut bar host: iterate members: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("move shortcut bar host: close members: %w", err)
	}
	index := -1
	for candidate, id := range ids {
		if id == hostID {
			index = candidate
			break
		}
	}
	if index < 0 {
		return nil, ErrHostNotInShortcutBar
	}
	neighbor := index + direction
	if neighbor >= 0 && neighbor < len(ids) {
		ids[index], ids[neighbor] = ids[neighbor], ids[index]
	}
	var maxOrder int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), 0) FROM shortcut_bar_hosts WHERE bar_id = ?`, barID).Scan(&maxOrder); err != nil {
		return nil, fmt.Errorf("move shortcut bar host: read maximum order: %w", err)
	}
	offset := maxOrder + len(ids) + 1
	if _, err := tx.ExecContext(ctx, `UPDATE shortcut_bar_hosts SET sort_order = sort_order + ? WHERE bar_id = ?`, offset, barID); err != nil {
		return nil, fmt.Errorf("move shortcut bar host: reserve temporary order: %w", err)
	}
	for index, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE shortcut_bar_hosts SET sort_order = ? WHERE bar_id = ? AND host_id = ?`, index+1, barID, id); err != nil {
			return nil, fmt.Errorf("move shortcut bar host: normalize order: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("move shortcut bar host: commit: %w", err)
	}
	return ids, nil
}

// DeleteShortcutBar deletes a named bar. Its member associations cascade;
// saved hosts remain intact.
func (s *Store) DeleteShortcutBar(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM shortcut_bars WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete shortcut bar: %w", err)
	}
	return requireAffected(result, "delete shortcut bar")
}
