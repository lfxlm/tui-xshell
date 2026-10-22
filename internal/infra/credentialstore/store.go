// Package credentialstore stores secrets in the operating system's credential
// vault. References, rather than secret values, are persisted by the app.
package credentialstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const serviceName = "tui-ssh-workbench"

var (
	ErrNotFound         = keyring.ErrNotFound
	ErrInvalidReference = errors.New("credential reference must not be empty")
)

// keyringAPI is the narrow system-vault API used by Store. Keeping it small
// allows tests to exercise error handling without reading or writing the real
// user's credentials.
type keyringAPI interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

type systemKeyring struct{}

func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// Store is a system-keyring-backed secret store.
type Store struct {
	keyring keyringAPI
}

// New returns a Store backed by the current platform's system credential vault.
func New() *Store {
	return &Store{keyring: systemKeyring{}}
}

func newWithKeyring(ring keyringAPI) *Store {
	return &Store{keyring: ring}
}

// NewReference returns an opaque reference suitable for Host.CredentialRef.
// Each write gets a fresh reference so the keyring update and SQLite update can
// be ordered safely without overwriting the credential currently in use.
func NewReference() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate credential reference: %w", err)
	}
	return "credential:" + hex.EncodeToString(random[:]), nil
}

// Set writes a secret to the operating system's credential vault.
func (s *Store) Set(reference, secret string) error {
	if s == nil || s.keyring == nil {
		return errors.New("credential store is not initialized")
	}
	reference, err := normalizeReference(reference)
	if err != nil {
		return err
	}
	if err := s.keyring.Set(serviceName, reference, secret); err != nil {
		return fmt.Errorf("store credential in system keyring: %w", err)
	}
	return nil
}

// Get reads a secret from the operating system's credential vault.
func (s *Store) Get(reference string) (string, error) {
	if s == nil || s.keyring == nil {
		return "", errors.New("credential store is not initialized")
	}
	reference, err := normalizeReference(reference)
	if err != nil {
		return "", err
	}
	secret, err := s.keyring.Get(serviceName, reference)
	if err != nil {
		return "", fmt.Errorf("read credential from system keyring: %w", err)
	}
	return secret, nil
}

// Delete removes a secret from the system vault. Deleting an already absent
// credential is treated as success so host deletion can be safely retried.
func (s *Store) Delete(reference string) error {
	if s == nil || s.keyring == nil {
		return errors.New("credential store is not initialized")
	}
	reference, err := normalizeReference(reference)
	if err != nil {
		return err
	}
	if err := s.keyring.Delete(serviceName, reference); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete credential from system keyring: %w", err)
	}
	return nil
}

func normalizeReference(reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", ErrInvalidReference
	}
	return reference, nil
}
