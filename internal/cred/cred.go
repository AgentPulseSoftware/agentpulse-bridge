// Package cred stores and retrieves the bridge's own secret (BR-06,
// SEC-02, SPEC 11.2): the macOS Keychain, Linux Secret Service, or —
// only when neither is available — a file only the operator can read.
// The secret is never written to a log, never included in an error
// message, and never placed in a process's argument list (visible via
// `ps` to any local user); see the two backend files for exactly how
// each avoids that.
package cred

import (
	"errors"
)

// service and account identify the bridge's credential in every backend
// (BR-06: "service agentpulse, account bridge-secret").
const (
	service = "agentpulse"
	account = "bridge-secret"
)

// The three names Store.Name() returns, exported so callers compare
// against a name once agreed here rather than a literal string copied at
// each call site, e.g. internal/doctor's credential-store check.
const (
	NameKeychain      = "macOS Keychain"
	NameSecretService = "Secret Service (secret-tool)"
	NameFileFallback  = "file fallback"
)

// ErrNotFound is returned by Get when the backend is reachable but holds
// no secret for the bridge yet — an unpaired bridge, or one whose
// credential was just cleared (ERR-04's 3-strike unpair). Callers treat
// this the same as an empty secret; it exists as a distinct sentinel so a
// backend can tell "nothing stored" apart from a real I/O failure.
var ErrNotFound = errors.New("cred: no secret stored")

// ErrUnsupportedSecret is returned by Set when the secret contains a
// character the backend's transport cannot carry unambiguously. Today
// only the macOS Keychain backend raises it: that backend hands
// security(1) one command line on standard input, and security's own
// command reader treats quotes and backslashes as grouping characters
// (see keychain_darwin.go). Rather than escape, the backend refuses
// anything outside the base64url alphabet the relay actually issues
// (SEC-02: 32 random bytes, base64url, padding stripped), so a
// credential can never be silently mangled on its way into the store.
//
// Its message never contains the secret, or any part of it, or its
// length, so it stays safe to log and to return to the user (ERR-02).
var ErrUnsupportedSecret = errors.New("cred: secret contains characters this credential store cannot carry")

// Store is one place the bridge's secret can live. Every backend
// implements exactly this: Get/Set/Delete for the secret itself, and
// Name for "agentpulse doctor" (BR-06: "doctor reports which
// store is active") to report which one is in use.
type Store interface {
	// Get returns the stored secret, or ErrNotFound if the backend holds
	// none.
	Get() (string, error)
	// Set stores secret, overwriting any existing value.
	Set(secret string) error
	// Delete removes the stored secret. Deleting a secret that isn't
	// there is not an error.
	Delete() error
	// Name is a short, human-readable label for the active backend
	// ("macOS Keychain", "Secret Service (secret-tool)", "file fallback"),
	// with no dynamic content (never a path, never a status detail) so it
	// is always safe to log or print verbatim.
	Name() string
}
