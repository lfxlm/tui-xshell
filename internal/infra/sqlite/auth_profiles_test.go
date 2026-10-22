package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tui-ssh-workbench/internal/model"
)

func TestAuthProfileCRUDAndHostReference(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "auth-profiles.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile, err := store.CreateAuthProfile(ctx, model.AuthProfile{
		Name: "  Development  ", User: " operator ", CredentialRef: " keychain-ref ", IdentityFile: " ~/.ssh/id_ed25519 ",
	})
	if err != nil {
		t.Fatalf("create auth profile: %v", err)
	}
	if profile.Id == "" || profile.Name != "Development" || profile.User != "operator" ||
		profile.CredentialRef != "keychain-ref" || profile.IdentityFile != "~/.ssh/id_ed25519" {
		t.Fatalf("normalized auth profile = %#v", profile)
	}

	host, err := store.CreateHost(ctx, model.Host{
		Kind: model.HostKindSSH, Name: "dev", IP: "127.0.0.1", Port: 22,
		User: "local-user", CredentialRef: "local-ref", AuthProfileID: profile.Id,
	})
	if err != nil {
		t.Fatalf("create host using auth profile: %v", err)
	}
	loadedHost, err := store.GetHost(ctx, host.Id)
	if err != nil || loadedHost.AuthProfileID != profile.Id {
		t.Fatalf("host auth profile reference = %q, err=%v; want %q", loadedHost.AuthProfileID, err, profile.Id)
	}
	if err := store.DeleteAuthProfile(ctx, profile.Id); !errors.Is(err, ErrAuthProfileInUse) {
		t.Fatalf("delete referenced auth profile error = %v, want %v", err, ErrAuthProfileInUse)
	}

	profile.Name = "Development Updated"
	profile.User = " deploy "
	if err := store.UpdateAuthProfile(ctx, profile); err != nil {
		t.Fatalf("update auth profile: %v", err)
	}
	loadedProfile, err := store.GetAuthProfile(ctx, profile.Id)
	if err != nil || loadedProfile.Name != "Development Updated" || loadedProfile.User != "deploy" {
		t.Fatalf("updated auth profile = %#v, err=%v", loadedProfile, err)
	}
	profiles, err := store.ListAuthProfiles(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].Id != profile.Id {
		t.Fatalf("list auth profiles = %#v, err=%v", profiles, err)
	}

	host.AuthProfileID = ""
	if err := store.UpdateHost(ctx, host); err != nil {
		t.Fatalf("clear host profile reference: %v", err)
	}
	if err := store.DeleteAuthProfile(ctx, profile.Id); err != nil {
		t.Fatalf("delete unused auth profile: %v", err)
	}
}

func TestGroupAuthProfilesInheritAndHostOverrideWins(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "group-auth-profile.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	root, err := store.CreateGroup(ctx, model.Group{Name: "Engineering"})
	if err != nil {
		t.Fatalf("create root group: %v", err)
	}
	child, err := store.CreateGroup(ctx, model.Group{Name: "Backend", ParentId: root.Id})
	if err != nil {
		t.Fatalf("create child group: %v", err)
	}
	parentProfile, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "Engineering SSH", User: "engineer"})
	if err != nil {
		t.Fatalf("create parent profile: %v", err)
	}
	root.AuthProfileID = parentProfile.Id
	if err := store.UpdateGroup(ctx, root); err != nil {
		t.Fatalf("set root inherited profile: %v", err)
	}

	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "api", IP: "example.test", Port: 22, GroupId: child.Id})
	if err != nil {
		t.Fatalf("create child host: %v", err)
	}
	profileID, err := store.EffectiveAuthProfileID(ctx, host)
	if err != nil || profileID != parentProfile.Id {
		t.Fatalf("inherited profile = %q, err=%v; want %q", profileID, err, parentProfile.Id)
	}

	childProfile, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "Backend SSH", User: "backend"})
	if err != nil {
		t.Fatalf("create child profile: %v", err)
	}
	child.AuthProfileID = childProfile.Id
	if err := store.UpdateGroup(ctx, child); err != nil {
		t.Fatalf("override profile in child group: %v", err)
	}
	profileID, err = store.EffectiveAuthProfileID(ctx, host)
	if err != nil || profileID != childProfile.Id {
		t.Fatalf("nearest inherited profile = %q, err=%v; want %q", profileID, err, childProfile.Id)
	}

	host.AuthProfileID = parentProfile.Id
	profileID, err = store.EffectiveAuthProfileID(ctx, host)
	if err != nil || profileID != parentProfile.Id {
		t.Fatalf("host override profile = %q, err=%v; want %q", profileID, err, parentProfile.Id)
	}
	if err := store.DeleteAuthProfile(ctx, childProfile.Id); !errors.Is(err, ErrAuthProfileInUse) {
		t.Fatalf("delete inherited profile error = %v, want %v", err, ErrAuthProfileInUse)
	}

	encoded, err := json.Marshal(child)
	if err != nil {
		t.Fatalf("marshal portable group: %v", err)
	}
	if strings.Contains(string(encoded), childProfile.Id) || strings.Contains(string(encoded), "auth_profile_id") {
		t.Fatalf("portable group JSON leaked a local profile reference: %s", encoded)
	}

	child.AuthProfileID = ""
	if err := store.UpdateGroup(ctx, child); err != nil {
		t.Fatalf("clear child profile: %v", err)
	}
	if err := store.DeleteAuthProfile(ctx, childProfile.Id); err != nil {
		t.Fatalf("delete detached child profile: %v", err)
	}
}

