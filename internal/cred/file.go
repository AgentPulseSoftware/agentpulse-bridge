package cred

import (
	"os"
	"runtime"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/atomicfile"
)

// fileStore is BR-06's fallback: used only when neither the macOS
// Keychain nor a Linux Secret Service is available. It stores the raw
// secret, and nothing else, at path (xdgpaths.CredentialsPath():
// $XDG_CONFIG_HOME/agentpulse/credentials), mode 0600, written
// atomically (internal/atomicfile — a temp file in the same directory,
// then renamed into place).
type fileStore struct {
	path string
}

func newFileStore(path string) *fileStore {
	return &fileStore{path: path}
}

func (f *fileStore) Name() string { return NameFileFallback }

// Get reads the secret. A missing file is ErrNotFound, not an error — the
// normal state before pairing. A file that exists with looser
// permissions than 0600 (an older install, or a manually created file)
// is corrected in place rather than refused: this file is
// meaningful to no other program, so tightening its mode on read is
// strictly safer than the alternative of leaving a secret world- or
// group-readable, and refusing to read it would turn a fixable local
// permissions slip into a hard failure for every command that needs the
// credential.
func (f *fileStore) Get() (string, error) {
	info, err := os.Stat(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			_ = os.Chmod(f.path, 0o600) // best effort; Get still proceeds either way
		}
	}
	data, err := os.ReadFile(f.path) //nolint:gosec // path is the bridge's own XDG config dir, not attacker input
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	if len(data) == 0 {
		return "", ErrNotFound
	}
	return string(data), nil
}

// Set writes secret to path atomically, mode 0600.
func (f *fileStore) Set(secret string) error {
	return atomicfile.WriteBytes(f.path, []byte(secret), 0o600)
}

// Delete removes the credentials file. A file that doesn't exist is not
// an error.
func (f *fileStore) Delete() error {
	err := os.Remove(f.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
