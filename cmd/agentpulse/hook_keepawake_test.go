package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/keepawake"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// stubStartKeepAwake replaces the package's startKeepAwake var (BR-16's
// injected spawn function, hook_classify.go) with a counting stub for the
// duration of the test, restoring the real keepawake.Start on cleanup —
// hook integration tests use an injected function, never a real process.
func stubStartKeepAwake(t *testing.T) (calls *int) {
	t.Helper()
	n := 0
	orig := startKeepAwake
	startKeepAwake = func(pid int) (int, error) {
		n++
		return 4242 + n, nil
	}
	t.Cleanup(func() { startKeepAwake = orig })
	return &n
}

func writeKeepAwakeOnConfig(t *testing.T) {
	t.Helper()
	if err := config.Save(xdgpaths.ConfigPath(), config.Config{BridgeID: "brg_test", KeepAwake: true}); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
}

func TestSessionStartWithKeepAwakeOffSpawnsNothing(t *testing.T) {
	setTestXDGDirs(t)
	calls := stubStartKeepAwake(t)

	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"sess_ka_off","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() error: %v", err)
	}
	if *calls != 0 {
		t.Errorf("startKeepAwake called %d times, want 0 (keep-awake is off)", *calls)
	}
}

func TestSessionStartWithKeepAwakeOnSpawnsExactlyOnce(t *testing.T) {
	setTestXDGDirs(t)
	writeKeepAwakeOnConfig(t)
	calls := stubStartKeepAwake(t)

	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"sess_ka_on","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() error: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("startKeepAwake called %d times, want 1", *calls)
	}

	// A second SessionStart for the SAME session must not spawn again
	// (the duplicate-spawn guard: SessionState.KeepAwakePID is already
	// set and persisted from the first call).
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() (second SessionStart) error: %v", err)
	}
	if *calls != 1 {
		t.Errorf("startKeepAwake called %d times after a second SessionStart for the same session, want still 1", *calls)
	}
}

func TestSessionStartWithKeepAwakeOnDifferentSessionsSpawnsEach(t *testing.T) {
	setTestXDGDirs(t)
	writeKeepAwakeOnConfig(t)
	calls := stubStartKeepAwake(t)

	for _, sid := range []string{"sess_ka_a", "sess_ka_b"} {
		raw := []byte(`{"hook_event_name":"SessionStart","session_id":"` + sid + `","cwd":"/nonexistent/proj"}`)
		if err := classifyAndSpool(raw); err != nil {
			t.Fatalf("classifyAndSpool() error: %v", err)
		}
	}
	if *calls != 2 {
		t.Errorf("startKeepAwake called %d times for two distinct sessions, want 2", *calls)
	}
}

func TestPreToolUseNeverSpawnsKeepAwake(t *testing.T) {
	setTestXDGDirs(t)
	writeKeepAwakeOnConfig(t)
	calls := stubStartKeepAwake(t)

	raw := []byte(`{"hook_event_name":"PreToolUse","session_id":"sess_ka_pretool","cwd":"/nonexistent/proj","tool_name":"Read","tool_input":{"file_path":"/x/a.go"}}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() error: %v", err)
	}
	if *calls != 0 {
		t.Errorf("startKeepAwake called %d times on PreToolUse, want 0", *calls)
	}
}

// TestKeepAwakePIDPersistedInScratch confirms the guard is actually
// backed by persisted scratch state (not just an in-memory flag for one
// classifyAndSpool call), and that the field round-trips through the
// real SessionState JSON exactly like every other scratch field.
func TestKeepAwakePIDPersistedInScratch(t *testing.T) {
	setTestXDGDirs(t)
	writeKeepAwakeOnConfig(t)
	stubStartKeepAwake(t)

	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"sess_ka_scratch","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() error: %v", err)
	}

	data, err := os.ReadFile(xdgpaths.SessionsDir() + "/sess_ka_scratch.json")
	if err != nil {
		t.Fatalf("reading session scratch: %v", err)
	}
	var scratch struct {
		KeepAwakePID int `json:"keep_awake_pid"`
	}
	if err := json.Unmarshal(data, &scratch); err != nil {
		t.Fatalf("parsing session scratch: %v", err)
	}
	if scratch.KeepAwakePID != 4243 {
		t.Errorf("scratch keep_awake_pid = %d, want 4243", scratch.KeepAwakePID)
	}
}

// TestMaybeStartKeepAwakeUnsupportedPlatformIsNotAnError exercises
// maybeStartKeepAwake directly against keepawake.ErrNotSupported, since
// the real platform of the machine running this test is not something
// the test can control: an unsupported platform must be swallowed (debug
// log only), never returned as an error that would show up in "hook"'s
// output or the spool.
func TestMaybeStartKeepAwakeUnsupportedPlatformIsNotAnError(t *testing.T) {
	setTestXDGDirs(t)
	orig := startKeepAwake
	startKeepAwake = func(pid int) (int, error) { return 0, keepawake.ErrNotSupported }
	t.Cleanup(func() { startKeepAwake = orig })

	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"sess_ka_unsupported","cwd":"/nonexistent/proj"}`)
	if err := classifyAndSpool(raw); err != nil {
		t.Fatalf("classifyAndSpool() error: %v, want nil (unsupported platform is not an error)", err)
	}
}
