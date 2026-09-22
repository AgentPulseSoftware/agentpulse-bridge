package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// fakeCredStore is a minimal in-memory cred.Store, used so no test in this
// file ever touches a real credential backend (the macOS Keychain, a
// Linux Secret Service). cred_test.go defines an equivalent for
// internal/cred's own tests; Go test files aren't importable across
// packages, so each package keeps its own copy.
type fakeCredStore struct {
	secret string
	has    bool
}

func (f *fakeCredStore) Name() string { return "fake" }

func (f *fakeCredStore) Get() (string, error) {
	if !f.has {
		return "", cred.ErrNotFound
	}
	return f.secret, nil
}

func (f *fakeCredStore) Set(secret string) error {
	f.secret = secret
	f.has = true
	return nil
}

func (f *fakeCredStore) Delete() error {
	f.secret = ""
	f.has = false
	return nil
}

// This file is a table test per SPEC section 14 (ERR-03..ERR-06) row
// against a fake relay: it exercises runFlush directly (the same pattern
// hook_test.go uses for runHook), so no binary needs to be exec'd and no
// real 5-second budget need be waited out.

// pairedConfig writes a config.json that looks paired: a real bridge_id,
// so runFlush gets past its "not paired" early return. pairedAt is passed
// through verbatim (RFC 3339, or "" for "paired a long time ago" /
// never). The credential itself lives in the fakeCredStore
// testFlushDeps injects, never in config.json (the secret moved to
// internal/cred).
func pairedConfig(t *testing.T, pairedAt string) {
	t.Helper()
	cfg := config.Config{BridgeID: "brg_test", PairedAt: pairedAt}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling test config: %v", err)
	}
	if err := os.MkdirAll(xdgpaths.ConfigDir(), 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	if err := os.WriteFile(xdgpaths.ConfigPath(), data, 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
}

// seedSpool appends each of events, in order, to the real spool file, the
// same way "agentpulse hook" would.
func seedSpool(t *testing.T, events ...string) {
	t.Helper()
	for _, e := range events {
		if err := spool.Append(xdgpaths.SpoolPath(), []byte(e)); err != nil {
			t.Fatalf("seeding spool: %v", err)
		}
	}
}

// remainingSpool drains the spool (putting everything found straight back,
// so the file is unchanged) and returns its lines as strings, for
// asserting what runFlush left behind.
func remainingSpool(t *testing.T) []string {
	t.Helper()
	batch, discarded, commit, err := spool.Drain(xdgpaths.SpoolPath())
	if err != nil {
		t.Fatalf("draining spool for assertion: %v", err)
	}
	if discarded != 0 {
		t.Fatalf("remainingSpool: %d unexpected malformed line(s)", discarded)
	}
	if err := commit(batch); err != nil {
		t.Fatalf("restoring spool after assertion drain: %v", err)
	}
	out := make([]string, len(batch))
	for i, b := range batch {
		out[i] = string(b)
	}
	return out
}

// event builds one minimal test event whose "type" field is distinctive
// enough to recognize in assertions.
func event(eventType string) string {
	return fmt.Sprintf(`{"schema":1,"event_id":"01J8TESTEVENT%s","type":%q}`, eventType, eventType)
}

// testFlushDeps returns flushDeps pointed at srv, with a short budget and
// a fast backoff schedule so a test exercising every retry step still
// runs in milliseconds, not the real 1s/2s/4s. credStore is a fake,
// pre-loaded with a secret (matching pairedConfig's real bridge_id), so
// no test in this file ever touches a real credential backend.
func testFlushDeps(srv *httptest.Server, budget time.Duration) flushDeps {
	var stderr bytes.Buffer
	return flushDeps{
		relayFlag: srv.URL,
		budget:    budget,
		debug:     true,
		stderr:    &stderr,
		credStore: &fakeCredStore{secret: "s3cr3t", has: true},
	}
}

// withFastBackoff shrinks the package-level retry backoff for the
// duration of one test, restoring it on cleanup, so a test that drives
// every retry step doesn't need seconds of real wall-clock time.
func withFastBackoff(t *testing.T) {
	t.Helper()
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })
}

