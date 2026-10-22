package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"tui-ssh-workbench/internal/model"
)

// BatchUpdateHosts applies the explicitly supplied fields to every requested
// host in one transaction. Unsupported network-specific fields reject the
// entire operation so mixed selections cannot be partially modified.
func (s *Store) BatchUpdateHosts(ctx context.Context, ids []string, patch model.HostBatchPatch) error {
	if len(ids) == 0 {
		return errors.New("at least one host is required")
	}
	if patch.GroupID == nil && patch.Favorite == nil && patch.Description == nil && patch.User == nil && patch.Port == nil && patch.IdentityFile == nil && len(patch.Names) == 0 {
		return errors.New("no host fields were selected for update")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return errors.New("host IDs must be non-empty and unique")
		}
		seen[id] = true
	}
	if len(patch.Names) > 0 {
		if len(patch.Names) != len(ids) {
			return errors.New("batch rename must include every selected host exactly once")
		}
		names := make(map[string]string, len(patch.Names))
		for id, name := range patch.Names {
			if !seen[id] {
				return fmt.Errorf("batch rename contains an unselected host: %s", id)
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("batch rename contains an empty name for host %s", id)
			}
			names[id] = name
		}
		patch.Names = names
	}
	if patch.GroupID != nil {
		value := strings.TrimSpace(*patch.GroupID)
		patch.GroupID = &value
	}
	if patch.Description != nil {
		value := strings.TrimSpace(*patch.Description)
		patch.Description = &value
	}
	if patch.User != nil {
		value := strings.TrimSpace(*patch.User)
		patch.User = &value
	}
	if patch.IdentityFile != nil {
		value := strings.TrimSpace(*patch.IdentityFile)
		patch.IdentityFile = &value
	}
	if patch.Port != nil && (*patch.Port < 1 || *patch.Port > 65535) {
		return errors.New("port must be between 1 and 65535")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("batch update hosts: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	kinds := make(map[string]model.HostKind, len(ids))
	for _, id := range ids {
		var kind model.HostKind
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM hosts WHERE id = ?`, id).Scan(&kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrNotFound, id)
			}
			return fmt.Errorf("batch update hosts: read host %q: %w", id, err)
		}
		kinds[id] = kind
	}
	for id, kind := range kinds {
		if (patch.User != nil || patch.Port != nil) && kind != model.HostKindSSH && kind != model.HostKindFTP && kind != model.HostKindTelnet {
			return fmt.Errorf("batch update hosts: %q does not support username or network port changes", id)
		}
		if patch.IdentityFile != nil && kind != model.HostKindSSH {
			return fmt.Errorf("batch update hosts: %q is not an SSH host; private key path cannot be changed", id)
		}
	}

	sets := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if patch.GroupID != nil {
		sets = append(sets, "group_id = ?")
		args = append(args, nullableID(*patch.GroupID))
	}
	if patch.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *patch.Description)
	}
	if patch.User != nil {
		sets = append(sets, "username = ?")
		args = append(args, *patch.User)
	}
	if patch.Port != nil {
		sets = append(sets, "port = ?")
		args = append(args, *patch.Port)
	}
	if patch.IdentityFile != nil {
		sets = append(sets, "identity_file = ?")
		args = append(args, *patch.IdentityFile)
	}
	if len(sets) > 0 {
		query := "UPDATE hosts SET " + strings.Join(sets, ", ") + " WHERE id = ?"
		for _, id := range ids {
			rowArgs := append(append([]any(nil), args...), id)
			result, err := tx.ExecContext(ctx, query, rowArgs...)
			if err != nil {
				return fmt.Errorf("batch update hosts: update %q: %w", id, err)
			}
			if err := requireAffected(result, "batch update host"); err != nil {
				return fmt.Errorf("batch update hosts: %q: %w", id, err)
			}
		}
	}
	if len(patch.Names) > 0 {
		for _, id := range ids {
			result, err := tx.ExecContext(ctx, `UPDATE hosts SET name = ? WHERE id = ?`, patch.Names[id], id)
			if err != nil {
				return fmt.Errorf("batch update hosts: rename %q: %w", id, err)
			}
			if err := requireAffected(result, "batch rename host"); err != nil {
				return fmt.Errorf("batch update hosts: rename %q: %w", id, err)
			}
		}
	}
	if patch.Favorite != nil {
		for _, id := range ids {
			if *patch.Favorite {
				var currentlyFavorite bool
				if err := tx.QueryRowContext(ctx, `SELECT favorite FROM hosts WHERE id = ?`, id).Scan(&currentlyFavorite); err != nil {
					return fmt.Errorf("batch update hosts: read favorite for %q: %w", id, err)
				}
				if currentlyFavorite {
					continue
				}
				var nextOrder int
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(favorite_order), 0) + 1 FROM hosts WHERE favorite = 1`).Scan(&nextOrder); err != nil {
					return fmt.Errorf("batch update hosts: choose favorite order: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE hosts SET favorite = 1, favorite_order = ? WHERE id = ?`, nextOrder, id); err != nil {
					return fmt.Errorf("batch update hosts: favorite %q: %w", id, err)
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE hosts SET favorite = 0, favorite_order = 0 WHERE id = ?`, id); err != nil {
				return fmt.Errorf("batch update hosts: unfavorite %q: %w", id, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("batch update hosts: commit transaction: %w", err)
	}
	return nil
}
