//go:build !dev

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestRunHookNeverRecordsInReleaseBuild proves that even when
// AGENTPULSE_RECORD_DIR is set, a release build (no "dev" tag) never
// writes anything to it — maybeRecord is a no-op, and internal/recorder is
// not even linked into this binary (see hook_release.go). Fixture
// recording writes raw, unscrubbed session data to disk, so it must not be
// reachable outside a development build.
func TestRunHookNeverRecordsInReleaseBuild(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	dir := t.TempDir()
	raw := `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
	var stderr bytes.Buffer

	runHook(strings.NewReader(raw), dir, false, &stderr)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Errorf("release build recorded %d file(s) to %s, want none: %v", len(entries), dir, entries)
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output: %q", stderr.String())
	}
}
