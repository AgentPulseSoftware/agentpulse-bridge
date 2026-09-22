package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"https ok", "https://relay.example.com", false},
		{"https trailing slash trimmed", "https://relay.example.com/", false},
		{"http localhost ok", "http://localhost:8787", false},
		{"http 127.0.0.1 ok", "http://127.0.0.1:8787", false},
		{"http other host rejected", "http://relay.example.com", true},
		{"ftp rejected", "ftp://relay.example.com", true},
		{"malformed rejected", "://not a url", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateBaseURL(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateBaseURL(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if err == nil && strings.HasSuffix(got, "/") {
				t.Errorf("ValidateBaseURL(%q) = %q, trailing slash not trimmed", tt.raw, got)
			}
		})
	}
}

// fakeRelay records every request it receives and answers according to a
// caller-supplied handler, so each test only writes the handler logic
// specific to the ERR row it exercises.
type fakeRelay struct {
	mu       sync.Mutex
	requests []recordedRequest
	handler  func(w http.ResponseWriter, batch requestBatch)
}

type recordedRequest struct {
	auth   string
	events int
	bytes  int
}

type requestBatch struct {
	Events           []json.RawMessage `json:"events"`
	WatchListVersion int               `json:"watch_list_version"`
}

func newFakeRelay(t *testing.T, handler func(w http.ResponseWriter, batch requestBatch)) (*fakeRelay, *httptest.Server) {
	t.Helper()
	fr := &fakeRelay{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readAll(t, r)
		var batch requestBatch
		_ = json.Unmarshal(body, &batch)

		fr.mu.Lock()
		fr.requests = append(fr.requests, recordedRequest{
			auth:   r.Header.Get("Authorization"),
			events: len(batch.Events),
			bytes:  len(body),
		})
		fr.mu.Unlock()

		fr.handler(w, batch)
	}))
	t.Cleanup(srv.Close)
	return fr, srv
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer func() { _ = r.Body.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func (fr *fakeRelay) requestCount() int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return len(fr.requests)
}

func testEvent(eventType string, n int) []byte {
	return []byte(fmt.Sprintf(`{"schema":1,"event_id":"01J8000000000000000000%04d","type":%q,"n":%d}`, n, eventType, n))
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(Config{
		BaseURL:      baseURL,
		BridgeID:     "brg_test",
		BridgeSecret: "s3cr3t",
		Version:      "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("NewClient() returned error: %v", err)
	}
	return c
}

func TestPostEventsSuccessSendsAuthAndReturnsCountsAndWatchList(t *testing.T) {
	fr, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		_ = json.NewEncoder(w).Encode(okBody{
			Accepted:   len(batch.Events),
			Duplicates: 0,
			WatchList:  &WatchList{Version: 9, Projects: map[string]ProjectEntry{"abc": {Watched: true}}},
			Settings:   &Settings{TaskLabel: true},
		})
	})
	c := newTestClient(t, srv.URL)

	events := [][]byte{testEvent("stop", 1), testEvent("commit", 2)}
	resp, err := c.PostEvents(context.Background(), events, 3)
	if err != nil {
		t.Fatalf("PostEvents() returned error: %v", err)
	}
	if resp.Sent != len(events) {
		t.Errorf("Sent = %d, want %d", resp.Sent, len(events))
	}
	if resp.Accepted != 2 {
		t.Errorf("Accepted = %d, want 2", resp.Accepted)
	}
	if resp.WatchList == nil || resp.WatchList.Version != 9 {
		t.Errorf("WatchList = %+v, want version 9", resp.WatchList)
	}
	if resp.Settings == nil || !resp.Settings.TaskLabel {
		t.Errorf("Settings = %+v, want TaskLabel true", resp.Settings)
	}

	if got := fr.requests[0].auth; got != "Bearer brg_test.s3cr3t" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer brg_test.s3cr3t")
	}
}

