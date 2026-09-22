// Package lockfile provides small, reusable exclusive advisory locking
// (POSIX flock) helpers over a dedicated lock file, for callers that need
// this guarantee for a file of their own: one flush process per user
// ("agentpulse flush" lock file) and one lock per Claude Code session
// (cmd/agentpulse/hook_classify.go).
//
// internal/spool keeps its own inline lock on the spool data file itself
// rather than using this package: its locking semantics are specific to
// Append and switching would change Append's existing behavior.
package lockfile

import (
	"os"
	"path/filepath"
	"syscall"
)

// Lock blocking-acquires an exclusive lock on the file at path, creating
// it and its parent directory (mode 0700) if needed. The returned unlock
// function releases the lock and closes the file; the caller must call it
// exactly once, typically via defer, when the critical section ends.
func Lock(path string) (unlock func(), err error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// TryLock attempts a non-blocking exclusive lock on the file at path,
// creating it and its parent directory (mode 0700) if needed. When another
// process (or another open file description on the same file) already
// holds the lock, ok is false and err is nil: that is the expected, common
// outcome for a caller like "agentpulse flush" ("if the lock is held, exit
// 0 immediately and silently"), not a failure to report. err is non-nil
// only for an actual I/O problem — the directory couldn't be created, or
// the file couldn't be opened.
func TryLock(path string) (unlock func(), ok bool, err error) {
	f, err := open(path)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false, nil
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is derived from the bridge's own XDG state dir, not attacker input
}
