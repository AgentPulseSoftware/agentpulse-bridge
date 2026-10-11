package main

import (
	"errors"
	"os"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
)

// TestMain makes defaultCredStore refuse every call for the whole test
// binary. Every test here supplies its own credential store through its
// command's deps; one that forgets gets this error instead of the real
// macOS Keychain or Linux Secret Service. The one sanctioned real-Keychain
// run (AGENTPULSE_CRED_E2E=1 in pair_e2e_test.go) calls cred.Default()
// directly and is unaffected.
func TestMain(m *testing.M) {
	defaultCredStore = func() cred.Store { return refusingCredStore{} }
	os.Exit(m.Run())
}

var errRealCredStoreInTest = errors.New("a test reached the real credential store; supply a fake through the command's deps")

// refusingCredStore is the cred.Store TestMain installs as the default.
type refusingCredStore struct{}

func (refusingCredStore) Get() (string, error) { return "", errRealCredStoreInTest }
func (refusingCredStore) Set(string) error     { return errRealCredStoreInTest }
func (refusingCredStore) Delete() error        { return errRealCredStoreInTest }
func (refusingCredStore) Name() string         { return "refusing test store" }
