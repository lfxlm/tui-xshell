package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// SFTPExternalEditorCommand returns the configured local editor command used
// when opening files from the SFTP workspace.
func (s *Store) SFTPExternalEditorCommand(ctx context.Context) (string, error) {
	var command string
	if err := s.db.QueryRowContext(ctx, `SELECT sftp_external_editor_command FROM app_preferences WHERE id = 1`).Scan(&command); err != nil {
		return "", fmt.Errorf("read SFTP external editor command: %w", err)
	}
	return command, nil
}

// SetSFTPExternalEditorCommand saves a local editor command. An empty value
// clears the preference so the application can fall back to environment
// variables.
func (s *Store) SetSFTPExternalEditorCommand(ctx context.Context, command string) error {
	command = strings.TrimSpace(command)
	if len(command) > 1024 {
		return fmt.Errorf("SFTP external editor command cannot exceed 1024 bytes")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE app_preferences SET sftp_external_editor_command = ? WHERE id = 1`, command)
	if err != nil {
		return fmt.Errorf("save SFTP external editor command: %w", err)
	}
	return requireAffected(result, "save SFTP external editor command")
}