// TestPostEventsBadRequestNamesOneEvent exercises ERR-03: the fake relay
// rejects the whole batch with a 400 naming exactly one event in the shape
// the real relay actually emits, and
// PostEvents must stop before that chunk (Sent unchanged) while reporting
// the named event as Dropped.
func TestPostEventsBadRequestNamesOneEvent(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "invalid_body",
			"detail": "events.1.type: invalid enum value",
		})
	})
	c := newTestClient(t, srv.URL)

	events := [][]byte{testEvent("stop", 0), testEvent("bogus", 1), testEvent("commit", 2)}
	resp, err := c.PostEvents(context.Background(), events, 0)
	if err == nil {
		t.Fatal("PostEvents() returned no error for a 400 response")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindBadRequest {
		t.Fatalf("error = %v, want a *StatusError with Kind KindBadRequest", err)
	}
	if resp.Sent != 0 {
		t.Errorf("Sent = %d, want 0 (the whole chunk containing the bad event never got a 2xx)", resp.Sent)
	}
	if len(resp.Dropped) != 1 || resp.Dropped[0].Index != 1 {
		t.Fatalf("Dropped = %+v, want exactly index 1", resp.Dropped)
	}
	if resp.Dropped[0].Type != "bogus" {
		t.Errorf("Dropped[0].Type = %q, want %q", resp.Dropped[0].Type, "bogus")
	}
}

func TestPostEventsBadRequestWithoutIdentifiableEvent(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_json"})
	})
	c := newTestClient(t, srv.URL)

	resp, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindBadRequest {
		t.Fatalf("error = %v, want a *StatusError with Kind KindBadRequest", err)
	}
	if len(resp.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want none (no event was identifiable)", resp.Dropped)
	}
}

func TestPostEventsBadRequestRecordsRequiredBridgeVersion(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":                   "unsupported_schema",
			"required_bridge_version": "1.4.0",
		})
	})
	c := newTestClient(t, srv.URL)

	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *StatusError", err)
	}
	if se.RequiredBridgeVersion != "1.4.0" {
		t.Errorf("RequiredBridgeVersion = %q, want %q", se.RequiredBridgeVersion, "1.4.0")
	}
	if strings.Contains(se.Error(), "1.4.0") {
		t.Errorf("Error() = %q must not embed response body text", se.Error())
	}
}

func TestPostEventsUnauthorized(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := newTestClient(t, srv.URL)

	resp, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindUnauthorized {
		t.Fatalf("error = %v, want *StatusError Kind=KindUnauthorized", err)
	}
	if resp.Sent != 0 {
		t.Errorf("Sent = %d, want 0", resp.Sent)
	}
}

func TestPostEventsRateLimitedWithRetryAfter(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := newTestClient(t, srv.URL)

	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindRateLimited {
		t.Fatalf("error = %v, want *StatusError Kind=KindRateLimited", err)
	}
	if !se.RetryAfterValid || se.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter = %v valid=%v, want 2s valid=true", se.RetryAfter, se.RetryAfterValid)
	}
}

func TestPostEventsRateLimitedWithoutRetryAfter(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := newTestClient(t, srv.URL)

	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindRateLimited {
		t.Fatalf("error = %v, want *StatusError Kind=KindRateLimited", err)
	}
	if se.RetryAfterValid {
		t.Errorf("RetryAfterValid = true, want false when the header is absent")
	}
}

func TestPostEventsServerError(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := newTestClient(t, srv.URL)

	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindServerError {
		t.Fatalf("error = %v, want *StatusError Kind=KindServerError", err)
	}
}

func TestPostEventsConnectionRefused(t *testing.T) {
	// A server that is immediately closed leaves its port refusing
	// connections, simulating "the relay/network is down" (ERR-06).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	c := newTestClient(t, srv.URL)

	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindNetwork {
		t.Fatalf("error = %v, want *StatusError Kind=KindNetwork", err)
	}
}

