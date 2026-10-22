package sshkeys

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestListManagedIdentitiesReportsFingerprintAndEncryption(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	privatePath := filepath.Join(home, ".ssh", "workbench_plain")
	if _, _, err := Generate(privatePath, Ed25519, ""); err != nil {
		t.Fatal(err)
	}
	encryptedPath := filepath.Join(home, ".ssh", "workbench_encrypted")
	if _, _, err := Generate(encryptedPath, Ed25519, "private phrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Generate(filepath.Join(home, ".ssh", "personal_identity"), Ed25519, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "workbench_broken"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	identities, err := ListManaged()
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 3 {
		t.Fatalf("managed identity count = %d, want three: %#v", len(identities), identities)
	}
	byName := make(map[string]ManagedIdentity, len(identities))
	for _, identity := range identities {
		byName[filepath.Base(identity.PrivatePath)] = identity
	}
	plain := byName["workbench_plain"]
	if plain.Algorithm == "" || !strings.HasPrefix(plain.Fingerprint, "SHA256:") || plain.Encrypted || !plain.HasPublic || plain.Invalid {
		t.Fatalf("plain identity metadata = %#v", plain)
	}
	encrypted := byName["workbench_encrypted"]
	if encrypted.Algorithm == "" || !strings.HasPrefix(encrypted.Fingerprint, "SHA256:") || !encrypted.Encrypted || !encrypted.HasPublic || encrypted.Invalid {
		t.Fatalf("encrypted identity metadata = %#v", encrypted)
	}
	if broken := byName["workbench_broken"]; !broken.Invalid {
		t.Fatalf("malformed managed identity should remain visible as invalid: %#v", broken)
	}
}

func TestNextManagedPathSkipsExistingPrivateAndPublicFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	directory := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "workbench_ed25519"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "workbench_ed25519_2.pub"), []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := NextManagedPath(Ed25519)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(directory, "workbench_ed25519_3"); path != want {
		t.Fatalf("next managed key path = %q, want %q", path, want)
	}
}

func TestDeleteManagedRemovesPairAndRejectsUnsafePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	privatePath := filepath.Join(home, ".ssh", "workbench_remove")
	_, publicPath, err := Generate(privatePath, Ed25519, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteManaged(privatePath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted key path %q still exists (err=%v)", path, err)
		}
	}

	customPath := filepath.Join(home, ".ssh", "personal_identity")
	if err := os.WriteFile(customPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DeleteManaged(customPath); err == nil {
		t.Fatal("custom identity path was accepted for app-managed deletion")
	}
	if data, err := os.ReadFile(customPath); err != nil || string(data) != "keep" {
		t.Fatalf("custom identity changed: data=%q err=%v", data, err)
	}
}

func TestDeleteManagedRefusesSymlinkAndSymlinkedSSHDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sshDirectory := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(home, "outside-key")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(sshDirectory, "workbench_link")
	if err := os.Symlink(external, symlink); err != nil {
		t.Fatal(err)
	}
	if err := DeleteManaged(symlink); err == nil {
		t.Fatal("symlink identity was accepted for deletion")
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}

	otherHome := t.TempDir()
	realSSHDirectory := filepath.Join(otherHome, "ssh")
	if err := os.Mkdir(realSSHDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSSHDirectory, filepath.Join(otherHome, ".ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", otherHome)
	t.Setenv("USERPROFILE", otherHome)
	if err := DeleteManaged(filepath.Join(otherHome, ".ssh", "workbench_identity")); err == nil {
		t.Fatal("symlinked SSH directory was accepted")
	}
}
