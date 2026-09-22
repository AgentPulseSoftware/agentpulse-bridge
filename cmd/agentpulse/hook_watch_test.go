package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// projectKeyHashForTest mirrors internal/classify's unexported
// deriveProject for a cwd that is not inside a git work tree (an
// isolated t.TempDir() never is): the BR-13 project key is cwd itself.
func projectKeyHashForTest(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return hex.EncodeToString(sum[:])
}

// hookDoc builds a minimal SessionStart hook document for cwd/sessionID,
// which Classify always turns into exactly one event (SPEC 7.2), so it is
// a reliable probe for what BR-10's watch gating did with it.
func hookDoc(cwd, sessionID string) string {
	return fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":%q,"cwd":%q}`, sessionID, cwd)
}

// seedUnwatchedProject writes a watchlist.json that marks cwd's project
// (by its BR-13 key, an ordinary non-repo directory: the key is cwd
// itself) explicitly unwatched, as if the relay had already told this
// bridge so.
func seedUnwatchedProject(t *testing.T, keyHash string) {
	t.Helper()
	f := watch.File{WatchList: &watch.List{WatchNewProjects: true, Projects: map[string]watch.ProjectEntry{
		keyHash: {Watched: false},
	}}}
	if err := watch.Save(xdgpaths.WatchListPath(), f); err != nil {
		t.Fatalf("seeding watchlist.json: %v", err)
	}
}

// TestHookUnwatchedProjectDropsAndThrottlesProjectSeen is the named
// integration test: an unwatched project's event produces nothing but one
// throttled project_seen, never a second one within six hours.
func TestHookUnwatchedProjectDropsAndThrottlesProjectSeen(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	cwd := t.TempDir()
	keyHash := projectKeyHashForTest(cwd)
	seedUnwatchedProject(t, keyHash)

	var stderr bytes.Buffer
	runHook(strings.NewReader(hookDoc(cwd, "sess-1")), "", false, &stderr)

	got := remainingSpool(t)
	if len(got) != 1 {
		t.Fatalf("spool after first hook call = %v, want exactly one project_seen event", got)
	}
	var ev struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(got[0]), &ev); err != nil {
		t.Fatalf("unmarshaling spooled event: %v", err)
	}
	if ev.Type != "project_seen" {
		t.Errorf("event type = %q, want project_seen", ev.Type)
	}

	// A second hook call for the same session, well inside the six-hour
	// throttle window, must add nothing: remainingSpool restores whatever
	// it drains, so the spool must still hold exactly the one event above
	// and no more.
	runHook(strings.NewReader(hookDoc(cwd, "sess-1")), "", false, &stderr)
	if got2 := remainingSpool(t); len(got2) != 1 || got2[0] != got[0] {
		t.Errorf("spool after second hook call = %v, want unchanged (BR-10's throttle): %v", got2, got)
	}
}

// TestHookWatchedProjectSpoolsNormally is the control case: a project the
// watch list has no opinion on (WatchNewProjects defaults true) spools
// its event exactly as classified, untouched.
func TestHookWatchedProjectSpoolsNormally(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	cwd := t.TempDir()

	var stderr bytes.Buffer
	runHook(strings.NewReader(hookDoc(cwd, "sess-2")), "", false, &stderr)

	got := remainingSpool(t)
	if len(got) != 1 {
		t.Fatalf("spool = %v, want exactly one event", got)
	}
	var ev struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(got[0]), &ev); err != nil {
		t.Fatalf("unmarshaling spooled event: %v", err)
	}
	if ev.Type != "session_start" {
		t.Errorf("event type = %q, want session_start (unmodified)", ev.Type)
	}
}
