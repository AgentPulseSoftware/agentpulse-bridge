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
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
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

// TestClassifyAndSpoolTaskLabelFollowsWatchlistSettings is P5-23's proof
// that "agentpulse hook" reads BR-17's task-label opt-in from the same
// file "agentpulse flush" writes it to (watchlist.json's
// Settings.TaskLabel, applied from a batch response exactly as flush.go
// does): off by default, the label appears once the setting is turned
// on, bounded to 80 characters and first line only, and disappears again
// once it is turned off — all without a real home directory.
func TestClassifyAndSpoolTaskLabelFollowsWatchlistSettings(t *testing.T) {
	setTestXDGDirs(t)

	longFirstLine := strings.Repeat("word ", 30) + "tail" // > 80 runes once collapsed
	prompt := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess_label","cwd":"/nonexistent/proj","prompt":"` +
		longFirstLine + `\nsecond line never sent"}`)
	stop := []byte(`{"hook_event_name":"Stop","session_id":"sess_label","cwd":"/nonexistent/proj"}`)

	// Off by default: no watchlist.json has been written yet.
	if err := classifyAndSpool(prompt); err != nil {
		t.Fatalf("classifyAndSpool() (off) returned error: %v", err)
	}
	if label, ok := lastPromptTaskLabel(t); ok {
		t.Errorf("task_label = %q with the setting off, want no field", label)
	}

	// The relay turns it on: this writes exactly what flush.go writes
	// after a batch response names Settings (BR-17, P5-16) — the write
	// path and the read path now share this one file.
	setWatchlistTaskLabel(t, true)
	if err := classifyAndSpool(stop); err != nil { // re-arms BR-17's per-prompt gate
		t.Fatalf("classifyAndSpool() (stop) returned error: %v", err)
	}
	if err := classifyAndSpool(prompt); err != nil {
		t.Fatalf("classifyAndSpool() (on) returned error: %v", err)
	}
	label, ok := lastPromptTaskLabel(t)
	if !ok {
		t.Fatal("task_label missing with the setting on")
	}
	if strings.Contains(label, "second line") {
		t.Errorf("task_label = %q, want only the first line", label)
	}
	if r := len([]rune(label)); r > 80 {
		t.Errorf("task_label is %d runes, want at most 80", r)
	}

	// The relay turns it off again.
	setWatchlistTaskLabel(t, false)
	if err := classifyAndSpool(stop); err != nil {
		t.Fatalf("classifyAndSpool() (stop) returned error: %v", err)
	}
	if err := classifyAndSpool(prompt); err != nil {
		t.Fatalf("classifyAndSpool() (off again) returned error: %v", err)
	}
	if label, ok := lastPromptTaskLabel(t); ok {
		t.Errorf("task_label = %q with the setting off again, want no field", label)
	}
}

// setWatchlistTaskLabel writes watchlist.json's Settings.TaskLabel
// exactly as "agentpulse flush" would after a batch response named it
// (flush.go), without touching the relay or any real home directory.
func setWatchlistTaskLabel(t *testing.T, on bool) {
	t.Helper()
	path := xdgpaths.WatchListPath()
	f := watch.Load(path)
	f.Settings = &relay.Settings{TaskLabel: on}
	if err := watch.Save(path, f); err != nil {
		t.Fatalf("watch.Save() error: %v", err)
	}
}

// lastPromptTaskLabel returns the task_label of the most recently
// spooled prompt_submitted event, and whether the field was present at
// all (BR-17: omitted, not empty-stringed, when off).
func lastPromptTaskLabel(t *testing.T) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(xdgpaths.SpoolPath()) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading spool: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var lastPrompt json.RawMessage
	for scanner.Scan() {
		var ev classify.Event
		line := append([]byte(nil), scanner.Bytes()...)
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("spool line is not valid JSON: %v (%q)", err, string(line))
		}
		if ev.Type == classify.TypePromptSubmitted {
			lastPrompt = line
		}
	}
	if lastPrompt == nil {
		t.Fatal("no prompt_submitted event found in the spool")
	}
	var payload struct {
		Payload struct {
			TaskLabel *string `json:"task_label"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(lastPrompt, &payload); err != nil {
		t.Fatalf("re-parsing spooled prompt_submitted event: %v", err)
	}
	if payload.Payload.TaskLabel == nil {
		return "", false
	}
	return *payload.Payload.TaskLabel, true
}
