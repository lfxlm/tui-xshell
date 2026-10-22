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

// ListSFTPFilters returns saved filters in stable, case-insensitive name order.
func (s *Store) ListSFTPFilters(ctx context.Context) ([]model.SFTPFilter, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, expression FROM sftp_filters
		ORDER BY name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list SFTP filters: %w", err)
	}
	defer rows.Close()

	filters := make([]model.SFTPFilter, 0)
	for rows.Next() {
		var filter model.SFTPFilter
		if err := rows.Scan(&filter.Id, &filter.Name, &filter.Expression); err != nil {
			return nil, fmt.Errorf("scan SFTP filter: %w", err)
		}
		filters = append(filters, filter)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SFTP filters: %w", err)
	}
	return filters, nil
}

// SaveSFTPFilter creates or updates a named filter. Names are unique without
// regard to case, and control characters are rejected in both fields.
func (s *Store) SaveSFTPFilter(ctx context.Context, filter model.SFTPFilter) (model.SFTPFilter, error) {
	filter.Name = strings.TrimSpace(filter.Name)
	filter.Expression = strings.TrimSpace(filter.Expression)
	if filter.Name == "" {
		return model.SFTPFilter{}, errors.New("SFTP filter name is required")
	}
	if filter.Expression == "" {
		return model.SFTPFilter{}, errors.New("SFTP filter expression is required")
	}
	if len([]rune(filter.Name)) > 80 {
		return model.SFTPFilter{}, errors.New("SFTP filter name must not exceed 80 characters")
	}
	if len([]rune(filter.Expression)) > 256 {
		return model.SFTPFilter{}, errors.New("SFTP filter expression must not exceed 256 characters")
	}
	for _, value := range filter.Name + filter.Expression {
		if unicode.IsControl(value) {
			return model.SFTPFilter{}, errors.New("SFTP filter fields must not contain control characters")
		}
	}
	if filter.Id == "" {
		id, err := newID()
		if err != nil {
			return model.SFTPFilter{}, fmt.Errorf("generate SFTP filter ID: %w", err)
		}
		filter.Id = id
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sftp_filters (id, name, expression) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, expression = excluded.expression
	`, filter.Id, filter.Name, filter.Expression)
	if err != nil {
		return model.SFTPFilter{}, fmt.Errorf("save SFTP filter: %w", err)
	}
	return filter, nil
}

// DeleteSFTPFilter removes a saved filter without changing active pane filters.
func (s *Store) DeleteSFTPFilter(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sftp_filters WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SFTP filter: %w", err)
	}
	return requireAffected(result, "delete SFTP filter")
}

// GetSFTPFilter returns a saved filter by ID for callers and tests.
func (s *Store) GetSFTPFilter(ctx context.Context, id string) (model.SFTPFilter, error) {
	var filter model.SFTPFilter
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, expression FROM sftp_filters WHERE id = ?
	`, id).Scan(&filter.Id, &filter.Name, &filter.Expression)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SFTPFilter{}, ErrNotFound
	}
	if err != nil {
		return model.SFTPFilter{}, fmt.Errorf("get SFTP filter: %w", err)
	}
	return filter, nil
}
