package coalesce

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestFixtureBacklogMatchesFIFOReplay is card P5-26's main test: a
// 500-event, multi-session backlog built from the classifier's fixture
// corpus shrinks to at most two batches and leaves every session, and
// every helper chain, exactly where replaying all 500 in order would.
func TestFixtureBacklogMatchesFIFOReplay(t *testing.T) {
	for seed := range uint64(12) {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			lines := newBacklogGen(t, seed, 6, "run").take(500)
			now := backlogStart.Add(24 * time.Hour)
			out := Backlog(lines, now)
			if len(out) > 100 {
				t.Errorf("coalesced to %d events, want at most two batches of 50", len(out))
			}
			assertSubsequence(t, lines, out)

			fifo := replay(t, nil, lines)
			got := replay(t, nil, out)
			assertSameModels(t, fifo, got)

			for id := range fifo {
				if a, b := mainState(lines, id), mainState(out, id); a != b {
					t.Errorf("session %s: main chain ends %q after FIFO replay, %q after coalescing", id, a, b)
				}
				if last := lastOf(lines, id); !containsLine(out, last) {
					t.Errorf("session %s: its latest event was dropped", id)
				}
			}
			for _, l := range lines {
				switch typeOf(l) {
				case "paused", "pr_created", "commit":
					if !containsLine(out, l) {
						t.Errorf("a %s event was dropped", typeOf(l))
					}
				}
			}
			t.Logf("500 events -> %d", len(out))
		})
	}
}

// TestFixtureBacklogFromAnyPriorState checks the same equivalence when the
// relay already holds state for these sessions and helpers (collected from
// an earlier backlog that reuses the same session and helper ids), and
// when a coalesced backlog is itself coalesced again after more events
// arrive, as happens when a flush is cut short.
func TestFixtureBacklogFromAnyPriorState(t *testing.T) {
	now := backlogStart.Add(24 * time.Hour)
	for seed := range uint64(12) {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			prior := replay(t, nil, newBacklogGen(t, seed+100, 6, "run").take(300))
			g := newBacklogGen(t, seed, 6, "run")
			x, y := g.take(250), g.take(250)

			fifo := replay(t, prior, append(slices.Clone(x), y...))
			once := replay(t, prior, Backlog(append(slices.Clone(x), y...), now))
			assertSameModels(t, fifo, once)

			twice := replay(t, prior, Backlog(append(Backlog(x, now), y...), now))
			assertSameModels(t, fifo, twice)
		})
	}
}

func TestBacklogLeavesALiveSpoolAlone(t *testing.T) {
	now := backlogStart.Add(time.Hour)
	lines := [][]byte{
		line(now.Add(-3*time.Minute), "s", "activity", `{"category":"read"}`, ""),
		line(now.Add(-2*time.Minute), "s", "activity", `{"category":"edit"}`, ""),
		line(now.Add(-1*time.Minute), "s", "activity", `{"category":"read"}`, ""),
	}
	if IsBacklog(lines, now) {
		t.Fatal("three fresh events counted as a backlog")
	}
	if got := Backlog(lines, now); !slices.EqualFunc(got, lines, func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Errorf("Backlog changed a live spool: %d lines out of %d", len(got), len(lines))
	}
}

