package credentialstore

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	values       map[string]string
	setErr       error
	getErr       error
	deleteErr    error
	lastService  string
	lastUser     string
	lastPassword string
}

func (f *fakeKeyring) Set(service, user, password string) error {
	f.lastService, f.lastUser, f.lastPassword = service, user, password
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = make(map[string]string)
	}
	f.values[service+"\x00"+user] = password
	return nil
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.lastService, f.lastUser = service, user
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	f.lastService, f.lastUser = service, user
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.values, service+"\x00"+user)
	return nil
}

func TestStoreUsesOpaqueReferenceAndNeverPersistsSecretInReference(t *testing.T) {
	fake := &fakeKeyring{}
	store := newWithKeyring(fake)
	reference, err := NewReference()
	if err != nil {
		t.Fatalf("NewReference(): %v", err)
	}
	if reference == "" || reference == "private-password" {
		t.Fatalf("NewReference() = %q, want an opaque non-secret reference", reference)
	}
	if err := store.Set(reference, "private-password"); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	if fake.lastService != serviceName || fake.lastUser != reference {
		t.Fatalf("keyring key = (%q, %q), want (%q, %q)", fake.lastService, fake.lastUser, serviceName, reference)
	}
	if reference == fake.lastPassword {
		t.Fatal("credential reference unexpectedly contains the secret")
	}
	got, err := store.Get(reference)
	if err != nil || got != "private-password" {
		t.Fatalf("Get() = %q, %v; want secret", got, err)
	}
	if err := store.Delete(reference); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if _, err := store.Get(reference); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsEmptyReference(t *testing.T) {
	store := newWithKeyring(&fakeKeyring{})
	if err := store.Set("  ", "secret"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Set() error = %v, want ErrInvalidReference", err)
	}
	if _, err := store.Get(""); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Get() error = %v, want ErrInvalidReference", err)
	}
	if err := store.Delete(""); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Delete() error = %v, want ErrInvalidReference", err)
	}
}

func TestStorePreservesBackendErrors(t *testing.T) {
	backendErr := errors.New("vault unavailable")
	fake := &fakeKeyring{setErr: backendErr, getErr: backendErr, deleteErr: backendErr}
	store := newWithKeyring(fake)
	if err := store.Set("host:x", "secret"); !errors.Is(err, backendErr) {
		t.Fatalf("Set() error = %v, want wrapped backend error", err)
	}
	if _, err := store.Get("host:x"); !errors.Is(err, backendErr) {
		t.Fatalf("Get() error = %v, want wrapped backend error", err)
	}
	if err := store.Delete("host:x"); !errors.Is(err, backendErr) {
		t.Fatalf("Delete() error = %v, want wrapped backend error", err)
	}
}

func TestDeleteMissingCredentialIsIdempotent(t *testing.T) {
	store := newWithKeyring(&fakeKeyring{deleteErr: keyring.ErrNotFound})
	if err := store.Delete("host:missing"); err != nil {
		t.Fatalf("Delete() missing credential: %v", err)
	}
}
