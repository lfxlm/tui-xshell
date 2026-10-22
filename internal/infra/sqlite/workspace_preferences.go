package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// FooterShortcutsVisible returns the saved setting and whether the user has
// explicitly chosen a value. An unset preference follows config.toml.
func (s *Store) FooterShortcutsVisible(ctx context.Context) (visible, configured bool, err error) {
	var value sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT footer_shortcuts_visible FROM app_preferences WHERE id = 1`).Scan(&value); err != nil {
		return false, false, fmt.Errorf("read footer shortcut preference: %w", err)
	}
	if !value.Valid {
		return false, false, nil
	}
	return value.Int64 != 0, true, nil
}

// SetFooterShortcutsVisible saves an in-app override for the config-file setting.
func (s *Store) SetFooterShortcutsVisible(ctx context.Context, visible bool) error {
	value := 0
	if visible {
		value = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_preferences SET footer_shortcuts_visible = ? WHERE id = 1`, value)
	if err != nil {
		return fmt.Errorf("save footer shortcut preference: %w", err)
	}
	return requireAffected(result, "save footer shortcut preference")
}
