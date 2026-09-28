package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// markedEvent is event(eventType) inside a subagent.
func markedEvent(eventType string) string {
	return fmt.Sprintf(`{"schema":1,"event_id":"01J8TESTEVENT%s","type":%q,"subagent":{"id":"0123456789abcdef","type":"reviewer"}}`, eventType, eventType)
}

// oldRelay behaves like a relay that does not know the `subagent` field:
// it answers 400 naming the first event that carries it, in the relay's
// own wording for an unknown field (relay describeIssue), and otherwise
// accepts the batch, recording every accepted event.
type oldRelay struct {
	mu       sync.Mutex
	requests int
	accepted []string
}

func (o *oldRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var batch struct {
		Events []json.RawMessage `json:"events"`
	}
	_ = json.NewDecoder(r.Body).Decode(&batch)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests++
	for i, raw := range batch.Events {
		if bytes.Contains(raw, []byte(`"subagent"`)) {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":  "invalid_body",
				"detail": fmt.Sprintf("events.%d: unexpected field subagent", i),
			})
			return
		}
	}
	for _, raw := range batch.Events {
		o.accepted = append(o.accepted, string(raw))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(batch.Events)})
}

func acceptedTypes(accepted []string) string {
	var types []string
	for _, raw := range accepted {
		var e struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(raw), &e)
		types = append(types, e.Type)
	}
	return strings.Join(types, ",")
}

func TestRunFlushSubagentRejectedIsResentWithoutItAndMarkingPauses(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"), markedEvent("activity"), markedEvent("subagent_start"), markedEvent("commit"), markedEvent("subagent_stop"))

	relay := &oldRelay{}
	srv := httptest.NewServer(relay)
	defer srv.Close()

	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	deps := testFlushDeps(srv, time.Second)
	deps.now = func() time.Time { return now }
	runFlush(deps)

	if got := remainingSpool(t); len(got) != 0 {
		t.Errorf("remaining spool = %v, want empty", got)
	}
	if got, want := acceptedTypes(relay.accepted), "stop,activity,commit"; got != want {
		t.Errorf("accepted = %s, want %s (start and stop dropped, the rest sent once without subagent)", got, want)
	}
	if relay.requests != 2 {
		t.Errorf("requests = %d, want 2 (one rejected, one resend)", relay.requests)
	}
	fstate := state.Load(xdgpaths.StatePath())
	if want := now.Add(subagentFallbackPeriod).Format(time.RFC3339); fstate.SubagentsOffUntil != want {
		t.Errorf("SubagentsOffUntil = %q, want %q", fstate.SubagentsOffUntil, want)
	}
	if subagentsPaused(fstate, now.Add(subagentFallbackPeriod-time.Second)) != true ||
		subagentsPaused(fstate, now.Add(subagentFallbackPeriod)) != false {
		t.Error("pause does not cover exactly the fallback period")
	}
	if logged := deps.stderr.(*bytes.Buffer).String(); strings.Contains(logged, "reviewer") {
		t.Errorf("debug output names the subagent type:\n%s", logged)
	}
}

func TestRunFlushWhilePausedStripsSpooledSubagentFields(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		until    time.Time
		requests int
	}{
		{"pause running", now.Add(time.Minute), 1},
		{"pause over", now.Add(-time.Minute), 2}, // the old relay rejects once, and the pause starts again
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestXDGDirs(t)
			withFastBackoff(t)
			pairedConfig(t, "")
			if err := state.Save(xdgpaths.StatePath(), state.State{SubagentsOffUntil: tt.until.Format(time.RFC3339)}); err != nil {
				t.Fatal(err)
			}
			seedSpool(t, markedEvent("subagent_start"), markedEvent("activity"))

			relay := &oldRelay{}
			srv := httptest.NewServer(relay)
			defer srv.Close()
			deps := testFlushDeps(srv, time.Second)
			deps.now = func() time.Time { return now }
			runFlush(deps)

			if got := acceptedTypes(relay.accepted); got != "activity" {
				t.Errorf("accepted = %s, want activity", got)
			}
			if relay.requests != tt.requests {
				t.Errorf("requests = %d, want %d", relay.requests, tt.requests)
			}
		})
	}
}

func TestStripSubagentsKeepsOtherLinesByteForByte(t *testing.T) {
	plain := []byte(event("stop"))
	garbled := []byte(`{"subagent": not json`)
	got := stripSubagents([][]byte{plain, []byte(markedEvent("subagent_stop")), garbled, []byte(markedEvent("commit"))})
	if len(got) != 3 || !bytes.Equal(got[0], plain) || !bytes.Equal(got[1], garbled) {
		t.Fatalf("stripSubagents = %q", got)
	}
	if bytes.Contains(got[2], []byte("subagent")) || !bytes.Contains(got[2], []byte(`"type":"commit"`)) {
		t.Errorf("stripped commit = %s", got[2])
	}
}
