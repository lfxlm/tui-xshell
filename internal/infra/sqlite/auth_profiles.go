package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"tui-ssh-workbench/internal/model"
)

// CreateAuthProfile stores reusable authentication settings. The profile only
// contains an opaque keychain reference; the credential itself stays in the
// operating-system keychain.
func (s *Store) CreateAuthProfile(ctx context.Context, profile model.AuthProfile) (model.AuthProfile, error) {
	if err := normalizeAuthProfile(&profile); err != nil {
		return model.AuthProfile{}, err
	}
	if profile.Id == "" {
		id, err := newID()
		if err != nil {
			return model.AuthProfile{}, fmt.Errorf("generate authentication profile ID: %w", err)
		}
		profile.Id = id
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_profiles (id, name, username, credential_ref, identity_file)
		VALUES (?, ?, ?, ?, ?)
	`, profile.Id, profile.Name, profile.User, profile.CredentialRef, profile.IdentityFile)
	if err != nil {
		return model.AuthProfile{}, fmt.Errorf("create authentication profile: %w", err)
	}
	return profile, nil
}

// UpdateAuthProfile replaces a saved profile's metadata and keychain reference.
func (s *Store) UpdateAuthProfile(ctx context.Context, profile model.AuthProfile) error {
	if profile.Id == "" {
		return errors.New("authentication profile ID is required")
	}
	if err := normalizeAuthProfile(&profile); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE auth_profiles
		SET name = ?, username = ?, credential_ref = ?, identity_file = ?
		WHERE id = ?
	`, profile.Name, profile.User, profile.CredentialRef, profile.IdentityFile, profile.Id)
	if err != nil {
		return fmt.Errorf("update authentication profile: %w", err)
	}
	return requireAffected(result, "update authentication profile")
}

// GetAuthProfile retrieves a profile by ID.
func (s *Store) GetAuthProfile(ctx context.Context, id string) (model.AuthProfile, error) {
	var profile model.AuthProfile
	if err := scanAuthProfile(s.db.QueryRowContext(ctx, authProfileSelect+` WHERE id = ?`, id), &profile); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.AuthProfile{}, ErrNotFound
		}
		return model.AuthProfile{}, fmt.Errorf("get authentication profile: %w", err)
	}
	return profile, nil
}

// ListAuthProfiles returns saved profiles sorted by name.
func (s *Store) ListAuthProfiles(ctx context.Context) ([]model.AuthProfile, error) {
	rows, err := s.db.QueryContext(ctx, authProfileSelect+` ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, fmt.Errorf("list authentication profiles: %w", err)
	}
	defer rows.Close()

	profiles := make([]model.AuthProfile, 0)
	for rows.Next() {
		var profile model.AuthProfile
		if err := scanAuthProfile(rows, &profile); err != nil {
			return nil, fmt.Errorf("scan authentication profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authentication profiles: %w", err)
	}
	return profiles, nil
}

// DeleteAuthProfile removes a profile only when no host still references it.
func (s *Store) DeleteAuthProfile(ctx context.Context, id string) error {
	var references int
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM hosts WHERE auth_profile_id = ?)
		     + (SELECT COUNT(*) FROM groups WHERE auth_profile_id = ?)
	`, id, id).Scan(&references); err != nil {
		return fmt.Errorf("check authentication profile references: %w", err)
	}
	if references > 0 {
		return ErrAuthProfileInUse
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM auth_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete authentication profile: %w", err)
	}
	return requireAffected(result, "delete authentication profile")
}

const authProfileSelect = `
	SELECT id, name, username, credential_ref, identity_file
	FROM auth_profiles
`

func scanAuthProfile(row rowScanner, profile *model.AuthProfile) error {
	return row.Scan(&profile.Id, &profile.Name, &profile.User, &profile.CredentialRef, &profile.IdentityFile)
}

func normalizeAuthProfile(profile *model.AuthProfile) error {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.User = strings.TrimSpace(profile.User)
	profile.CredentialRef = strings.TrimSpace(profile.CredentialRef)
	profile.IdentityFile = strings.TrimSpace(profile.IdentityFile)
	if profile.Name == "" {
		return errors.New("authentication profile name is required")
	}
	if len([]rune(profile.Name)) > 80 {
		return errors.New("authentication profile name cannot exceed 80 characters")
	}
	return nil
}
