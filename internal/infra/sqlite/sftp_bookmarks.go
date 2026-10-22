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

// ListSFTPBookmarks returns shortcuts for one local/remote scope.
func (s *Store) ListSFTPBookmarks(ctx context.Context, scopeKey string, remote bool) ([]model.SFTPBookmark, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_key, name, path, remote FROM sftp_bookmarks
		WHERE scope_key = ? AND remote = ? ORDER BY name COLLATE NOCASE, id
	`, scopeKey, remote)
	if err != nil {
		return nil, fmt.Errorf("list SFTP bookmarks: %w", err)
	}
	defer rows.Close()

	bookmarks := make([]model.SFTPBookmark, 0)
	for rows.Next() {
		var bookmark model.SFTPBookmark
		if err := rows.Scan(&bookmark.Id, &bookmark.ScopeKey, &bookmark.Name, &bookmark.Path, &bookmark.Remote); err != nil {
			return nil, fmt.Errorf("scan SFTP bookmark: %w", err)
		}
		bookmarks = append(bookmarks, bookmark)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate SFTP bookmarks: %w", err)
	}
	return bookmarks, nil
}

// SaveSFTPBookmark creates or updates a machine-local path shortcut.
func (s *Store) SaveSFTPBookmark(ctx context.Context, bookmark model.SFTPBookmark) (model.SFTPBookmark, error) {
	bookmark.ScopeKey = strings.TrimSpace(bookmark.ScopeKey)
	bookmark.Name = strings.TrimSpace(bookmark.Name)
	bookmark.Path = strings.TrimSpace(bookmark.Path)
	if bookmark.ScopeKey == "" || len([]rune(bookmark.ScopeKey)) > 128 {
		return model.SFTPBookmark{}, errors.New("SFTP bookmark scope is invalid")
	}
	if bookmark.Name == "" || len([]rune(bookmark.Name)) > 80 {
		return model.SFTPBookmark{}, errors.New("SFTP bookmark name must contain 1 to 80 characters")
	}
	if bookmark.Path == "" || len(bookmark.Path) > 4096 {
		return model.SFTPBookmark{}, errors.New("SFTP bookmark path must contain 1 to 4096 bytes")
	}
	for _, value := range bookmark.ScopeKey + bookmark.Name + bookmark.Path {
		if unicode.IsControl(value) {
			return model.SFTPBookmark{}, errors.New("SFTP bookmark fields must not contain control characters")
		}
	}
	if bookmark.Id == "" {
		id, err := newID()
		if err != nil {
			return model.SFTPBookmark{}, fmt.Errorf("generate SFTP bookmark ID: %w", err)
		}
		bookmark.Id = id
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sftp_bookmarks (id, scope_key, remote, name, path)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			scope_key = excluded.scope_key, remote = excluded.remote,
			name = excluded.name, path = excluded.path
	`, bookmark.Id, bookmark.ScopeKey, bookmark.Remote, bookmark.Name, bookmark.Path)
	if err != nil {
		return model.SFTPBookmark{}, fmt.Errorf("save SFTP bookmark: %w", err)
	}
	return bookmark, nil
}

// DeleteSFTPBookmark removes a saved local or remote path shortcut.
func (s *Store) DeleteSFTPBookmark(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sftp_bookmarks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete SFTP bookmark: %w", err)
	}
	return requireAffected(result, "delete SFTP bookmark")
}

// GetSFTPBookmark returns one saved path shortcut.
func (s *Store) GetSFTPBookmark(ctx context.Context, id string) (model.SFTPBookmark, error) {
	var bookmark model.SFTPBookmark
	err := s.db.QueryRowContext(ctx, `
		SELECT id, scope_key, name, path, remote FROM sftp_bookmarks WHERE id = ?
	`, id).Scan(&bookmark.Id, &bookmark.ScopeKey, &bookmark.Name, &bookmark.Path, &bookmark.Remote)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SFTPBookmark{}, ErrNotFound
	}
	if err != nil {
		return model.SFTPBookmark{}, fmt.Errorf("get SFTP bookmark: %w", err)
	}
	return bookmark, nil
}