func TestPostEventsHungServerCutOffByContext(t *testing.T) {
	unblock := make(chan struct{})
	// A plain defer, not t.Cleanup: t.Cleanup functions run in
	// last-registered-first-out order, and newFakeRelay registers its own
	// srv.Close (which blocks until the hung handler returns) via
	// t.Cleanup too. Registering this one the same way would make
	// srv.Close run *first* and deadlock waiting on a handler that is
	// waiting on this channel. A plain defer runs before any t.Cleanup.
	defer close(unblock)
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		<-unblock
	})
	c := newTestClient(t, srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.PostEvents(ctx, [][]byte{testEvent("stop", 0)}, 0)
	elapsed := time.Since(start)

	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindTimeout {
		t.Fatalf("error = %v, want *StatusError Kind=KindTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("PostEvents took %v to return after a 100ms context timeout, want it cut off promptly", elapsed)
	}
}

func TestPostEventsAlreadyExpiredContext(t *testing.T) {
	_, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {})
	c := newTestClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.PostEvents(ctx, [][]byte{testEvent("stop", 0)}, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindTimeout {
		t.Fatalf("error = %v, want *StatusError Kind=KindTimeout", err)
	}
}

// TestPostEventsChunks120EventsIntoThreeRequests is the chunking test:
// 120 events, each request at most 50 events and at most 16 KB.
func TestPostEventsChunks120EventsIntoThreeRequests(t *testing.T) {
	fr, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		_ = json.NewEncoder(w).Encode(okBody{Accepted: len(batch.Events)})
	})
	c := newTestClient(t, srv.URL)

	events := make([][]byte, 120)
	for i := range events {
		events[i] = testEvent("activity", i)
	}

	resp, err := c.PostEvents(context.Background(), events, 0)
	if err != nil {
		t.Fatalf("PostEvents() returned error: %v", err)
	}
	if resp.Sent != 120 {
		t.Errorf("Sent = %d, want 120", resp.Sent)
	}
	if fr.requestCount() != 3 {
		t.Fatalf("relay received %d requests, want 3", fr.requestCount())
	}
	total := 0
	for i, req := range fr.requests {
		if req.events > MaxEventsPerChunk {
			t.Errorf("request %d had %d events, want <= %d", i, req.events, MaxEventsPerChunk)
		}
		if req.bytes > MaxChunkBytes {
			t.Errorf("request %d body was %d bytes, want <= %d", i, req.bytes, MaxChunkBytes)
		}
		total += req.events
	}
	if total != 120 {
		t.Errorf("requests carried %d events total, want 120", total)
	}
}

// TestPostEventsChunksByByteSizeBeforeCount builds events large enough
// that the 16 KB body cap binds before the 50-event cap would (the size
// bound binds first for large batches).
func TestPostEventsChunksByByteSizeBeforeCount(t *testing.T) {
	fr, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		_ = json.NewEncoder(w).Encode(okBody{Accepted: len(batch.Events)})
	})
	c := newTestClient(t, srv.URL)

	// ~700 bytes per event * 30 ~= 21KB, more than MaxChunkBytes but well
	// under MaxEventsPerChunk, so chunk membership must be byte-bound.
	events := make([][]byte, 30)
	for i := range events {
		pad := strings.Repeat("p", 650)
		events[i] = []byte(fmt.Sprintf(`{"schema":1,"type":"activity","pad":%q,"n":%d}`, pad, i))
	}

	resp, err := c.PostEvents(context.Background(), events, 0)
	if err != nil {
		t.Fatalf("PostEvents() returned error: %v", err)
	}
	if resp.Sent != 30 {
		t.Errorf("Sent = %d, want 30", resp.Sent)
	}
	if fr.requestCount() < 2 {
		t.Fatalf("relay received %d requests, want more than 1 (byte cap should have split them)", fr.requestCount())
	}
	for i, req := range fr.requests {
		if req.bytes > MaxChunkBytes {
			t.Errorf("request %d body was %d bytes, want <= %d", i, req.bytes, MaxChunkBytes)
		}
	}
}