func TestRunFlushSuccessDrainsSpool(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"), event("commit"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 2, "duplicates": 0})
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if got := remainingSpool(t); len(got) != 0 {
		t.Errorf("remaining spool = %v, want empty after a 200", got)
	}
}

func TestRunFlushBadRequestDropsNamedEventKeepsRest(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"), event("bogus"), event("commit"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			Events []json.RawMessage `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&batch)
		for i, raw := range batch.Events {
			var e struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &e)
			if e.Type == "bogus" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":  "invalid_body",
					"detail": fmt.Sprintf("events.%d.type: invalid enum value", i),
				})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(batch.Events)})
	}))
	defer srv.Close()

	deps := testFlushDeps(srv, time.Second)
	runFlush(deps)

	got := remainingSpool(t)
	if len(got) != 0 {
		t.Errorf("remaining spool = %v, want empty (bad event dropped, rest delivered on retry)", got)
	}
	// ERR-03: "log with event type only" — the dropped event's type
	// belongs in the log, nothing else about it does.
	logged := deps.stderr.(*bytes.Buffer).String()
	if !strings.Contains(logged, `"bogus"`) {
		t.Errorf("debug log does not name the dropped event's type:\n%s", logged)
	}
}

func TestRunFlushBadRequestWithoutIdentifiableEventLeavesBatchSpooled(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"), event("commit"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_json"})
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if got := remainingSpool(t); len(got) != 2 {
		t.Errorf("remaining spool = %v, want both events still spooled (no event identifiable)", got)
	}
}

func TestRunFlushUnauthorizedOutsideGraceIncrementsCounterAndUnpairsAtThree(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	// Paired "long ago": well outside the 60s grace window.
	pairedConfig(t, time.Now().Add(-time.Hour).Format(time.RFC3339))
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	// One fake store shared across every iteration below (rather than a
	// fresh one from testFlushDeps each time), so ERR-04's "clear the
	// credential at the 3-strike point" is observable across runs, the
	// same way the real cred.Store would persist across real "agentpulse
	// flush" processes.
	store := &fakeCredStore{secret: "s3cr3t", has: true}

	for i := 1; i <= 3; i++ {
		deps := testFlushDeps(srv, time.Second)
		deps.credStore = store
		runFlush(deps)
		st := state.Load(xdgpaths.StatePath())
		if i < 3 {
			if st.Consecutive401 != i {
				t.Fatalf("after run %d: Consecutive401 = %d, want %d", i, st.Consecutive401, i)
			}
			if st.UnpairedReason != "" {
				t.Fatalf("after run %d: UnpairedReason = %q, want empty before the 3rd 401", i, st.UnpairedReason)
			}
		} else {
			if st.UnpairedReason == "" {
				t.Fatalf("after run %d: UnpairedReason empty, want it set at the 3-strike point", i)
			}
			if store.has {
				t.Errorf("credential store still has a secret, want it cleared at the 3-strike point")
			}
		}
		if got := remainingSpool(t); len(got) != 1 {
			t.Errorf("after run %d: remaining spool = %v, want the event still spooled (401 never delivers)", i, got)
		}
	}
}

