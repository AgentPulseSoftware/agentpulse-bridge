// Package atomicfile writes JSON files the way every piece of the
// bridge's local state must be written (BR-05, ERR-02): a temporary file
// in the same directory, written and closed, then renamed into place, so
// a crash or a concurrent reader never observes a half-written file, and
// the target path only ever changes via the one atomic os.Rename call.
//
// internal/config, internal/state, internal/cred, and internal/watch use
// this, so a write failure can
// be reported as a typed WriteError (ERR-02) that "status" and "doctor"
// render as FAIL naming the path.
//
// internal/scratch keeps its own copy of this pattern rather than
// switching to this package: scratch's format and implementation are
// deliberately out of scope here, the same way internal/spool keeps its
// own inline flock rather than switching to internal/lockfile.
package atomicfile

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteError is returned when an atomic write fails: the parent directory
// couldn't be created, the temporary file couldn't be written, or the
// rename into place failed (disk full, permissions, ...). It names the
// path that failed (ERR-02) without ever including the data that was
// being written, so callers can safely surface Error() to a log or to
// "doctor"'s one-line remedy without risking a secret leaking into it.
type WriteError struct {
	Path string
	Err  error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("writing %s: %v", e.Path, e.Err)
}

func (e *WriteError) Unwrap() error { return e.Err }

// WriteJSON marshals v and writes it to path atomically, mode perm with a
// 0700 parent directory: a temp file (".tmp-*") is created in path's own
// directory, written, closed, chmod'd to perm, and renamed over path. The
// temp file being in the same directory as path is what makes the final
// os.Rename an atomic, same-filesystem move rather than a copy — the
// property the "crash between temp and rename leaves the old file intact"
// test relies on.
//
// Every failure is wrapped in a *WriteError naming path (ERR-02); v is
// never included, so a caller writing credentials.json (internal/cred's
// fallback store) cannot accidentally leak the secret into an error
// string.
func WriteJSON(path string, v any, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &WriteError{Path: path, Err: err}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return &WriteError{Path: path, Err: err}
	}
	if err := WriteBytes(path, data, perm); err != nil {
		return err
	}
	return nil
}

// WriteBytes atomically writes data to path, mode perm, the same way
// WriteJSON does, for callers that already have their own encoded bytes
// (internal/cred's fallback file store, which writes a raw secret rather
// than JSON).
func WriteBytes(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &WriteError{Path: path, Err: err}
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return &WriteError{Path: path, Err: err}
	}
	tmpPath := tmp.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return &WriteError{Path: path, Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &WriteError{Path: path, Err: err}
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return &WriteError{Path: path, Err: err}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return &WriteError{Path: path, Err: err}
	}
	succeeded = true
	return nil
}
