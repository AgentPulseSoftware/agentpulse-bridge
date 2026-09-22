package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

func TestClassifyAndSpoolAppendsOneLineToSpool(t *testing.T) {
	setTestXDGDirs(t)

	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"sess_int_1","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() returned error: %v", err)
	}

	data, err := os.ReadFile(xdgpaths.SpoolPath()) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d spool lines, want 1: %q", len(lines), string(data))
	}
	var ev classify.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("spool line is not valid JSON: %v", err)
	}
	if ev.Type != classify.TypeSessionStart {
		t.Errorf("Type = %q, want %q", ev.Type, classify.TypeSessionStart)
	}
	if ev.SessionID != "sess_int_1" {
		t.Errorf("SessionID = %q, want sess_int_1", ev.SessionID)
	}
}

func TestClassifyAndSpoolDoesNothingWithoutSessionID(t *testing.T) {
	setTestXDGDirs(t)

	raw := []byte(`{"hook_event_name":"Stop"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() returned error: %v", err)
	}
	if _, err := os.Stat(xdgpaths.SpoolPath()); !os.IsNotExist(err) {
		t.Errorf("expected no spool file to be created, stat err = %v", err)
	}
}

func TestClassifyAndSpoolReturnsErrorForMalformedJSON(t *testing.T) {
	setTestXDGDirs(t)
	if err := classifyAndSpool([]byte("not json")); err == nil {
		t.Error("expected an error for malformed hook JSON")
	}
}

// TestClassifyAndSpoolReturnsErrorWhenSpoolDirUnwritable is ERR-02's
// "hook" half: a spool directory hook cannot write to (disk full,
// permissions — here, a scratch state dir chmodded 0500) must be reported
// through the returned error, exactly like any other classifyAndSpool
// failure, so runHook's caller (only "agentpulse doctor" and "status"
// ever surface it) treats this no differently than the malformed-JSON
// case above: no panic, no non-zero exit anywhere upstream.
func TestClassifyAndSpoolReturnsErrorWhenSpoolDirUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	setTestXDGDirs(t)

	stateDir := xdgpaths.StateDir()
	if err := os.MkdirAll(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	raw := []byte(`{"hook_event_name":"Stop","session_id":"sess_unwritable","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err == nil {
		t.Error("expected an error when the spool directory is unwritable")
	}
	if _, err := os.Stat(xdgpaths.SpoolPath()); !os.IsNotExist(err) {
		t.Errorf("expected no spool file to have been created, stat err = %v", err)
	}
}

// TestClassifyAndSpoolFullSessionLifecycle drives a realistic sequence of
// hook calls for one session through the real "hook" command end to end
// (SessionStart, a prompt, a read, a verification, a stop, then
// SessionEnd) and checks: every line landed in the spool, scratch state
// persisted counters across calls, and SessionEnd deleted the scratch file
// (BR-05).
func TestClassifyAndSpoolFullSessionLifecycle(t *testing.T) {
	setTestXDGDirs(t)

	steps := []string{
		`{"hook_event_name":"SessionStart","session_id":"sess_lifecycle","cwd":"/nonexistent/proj"}`,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","prompt":"fix the bug"}`,
		`{"hook_event_name":"PreToolUse","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","tool_name":"Read","tool_input":{"file_path":"/x/a.go"}}`,
		`{"hook_event_name":"PostToolUse","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","tool_name":"Read"}`,
		`{"hook_event_name":"PreToolUse","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","tool_name":"Bash","tool_input":{"command":"pytest -q"}}`,
		`{"hook_event_name":"PostToolUse","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","tool_name":"Bash","tool_response":{"stdout":"3 passed in 0.1s","stderr":""}}`,
		`{"hook_event_name":"Stop","session_id":"sess_lifecycle","cwd":"/nonexistent/proj"}`,
		`{"hook_event_name":"SessionEnd","session_id":"sess_lifecycle","cwd":"/nonexistent/proj","reason":"clear"}`,
	}

	for i, step := range steps {
		if err := classifyAndSpool([]byte(step)); err != nil {
			t.Fatalf("step %d: classifyAndSpool() returned error: %v", i, err)
		}
	}

	data, err := os.ReadFile(xdgpaths.SpoolPath()) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var types []string
	for scanner.Scan() {
		var ev classify.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("spool line is not valid JSON: %v (%q)", err, scanner.Text())
		}
		types = append(types, ev.Type)
	}
	want := []string{
		classify.TypeSessionStart,
		classify.TypePromptSubmitted,
		classify.TypeActivity, // read
		classify.TypeVerificationStarted,
		classify.TypeVerificationFinished,
		classify.TypeStop,
		classify.TypeSessionEnd,
	}
	if len(types) != len(want) {
		t.Fatalf("got %d spooled events %v, want %d %v", len(types), types, len(want), want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d type = %q, want %q", i, types[i], want[i])
		}
	}

	// Scratch must be gone after SessionEnd (BR-05).
	entries, err := os.ReadDir(xdgpaths.SessionsDir())
	if err != nil {
		t.Fatalf("reading sessions dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no scratch files left after SessionEnd, got %v", entries)
	}
}