func TestPostEventsStopsAtFirstFailingChunkPreservingEarlierProgress(t *testing.T) {
	var calls int32
	fr, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		n := atomicAdd(&calls)
		if n == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(okBody{Accepted: len(batch.Events)})
	})
	c := newTestClient(t, srv.URL)

	events := make([][]byte, 120)
	for i := range events {
		events[i] = testEvent("activity", i)
	}

	resp, err := c.PostEvents(context.Background(), events, 0)
	var se *StatusError
	if !errors.As(err, &se) || se.Kind != KindServerError {
		t.Fatalf("error = %v, want *StatusError Kind=KindServerError", err)
	}
	if resp.Sent != 50 {
		t.Errorf("Sent = %d, want 50 (only the first chunk succeeded)", resp.Sent)
	}
	if fr.requestCount() != 2 {
		t.Errorf("relay received %d requests, want 2 (stop at the first failure)", fr.requestCount())
	}
}

var addMu sync.Mutex

func atomicAdd(p *int32) int32 {
	addMu.Lock()
	defer addMu.Unlock()
	*p++
	return *p
}

func TestNewClientRejectsInvalidBaseURL(t *testing.T) {
	_, err := NewClient(Config{BaseURL: "http://example.com", BridgeID: "brg_x", BridgeSecret: "s"})
	if err == nil {
		t.Fatal("NewClient() returned no error for a non-https, non-localhost URL")
	}
}

func TestParseEventIndex(t *testing.T) {
	tests := []struct {
		detail  string
		wantIdx int
		wantOK  bool
	}{
		{"events.3.type: invalid enum value", 3, true},
		{"events.0.bridge_id: must match the authenticated bridge", 0, true},
		{"(root): invalid request body", 0, false},
		{"", 0, false},
		{"events.type: missing index", 0, false},
	}
	for _, tt := range tests {
		idx, ok := parseEventIndex(tt.detail)
		if ok != tt.wantOK || (ok && idx != tt.wantIdx) {
			t.Errorf("parseEventIndex(%q) = (%d, %v), want (%d, %v)", tt.detail, idx, ok, tt.wantIdx, tt.wantOK)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
		wantOK bool
	}{
		{"5", 5 * time.Second, true},
		{"0", 0, true},
		{"", 0, false},
		{"-1", 0, false},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0, false}, // HTTP-date form not honored (ERR-05: delta-seconds only)
	}
	for _, tt := range tests {
		got, ok := parseRetryAfter(tt.header)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("parseRetryAfter(%q) = (%v, %v), want (%v, %v)", tt.header, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestPostEventsEmptyInputIsANoop(t *testing.T) {
	fr, srv := newFakeRelay(t, func(w http.ResponseWriter, batch requestBatch) {
		_ = json.NewEncoder(w).Encode(okBody{})
	})
	c := newTestClient(t, srv.URL)

	resp, err := c.PostEvents(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("PostEvents() returned error for empty input: %v", err)
	}
	if resp.Sent != 0 {
		t.Errorf("Sent = %d, want 0", resp.Sent)
	}
	if fr.requestCount() != 0 {
		t.Errorf("relay received %d requests for empty input, want 0", fr.requestCount())
	}
}

func TestUserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_ = json.NewEncoder(w).Encode(okBody{})
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, BridgeID: "brg_x", BridgeSecret: "s", Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0); err != nil {
		t.Fatal(err)
	}
	if gotUA != "agentpulse/1.2.3" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "agentpulse/1.2.3")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed = true
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c := newTestClient(t, redirector.URL)
	_, err := c.PostEvents(context.Background(), [][]byte{testEvent("stop", 0)}, 0)
	// A 302 falls into the "unexpected status" default branch, which is
	// KindServerError-shaped here — what matters is that the redirect was
	// never followed.
	if err == nil {
		t.Fatal("PostEvents() returned no error for a 302 response")
	}
	if followed {
		t.Error("client followed a redirect; it must not (BR-18/SEC-14)")
	}
}
