package sqlite

import (
	"context"
	"fmt"
)

var validAppearanceSchemes = map[string]bool{
	"default": true, "nord": true, "dracula": true, "solarized-dark": true,
	"high-contrast": true, "catppuccin-mocha": true, "tokyo-night": true,
	"gruvbox-dark": true, "one-dark": true,
}

// AppearanceScheme returns the user's saved interactive workbench palette.
func (s *Store) AppearanceScheme(ctx context.Context) (string, error) {
	var scheme string
	if err := s.db.QueryRowContext(ctx, `SELECT appearance_scheme FROM app_preferences WHERE id = 1`).Scan(&scheme); err != nil {
		return "", fmt.Errorf("read appearance scheme: %w", err)
	}
	return scheme, nil
}

// SetAppearanceScheme saves the user's interactive workbench palette.
func (s *Store) SetAppearanceScheme(ctx context.Context, scheme string) error {
	if !validAppearanceSchemes[scheme] {
		return fmt.Errorf("unsupported appearance scheme %q", scheme)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_preferences SET appearance_scheme = ? WHERE id = 1`, scheme)
	if err != nil {
		return fmt.Errorf("save appearance scheme: %w", err)
	}
	return requireAffected(result, "save appearance scheme")
}
