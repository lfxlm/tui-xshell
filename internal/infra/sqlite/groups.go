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

// CreateGroup stores a group and returns it with a generated ID when needed.
func (s *Store) CreateGroup(ctx context.Context, group model.Group) (model.Group, error) {
	if err := normalizeGroup(&group); err != nil {
		return model.Group{}, err
	}
	if group.Name == "" {
		return model.Group{}, errors.New("group name is required")
	}
	if group.Id == "" {
		id, err := newID()
		if err != nil {
			return model.Group{}, fmt.Errorf("generate group ID: %w", err)
		}
		group.Id = id
	}
	if group.ParentId != "" {
		if _, err := s.GetGroup(ctx, group.ParentId); err != nil {
			return model.Group{}, fmt.Errorf("find parent group: %w", err)
		}
	}
	if group.AuthProfileID != "" {
		if _, err := s.GetAuthProfile(ctx, group.AuthProfileID); err != nil {
			return model.Group{}, fmt.Errorf("find inherited authentication profile: %w", err)
		}
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO groups (id, name, parent_id, auth_profile_id, local_start_dir, remote_start_dir) VALUES (?, ?, ?, ?, ?, ?)`,
		group.Id, group.Name, nullableID(group.ParentId), nullableID(group.AuthProfileID), group.LocalStartDir, group.RemoteStartDir,
	); err != nil {
		return model.Group{}, fmt.Errorf("create group: %w", err)
	}
	return group, nil
}

// GetGroup retrieves a group by ID.
func (s *Store) GetGroup(ctx context.Context, id string) (model.Group, error) {
	var group model.Group
	var parentID, authProfileID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, parent_id, auth_profile_id, local_start_dir, remote_start_dir FROM groups WHERE id = ?`, id,
	).Scan(&group.Id, &group.Name, &parentID, &authProfileID, &group.LocalStartDir, &group.RemoteStartDir)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Group{}, ErrNotFound
	}
	if err != nil {
		return model.Group{}, fmt.Errorf("get group: %w", err)
	}
	group.ParentId = parentID.String
	group.AuthProfileID = authProfileID.String
	return group, nil
}

// ListGroups returns all groups in a stable order. Parent-child relationships
// are represented by ParentId and can be assembled into a tree by the caller.
func (s *Store) ListGroups(ctx context.Context) ([]model.Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, parent_id, auth_profile_id, local_start_dir, remote_start_dir FROM groups ORDER BY COALESCE(parent_id, ''), name COLLATE NOCASE, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	groups := make([]model.Group, 0)
	for rows.Next() {
		var group model.Group
		var parentID, authProfileID sql.NullString
		if err := rows.Scan(&group.Id, &group.Name, &parentID, &authProfileID, &group.LocalStartDir, &group.RemoteStartDir); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		group.ParentId = parentID.String
		group.AuthProfileID = authProfileID.String
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return groups, nil
}

// UpdateGroup changes a group's name or parent. Moving a group below itself is rejected.
func (s *Store) UpdateGroup(ctx context.Context, group model.Group) error {
	if err := normalizeGroup(&group); err != nil {
		return err
	}
	if group.Id == "" {
		return errors.New("group ID is required")
	}
	if group.Name == "" {
		return errors.New("group name is required")
	}
	if _, err := s.GetGroup(ctx, group.Id); err != nil {
		return fmt.Errorf("find group to update: %w", err)
	}
	if group.ParentId != "" {
		if group.ParentId == group.Id {
			return errors.New("a group cannot be its own parent")
		}
		if _, err := s.GetGroup(ctx, group.ParentId); err != nil {
			return fmt.Errorf("find parent group: %w", err)
		}
		var isDescendant bool
		err := s.db.QueryRowContext(ctx, `
			WITH RECURSIVE descendants(id) AS (
				SELECT id FROM groups WHERE parent_id = ?
				UNION ALL
				SELECT groups.id FROM groups JOIN descendants ON groups.parent_id = descendants.id
			)
			SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?)
		`, group.Id, group.ParentId).Scan(&isDescendant)
		if err != nil {
			return fmt.Errorf("check group hierarchy: %w", err)
		}
		if isDescendant {
			return errors.New("a group cannot be moved below one of its descendants")
		}
	}
	if group.AuthProfileID != "" {
		if _, err := s.GetAuthProfile(ctx, group.AuthProfileID); err != nil {
			return fmt.Errorf("find inherited authentication profile: %w", err)
		}
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE groups SET name = ?, parent_id = ?, auth_profile_id = ?, local_start_dir = ?, remote_start_dir = ? WHERE id = ?`,
		group.Name, nullableID(group.ParentId), nullableID(group.AuthProfileID), group.LocalStartDir, group.RemoteStartDir, group.Id,
	)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	return requireAffected(result, "update group")
}

func normalizeGroup(group *model.Group) error {
	group.Name = strings.TrimSpace(group.Name)
	group.AuthProfileID = strings.TrimSpace(group.AuthProfileID)
	group.LocalStartDir = strings.TrimSpace(group.LocalStartDir)
	group.RemoteStartDir = strings.TrimSpace(group.RemoteStartDir)
	if group.Name == "" {
		return errors.New("group name is required")
	}
	for name, value := range map[string]string{"local starting directory": group.LocalStartDir, "remote starting directory": group.RemoteStartDir} {
		if len([]rune(value)) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("%s must be at most 1024 characters and contain no control characters", name)
		}
	}
	return nil
}

// EffectiveAuthProfileID returns a host's explicitly assigned profile, or the
// nearest profile inherited from its group ancestry.
func (s *Store) EffectiveAuthProfileID(ctx context.Context, host model.Host) (string, error) {
	if host.AuthProfileID != "" {
		return host.AuthProfileID, nil
	}
	seen := make(map[string]struct{})
	for groupID := host.GroupId; groupID != ""; {
		if _, ok := seen[groupID]; ok {
			return "", errors.New("group hierarchy contains a cycle")
		}
		seen[groupID] = struct{}{}
		group, err := s.GetGroup(ctx, groupID)
		if err != nil {
			return "", fmt.Errorf("resolve inherited authentication profile: %w", err)
		}
		if group.AuthProfileID != "" {
			return group.AuthProfileID, nil
		}
		groupID = group.ParentId
	}
	return "", nil
}

// DeleteGroup removes an empty group. Children and hosts must be moved or deleted first.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	var childCount, hostCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE parent_id = ?`, id).Scan(&childCount); err != nil {
		return fmt.Errorf("check child groups: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hosts WHERE group_id = ?`, id).Scan(&hostCount); err != nil {
		return fmt.Errorf("check hosts in group: %w", err)
	}
	if childCount > 0 || hostCount > 0 {
		return ErrGroupNotEmpty
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return requireAffected(result, "delete group")
}
