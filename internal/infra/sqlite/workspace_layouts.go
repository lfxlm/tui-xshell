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

const maxWorkspaceLayoutBytes = 256 << 10

// ListWorkspaceLayouts returns named layouts with the most recently updated
// item first.
func (s *Store) ListWorkspaceLayouts(ctx context.Context) ([]model.WorkspaceLayout, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, data, updated_at FROM workspace_layouts
		ORDER BY updated_at DESC, name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list workspace layouts: %w", err)
	}
	defer rows.Close()

	layouts := make([]model.WorkspaceLayout, 0)
	for rows.Next() {
		var layout model.WorkspaceLayout
		var updatedAt int64
		if err := rows.Scan(&layout.Id, &layout.Name, &layout.Data, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace layout: %w", err)
		}
		layout.UpdatedAt = nanosToTime(updatedAt)
		layouts = append(layouts, layout)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace layouts: %w", err)
	}
	return layouts, nil
}

// SaveWorkspaceLayout creates or updates one named layout. Names are unique
// without regard to case; Data must be valid, bounded JSON.
func (s *Store) SaveWorkspaceLayout(ctx context.Context, layout model.WorkspaceLayout) (model.WorkspaceLayout, error) {
	layout.Name = strings.TrimSpace(layout.Name)
	if layout.Name == "" || len([]rune(layout.Name)) > 80 {
		return model.WorkspaceLayout{}, errors.New("工作区布局名称不能为空且不能超过 80 个字符")
	}
	for _, character := range layout.Name {
		if unicode.IsControl(character) {
			return model.WorkspaceLayout{}, errors.New("工作区布局名称不能包含控制字符")
		}
	}
	if len(layout.Data) == 0 || len(layout.Data) > maxWorkspaceLayoutBytes || !json.Valid([]byte(layout.Data)) {
		return model.WorkspaceLayout{}, errors.New("工作区布局数据必须是有效 JSON 且不能超过 256 KiB")
	}
	if layout.Id == "" {
		id, err := newID()
		if err != nil {
			return model.WorkspaceLayout{}, fmt.Errorf("generate workspace layout ID: %w", err)
		}
		layout.Id = id
	}
	layout.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workspace_layouts (id, name, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			data = excluded.data,
			updated_at = excluded.updated_at
	`, layout.Id, layout.Name, layout.Data, timeToNanos(layout.UpdatedAt))
	if err != nil {
		return model.WorkspaceLayout{}, fmt.Errorf("save workspace layout: %w", err)
	}
	return layout, nil
}

// GetWorkspaceLayout retrieves one named layout by ID.
func (s *Store) GetWorkspaceLayout(ctx context.Context, id string) (model.WorkspaceLayout, error) {
	var layout model.WorkspaceLayout
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, data, updated_at FROM workspace_layouts WHERE id = ?
	`, id).Scan(&layout.Id, &layout.Name, &layout.Data, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.WorkspaceLayout{}, ErrNotFound
	}
	if err != nil {
		return model.WorkspaceLayout{}, fmt.Errorf("get workspace layout: %w", err)
	}
	layout.UpdatedAt = nanosToTime(updatedAt)
	return layout, nil
}

// DeleteWorkspaceLayout removes one saved layout by ID.
func (s *Store) DeleteWorkspaceLayout(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspace_layouts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workspace layout: %w", err)
	}
	return requireAffected(result, "delete workspace layout")
}

// LastWorkspaceData returns the most recent terminal workspace snapshot, or
// an empty string when no workspace has been saved.
func (s *Store) LastWorkspaceData(ctx context.Context) (string, error) {
	var data string
	if err := s.db.QueryRowContext(ctx, `SELECT last_workspace_data FROM app_preferences WHERE id = 1`).Scan(&data); err != nil {
		return "", fmt.Errorf("read last workspace: %w", err)
	}
	return data, nil
}

// SetLastWorkspaceData stores a bounded, versioned JSON snapshot. An empty
// value clears the saved workspace.
func (s *Store) SetLastWorkspaceData(ctx context.Context, data string) error {
	if len(data) > maxWorkspaceLayoutBytes || (data != "" && !json.Valid([]byte(data))) {
		return errors.New("上次工作区数据必须是有效 JSON 且不能超过 256 KiB")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_preferences SET last_workspace_data = ? WHERE id = 1`, data)
	if err != nil {
		return fmt.Errorf("save last workspace: %w", err)
	}
	return requireAffected(result, "save last workspace")
}
