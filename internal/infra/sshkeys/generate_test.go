package sshkeys

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateEd25519EncryptedKeyPair(t *testing.T) {
	privatePath, publicPath, err := Generate(filepath.Join(t.TempDir(), "identity"), Ed25519, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	privateBytes, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privateBytes); err == nil {
		t.Fatal("generated private key unexpectedly parsed without its passphrase")
	}
	if _, err := ssh.ParsePrivateKeyWithPassphrase(privateBytes, []byte("correct horse")); err != nil {
		t.Fatalf("generated private key did not parse with passphrase: %v", err)
	}
	publicBytes, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey(publicBytes); err != nil {
		t.Fatalf("generated public key is invalid: %v", err)
	}
	if !strings.HasSuffix(publicPath, ".pub") || !filepath.IsAbs(privatePath) {
		t.Fatalf("returned paths = %q and %q, want absolute private path and .pub companion", privatePath, publicPath)
	}
	if runtime.GOOS != "windows" {
		privateInfo, err := os.Stat(privatePath)
		if err != nil {
			t.Fatal(err)
		}
		if privateInfo.Mode().Perm() != 0o600 {
			t.Fatalf("private key permissions = %o, want 600", privateInfo.Mode().Perm())
		}
		publicInfo, err := os.Stat(publicPath)
		if err != nil {
			t.Fatal(err)
		}
		if publicInfo.Mode().Perm() != 0o644 {
			t.Fatalf("public key permissions = %o, want 644", publicInfo.Mode().Perm())
		}
	}
}

func TestGenerateRSA3072KeyPair(t *testing.T) {
	privatePath, _, err := Generate(filepath.Join(t.TempDir(), "rsa_identity"), RSA3072, "")
	if err != nil {
		t.Fatal(err)
	}
	privateBytes, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		t.Fatalf("generated RSA private key did not parse: %v", err)
	}
	if got := signer.PublicKey().Type(); got != ssh.KeyAlgoRSA {
		t.Fatalf("generated key type = %q, want %q", got, ssh.KeyAlgoRSA)
	}
}

func TestGenerateDoesNotOverwriteAndRollsBackPrivateOnPublicCollision(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "identity")
	if err := os.WriteFile(privatePath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Generate(privatePath, Ed25519, ""); err == nil {
		t.Fatal("expected existing private-key path to be rejected")
	}
	if data, err := os.ReadFile(privatePath); err != nil || string(data) != "keep me" {
		t.Fatalf("existing private key changed: data=%q err=%v", data, err)
	}

	rollbackPath := filepath.Join(directory, "rollback")
	publicPath := rollbackPath + ".pub"
	if err := os.WriteFile(publicPath, []byte("keep public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Generate(rollbackPath, Ed25519, ""); err == nil {
		t.Fatal("expected existing public-key path to be rejected")
	}
	if _, err := os.Stat(rollbackPath); !os.IsNotExist(err) {
		t.Fatalf("private key should be removed after public-key collision, stat err=%v", err)
	}
	if data, err := os.ReadFile(publicPath); err != nil || string(data) != "keep public" {
		t.Fatalf("existing public key changed: data=%q err=%v", data, err)
	}
}

func TestGenerateExpandsHomeAndRejectsUnknownAlgorithm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	privatePath, _, err := Generate("~/.ssh/test_identity", Ed25519, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".ssh", "test_identity"); privatePath != want {
		t.Fatalf("expanded private key path = %q, want %q", privatePath, want)
	}
	if _, _, err := Generate(filepath.Join(t.TempDir(), "invalid"), Algorithm("unknown"), ""); err == nil {
		t.Fatal("expected unsupported algorithm to be rejected")
	}
}
