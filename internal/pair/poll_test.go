package pair

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
)

// fakeClock is a clock the test drives: every Sleep jumps it forward, so a
// ten-minute expiry is exercised in microseconds.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	return nil
}

// response is one scripted relay answer.
type response struct {
	status int
	body   string
	header map[string]string
}

// newScriptedRelay serves responses in order, repeating the last one
// forever, and records how many requests it saw.
func newScriptedRelay(t *testing.T, responses []response) (*relay.Client, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bridges/me/pairing" {
			t.Errorf("poll requested %s, want /v1/bridges/me/pairing", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer brg_test.s3cr3t" {
			t.Errorf("Authorization = %q, want the bridge credential", got)
		}
		resp := responses[min(calls, len(responses)-1)]
		calls++
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(srv.Close)

	client, err := relay.NewClient(relay.Config{
		BaseURL:      srv.URL,
		BridgeID:     "brg_test",
		BridgeSecret: "s3cr3t",
		Version:      "test",
	})
	if err != nil {
		t.Fatalf("relay.NewClient: %v", err)
	}
	return client, &calls
}

func TestPoll(t *testing.T) {
	const (
		waiting = `{"paired":false,"expires_at":"2026-09-14T12:00:00Z"}`
		paired  = `{"paired":true,"device_name":"Sam's iPhone"}`
	)

	tests := []struct {
		name       string
		responses  []response
		lifetime   time.Duration
		wantDevice string
		wantErr    error
		minCalls   int
	}{
		{
			name:       "paired on the first poll",
			responses:  []response{{status: 200, body: paired}},
			lifetime:   10 * time.Minute,
			wantDevice: "Sam's iPhone",
			minCalls:   1,
		},
		{
			name:       "waits, then the phone redeems the code",
			responses:  []response{{status: 200, body: waiting}, {status: 200, body: waiting}, {status: 200, body: paired}},
			lifetime:   10 * time.Minute,
			wantDevice: "Sam's iPhone",
			minCalls:   3,
		},
		{
			name:      "the code expires while waiting",
			responses: []response{{status: 200, body: waiting}},
			lifetime:  10 * time.Minute,
			wantErr:   ErrExpired,
			minCalls:  300, // 10 minutes at one poll every 2 seconds
		},
		{
			name:       "a 401 inside the grace window is retried",
			responses:  []response{{status: 401, body: `{"error":"unauthorized"}`}, {status: 200, body: paired}},
			lifetime:   10 * time.Minute,
			wantDevice: "Sam's iPhone",
			minCalls:   2,
		},
		{
			name: "a 401 after the grace window gives up",
			// The scripted relay repeats its last answer, so the 401s
			// keep coming: they are retried while the clock is inside
			// the 60-second grace window and fatal once it is past it.
			responses: []response{
				{status: 200, body: waiting},
				{status: 401, body: `{"error":"unauthorized"}`},
			},
			lifetime: 10 * time.Minute,
			wantErr:  ErrRejected,
			minCalls: 30,
		},
		{
			name:       "a 500 is retried",
			responses:  []response{{status: 500, body: `{"error":"internal"}`}, {status: 200, body: paired}},
			lifetime:   10 * time.Minute,
			wantDevice: "Sam's iPhone",
			minCalls:   2,
		},
		{
			name: "a 429 is retried after the delay it names",
			responses: []response{
				{status: 429, body: `{"error":"rate_limited"}`, header: map[string]string{"Retry-After": "30"}},
				{status: 200, body: paired},
			},
			lifetime:   10 * time.Minute,
			wantDevice: "Sam's iPhone",
			minCalls:   2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, calls := newScriptedRelay(t, tc.responses)
			clock := &fakeClock{now: time.Date(2026, 9, 14, 11, 50, 0, 0, time.UTC)}
			// The grace window is measured from Start; a 401 after the
			// first two-second poll is inside it, one after a wait of a
			// minute or more is not.
			start := clock.now

			device, err := Poll(context.Background(), client, PollOptions{
				Deadline:  start.Add(tc.lifetime),
				Start:     start,
				Now:       clock.Now,
				Sleep:     clock.Sleep,
				AuthGrace: 60 * time.Second,
			})

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Poll error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if device != tc.wantDevice {
				t.Errorf("device name = %q, want %q", device, tc.wantDevice)
			}
			if *calls < tc.minCalls {
				t.Errorf("made %d poll requests, want at least %d", *calls, tc.minCalls)
			}
		})
	}
}

// TestPollHonorsRetryAfter checks that a 429's Retry-After actually slows
// the loop down rather than being ignored (ERR-05).
func TestPollHonorsRetryAfter(t *testing.T) {
	client, _ := newScriptedRelay(t, []response{
		{status: 429, body: `{"error":"rate_limited"}`, header: map[string]string{"Retry-After": "45"}},
		{status: 200, body: `{"paired":true,"device_name":"Sam's iPhone"}`},
	})
	clock := &fakeClock{now: time.Date(2026, 9, 14, 11, 50, 0, 0, time.UTC)}
	start := clock.now

	if _, err := Poll(context.Background(), client, PollOptions{
		Deadline: start.Add(10 * time.Minute),
		Start:    start,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
	}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if waited := clock.now.Sub(start); waited < 45*time.Second {
		t.Errorf("the loop waited %v after a 429, want at least the 45s the relay asked for", waited)
	}
}

// TestPollStopsOnCancel is the Ctrl-C case: the loop returns the context's
// own error promptly and writes nothing.
func TestPollStopsOnCancel(t *testing.T) {
	client, _ := newScriptedRelay(t, []response{{status: 200, body: `{"paired":false}`}})
	ctx, cancel := context.WithCancel(context.Background())
	clock := &fakeClock{now: time.Now()}

	polls := 0
	_, err := Poll(ctx, client, PollOptions{
		Deadline: clock.now.Add(10 * time.Minute),
		Start:    clock.now,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
		Progress: func(time.Duration) {
			polls++
			if polls == 2 {
				cancel() // the person pressed Ctrl-C after the second poll
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Poll error = %v, want context.Canceled", err)
	}
	if polls > 3 {
		t.Errorf("polled %d times after cancellation, want it to stop straight away", polls)
	}
}

// TestPollReportsProgressOnOneLine checks the status callback gets a
// countdown that only ever decreases (BR-07's single updating line).
func TestPollReportsProgressOnOneLine(t *testing.T) {
	client, _ := newScriptedRelay(t, []response{
		{status: 200, body: `{"paired":false}`},
		{status: 200, body: `{"paired":false}`},
		{status: 200, body: `{"paired":true,"device_name":"iPhone"}`},
	})
	clock := &fakeClock{now: time.Now()}
	var seen []time.Duration

	if _, err := Poll(context.Background(), client, PollOptions{
		Deadline: clock.now.Add(10 * time.Minute),
		Start:    clock.now,
		Now:      clock.Now,
		Sleep:    clock.Sleep,
		Progress: func(remaining time.Duration) { seen = append(seen, remaining) },
	}); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("progress was reported %d times, want once per wait (2)", len(seen))
	}
	if seen[1] >= seen[0] {
		t.Errorf("countdown went %v then %v, want it to decrease", seen[0], seen[1])
	}
}
