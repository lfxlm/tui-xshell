package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"tui-ssh-workbench/internal/model"
)

var ErrIdentityReferenceChanged = errors.New("SSH identity reference changed during rotation")

// ReplaceIdentityFileReferences updates a set of host/profile identity paths
// atomically. Each update includes the exact old path observed by the caller so
// a stale rotation cannot overwrite a concurrent edit.
func (s *Store) ReplaceIdentityFileReferences(ctx context.Context, references []model.IdentityFileReference, newPath string) error {
	newPath = strings.TrimSpace(newPath)
	if newPath == "" {
		return errors.New("replacement SSH identity path is required")
	}
	if len(references) == 0 {
		return errors.New("no SSH identity references to replace")
	}

	seen := make(map[string]struct{}, len(references))
	normalized := make([]model.IdentityFileReference, len(references))
	for index, reference := range references {
		reference.HostID = strings.TrimSpace(reference.HostID)
		reference.AuthProfileID = strings.TrimSpace(reference.AuthProfileID)
		reference.CurrentPath = strings.TrimSpace(reference.CurrentPath)
		if (reference.HostID == "") == (reference.AuthProfileID == "") || reference.CurrentPath == "" {
			return errors.New("each SSH identity reference must identify exactly one host or authentication profile and include its current path")
		}
		if reference.CurrentPath == newPath {
			return errors.New("replacement SSH identity path matches an existing reference")
		}
		key := "host:" + reference.HostID
		if reference.AuthProfileID != "" {
			key = "profile:" + reference.AuthProfileID
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate SSH identity reference %q", key)
		}
		seen[key] = struct{}{}
		normalized[index] = reference
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rotate SSH identity: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, reference := range normalized {
		var result interface{ RowsAffected() (int64, error) }
		if reference.HostID != "" {
			result, err = tx.ExecContext(ctx, `UPDATE hosts SET identity_file = ? WHERE id = ? AND identity_file = ?`, newPath, reference.HostID, strings.TrimSpace(reference.CurrentPath))
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE auth_profiles SET identity_file = ? WHERE id = ? AND identity_file = ?`, newPath, reference.AuthProfileID, strings.TrimSpace(reference.CurrentPath))
		}
		if err != nil {
			return fmt.Errorf("rotate SSH identity reference: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("rotate SSH identity reference: read affected row count: %w", err)
		}
		if updated != 1 {
			return fmt.Errorf("%w: %s", ErrIdentityReferenceChanged, referenceName(reference))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rotate SSH identity: commit transaction: %w", err)
	}
	return nil
}

func referenceName(reference model.IdentityFileReference) string {
	if reference.HostID != "" {
		return "host " + reference.HostID
	}
	return "authentication profile " + reference.AuthProfileID
}
