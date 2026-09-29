package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// flushNow is the wall clock the tests in this file hand runFlush.
var flushNow = time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)

// backlogEvent is one full SPEC 10.1 event, as "agentpulse hook" spools it.
func backlogEvent(n int, ts time.Time, session, typ, payload string) string {
	return fmt.Sprintf(`{"schema":1,"event_id":"01J8BACKLOG%08d","bridge_id":"brg_test","session_id":%q,"project":{"key_hash":"%064d","name":"project"},"ts":%q,"type":%q,"counters":{"files_read":%d,"files_edited":%d,"verification_runs":%d,"commits":0},"payload":%s}`,
		n, session, 0, ts.Format(time.RFC3339Nano), typ, n, n/2, n/4, payload)
}

// fiveHundredEventBacklog is five hours of six sessions working in a loop
// of prompt, read, edit, test (failing, then passing) and stop, spooled
// while the relay could not be reached. The last session ends paused.
func fiveHundredEventBacklog() []string {
	loop := []struct{ typ, payload string }{
		{"prompt_submitted", `{}`},
		{"activity", `{"category":"read"}`},
		{"activity", `{"category":"edit"}`},
		{"verification_started", `{"kind":"test","runner":"go"}`},
		{"verification_finished", `{"kind":"test","runner":"go","outcome":"fail","passed":9,"failed":1,"total":10}`},
		{"activity", `{"category":"edit"}`},
		{"verification_started", `{"kind":"test","runner":"go"}`},
		{"verification_finished", `{"kind":"test","runner":"go","outcome":"pass","passed":10,"failed":0,"total":10}`},
		{"stop", `{}`},
	}
	const sessions = 6
	start := flushNow.Add(-5 * time.Hour)
	var out []string
	step := make([]int, sessions)
	ts := func(n int) time.Time { return start.Add(time.Duration(n) * 30 * time.Second) }
	for n := 0; len(out) < spool.Cap-1; n++ {
		k := n % sessions
		id := fmt.Sprintf("sess-%d", k)
		if step[k] == 0 {
			out = append(out, backlogEvent(n, ts(n), id, "session_start", `{"agent":"claude-code","bridge_version":"1.0.0","os":"darwin"}`))
		} else {
			l := loop[(step[k]-1)%len(loop)]
			out = append(out, backlogEvent(n, ts(n), id, l.typ, l.payload))
		}
		step[k]++
	}
	n := len(out)
	return append(out, backlogEvent(n, ts(n), fmt.Sprintf("sess-%d", sessions-1), "paused", `{"cause":"usage_limit"}`))
}

// recordingRelay is a fake relay events endpoint that accepts every batch
// and records what it received, one slice per request.
type recordingRelay struct {
	mu       sync.Mutex
	requests [][]json.RawMessage
}

func (r *recordingRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Events []json.RawMessage `json:"events"`
	}
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.requests = append(r.requests, body.Events)
	r.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(body.Events)})
}

// TestRunFlushSendsA500EventBacklogInTwoRequests is card P5-26's flush
// test: a full spool reaches the relay in at most two requests, keeps
// spool order, and includes every session's latest event and the
// trailing pause. That the relay ends in the same state as after a full
// replay is shown in internal/coalesce's tests.
func TestRunFlushSendsA500EventBacklogInTwoRequests(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	backlog := fiveHundredEventBacklog()
	seedSpool(t, backlog...)

	rr := &recordingRelay{}
	srv := httptest.NewServer(rr)
	defer srv.Close()

	deps := testFlushDeps(srv, time.Second)
	deps.now = func() time.Time { return flushNow }
	runFlush(deps)

	if got := remainingSpool(t); len(got) != 0 {
		t.Fatalf("%d events left in the spool, want none", len(got))
	}
	if len(rr.requests) == 0 || len(rr.requests) > 2 {
		t.Fatalf("backlog sent in %d requests, want one or two", len(rr.requests))
	}
	var sent []string
	for _, req := range rr.requests {
		if len(req) > relay.MaxEventsPerChunk {
			t.Errorf("a request carried %d events, over the batch limit", len(req))
		}
		for _, e := range req {
			sent = append(sent, string(e))
		}
	}

	pos := map[string]int{}
	for i, l := range backlog {
		pos[l] = i
	}
	last := map[string]string{}
	for _, l := range backlog {
		var e struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal([]byte(l), &e)
		last[e.SessionID] = l
	}
	prev := -1
	for _, s := range sent {
		i, ok := pos[s]
		if !ok {
			t.Fatalf("sent an event that was not in the spool: %s", s)
		}
		if i <= prev {
			t.Errorf("events sent out of spool order")
		}
		prev = i
	}
	for id, l := range last {
		if !containsString(sent, l) {
			t.Errorf("session %s: its latest event was not sent", id)
		}
	}
	if !containsString(sent, backlog[len(backlog)-1]) {
		t.Error("the trailing paused event was not sent")
	}
	t.Logf("500 spooled events sent as %d in %d requests", len(sent), len(rr.requests))
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// countingRelay answers every request with a 429 carrying retryAfter (no
// header when empty) and counts the requests.
func countingRelay(retryAfter string) (*httptest.Server, *int) {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	return srv, &n
}

// TestRunFlushHonoursRetryAfterAcrossRuns checks ERR-05 across flush runs:
// every hook starts a flush, and none of them may contact the relay again
// until the Retry-After has passed.
func TestRunFlushHonoursRetryAfterAcrossRuns(t *testing.T) {
	setTestXDGDirs(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))
	srv, requests := countingRelay("60")
	defer srv.Close()

	run := func(at time.Time) {
		deps := testFlushDeps(srv, time.Second)
		deps.now = func() time.Time { return at }
		runFlush(deps)
	}

	run(flushNow)
	if *requests != 1 {
		t.Fatalf("first run made %d requests, want 1 (60 s does not fit a 1 s budget)", *requests)
	}
	st := state.Load(xdgpaths.StatePath())
	if want := flushNow.Add(60 * time.Second).Format(time.RFC3339); st.RateLimitedUntil != want {
		t.Errorf("RateLimitedUntil = %q, want %q", st.RateLimitedUntil, want)
	}

	for _, after := range []time.Duration{time.Second, 30 * time.Second, 59 * time.Second} {
		run(flushNow.Add(after))
	}
	if *requests != 1 {
		t.Errorf("runs inside the Retry-After made %d more requests, want none", *requests-1)
	}
	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("spool = %v, want the event kept", got)
	}

	run(flushNow.Add(61 * time.Second))
	if *requests != 2 {
		t.Errorf("a run after the Retry-After made %d requests, want 1", *requests-1)
	}
}

