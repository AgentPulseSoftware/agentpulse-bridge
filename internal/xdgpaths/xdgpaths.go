// Package xdgpaths resolves the bridge's XDG Base Directory paths (BR-05):
// configuration under $XDG_CONFIG_HOME/agentpulse, and per-session scratch
// state and the event spool under $XDG_STATE_HOME/agentpulse. It is the one
// place these defaults live, so every command agrees on where things are.
package xdgpaths

import (
	"os"
	"path/filepath"
)

const appName = "agentpulse"

// ConfigDir returns $XDG_CONFIG_HOME/agentpulse, defaulting to
// ~/.config/agentpulse when XDG_CONFIG_HOME is unset (BR-05).
func ConfigDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, appName)
	}
	return filepath.Join(homeDir(), ".config", appName)
}

// ConfigPath returns the path to config.json inside ConfigDir (BR-05).
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.json")
}

// StateDir returns $XDG_STATE_HOME/agentpulse, defaulting to
// ~/.local/state/agentpulse when XDG_STATE_HOME is unset (BR-05).
func StateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, appName)
	}
	return filepath.Join(homeDir(), ".local", "state", appName)
}

// SessionsDir returns the directory holding per-session scratch files
// (BR-05): $XDG_STATE_HOME/agentpulse/sessions.
func SessionsDir() string {
	return filepath.Join(StateDir(), "sessions")
}

// SpoolPath returns the path to the newline-delimited event spool
// (BR-02/BR-03): $XDG_STATE_HOME/agentpulse/spool.ndjson.
func SpoolPath() string {
	return filepath.Join(StateDir(), "spool.ndjson")
}

// StatePath returns the path to state.json inside StateDir: the
// bridge's mutable runtime state (consecutive 401s, last event sent,
// last flush outcome, and so on), as opposed to config.json's operator-set
// configuration.
func StatePath() string {
	return filepath.Join(StateDir(), "state.json")
}

// WatchListPath returns the path to the local watch list (BR-10, BR-11):
// $XDG_STATE_HOME/agentpulse/watchlist.json. It lives with the state
// rather than the configuration because the relay replaces it wholesale
// (BR-11), so it is not a file an operator edits. "agentpulse unpair"
// deletes it (BR-09).
func WatchListPath() string {
	return filepath.Join(StateDir(), "watchlist.json")
}

// CredentialsPath returns the path to the fallback credential file inside
// ConfigDir (BR-06): used only when neither the macOS Keychain nor a
// Linux Secret Service is available.
func CredentialsPath() string {
	return filepath.Join(ConfigDir(), "credentials")
}

// LogPath returns the path to the bridge's rotating diagnostic log
// (BR-04): $XDG_STATE_HOME/agentpulse/bridge.log.
func LogPath() string {
	return filepath.Join(StateDir(), "bridge.log")
}

// homeDir returns the user's home directory, or "." if it cannot be
// determined — a fallback, never an error, since path resolution must
// never be the reason "agentpulse hook" fails to exit 0 (BR-02).
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
