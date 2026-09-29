package classify

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixtureDir is the fixture corpus "make fixtures" replays through the
// built binary; these tests replay it through Classify directly, so every
// emitted event can be schema-validated and compared byte for byte.
const fixtureDir = "../../testdata/fixtures"

// replayFixture classifies every hook document of one scenario in order,
// one SessionState per session_id, and returns each emitted event
// marshaled with its random event_id blanked. prepare may rewrite each
// input before it is classified.
func replayFixture(t *testing.T, dir string, prepare func(*HookInput)) [][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9]-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	states := map[string]*SessionState{}
	var out [][]byte
	for i, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // fixture path from the repo's own testdata
		if err != nil {
			t.Fatal(err)
		}
		in, err := ParseHookInput(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		in.Now = fixedNow.Add(time.Duration(i) * time.Second)
		in.BridgeID = "brg_test0001"
		in.BridgeVersion = "0.0.0-test"
		prepare(&in)
		st, ok := states[in.SessionID]
		if !ok {
			st = NewSessionState()
			states[in.SessionID] = st
		}
		ev, ok := Classify(in, st)
		if !ok {
			continue
		}
		ev.EventID = ""
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return out
}

// agentValues returns every agent_id and transcript path a scenario's
// hook documents carry, and the error text and notification text of
// StopFailure and the usage-limit notifications: none of them may appear
// in any emitted byte (BR-19, card P5-18; ADR-006 section 7). A
// StopFailure's raw error value is included too, unless it is spelled
// like one of the cause codes the event legitimately carries.
func agentValues(t *testing.T, dir string) [][]byte {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9]-*.json"))
	var vals [][]byte
	for _, f := range files {
		raw, _ := os.ReadFile(f) //nolint:gosec // fixture path from the repo's own testdata
		var doc struct {
			HookEventName        string `json:"hook_event_name"`
			AgentID              string `json:"agent_id"`
			TranscriptPath       string `json:"transcript_path"`
			AgentTranscriptPath  string `json:"agent_transcript_path"`
			Error                string `json:"error"`
			ErrorDetails         string `json:"error_details"`
			LastAssistantMessage string `json:"last_assistant_message"`
			NotificationType     string `json:"notification_type"`
			Message              string `json:"message"`
			Title                string `json:"title"`
		}
		_ = json.Unmarshal(raw, &doc)
		candidates := []string{doc.AgentID, doc.TranscriptPath, doc.AgentTranscriptPath}
		if doc.HookEventName == HookStopFailure {
			candidates = append(candidates, doc.ErrorDetails, doc.LastAssistantMessage)
			if !isCauseCode(doc.Error) {
				candidates = append(candidates, doc.Error)
			}
		}
		if strings.HasPrefix(doc.NotificationType, "quota_auto_resume_") {
			candidates = append(candidates, doc.Message, doc.Title)
		}
		for _, v := range candidates {
			if v != "" {
				vals = append(vals, []byte(v))
			}
		}
	}
	return vals
}

func isCauseCode(s string) bool {
	switch s {
	case CauseUsageLimit, CauseLimitReset, CauseBilling, CauseAuth, CauseOverloaded, CauseOutputLimit, CauseAPIError:
		return true
	}
	return false
}

func TestFixtureReplayBothModes(t *testing.T) {
	sch := loadEventSchema(t)
	dirs, err := filepath.Glob(filepath.Join(fixtureDir, "*", "expected.json"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixture scenarios under %s: %v", fixtureDir, err)
	}
	for _, exp := range dirs {
		dir := filepath.Dir(exp)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			on := replayFixture(t, dir, func(in *HookInput) { in.SubagentsOn = true })
			off := replayFixture(t, dir, func(in *HookInput) { in.SubagentsOn = false })
			// Off mode must be exactly what the classifier produced before
			// it knew agent_id and agent_type existed. StopFailure and the
			// usage-limit notifications are newer than that and read
			// agent_id in every mode (ADR-006 section 2: inside a subagent
			// they send nothing), so they keep it here.
			legacy := replayFixture(t, dir, func(in *HookInput) {
				if in.HookEventName == HookStopFailure || strings.HasPrefix(in.NotificationType, "quota_auto_resume_") {
					return
				}
				in.AgentID, in.AgentType = "", ""
			})

			if len(off) != len(legacy) {
				t.Fatalf("off mode emitted %d events, legacy %d", len(off), len(legacy))
			}
			for i := range off {
				if !bytes.Equal(off[i], legacy[i]) {
					t.Errorf("event %d differs with subagents off:\n off:    %s\n legacy: %s", i, off[i], legacy[i])
				}
			}

			secrets := agentValues(t, dir)
			for _, line := range append(on, off...) {
				for _, s := range secrets {
					if bytes.Contains(line, s) {
						t.Errorf("emitted event contains hook value %q: %s", s, line)
					}
				}
				if bytes.Contains(line, []byte("transcript")) {
					t.Errorf("emitted event mentions a transcript: %s", line)
				}
				if sch == nil {
					continue
				}
				var ev Event
				if err := json.Unmarshal(line, &ev); err != nil {
					t.Fatal(err)
				}
				ev.EventID = newULID(fixedNow) // blanked for the byte comparison
				assertValidEvent(t, sch, &ev)
			}
		})
	}
}