// TestRunFlushTwoUnauthorizedThenSuccessDoesNotUnpair is ERR-04's other
// half: "any successful request resets the counter to 0" must mean
// exactly that, not "only a run with zero 401s leaves the credential
// alone". Two 401s (outside the pairing grace, so each counts) followed
// by a run that succeeds must leave both Consecutive401 and
// UnpairedReason exactly as if nothing had happened, and must never touch
// the credential store — only a *third consecutive* 401 does that.
func TestRunFlushTwoUnauthorizedThenSuccessDoesNotUnpair(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, time.Now().Add(-time.Hour).Format(time.RFC3339))
	seedSpool(t, event("stop"))

	var attempt int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt <= 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 1})
	}))
	defer srv.Close()

	store := &fakeCredStore{secret: "s3cr3t", has: true}

	for i := 1; i <= 3; i++ {
		deps := testFlushDeps(srv, time.Second)
		deps.credStore = store
		runFlush(deps)
	}

	st := state.Load(xdgpaths.StatePath())
	if st.Consecutive401 != 0 {
		t.Errorf("Consecutive401 = %d, want 0 (the 3rd run's success resets it)", st.Consecutive401)
	}
	if st.UnpairedReason != "" {
		t.Errorf("UnpairedReason = %q, want empty (two 401s then a success never unpairs)", st.UnpairedReason)
	}
	if !store.has {
		t.Error("credential store was cleared, want it untouched (only 3 CONSECUTIVE 401s clear it)")
	}
	if got := remainingSpool(t); len(got) != 0 {
		t.Errorf("remaining spool = %v, want empty (the 3rd run delivered the event)", got)
	}
}

func TestRunFlushUnauthorizedInsidePairingGraceRetriesInstead(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, time.Now().Format(time.RFC3339))
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	// A short budget: enough for a couple of the (now millisecond-scale)
	// backoff steps, not the real 5s.
	runFlush(testFlushDeps(srv, 50*time.Millisecond))

	st := state.Load(xdgpaths.StatePath())
	if st.Consecutive401 != 0 {
		t.Errorf("Consecutive401 = %d, want 0 (grace-period 401s don't count)", st.Consecutive401)
	}
	if st.UnpairedReason != "" {
		t.Errorf("UnpairedReason = %q, want empty (grace-period 401s never unpair)", st.UnpairedReason)
	}
	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event still spooled", got)
	}
}

func TestRunFlushRateLimitedWithRetryAfterSucceedsOnRetry(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	first := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first {
			first = false
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 1})
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if got := remainingSpool(t); len(got) != 0 {
		t.Errorf("remaining spool = %v, want empty (delivered after honoring Retry-After)", got)
	}
}

func TestRunFlushRateLimitedWithoutRetryAfterLeavesSpooled(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event still spooled (no Retry-After, stop immediately)", got)
	}
}

func TestRunFlushServerErrorRetriesThenLeavesSpooled(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if attempts < 2 {
		t.Errorf("attempts = %d, want at least one retry after the first 500", attempts)
	}
	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event still spooled", got)
	}
}

func TestRunFlushConnectionRefusedLeavesSpooled(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed immediately: the port now refuses connections

	runFlush(testFlushDeps(srv, time.Second))

	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event still spooled", got)
	}
}

func TestRunFlushHungServerCutOffByBudget(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// httptest.Server.Close() blocks until every in-flight handler
	// returns, so the handler must be unblocked (closing block) before
	// Close() is called, not after — a plain "defer srv.Close()" ordered
	// after "defer close(block)" would deadlock (LIFO runs Close first).
	defer func() {
		close(block)
		srv.Close()
	}()

	start := time.Now()
	budget := 100 * time.Millisecond
	runFlush(testFlushDeps(srv, budget))
	if elapsed := time.Since(start); elapsed > budget+2*time.Second {
		t.Errorf("runFlush took %v, want cut off close to the %v budget", elapsed, budget)
	}

	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event still spooled", got)
	}
}

func TestRunFlushNeverExtendsPastBudgetByMuch(t *testing.T) {
	// A dedicated timing assertion (separate from the correctness test
	// above) using the real backoff schedule against a normal (non-hung)
	// 500 responder, matching NFR-02's "never blocks past the flush
	// budget" for the ordinary retry path too.
	setTestXDGDirs(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	budget := 300 * time.Millisecond
	start := time.Now()
	runFlush(testFlushDeps(srv, budget))
	if elapsed := time.Since(start); elapsed > budget+time.Second {
		t.Errorf("runFlush took %v, want close to the %v budget (1s backoff step must not be started)", elapsed, budget)
	}
}

func TestRunFlushUnpairedIsANoopThatDoesNotTouchSpool(t *testing.T) {
	setTestXDGDirs(t)
	// No config written at all: config.Load falls back to Default(),
	// which is unpaired (BridgeID == UnpairedBridgeID, no secret).
	seedSpool(t, event("stop"))

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	if called {
		t.Error("relay was contacted while unpaired, want no request at all")
	}
	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event untouched", got)
	}
	// This early return must
	// not leave a stale LastFlush from some earlier, once-paired run —
	// "status" and "doctor" need to see "unpaired" here.
	if st := state.Load(xdgpaths.StatePath()); st.LastFlush == nil || st.LastFlush.Outcome != state.OutcomeUnpaired {
		t.Errorf("LastFlush = %+v, want Outcome %q", st.LastFlush, state.OutcomeUnpaired)
	}
}

