package coalesce

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)

// line is one spooled SPEC 10.1 event, reduced to what the tests need,
// with a unique event_id so equal-looking events stay distinguishable.
func line(n int, ts time.Time, session, typ, helper string) []byte {
	sub := ""
	if helper != "" {
		sub = fmt.Sprintf(`,"subagent":{"id":%q,"type":"Explore"}`, helper)
	}
	return fmt.Appendf(nil, `{"schema":1,"event_id":"ev-%04d","session_id":%q,"ts":%q,"type":%q%s,"payload":{}}`,
		n, session, ts.Format(time.RFC3339Nano), typ, sub)
}

func ago(d time.Duration) time.Time { return testNow.Add(-d) }

func TestDropStaleActivity(t *testing.T) {
	old := ago(time.Hour)
	tests := []struct {
		name  string
		lines [][]byte
		want  []int // indexes of lines kept
	}{
		{"empty", nil, nil},
		{"fresh activity is kept", [][]byte{
			line(0, ago(time.Minute), "s", "activity", ""),
			line(1, ago(time.Minute), "s", "stop", ""),
		}, []int{0, 1}},
		{"activity exactly at the window is kept", [][]byte{
			line(0, ago(StaleAfter), "s", "activity", ""),
			line(1, ago(time.Minute), "s", "stop", ""),
		}, []int{0, 1}},
		{"stale activity followed in its chain is dropped", [][]byte{
			line(0, old, "s", "activity", ""),
			line(1, old, "s", "activity", ""),
			line(2, old, "s", "stop", ""),
		}, []int{2}},
		{"stale activity last in its session is kept", [][]byte{
			line(0, old, "s", "activity", ""),
			line(1, old, "s", "activity", ""),
		}, []int{1}},
		{"other sessions do not count as later", [][]byte{
			line(0, old, "a", "activity", ""),
			line(1, old, "b", "stop", ""),
		}, []int{0, 1}},
		{"stale helper activity last in its chain is kept", [][]byte{
			line(0, old, "s", "subagent_start", "h1"),
			line(1, old, "s", "activity", "h1"),
			line(2, old, "s", "activity", "h1"),
			line(3, old, "s", "activity", ""),
			line(4, old, "s", "stop", ""),
		}, []int{0, 2, 4}},
		{"every other type is kept however old", [][]byte{
			line(0, old, "s", "session_start", ""),
			line(1, old, "s", "prompt_submitted", ""),
			line(2, old, "s", "needs_input", ""),
			line(3, old, "s", "input_resolved", ""),
			line(4, old, "s", "verification_started", ""),
			line(5, old, "s", "verification_finished", ""),
			line(6, old, "s", "pr_created", ""),
			line(7, old, "s", "commit", ""),
			line(8, old, "s", "paused", ""),
			line(9, old, "s", "stop", ""),
			line(10, old, "s", "project_seen", ""),
			line(11, old, "s", "session_end", ""),
		}, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
		{"unreadable lines are kept", [][]byte{
			[]byte(`not json`),
			[]byte(`{"type":"activity","ts":"2026-09-29T00:00:00Z"}`),
			[]byte(`{"session_id":"s","type":"activity","ts":"yesterday"}`),
			line(3, old, "s", "stop", ""),
		}, []int{0, 1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var want [][]byte
			for _, i := range tt.want {
				want = append(want, tt.lines[i])
			}
			got := DropStaleActivity(tt.lines, testNow)
			if !slices.EqualFunc(got, want, bytes.Equal) {
				t.Errorf("kept\n%s\nwant\n%s", bytes.Join(got, []byte("\n")), bytes.Join(want, []byte("\n")))
			}
		})
	}
}

type parsed struct {
	session, helper, typ string
}

func parse(t *testing.T, l []byte) parsed {
	t.Helper()
	var h header
	if err := json.Unmarshal(l, &h); err != nil {
		t.Fatal(err)
	}
	p := parsed{session: h.SessionID, typ: h.Type}
	if h.Subagent != nil {
		p.helper = h.Subagent.ID
	}
	return p
}

// backlog is n events from several sessions spread over five hours, in
// the shape a real one takes: turns of prompt, mostly activity,
// verification, and stop, with helpers starting and stopping, questions,
// pauses, commits and sessions ending and restarting.
func backlog(n int) [][]byte {
	r := rand.New(rand.NewPCG(26, 81))
	start := testNow.Add(-5 * time.Hour)
	step := 5 * time.Hour / time.Duration(n)
	var out [][]byte
	helpers := map[string]string{}
	for i := 0; i < n; i++ {
		s := fmt.Sprintf("sess-%d", r.IntN(7))
		ts := start.Add(time.Duration(i) * step)
		typ, helper := "activity", ""
		switch x := r.IntN(100); {
		case x < 55:
			if h := helpers[s]; h != "" && r.IntN(2) == 0 {
				helper = h
			}
		case x < 60:
			typ = "prompt_submitted"
		case x < 64:
			typ = "stop"
		case x < 68:
			typ = "verification_started"
		case x < 72:
			typ = "verification_finished"
		case x < 75:
			typ = "needs_input"
		case x < 78:
			typ = "input_resolved"
		case x < 80:
			typ = "commit"
		case x < 81:
			typ = "pr_created"
		case x < 83:
			typ = "paused"
		case x < 85:
			typ = "session_start"
		case x < 86:
			typ = "session_end"
		case x < 93:
			if helpers[s] == "" {
				helpers[s] = fmt.Sprintf("helper-%d", i)
				typ, helper = "subagent_start", helpers[s]
			}
		default:
			if helpers[s] != "" {
				typ, helper = "subagent_stop", helpers[s]
				delete(helpers, s)
			}
		}
		out = append(out, line(i, ts, s, typ, helper))
	}
	return out
}

// TestFiveHundredEventBacklog is card P5-26's backlog test under D81.
func TestFiveHundredEventBacklog(t *testing.T) {
	in := backlog(500)
	got := DropStaleActivity(in, testNow)

	// Order is preserved and nothing is invented: got is a subsequence of in.
	j := 0
	for _, l := range got {
		for j < len(in) && !bytes.Equal(in[j], l) {
			j++
		}
		if j == len(in) {
			t.Fatalf("output is not an in-order subsequence of the backlog at %s", l)
		}
		j++
	}

	lastOfSession := map[string][]byte{}
	lastOfChain := map[parsed][]byte{}
	for _, l := range in {
		p := parse(t, l)
		lastOfSession[p.session] = l
		lastOfChain[parsed{session: p.session, helper: p.helper}] = l
	}
	kept := func(l []byte) bool { return slices.ContainsFunc(got, func(g []byte) bool { return bytes.Equal(g, l) }) }
	for s, l := range lastOfSession {
		if !kept(l) {
			t.Errorf("session %s: its last event was dropped", s)
		}
	}
	for c, l := range lastOfChain {
		if !kept(l) {
			t.Errorf("chain %+v: its last event was dropped", c)
		}
	}

	var inActivity, outActivity int
	for _, l := range in {
		p := parse(t, l)
		if p.typ == "activity" {
			inActivity++
			continue
		}
		// Includes every helper start and stop and every verification.
		if !kept(l) {
			t.Errorf("a %s event was dropped: %s", p.typ, l)
		}
	}
	for _, l := range got {
		if parse(t, l).typ == "activity" {
			outActivity++
		}
	}
	if outActivity > len(lastOfChain) {
		t.Errorf("%d activity events kept, want at most one per chain (%d chains)", outActivity, len(lastOfChain))
	}
	if outActivity*2 > len(got) {
		t.Errorf("%d of %d kept events are activity, want mostly other types", outActivity, len(got))
	}
	t.Logf("500 events (%d activity) became %d (%d activity)", inActivity, len(got), outActivity)
}

// TestReplaySequenceIsUnchangedApartFromStaleActivity is the PR #7
// review's first probe under D81: a backlog in which one session finishes
// two turns (two Done alerts on the relay) and runs a helper whose
// start and stop write recap lines. Everything but the stale activity
// reaches the relay, in order, so it fires what an uncoalesced replay
// would fire.
func TestReplaySequenceIsUnchangedApartFromStaleActivity(t *testing.T) {
	events := []struct{ typ, helper string }{
		{"session_start", ""},
		{"prompt_submitted", ""},
		{"activity", ""},
		{"subagent_start", "h1"},
		{"activity", "h1"},
		{"activity", "h1"},
		{"subagent_stop", "h1"},
		{"activity", ""},
		{"stop", ""},
		{"prompt_submitted", ""},
		{"activity", ""},
		{"verification_started", ""},
		{"verification_finished", ""},
		{"activity", ""},
		{"stop", ""},
	}
	var in [][]byte
	var want []string
	for i, e := range events {
		in = append(in, line(i, ago(time.Hour-time.Duration(i)*time.Minute), "s", e.typ, e.helper))
		if e.typ != "activity" {
			want = append(want, e.typ+"/"+e.helper)
		}
	}
	var got []string
	for _, l := range DropStaleActivity(in, testNow) {
		p := parse(t, l)
		got = append(got, p.typ+"/"+p.helper)
	}
	if !slices.Equal(got, want) {
		t.Errorf("sent %v\nwant %v", got, want)
	}
}

func BenchmarkDropStaleActivity(b *testing.B) {
	in := backlog(500)
	for b.Loop() {
		DropStaleActivity(in, testNow)
	}
}