func TestVersionThirtyTwoMigratesGroupAuthProfileColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schema-v32.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	group, err := store.CreateGroup(ctx, model.Group{Name: "Existing"})
	if err != nil {
		t.Fatalf("create legacy group: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE groups DROP COLUMN auth_profile_id`); err != nil {
		t.Fatalf("prepare schema version 32 groups table: %v", err)
	}
	for _, column := range []string{"local_start_dir", "remote_start_dir"} {
		if _, err := db.Exec(`ALTER TABLE groups DROP COLUMN ` + column); err != nil {
			t.Fatalf("prepare schema version 32 groups table without %s: %v", column, err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 32`); err != nil {
		t.Fatalf("mark fixture as schema version 32: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate schema version 32: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	loaded, err := store.GetGroup(ctx, group.Id)
	if err != nil || loaded.Name != group.Name || loaded.AuthProfileID != "" {
		t.Fatalf("migrated group = %#v, err=%v", loaded, err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("migrated schema version = %d, err=%v; want %d", version, err, schemaVersion)
	}
}

func TestAuthProfileValidationAndPortableHostJSON(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "auth-profile-validation.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "  "}); err == nil {
		t.Fatal("empty profile name was accepted")
	}
	first, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "Production"})
	if err != nil {
		t.Fatalf("create first profile: %v", err)
	}
	if _, err := store.CreateAuthProfile(ctx, model.AuthProfile{Name: "production"}); err == nil {
		t.Fatal("case-insensitive duplicate profile name was accepted")
	}

	encoded, err := json.Marshal(model.Host{
		Kind: model.HostKindSSH, Name: "prod", IP: "example.test", User: "host-user",
		CredentialRef: "local-secret-ref", AuthProfileID: first.Id, IdentityFile: "/Users/local/.ssh/key",
	})
	if err != nil {
		t.Fatalf("marshal portable host: %v", err)
	}
	for _, localValue := range []string{first.Id, "/Users/local/.ssh/key"} {
		if strings.Contains(string(encoded), localValue) {
			t.Fatalf("portable host JSON contains local auth data %q: %s", localValue, encoded)
		}
	}
	if strings.Contains(string(encoded), "auth_profile_id") {
		t.Fatalf("portable host JSON contains local profile linkage: %s", encoded)
	}
}

func TestVersionTwentyDatabaseMigratesAuthProfiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v20.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	host, err := store.CreateHost(ctx, model.Host{Kind: model.HostKindSSH, Name: "legacy", IP: "127.0.0.1", Port: 22})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close current store: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	for _, statement := range []string{
		`DROP INDEX hosts_by_auth_profile`,
		`ALTER TABLE hosts DROP COLUMN auth_profile_id`,
		`DROP TABLE auth_profiles`,
		`PRAGMA user_version = 20`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare version 20 schema with %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate version 20 database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	loaded, err := store.GetHost(ctx, host.Id)
	if err != nil || loaded.AuthProfileID != "" {
		t.Fatalf("migrated host auth profile = %q, err=%v", loaded.AuthProfileID, err)
	}
	profiles, err := store.ListAuthProfiles(ctx)
	if err != nil || len(profiles) != 0 {
		t.Fatalf("auth profiles after migration = %#v, err=%v", profiles, err)
	}
}

func TestHostWithAuthProfileOverridesOnlyConfiguredFields(t *testing.T) {
	host := model.Host{User: "host-user", CredentialRef: "host-ref", IdentityFile: "host-key"}
	got := host.WithAuthProfile(model.AuthProfile{User: "profile-user", CredentialRef: "profile-ref"})
	if got.User != "profile-user" || got.CredentialRef != "profile-ref" || got.IdentityFile != "host-key" {
		t.Fatalf("profile overlay = %#v", got)
	}
	fallback := host.WithAuthProfile(model.AuthProfile{})
	if fallback.User != host.User || fallback.CredentialRef != host.CredentialRef || fallback.IdentityFile != host.IdentityFile {
		t.Fatalf("empty profile should preserve host values: got %#v want %#v", fallback, host)
	}
}
