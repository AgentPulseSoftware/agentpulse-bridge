//go:build dev

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHookRecordsWhenRecordDirIsSet(t *testing.T) {
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
	if len(entries) != 1 {
		t.Fatalf("got %d recorded files, want 1", len(entries))
	}
	if entries[0].Name() != "001-PreToolUse.json" {
		t.Errorf("recorded filename = %q, want 001-PreToolUse.json", entries[0].Name())
	}
	got, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading recorded file: %v", err)
	}
	if string(got) != raw {
		t.Errorf("recorded content = %q, want exactly %q", got, raw)
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output without AGENTPULSE_DEBUG: %q", stderr.String())
	}
}

func TestRunHookNeverPanicsOnMalformedInput(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	dir := t.TempDir()
	var stderr bytes.Buffer
	// Malformed JSON must still be recorded verbatim (it's the raw hook
	// input), and must never cause the hook to fail (ERR-01).
	runHook(strings.NewReader("not json at all"), dir, false, &stderr)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != "001-unknown.json" {
		t.Fatalf("entries = %v, want a single 001-unknown.json", entries)
	}
}

func TestRunHookIsSilentUnlessDebugEnabled(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	// Point the record dir at a path that cannot be written to (a file, not
	// a directory), forcing an internal error, and confirm nothing is
	// printed unless debug is on.
	base := t.TempDir()
	notADir := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding file: %v", err)
	}
	blockedDir := filepath.Join(notADir, "sub")

	var quiet bytes.Buffer
	runHook(strings.NewReader(`{"hook_event_name":"Stop"}`), blockedDir, false, &quiet)
	if quiet.Len() != 0 {
		t.Errorf("expected silence without debug, got %q", quiet.String())
	}

	var loud bytes.Buffer
	runHook(strings.NewReader(`{"hook_event_name":"Stop"}`), blockedDir, true, &loud)
	if loud.Len() == 0 {
		t.Errorf("expected diagnostic output with AGENTPULSE_DEBUG=1, got nothing")
	}
}
