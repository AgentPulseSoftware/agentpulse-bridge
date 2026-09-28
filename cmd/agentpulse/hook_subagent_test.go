package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

const (
	testAgentID    = "raw-agent-id-7f3c91"
	testTranscript = "/nonexistent/transcripts/agent-7f3c91.jsonl"
)

func subagentHook(event, extra string) []byte {
	return []byte(`{"hook_event_name":"` + event + `","session_id":"sess_sub_int","cwd":"/nonexistent/proj",` +
		`"transcript_path":"` + testTranscript + `","agent_id":"` + testAgentID + `","agent_type":"reviewer"` + extra + `}`)
}

func spooledEvents(t *testing.T) []classify.Event {
	t.Helper()
	data, err := os.ReadFile(xdgpaths.SpoolPath()) //nolint:gosec // test-controlled temp path
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var evs []classify.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev classify.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("spool line %q: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// TestSubagentHooksNeverStoreRawAgentID is BR-19 for subagents: no file
// the bridge writes (spool, session scratch, locks, state) contains the
// raw agent_id or the transcript path.
func TestSubagentHooksNeverStoreRawAgentID(t *testing.T) {
	setTestXDGDirs(t)
	hooks := [][]byte{
		[]byte(`{"hook_event_name":"SessionStart","session_id":"sess_sub_int","cwd":"/nonexistent/proj"}`),
		subagentHook("SubagentStart", ""),
		subagentHook("PreToolUse", `,"tool_name":"Read","tool_input":{"file_path":"/nonexistent/proj/a.go"}`),
		subagentHook("SubagentStop", `,"agent_transcript_path":"`+testTranscript+`"`),
	}
	for _, raw := range hooks {
		if err := classifyAndSpool(raw); err != nil {
			t.Fatalf("classifyAndSpool: %v", err)
		}
	}

	evs := spooledEvents(t)
	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type)
		if ev.Type != classify.TypeSessionStart && (ev.Subagent == nil || ev.Subagent.ID != classify.SubagentID(testAgentID)) {
			t.Errorf("%s: Subagent = %+v, want the hashed id", ev.Type, ev.Subagent)
		}
	}
	if got := strings.Join(types, ","); got != "session_start,subagent_start,activity,subagent_stop" {
		t.Errorf("spooled types = %s", got)
	}

	for _, root := range []string{os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CONFIG_HOME")} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, _ := os.ReadFile(path) //nolint:gosec // test-controlled temp path
			for _, secret := range []string{testAgentID, "transcripts"} {
				if strings.Contains(string(data), secret) || strings.Contains(path, secret) {
					t.Errorf("%s contains %q", path, secret)
				}
			}
			return nil
		})
	}
}

func TestSubagentHooksWhilePausedAreClassifiedAsBefore(t *testing.T) {
	setTestXDGDirs(t)
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := state.Save(xdgpaths.StatePath(), state.State{SubagentsOffUntil: until}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		subagentHook("SubagentStart", ""),
		subagentHook("PreToolUse", `,"tool_name":"Read"`),
		subagentHook("SubagentStop", ""),
	} {
		if err := classifyAndSpool(raw); err != nil {
			t.Fatalf("classifyAndSpool: %v", err)
		}
	}
	evs := spooledEvents(t)
	if len(evs) != 1 || evs[0].Type != classify.TypeActivity || evs[0].Subagent != nil {
		t.Errorf("spooled %+v, want one activity without subagent", evs)
	}
}

func TestSubagentEventForUnwatchedProjectSendsNoProjectSeen(t *testing.T) {
	setTestXDGDirs(t)
	if err := watch.Save(xdgpaths.WatchListPath(), watch.File{WatchList: &watch.List{Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := classifyAndSpool(subagentHook("PreToolUse", `,"tool_name":"Read"`)); err != nil {
		t.Fatal(err)
	}
	if evs := spooledEvents(t); len(evs) != 0 {
		t.Fatalf("spooled %+v for a subagent in an unwatched project, want nothing", evs)
	}
	// The throttle was not spent: the main chain's next event still
	// produces the project's project_seen.
	if err := classifyAndSpool([]byte(`{"hook_event_name":"Stop","session_id":"sess_sub_int","cwd":"/nonexistent/proj"}`)); err != nil {
		t.Fatal(err)
	}
	evs := spooledEvents(t)
	if len(evs) != 1 || evs[0].Type != classify.TypeProjectSeen || evs[0].Subagent != nil {
		t.Errorf("spooled %+v, want one project_seen without subagent", evs)
	}
}
