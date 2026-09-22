// Package logging is the bridge's one diagnostic log (BR-04, an addition
// to SPEC 11.2's package list): a single rotating file at
// $XDG_STATE_HOME/agentpulse/bridge.log, mode 0600, rotated at 5 MB with
// two backups (bridge.log.1, bridge.log.2). Default level is info;
// AGENTPULSE_DEBUG=1 raises it to debug and additionally mirrors every
// line to stderr — without it, nothing this package writes ever reaches
// stdout or stderr, which is what lets "agentpulse hook" and "agentpulse
// flush" honor BR-04's "never prints unless AGENTPULSE_DEBUG=1" on every
// path, including their own diagnostics.
//
// Redaction is structural, not a filter: call sites pass only the short,
// fixed-vocabulary facts each diagnostic needs (an event type, an HTTP
// status, a path already known to be inside the bridge's own config or
// state directory, a credential backend name) as slog key-value
// arguments — the same discipline every call site in this codebase
// already applies to debugf. This package does not, and cannot, filter
// arbitrary content out of a message; it is the call site's job to never
// pass a secret, a full path outside the bridge's own directories, a
// command, tool output, or prompt text (BR-19's spirit applied to the
// bridge's own logging).
package logging

import (
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// maxLogBytes and maxLogBackups are BR-04's rotation policy.
const (
	maxLogBytes   = 5 * 1024 * 1024
	maxLogBackups = 2
)

// New builds a *slog.Logger writing text-formatted lines to w at level
// (and above). It is exported so tests, and anything that wants a logger
// pointed somewhere other than the real bridge.log, can build one
// directly without going through the process-wide Get().
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

var (
	mu        sync.Mutex
	cached    *slog.Logger
	cachedKey string
)

// Get returns the process-wide logger, writing to bridge.log inside the
// current $XDG_STATE_HOME (rotated per BR-04) and, when
// AGENTPULSE_DEBUG=1, also to os.Stderr at debug level. The build is
// cached and reused across calls within a process — a real "agentpulse
// hook" or "agentpulse flush" invocation calls this many times over its
// short life and must not reopen the log file on every call — but the
// cache key includes the state directory and the debug flag, so tests
// that change either via t.Setenv transparently get a fresh logger
// rather than a stale one from an earlier test.
func Get() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()

	debug := os.Getenv("AGENTPULSE_DEBUG") == "1"
	key := xdgpaths.StateDir() + "|"
	if debug {
		key += "debug"
	}
	if cached != nil && cachedKey == key {
		return cached
	}

	rw := newRotatingWriter(xdgpaths.LogPath(), maxLogBytes, maxLogBackups)
	level := slog.LevelInfo
	var w io.Writer = rw
	if debug {
		level = slog.LevelDebug
		w = io.MultiWriter(rw, os.Stderr)
	}

	cached = New(w, level)
	cachedKey = key
	return cached
}
