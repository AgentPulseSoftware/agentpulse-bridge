package cred

import (
	"errors"
	"testing"
)

// fakeStore is a minimal in-memory Store, used here for this package's
// own interface contract test. cmd/agentpulse's flush tests define their
// own equivalent (Go test files aren't importable across packages), used
// the same way: to exercise credential handling without ever touching a
// real backend.
type fakeStore struct {
	secret string
	has    bool
}

func (f *fakeStore) Name() string { return "fake" }

func (f *fakeStore) Get() (string, error) {
	if !f.has {
		return "", ErrNotFound
	}
	return f.secret, nil
}

func (f *fakeStore) Set(secret string) error {
	f.secret = secret
	f.has = true
	return nil
}

func (f *fakeStore) Delete() error {
	f.secret = ""
	f.has = false
	return nil
}

// assertStoreContract exercises the Store interface contract every
// backend must satisfy, independent of how it actually persists the
// secret: Get on nothing stored is ErrNotFound, Set then Get round
// trips, Delete then Get is ErrNotFound again, and Name is non-empty.
func assertStoreContract(t *testing.T, store Store) {
	t.Helper()

	if name := store.Name(); name == "" {
		t.Error("Name() returned empty string")
	}

	if _, err := store.Get(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() before Set = %v, want ErrNotFound", err)
	}

	if err := store.Set("s3cr3t-value"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get() after Set error: %v", err)
	}
	if got != "s3cr3t-value" {
		t.Errorf("Get() = %q, want %q", got, "s3cr3t-value")
	}

	if err := store.Set("replaced-value"); err != nil {
		t.Fatalf("Set() (overwrite) error: %v", err)
	}
	got, err = store.Get()
	if err != nil {
		t.Fatalf("Get() after overwrite error: %v", err)
	}
	if got != "replaced-value" {
		t.Errorf("Get() after overwrite = %q, want %q", got, "replaced-value")
	}

	if err := store.Delete(); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() after Delete = %v, want ErrNotFound", err)
	}

	// Deleting again (nothing stored) must not be an error.
	if err := store.Delete(); err != nil {
		t.Errorf("Delete() with nothing stored returned an error: %v", err)
	}
}

func TestFakeStoreSatisfiesContract(t *testing.T) {
	assertStoreContract(t, &fakeStore{})
}