func TestIsBacklog(t *testing.T) {
	now := backlogStart.Add(time.Hour)
	fresh := line(now.Add(-time.Minute), "s", "stop", `{}`, "")
	old := line(now.Add(-StaleAfter-time.Second), "s", "stop", `{}`, "")
	many := make([][]byte, 51)
	for i := range many {
		many[i] = fresh
	}
	tests := []struct {
		name  string
		lines [][]byte
		want  bool
	}{
		{"empty", nil, false},
		{"one fresh event", [][]byte{fresh}, false},
		{"one event older than the lost-contact window", [][]byte{old, fresh}, true},
		{"fifty fresh events", many[:50], false},
		{"fifty-one fresh events", many, true},
		{"an unparseable old-looking line is not evidence", [][]byte{[]byte(`{"ts":"2020-01-01T00:00:00Z"`), fresh}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBacklog(tt.lines, now); got != tt.want {
				t.Errorf("IsBacklog = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSurvivors pins down, on small hand-written backlogs, which events a
// session keeps and why.
func TestSurvivors(t *testing.T) {
	type ev struct {
		typ, payload, helper string
	}
	start := ev{"session_start", `{"agent":"claude-code","bridge_version":"1.0.0","os":"darwin"}`, ""}
	prompt := ev{"prompt_submitted", `{}`, ""}
	read := ev{"activity", `{"category":"read"}`, ""}
	edit := ev{"activity", `{"category":"edit"}`, ""}
	other := ev{"activity", `{"category":"other"}`, ""}
	vStart := ev{"verification_started", `{"kind":"test","runner":"pytest"}`, ""}
	vFail := ev{"verification_finished", `{"kind":"test","runner":"pytest","outcome":"fail","passed":1,"failed":2,"total":3}`, ""}
	vPass := ev{"verification_finished", `{"kind":"test","runner":"pytest","outcome":"pass","passed":3,"failed":0,"total":3}`, ""}
	needs := ev{"needs_input", `{"kind":"permission","tool_category":"command"}`, ""}
	resolved := ev{"input_resolved", `{}`, ""}
	stop := ev{"stop", `{}`, ""}
	paused := ev{"paused", `{"cause":"usage_limit"}`, ""}
	commit := ev{"commit", `{}`, ""}
	pr := ev{"pr_created", `{"number":7}`, ""}
	end := ev{"session_end", `{"reason":"prompt_input_exit"}`, ""}
	hStart := ev{"subagent_start", `{}`, "aaaaaaaaaaaaaaaa"}
	hRead := ev{"activity", `{"category":"read"}`, "aaaaaaaaaaaaaaaa"}
	hEdit := ev{"activity", `{"category":"edit"}`, "aaaaaaaaaaaaaaaa"}
	hStop := ev{"subagent_stop", `{}`, "aaaaaaaaaaaaaaaa"}

	tests := []struct {
		name   string
		events []ev
		want   []int // indices that survive
	}{
		{
			name:   "a long run of work collapses to its start, last prompt, last activity and stop",
			events: []ev{start, prompt, read, edit, read, edit, other, read, edit, stop},
			want:   []int{0, 1, 8, 9},
		},
		{
			name:   "a trailing pause survives, and so does the prompt that set the label",
			events: []ev{start, prompt, read, edit, paused},
			want:   []int{0, 1, 3, 4},
		},
		{
			// The second prompt goes: a prompt after a pause without a label
			// keeps the stored one (ADR-006 section 3), so the first prompt
			// is the one that decides the label.
			name:   "an earlier pause survives too (ADR-006), even though work resumed",
			events: []ev{start, prompt, read, paused, prompt, edit, stop},
			want:   []int{0, 1, 3, 5, 6},
		},
		{
			name:   "a failing run defines Fixing, so it survives the edit after it",
			events: []ev{start, prompt, vStart, vFail, read, edit},
			want:   []int{0, 1, 2, 3, 5},
		},
		{
			name:   "a passing run replaces the failing one",
			events: []ev{start, prompt, vStart, vFail, edit, vStart, vPass, stop},
			want:   []int{0, 1, 4, 5, 6, 7},
		},
		{
			name:   "Needs You at the end survives with the state before it",
			events: []ev{start, prompt, read, edit, other, needs},
			want:   []int{0, 1, 4, 5},
		},
		{
			name:   "commits and pull requests always survive",
			events: []ev{start, prompt, edit, commit, other, pr, stop},
			want:   []int{0, 1, 3, 4, 5, 6},
		},
		{
			name:   "a closed session keeps its end",
			events: []ev{start, prompt, read, stop, end},
			want:   []int{0, 1, 2, 3, 4},
		},
		{
			name:   "a resolved prompt collapses once later work moved on",
			events: []ev{start, prompt, needs, resolved, read, stop},
			want:   []int{0, 1, 4, 5},
		},
		{
			name:   "a helper that finished keeps only its stop",
			events: []ev{start, prompt, hStart, hRead, hEdit, hStop, stop},
			want:   []int{0, 1, 5, 6},
		},
		{
			name:   "a helper still running keeps its start and current state",
			events: []ev{start, prompt, hStart, hRead, hEdit, hRead, stop},
			want:   []int{0, 1, 2, 5, 6},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := backlogStart.Add(24 * time.Hour)
			lines := make([][]byte, len(tt.events))
			for i, e := range tt.events {
				lines[i] = line(backlogStart.Add(time.Duration(i)*time.Minute), "s", e.typ, e.payload, e.helper)
			}
			out := Backlog(lines, now)
			var got []int
			for i, l := range lines {
				if containsLine(out, l) {
					got = append(got, i)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("survivors = %v, want %v", got, tt.want)
			}
			assertSameModels(t, replay(t, nil, lines), replay(t, nil, out))
		})
	}
}

// TestBacklogKeepsWhatItCannotModel checks the conservative paths: lines
// with no session are always sent, and a session holding an event the
// model does not know, or events out of time order, is sent whole.
func TestBacklogKeepsWhatItCannotModel(t *testing.T) {
	now := backlogStart.Add(24 * time.Hour)
	at := func(m int) time.Time { return backlogStart.Add(time.Duration(m) * time.Minute) }
	lines := [][]byte{
		line(at(0), "unknown", "activity", `{"category":"read"}`, ""),
		line(at(1), "unknown", "some_future_type", `{}`, ""),
		line(at(2), "unknown", "activity", `{"category":"read"}`, ""),
		line(at(3), "skewed", "activity", `{"category":"read"}`, ""),
		line(at(1), "skewed", "activity", `{"category":"read"}`, ""),
		line(at(4), "skewed", "activity", `{"category":"read"}`, ""),
		[]byte(`not json`),
		line(at(5), "", "project_seen", `{}`, ""),
		line(at(6), "plain", "session_start", `{"agent":"claude-code","bridge_version":"1.0.0","os":"darwin"}`, ""),
		line(at(7), "plain", "activity", `{"category":"read"}`, ""),
		line(at(8), "plain", "activity", `{"category":"edit"}`, ""),
		line(at(9), "plain", "stop", `{}`, ""),
	}
	out := Backlog(lines, now)
	want := slices.Concat(lines[:9], lines[10:])
	if len(out) != len(want) {
		t.Fatalf("kept %d lines, want %d", len(out), len(want))
	}
	for i := range want {
		if string(out[i]) != string(want[i]) {
			t.Errorf("line %d = %s, want %s", i, out[i], want[i])
		}
	}
}

// BenchmarkBacklogOneSession is the worst case for the greedy pass: 500
// events of one session. It runs in the detached flush, never in the hook.
func BenchmarkBacklogOneSession(b *testing.B) {
	lines := newBacklogGen(b, 1, 1, "run").take(500)
	now := backlogStart.Add(24 * time.Hour)
	b.ResetTimer()
	for range b.N {
		Backlog(lines, now)
	}
}

// line builds one spooled event with only the envelope fields coalescing
// and the relay read, plus the counters every event carries.
func line(ts time.Time, session, typ, payload, helper string) []byte {
	sub := ""
	if helper != "" {
		sub = fmt.Sprintf(`,"subagent":{"id":%q,"type":"reviewer"}`, helper)
	}
	return fmt.Appendf(nil,
		`{"schema":1,"event_id":"%s-%d","bridge_id":"brg_test0001","session_id":%q,"project":{"key_hash":"00","name":"p"},"ts":%q,"type":%q,"counters":{"files_read":%d,"files_edited":0,"verification_runs":0,"commits":0},"payload":%s%s}`,
		typ, ts.UnixMilli(), session, ts.UTC().Format(time.RFC3339Nano), typ, ts.Unix()%1000, payload, sub)
}

func assertSubsequence(t *testing.T, all, sub [][]byte) {
	t.Helper()
	j := 0
	for _, l := range all {
		if j < len(sub) && string(sub[j]) == string(l) {
			j++
		}
	}
	if j != len(sub) {
		t.Errorf("coalesced output is not an in-order subsequence of the spool")
	}
}

func assertSameModels(t *testing.T, want, got map[string]sessionModel) {
	t.Helper()
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("session %s: missing after coalescing", id)
			continue
		}
		if !g.equal(w) {
			t.Errorf("session %s differs after coalescing:\n FIFO:      %+v\n coalesced: %+v", id, w, g)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("session %s: appeared only after coalescing", id)
		}
	}
}

func containsLine(lines [][]byte, l []byte) bool {
	return slices.ContainsFunc(lines, func(x []byte) bool { return string(x) == string(l) })
}

func lastOf(lines [][]byte, sessionID string) []byte {
	var last []byte
	for _, l := range lines {
		var e struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(l, &e) == nil && e.SessionID == sessionID {
			last = l
		}
	}
	return last
}

func typeOf(l []byte) string {
	var e struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(l, &e)
	return e.Type
}
