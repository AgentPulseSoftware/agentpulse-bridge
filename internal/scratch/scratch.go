// Package scratch persists internal/classify's SessionState to and from
// disk (BR-05: $XDG_STATE_HOME/agentpulse/sessions/<session_id>.json,
// mode 0600), deletes it on session_end, and sweeps files older than 24
// hours. It is a thin persistence adapter: all classification logic lives
// in internal/classify.
package scratch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

// unsafeSessionIDChars sanitizes a Claude Code session_id into a safe
// filename. The event schema itself restricts session_id to
// `^[A-Za-z0-9_-]{1,128}$` (the AgentPulse `event.v1` schema), so a real session
// id should never need this — but scratch file naming must never trust
// that defensively, since a path traversal here would be a local file
// safety bug, not just a malformed event.
var unsafeSessionIDChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

const maxSessionIDLen = 128

// SanitizeSessionID turns a Claude Code session_id into a safe filename
// component (no extension): the event schema itself restricts session_id
// to `^[A-Za-z0-9_-]{1,128}$` (the AgentPulse `event.v1` schema), so a real
// session id should never need this, but anything that builds a path from
// one must never trust that defensively — a path traversal here would be
// a local file safety bug, not just a malformed event. Exported so other
// per-session file names (cmd/agentpulse/hook_classify.go's per-session
// lock file) stay consistent with scratch's own.
func SanitizeSessionID(sessionID string) string {
	s := unsafeSessionIDChars.ReplaceAllString(sessionID, "_")
	if len(s) > maxSessionIDLen {
		s = s[:maxSessionIDLen]
	}
	if s == "" {
		s = "unknown"
	}
	return s
}

func safeSessionFileName(sessionID string) string {
	return SanitizeSessionID(sessionID) + ".json"
}

func sessionPath(stateDir, sessionID string) string {
	return filepath.Join(stateDir, "sessions", safeSessionFileName(sessionID))
}

// Load reads the scratch file for sessionID under stateDir. A missing,
// unreadable, or corrupt file yields a fresh classify.NewSessionState()
// rather than an error propagated to the caller: classification must never
// fail because scratch couldn't be read (BR-02's spirit — "agentpulse
// hook" always proceeds).
func Load(stateDir, sessionID string) *classify.SessionState {
	data, err := os.ReadFile(sessionPath(stateDir, sessionID)) //nolint:gosec // path built from XDG state dir + sanitized session id
	if err != nil {
		return classify.NewSessionState()
	}
	var s classify.SessionState
	if err := json.Unmarshal(data, &s); err != nil {
		return classify.NewSessionState()
	}
	return &s
}

// Save writes state for sessionID under stateDir, mode 0600 (BR-05),
// atomically: it writes to a temporary file in the same directory and
// renames it into place, so a crash or concurrent read never observes a
// half-written scratch file.
func Save(stateDir, sessionID string, state *classify.SessionState) error {
	dir := filepath.Join(stateDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".scratch-*")
	if err != nil {
		return err
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
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, sessionPath(stateDir, sessionID)); err != nil {
		return err
	}
	succeeded = true
	return nil
}

// Delete removes the scratch file for sessionID (BR-05: "deleted on
// session_end"). A file that doesn't exist is not an error.
func Delete(stateDir, sessionID string) error {
	err := os.Remove(sessionPath(stateDir, sessionID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Sweep removes scratch files under stateDir whose last modification is
// older than olderThan (BR-05: "or after 24 hours"). A missing sessions
// directory is not an error.
func Sweep(stateDir string, olderThan time.Duration) error {
	dir := filepath.Join(stateDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}
