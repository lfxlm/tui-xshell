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

// ListQuickCommands returns saved snippets ordered by name.
func (s *Store) ListQuickCommands(ctx context.Context) ([]model.QuickCommand, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, command FROM quick_commands
		ORDER BY name COLLATE NOCASE, id
	`)
	if err != nil {
		return nil, fmt.Errorf("list quick commands: %w", err)
	}
	defer rows.Close()

	commands := make([]model.QuickCommand, 0)
	for rows.Next() {
		var command model.QuickCommand
		if err := rows.Scan(&command.Id, &command.Name, &command.Command); err != nil {
			return nil, fmt.Errorf("scan quick command: %w", err)
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quick commands: %w", err)
	}
	return commands, nil
}

// SaveQuickCommand creates or updates a command snippet. Names are unique
// without regard to case so the picker remains unambiguous.
func (s *Store) SaveQuickCommand(ctx context.Context, command model.QuickCommand) (model.QuickCommand, error) {
	command.Name = strings.TrimSpace(command.Name)
	if command.Name == "" {
		return model.QuickCommand{}, errors.New("quick command name is required")
	}
	if strings.TrimSpace(command.Command) == "" {
		return model.QuickCommand{}, errors.New("quick command text is required")
	}
	for _, character := range command.Name {
		if unicode.IsControl(character) {
			return model.QuickCommand{}, errors.New("quick command name must not contain control characters")
		}
	}
	for _, character := range command.Command {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return model.QuickCommand{}, errors.New("quick command text may contain newlines and tabs, but no other control characters")
		}
	}
	if len([]rune(command.Name)) > 80 {
		return model.QuickCommand{}, errors.New("quick command name must not exceed 80 characters")
	}
	if len([]rune(command.Command)) > 4096 {
		return model.QuickCommand{}, errors.New("quick command text must not exceed 4096 characters")
	}
	if command.Id == "" {
		id, err := newID()
		if err != nil {
			return model.QuickCommand{}, fmt.Errorf("generate quick command ID: %w", err)
		}
		command.Id = id
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quick_commands (id, name, command) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, command = excluded.command
	`, command.Id, command.Name, command.Command)
	if err != nil {
		return model.QuickCommand{}, fmt.Errorf("save quick command: %w", err)
	}
	return command, nil
}

// DeleteQuickCommand removes a saved snippet by ID.
func (s *Store) DeleteQuickCommand(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM quick_commands WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete quick command: %w", err)
	}
	return requireAffected(result, "delete quick command")
}

// GetQuickCommand retrieves a snippet by ID for tests and callers that need a
// single saved command.
func (s *Store) GetQuickCommand(ctx context.Context, id string) (model.QuickCommand, error) {
	var command model.QuickCommand
	err := s.db.QueryRowContext(ctx, `SELECT id, name, command FROM quick_commands WHERE id = ?`, id).
		Scan(&command.Id, &command.Name, &command.Command)
	if errors.Is(err, sql.ErrNoRows) {
		return model.QuickCommand{}, ErrNotFound
	}
	if err != nil {
		return model.QuickCommand{}, fmt.Errorf("get quick command: %w", err)
	}
	return command, nil
}
