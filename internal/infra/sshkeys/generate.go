// Package sshkeys creates local SSH identities for saved host configurations.
package sshkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

type Algorithm string

const (
	Ed25519 Algorithm = "ed25519"
	RSA3072 Algorithm = "rsa-3072"
)

// Generate creates a private key and its OpenSSH authorized_keys companion.
// Existing files are never overwritten. The returned paths are absolute.
func Generate(path string, algorithm Algorithm, passphrase string) (privatePath, publicPath string, err error) {
	privatePath, err = ResolvePath(path)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("private key path is required")
	}
	publicPath = privatePath + ".pub"

	var privateKey any
	switch algorithm {
	case Ed25519:
		_, key, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			return "", "", fmt.Errorf("generate Ed25519 key: %w", generateErr)
		}
		privateKey = key
	case RSA3072:
		key, generateErr := rsa.GenerateKey(rand.Reader, 3072)
		if generateErr != nil {
			return "", "", fmt.Errorf("generate RSA key: %w", generateErr)
		}
		privateKey = key
	default:
		return "", "", fmt.Errorf("unsupported SSH key algorithm %q", algorithm)
	}

	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(privateKey, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "", []byte(passphrase))
	}
	if err != nil {
		return "", "", fmt.Errorf("encode private key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("encode public key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(privatePath), 0o700); err != nil {
		return "", "", fmt.Errorf("create key directory: %w", err)
	}
	if err := writeExclusive(privatePath, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", "", fmt.Errorf("write private key: %w", err)
	}
	if err := writeExclusive(publicPath, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644); err != nil {
		_ = os.Remove(privatePath)
		return "", "", fmt.Errorf("write public key: %w", err)
	}
	return privatePath, publicPath, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	return nil
}

// ResolvePath expands a leading home-directory marker and returns an absolute
// clean path. It does not inspect or create the target.
func ResolvePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("private key path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve key path: %w", err)
	}
	return filepath.Clean(absPath), nil
}