// TestRunFlushNoCredentialIsANoopThatDoesNotTouchSpool is
// TestRunFlushUnpairedIsANoopThatDoesNotTouchSpool's other half: a real
// bridge_id (paired) but no secret in the credential store (ERR-04
// already cleared it, or pairing never got that far) must behave the
// same way — nothing to authenticate a flush with.
func TestRunFlushNoCredentialIsANoopThatDoesNotTouchSpool(t *testing.T) {
	setTestXDGDirs(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	deps := testFlushDeps(srv, time.Second)
	deps.credStore = &fakeCredStore{} // has=false: nothing stored

	runFlush(deps)

	if called {
		t.Error("relay was contacted with no credential stored, want no request at all")
	}
	if got := remainingSpool(t); len(got) != 1 {
		t.Errorf("remaining spool = %v, want the event untouched", got)
	}
	if st := state.Load(xdgpaths.StatePath()); st.LastFlush == nil || st.LastFlush.Outcome != state.OutcomeUnpaired {
		t.Errorf("LastFlush = %+v, want Outcome %q", st.LastFlush, state.OutcomeUnpaired)
	}
}

// TestRunFlushSuccessRecordsLastEventAndLastFlush covers the new
// state fields (BR-14): a successful run must leave behind which event
// was last sent and that the flush itself succeeded.
func TestRunFlushSuccessRecordsLastEventAndLastFlush(t *testing.T) {
	setTestXDGDirs(t)
	withFastBackoff(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"), event("commit"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 2, "duplicates": 0})
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, time.Second))

	st := state.Load(xdgpaths.StatePath())
	if st.LastEvent == nil {
		t.Fatal("LastEvent is nil, want it set after a successful flush")
	}
	if st.LastEvent.Type != "commit" {
		t.Errorf("LastEvent.Type = %q, want %q (the last event in the batch)", st.LastEvent.Type, "commit")
	}
	if st.LastFlush == nil {
		t.Fatal("LastFlush is nil, want it set after a flush run")
	}
	if st.LastFlush.Outcome != state.OutcomeOK {
		t.Errorf("LastFlush.Outcome = %q, want %q", st.LastFlush.Outcome, state.OutcomeOK)
	}
}

// TestRunFlushServerErrorRecordsLastFlushOutcome checks the failure side
// of the same bookkeeping: a run that never delivers anything still
// records what happened, with the HTTP status the relay returned.
func TestRunFlushServerErrorRecordsLastFlushOutcome(t *testing.T) {
	setTestXDGDirs(t)
	pairedConfig(t, "")
	seedSpool(t, event("stop"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	runFlush(testFlushDeps(srv, 300*time.Millisecond))

	st := state.Load(xdgpaths.StatePath())
	if st.LastFlush == nil {
		t.Fatal("LastFlush is nil, want it set after a flush run")
	}
	if st.LastFlush.Outcome != state.OutcomeServerError {
		t.Errorf("LastFlush.Outcome = %q, want %q", st.LastFlush.Outcome, state.OutcomeServerError)
	}
	if st.LastFlush.HTTPStatus != http.StatusInternalServerError {
		t.Errorf("LastFlush.HTTPStatus = %d, want %d", st.LastFlush.HTTPStatus, http.StatusInternalServerError)
	}
}
