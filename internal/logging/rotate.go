package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingWriter is an io.Writer over one log file that rotates itself
// once it would exceed maxBytes, keeping at most maxBackups old copies
// (path.1 is the newest backup, path.maxBackups the oldest; anything
// older is deleted). Rotation is implemented in-package rather than
// pulling in a dependency: for something this small, a pinned, cgo-free,
// widely used rotator would not be materially smaller.
//
// A write that fails for any reason (the log directory can't be created,
// the file can't be opened, rotation itself fails, the write syscall
// fails) is dropped silently: Write still reports success (len(p), nil)
// so the caller — ultimately slog, whose own Logger.Info/.Debug/etc.
// methods have no error return at all — never sees a failure. Logging
// must never be the reason a command fails (BR-04's spirit).
type rotatingWriter struct {
	path       string
	maxBytes   int64
	maxBackups int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func newRotatingWriter(path string, maxBytes int64, maxBackups int) *rotatingWriter {
	return &rotatingWriter{path: path, maxBytes: maxBytes, maxBackups: maxBackups}
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpenLocked(); err != nil {
		return len(p), nil //nolint:nilerr // a log write must never fail the caller; see type doc
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return len(p), nil //nolint:nilerr // same: dropped silently, not surfaced
		}
	}

	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil {
		return len(p), nil //nolint:nilerr // same: dropped silently, not surfaced
	}
	return n, nil
}

func (w *rotatingWriter) ensureOpenLocked() error {
	if w.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is the bridge's own XDG state dir
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

// rotateLocked closes the current file, shifts path.1..path.maxBackups-1
// up by one (dropping whatever was at path.maxBackups), moves path itself
// to path.1, and reopens a fresh, empty path. Called with w.mu held.
func (w *rotatingWriter) rotateLocked() error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}

	oldest := fmt.Sprintf("%s.%d", w.path, w.maxBackups)
	_ = os.Remove(oldest)
	for i := w.maxBackups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", w.path, i)
		dst := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if _, err := os.Stat(w.path); err == nil {
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return err
		}
	}
	return w.ensureOpenLocked()
}
