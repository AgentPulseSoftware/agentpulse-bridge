package cred

import (
	"errors"
	"os"
	"testing"
)

// TestCredE2ERealBackend exercises whatever Default() actually selects on
// this machine — the real macOS Keychain or the real Linux Secret
// Service — end to end: Set, Get, Delete against the live backend.
//
// It is skipped unless AGENTPULSE_CRED_E2E=1 is set, because the macOS
// Keychain can prompt (an unlocked-but-first-access item triggers an
// "agentpulse wants to use your confidential information" dialog) and a
// Linux Secret Service needs a real, unlocked keyring — neither is
// something CI can satisfy, and neither should run by default on a
// developer's own machine just from `go test ./...`.
//
// Run it explicitly with:
//
//	AGENTPULSE_CRED_E2E=1 go test ./internal/cred/ -run TestCredE2ERealBackend -v
//
// It cleans up the item it creates (Delete, via t.Cleanup) even if an
// assertion fails partway through, and it proves the cleanup worked by
// asserting that a Get after the Delete returns ErrNotFound. This
// additionally requires the operator to confirm from outside the process
// that nothing was left in the real login Keychain:
//
//	security find-generic-password -s agentpulse
//
// which must print "The specified item could not be found in the
// keychain." after the run.
func TestCredE2ERealBackend(t *testing.T) {
	if os.Getenv("AGENTPULSE_CRED_E2E") != "1" {
		t.Skip("set AGENTPULSE_CRED_E2E=1 to run against the real platform credential store")
	}

	store := platformStoreFunc()
	if store == nil {
		t.Skip("no platform credential store available on this machine")
	}
	t.Logf("testing against: %s", store.Name())

	t.Cleanup(func() {
		_ = store.Delete()
	})

	if err := store.Set("agentpulse-e2e-test-secret"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got != "agentpulse-e2e-test-secret" {
		t.Errorf("Get() = %q, want %q", got, "agentpulse-e2e-test-secret")
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() after Delete() error = %v, want ErrNotFound: the test left a credential behind", err)
	}
}