// TestRunFlushRateLimitNeverHotLoops checks the bounds on Retry-After: a
// zero is waited out as one second, so a one-second budget sends once;
// a missing header waits the fallback; an absurd one is capped.
func TestRunFlushRateLimitNeverHotLoops(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		wantWait   time.Duration
	}{
		{"zero", "0", minRateLimitWait},
		{"missing", "", rateLimitFallbackWait},
		{"unparseable", "soon", rateLimitFallbackWait},
		{"a day", "86400", maxRateLimitWait},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setTestXDGDirs(t)
			pairedConfig(t, "")
			seedSpool(t, event("stop"))
			srv, requests := countingRelay(tt.retryAfter)
			defer srv.Close()

			deps := testFlushDeps(srv, time.Second)
			deps.now = func() time.Time { return flushNow }
			runFlush(deps)

			if *requests != 1 {
				t.Errorf("made %d requests, want 1", *requests)
			}
			st := state.Load(xdgpaths.StatePath())
			if want := flushNow.Add(tt.wantWait).Format(time.RFC3339); st.RateLimitedUntil != want {
				t.Errorf("RateLimitedUntil = %q, want %q", st.RateLimitedUntil, want)
			}
			if st.LastFlush == nil || st.LastFlush.Outcome != state.OutcomeRateLimited {
				t.Errorf("LastFlush = %+v, want outcome %q", st.LastFlush, state.OutcomeRateLimited)
			}
		})
	}
}

// TestRunFlushWhileRateLimitedStillCompactsTheSpool checks that a run
// held back by a Retry-After still coalesces the backlog in place, so the
// spool does not reach its 500-event cap and evict a session's latest
// state while it waits.
func TestRunFlushWhileRateLimitedStillCompactsTheSpool(t *testing.T) {
	setTestXDGDirs(t)
	pairedConfig(t, "")
	backlog := fiveHundredEventBacklog()
	seedSpool(t, backlog...)
	st := state.Load(xdgpaths.StatePath())
	st.RateLimitedUntil = flushNow.Add(time.Minute).Format(time.RFC3339)
	if err := state.Save(xdgpaths.StatePath(), st); err != nil {
		t.Fatal(err)
	}
	srv, requests := countingRelay("60")
	defer srv.Close()

	deps := testFlushDeps(srv, time.Second)
	deps.now = func() time.Time { return flushNow }
	runFlush(deps)

	if *requests != 0 {
		t.Errorf("made %d requests while rate limited, want none", *requests)
	}
	left := remainingSpool(t)
	if len(left) == 0 || len(left) > 2*relay.MaxEventsPerChunk {
		t.Errorf("spool holds %d events, want the backlog coalesced to at most two batches", len(left))
	}
	if left[len(left)-1] != backlog[len(backlog)-1] {
		t.Error("the trailing paused event is not the spool's last")
	}
}

func TestRateLimited(t *testing.T) {
	tests := []struct {
		name  string
		until string
		want  bool
	}{
		{"never limited", "", false},
		{"still waiting", flushNow.Add(time.Minute).Format(time.RFC3339), true},
		{"wait over", flushNow.Add(-time.Second).Format(time.RFC3339), false},
		{"unparseable", "tomorrow", false},
		{"further ahead than any wait (clock set back)", flushNow.Add(maxRateLimitWait + time.Minute).Format(time.RFC3339), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rateLimited(state.State{RateLimitedUntil: tt.until}, flushNow); got != tt.want {
				t.Errorf("rateLimited = %v, want %v", got, tt.want)
			}
		})
	}
}
