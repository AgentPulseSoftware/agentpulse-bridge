package coalesce

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

// fixtureDir is the classifier's fixture corpus (SPEC 9.4), the same one
// "make fixtures" replays through the built binary.
const fixtureDir = "../../testdata/fixtures"

// backlogStart is the synthetic backlog's first instant.
var backlogStart = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

// scenario is one fixture scenario's hook documents, in order.
type scenario struct {
	name string
	docs [][]byte
}

func loadScenarios(t testing.TB) []scenario {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(fixtureDir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	var out []scenario
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9]-*.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			continue
		}
		sort.Strings(files)
		sc := scenario{name: filepath.Base(dir)}
		for _, f := range files {
			raw, err := os.ReadFile(f) //nolint:gosec // fixture path from the repo's own testdata
			if err != nil {
				t.Fatal(err)
			}
			sc.docs = append(sc.docs, raw)
		}
		out = append(out, sc)
	}
	if len(out) < 20 {
		t.Fatalf("found %d fixture scenarios, want the whole corpus", len(out))
	}
	return out
}

// backlogGen turns the fixture corpus into a long, interleaved,
// multi-session spool: each of its sessions runs one scenario after
// another (as a long session, or a resumed one, would), the sessions'
// hooks interleave at random, and every hook goes through the real
// classifier. Helper ids are renamed per session and per run so helpers
// from different runs do not collide, unless two generators share a
// prefix.
type backlogGen struct {
	t         testing.TB
	rng       *rand.Rand
	scenarios []scenario
	prefix    string
	states    map[string]*classify.SessionState
	now       time.Time
	runs      []int
	queues    [][]queued
}

type queued struct {
	doc    []byte
	sessID string
	agent  string
	labels bool
}

func newBacklogGen(t testing.TB, seed uint64, sessions int, prefix string) *backlogGen {
	return &backlogGen{
		t:         t,
		rng:       rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		scenarios: loadScenarios(t),
		prefix:    prefix,
		states:    map[string]*classify.SessionState{},
		now:       backlogStart,
		runs:      make([]int, sessions),
		queues:    make([][]queued, sessions),
	}
}

// sessionID is the spooled session id of generator session k; a scenario
// with two sessions (two-concurrent-sessions) maps its second onto a
// sibling.
func sessionID(k, nth int) string {
	if nth == 0 {
		return fmt.Sprintf("sess-%d", k)
	}
	return fmt.Sprintf("sess-%d-%d", k, nth)
}

// refill queues the next scenario for session k.
func (g *backlogGen) refill(k int) {
	sc := g.scenarios[g.rng.IntN(len(g.scenarios))]
	run := g.runs[k]
	g.runs[k]++
	ids := map[string]int{}
	for _, doc := range sc.docs {
		var head struct {
			SessionID string `json:"session_id"`
			AgentID   string `json:"agent_id"`
		}
		if err := json.Unmarshal(doc, &head); err != nil {
			g.t.Fatal(err)
		}
		nth, ok := ids[head.SessionID]
		if !ok {
			nth = len(ids)
			ids[head.SessionID] = nth
		}
		agent := ""
		if head.AgentID != "" {
			agent = fmt.Sprintf("%s-%s-%d-%d", g.prefix, head.AgentID, k, run)
		}
		g.queues[k] = append(g.queues[k], queued{doc: doc, sessID: sessionID(k, nth), agent: agent, labels: k%2 == 0})
	}
}

// next returns the next spooled line of the backlog.
func (g *backlogGen) next() []byte {
	for {
		k := g.rng.IntN(len(g.queues))
		if len(g.queues[k]) == 0 {
			g.refill(k)
		}
		q := g.queues[k][0]
		g.queues[k] = g.queues[k][1:]
		g.now = g.now.Add(time.Duration(1+g.rng.IntN(90)) * time.Second)

		in, err := classify.ParseHookInput(q.doc)
		if err != nil {
			g.t.Fatal(err)
		}
		in.SessionID = q.sessID
		if q.agent != "" {
			in.AgentID = q.agent
		}
		in.Now = g.now
		in.BridgeID = "brg_test0001"
		in.BridgeVersion = "0.0.0-test"
		in.SubagentsOn = true
		in.TaskLabelOn = q.labels
		st, ok := g.states[in.SessionID]
		if !ok {
			st = classify.NewSessionState()
			g.states[in.SessionID] = st
		}
		ev, ok := classify.Classify(in, st)
		if !ok {
			continue
		}
		line, err := json.Marshal(ev)
		if err != nil {
			g.t.Fatal(err)
		}
		return line
	}
}

func (g *backlogGen) take(n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = g.next()
	}
	return out
}

// replay applies lines to per-session models that start from priors (nil
// for "the relay has never seen these sessions") and returns them.
func replay(t testing.TB, priors map[string]sessionModel, lines [][]byte) map[string]sessionModel {
	t.Helper()
	out := map[string]sessionModel{}
	for id, m := range priors {
		out[id] = m.clone()
	}
	for _, l := range lines {
		e, res := parse(l)
		if res != parsedSession {
			t.Fatalf("fixture backlog line is not a plain session event: %s", l)
		}
		m := out[e.sessionID]
		m.apply(e)
		out[e.sessionID] = m
	}
	return out
}

// mainState is an independent, deliberately simple reading of SPEC 7.3 and
// ADR-006 section 3 for a session's main chain, in the style of
// tools/fixturecheck's computeFinalState: the state name, "ended" when the
// session was closed.
func mainState(lines [][]byte, sessionID string) string {
	state, ended := "idle", false
	for _, l := range lines {
		var e struct {
			SessionID string          `json:"session_id"`
			Type      string          `json:"type"`
			Subagent  json.RawMessage `json:"subagent"`
			Payload   struct {
				Reason string `json:"reason"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(l, &e); err != nil || e.SessionID != sessionID || e.Subagent != nil {
			continue
		}
		ended = false
		switch e.Type {
		case "session_start":
			state = "idle"
		case "prompt_submitted", "verification_finished", "activity":
			// activity's read/edit split is in the full model; here any
			// activity is "moving".
			state = "working"
		case "verification_started":
			state = "testing"
		case "needs_input":
			state = "needs_you"
		case "input_resolved", "pr_created", "commit":
			if state == "needs_you" {
				state = "working"
			}
		case "stop":
			state = "done"
		case "paused":
			state = "paused"
		case "session_end":
			if e.Payload.Reason == "other" && (state == "working" || state == "testing" || state == "needs_you") {
				state = "failed"
			} else {
				ended = true
			}
		}
	}
	if ended {
		return state + ", ended"
	}
	return state
}
