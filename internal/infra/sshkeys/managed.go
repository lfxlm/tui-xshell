package sshkeys

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const managedKeyPrefix = "workbench_"

// ManagedIdentity is a local identity created under ~/.ssh/workbench_*.
// It contains only public metadata and file paths, never private-key bytes.
type ManagedIdentity struct {
	PrivatePath string
	PublicPath  string
	Algorithm   string
	Fingerprint string
	Encrypted   bool
	HasPublic   bool
	Invalid     bool
}

// ListManaged returns the workbench-managed private keys in ~/.ssh. Other
// identities are deliberately left to their owner and remain usable by path.
func ListManaged() ([]ManagedIdentity, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	directory := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []ManagedIdentity{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list SSH identities: %w", err)
	}
	identities := make([]ManagedIdentity, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, managedKeyPrefix) || strings.HasSuffix(name, ".pub") {
			continue
		}
		privatePath := filepath.Join(directory, name)
		info, err := os.Lstat(privatePath)
		if err != nil {
			return nil, fmt.Errorf("inspect SSH identity %q: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		identity := ManagedIdentity{PrivatePath: privatePath, PublicPath: privatePath + ".pub"}
		if publicInfo, err := os.Lstat(identity.PublicPath); err == nil {
			identity.HasPublic = publicInfo.Mode().IsRegular()
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect public key for %q: %w", name, err)
		}
		privateBytes, err := os.ReadFile(privatePath)
		if err != nil {
			return nil, fmt.Errorf("read SSH identity %q: %w", name, err)
		}
		if signer, err := ssh.ParsePrivateKey(privateBytes); err == nil {
			identity.Algorithm = signer.PublicKey().Type()
			identity.Fingerprint = ssh.FingerprintSHA256(signer.PublicKey())
		} else {
			var passphraseErr *ssh.PassphraseMissingError
			if errors.As(err, &passphraseErr) {
				identity.Encrypted = true
				if identity.HasPublic {
					if publicBytes, readErr := os.ReadFile(identity.PublicPath); readErr == nil {
						if key, _, _, _, parseErr := ssh.ParseAuthorizedKey(publicBytes); parseErr == nil {
							identity.Algorithm = key.Type()
							identity.Fingerprint = ssh.FingerprintSHA256(key)
						}
					}
				}
			} else {
				identity.Invalid = true
			}
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

// NextManagedPath chooses an unused default path for a newly generated key.
func NextManagedPath(algorithm Algorithm) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	base := "workbench_ed25519"
	if algorithm == RSA3072 {
		base = "workbench_rsa3072"
	} else if algorithm != Ed25519 {
		return "", fmt.Errorf("unsupported SSH key algorithm %q", algorithm)
	}
	directory := filepath.Join(home, ".ssh")
	for suffix := 1; suffix <= 10000; suffix++ {
		name := base
		if suffix > 1 {
			name = fmt.Sprintf("%s_%d", base, suffix)
		}
		candidate := filepath.Join(directory, name)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			if _, publicErr := os.Lstat(candidate + ".pub"); errors.Is(publicErr, os.ErrNotExist) {
				return candidate, nil
			} else if publicErr != nil && !errors.Is(publicErr, os.ErrNotExist) {
				return "", fmt.Errorf("inspect public-key path: %w", publicErr)
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect private-key path: %w", err)
		}
	}
	return "", errors.New("no unused workbench key path available")
}

// DeleteManaged removes an app-managed private key and its public companion.
// It refuses symlinks, non-regular files, and paths outside ~/.ssh/workbench_*.
func DeleteManaged(privatePath string) error {
	resolved, err := ResolvePath(privatePath)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	directory := filepath.Join(home, ".ssh")
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect SSH directory: %w", err)
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to manage keys through a non-directory SSH path")
	}
	if filepath.Dir(resolved) != directory || !strings.HasPrefix(filepath.Base(resolved), managedKeyPrefix) || strings.HasSuffix(resolved, ".pub") {
		return errors.New("only ~/.ssh/workbench_* identities can be deleted here")
	}
	privateInfo, err := os.Lstat(resolved)
	if err != nil {
		return fmt.Errorf("inspect private key: %w", err)
	}
	if !privateInfo.Mode().IsRegular() {
		return errors.New("refusing to delete a non-regular private-key path")
	}
	publicPath := resolved + ".pub"
	publicInfo, publicErr := os.Lstat(publicPath)
	if publicErr != nil && !errors.Is(publicErr, os.ErrNotExist) {
		return fmt.Errorf("inspect public key: %w", publicErr)
	}
	if publicErr == nil && !publicInfo.Mode().IsRegular() {
		return errors.New("refusing to delete a non-regular public-key path")
	}

	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("prepare key deletion: %w", err)
	}
	stagingDirectory := filepath.Join(directory, ".workbench-delete-"+hex.EncodeToString(token))
	if err := os.Mkdir(stagingDirectory, 0o700); err != nil {
		return fmt.Errorf("prepare key deletion: %w", err)
	}
	stagedPrivate := filepath.Join(stagingDirectory, filepath.Base(resolved))
	if err := os.Rename(resolved, stagedPrivate); err != nil {
		_ = os.Remove(stagingDirectory)
		return fmt.Errorf("stage private key for deletion: %w", err)
	}
	stagedPublic := filepath.Join(stagingDirectory, filepath.Base(publicPath))
	if publicErr == nil {
		if err := os.Rename(publicPath, stagedPublic); err != nil {
			_ = os.Rename(stagedPrivate, resolved)
			_ = os.Remove(stagingDirectory)
			return fmt.Errorf("stage public key for deletion: %w", err)
		}
	}
	if err := os.RemoveAll(stagingDirectory); err != nil {
		return fmt.Errorf("remove staged SSH key files: %w", err)
	}
	return nil
}
