// Package recorder implements the bridge's fixture-recording path (SPEC
// 9.4, 18): when AGENTPULSE_RECORD_DIR is set, "agentpulse hook" writes the
// raw hook JSON it received to disk verbatim, so it can later be scrubbed
// into a fixture. It does no classification and no spooling; recording is
// deliberately independent of classifier behavior.
package recorder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var counterPrefix = regexp.MustCompile(`^(\d+)-`)

// Write appends one raw Claude Code hook JSON document to dir, naming it
// "<NNN>-<hook_event_name>.json" where NNN is a zero-padded counter one
// higher than the highest counter already present in dir (or 1 if dir is
// empty or new). raw is written byte-for-byte, exactly as received on
// stdin. It returns the path written.
func Write(dir string, raw []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	next, err := nextCounter(dir)
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%03d-%s.json", next, eventName(raw))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// nextCounter scans dir for files named "<N>-....json" and returns one more
// than the highest N found, or 1 if none match.
func nextCounter(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := counterPrefix.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest + 1, nil
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// eventName best-effort extracts hook_event_name from a raw hook JSON
// document to use in the recorded filename. It never errors: malformed
// input, or a missing field, just becomes "unknown", since a naming
// convenience must never be the reason "agentpulse hook" fails to exit 0.
func eventName(raw []byte) string {
	var doc struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.HookEventName == "" {
		return "unknown"
	}
	return unsafeFilenameChars.ReplaceAllString(doc.HookEventName, "_")
}
