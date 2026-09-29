// Package coalesce shrinks a spool backlog to the events the relay needs to
// reach the same current state (card P5-26; BR-03, SPEC 11.6).
//
// When the spool has fallen behind (a 429, a network outage, a machine that
// slept), replaying hours of history in order keeps the phone behind by
// just as long, and the relay's rate limit (RL-04) makes the replay slower
// still. Backlog removes the events whose effect a later event overwrites,
// so the relay ends up, per session and per helper chain, in the state a
// full in-order replay would leave it in, usually within one batch.
//
// # What "the same state" means
//
// For every session, the relay's session row and its helper chains
// (ADR-005 section 4) end up identical: state, Needs You and what it is
// waiting on, the paused cause (ADR-006), the ended flag and when it ended,
// the last verification's outcome and counts and whether a fix is under
// way, the task label, whether a prompt was seen, the last activity
// category, the cumulative counters, and the times of the session's start,
// last event and last state change. Each live helper keeps its type, label
// number and the same fields, and each stopped helper stays stopped.
// model.go holds this model; it is checked against several possible prior
// relay states per session (priorsFor), because the relay may already hold
// state the bridge cannot see.
//
// What is not kept is history: the relay's history rows, and so the "Since
// you left" lines built from them, have fewer entries for the coalesced
// span (fewer "Ran tests" runs, for example). The name the relay picks for
// a session it has never seen can also differ when several new sessions
// start and end within one backlog. Both are the price of catching up.
//
// # Which events survive
//
// The rule is greedy: from the oldest event forward, an event is dropped
// when the session's final state is the same without it, under every prior
// state tried; otherwise it is kept. So an event survives only when it
// defines current state. In practice that is: the session's last event
// (counters, last contact); the session_start and session_end that define
// its start and end; the event that set the current state and the one that
// began its current run; the last prompt (task label), the last
// verification start and result (Fixing, counts), and the last activity
// (bubble); for each live helper, its start and the events that define its
// state; for each helper that stopped, its stop. Three types always
// survive regardless: paused (ADR-006's risk list: a pause must never be
// coalesced away), pr_created and commit (rare milestones the phone shows
// in "Since you left" and that no later event restates).
//
// Events the model does not understand (a type this bridge does not know,
// or a shape the relay would reject) make coalescing leave that whole
// session as it is. Lines that belong to no session (project_seen, or a
// line that does not parse) are always kept. Kept events stay in spool
// order and are never edited.
package coalesce

import (
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
)

// StaleAfter is the relay's lost-contact window (SPEC 7.3): a spool whose
// oldest event is older than this is a backlog even when it is short.
const StaleAfter = 10 * time.Minute

// alwaysKept are the types that survive coalescing even when the final
// state does not need them (see the package comment).
var alwaysKept = map[string]bool{
	classify.TypePaused:    true,
	classify.TypePRCreated: true,
	classify.TypeCommit:    true,
}

// IsBacklog reports whether lines are a backlog: more than one batch
// (relay.MaxEventsPerChunk), or an oldest event older than StaleAfter at
// now. A short, fresh spool, which is every flush while the relay keeps
// up, is not.
func IsBacklog(lines [][]byte, now time.Time) bool {
	if len(lines) > relay.MaxEventsPerChunk {
		return true
	}
	cutoff := now.Add(-StaleAfter).UnixMilli()
	for _, l := range lines {
		if e, res := parse(l); res == parsedSession && e.at < cutoff {
			return true
		}
	}
	return false
}

// Backlog returns lines unchanged unless they are a backlog (IsBacklog);
// then it returns the subsequence the package comment describes, in the
// same order. It never adds, edits or reorders a line.
func Backlog(lines [][]byte, now time.Time) [][]byte {
	if !IsBacklog(lines, now) {
		return lines
	}

	keep := make([]bool, len(lines))
	sessions := map[string][]int{} // session id -> line indices, in order
	opaque := map[string]bool{}
	var order []string
	parsed := make([]*event, len(lines))
	for i, l := range lines {
		e, res := parse(l)
		switch res {
		case parsedStandalone:
			keep[i] = true
			continue
		case parsedOpaqueSession:
			opaque[e.sessionID] = true
		}
		parsed[i] = e
		if _, seen := sessions[e.sessionID]; !seen {
			order = append(order, e.sessionID)
		}
		sessions[e.sessionID] = append(sessions[e.sessionID], i)
	}

	for _, id := range order {
		idx := sessions[id]
		if opaque[id] || !inTimeOrder(parsed, idx) {
			for _, i := range idx {
				keep[i] = true
			}
			continue
		}
		evs := make([]*event, len(idx))
		for k, i := range idx {
			evs[k] = parsed[i]
		}
		for k, kept := range minimize(evs) {
			if kept {
				keep[idx[k]] = true
			}
		}
	}

	out := make([][]byte, 0, len(lines))
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}

// inTimeOrder reports whether a session's events are in timestamp order.
// The relay applies a batch in timestamp order; the model applies events
// in spool order. The two agree when the spool is in time order, which it
// is unless the clock was set back mid-session; such a session is left as
// it is rather than coalesced on a guess.
func inTimeOrder(parsed []*event, idx []int) bool {
	for k := 1; k < len(idx); k++ {
		if parsed[idx[k]].at < parsed[idx[k-1]].at {
			return false
		}
	}
	return true
}

// minimize returns, for one session's events in order, which to keep: from
// the oldest forward, an event is dropped when replaying the rest yields
// the same final model under every prior (priorsFor).
func minimize(evs []*event) []bool {
	priors := priorsFor(evs)
	want := make([]sessionModel, len(priors))
	for p, prior := range priors {
		m := prior.clone()
		for _, e := range evs {
			m.apply(e)
		}
		want[p] = m
	}

	keep := make([]bool, len(evs))
	// prefix[p] is prior p after every event kept so far.
	prefix := make([]sessionModel, len(priors))
	for p, prior := range priors {
		prefix[p] = prior.clone()
	}
	for i, e := range evs {
		if alwaysKept[e.typ] || !droppable(prefix, evs[i+1:], want) {
			keep[i] = true
			for p := range prefix {
				prefix[p].apply(e)
			}
		}
	}
	return keep
}

// droppable reports whether applying rest to every prefix still reaches
// want, that is, whether the event before rest can be left out.
func droppable(prefix []sessionModel, rest []*event, want []sessionModel) bool {
	for p := range prefix {
		m := prefix[p].clone()
		for _, e := range rest {
			m.apply(e)
		}
		if !m.equal(want[p]) {
			return false
		}
	}
	return true
}

// priorsFor returns the relay states a session might be in before its
// backlog arrives. The bridge cannot know which, so an event is only
// dropped when it is redundant from all of them: the session unknown to
// the relay, and known in each of the states a later event reads (Needs
// You, paused and ended, failed, done, idle), with and without live,
// stopped or crowding helpers, a failing or passing last verification, and
// a stored task label.
func priorsFor(evs []*event) []sessionModel {
	t0 := evs[0].at
	for _, e := range evs {
		t0 = min(t0, e.at)
	}
	t0 -= time.Hour.Milliseconds()
	var helperIDs []string
	seen := map[string]bool{}
	for _, e := range evs {
		if e.chain != "" && !seen[e.chain] {
			seen[e.chain] = true
			helperIDs = append(helperIDs, e.chain)
		}
	}
	failing := verification{kind: "test", runner: "prior", outcome: "fail", passed: 1, failed: 2, total: 3, at: t0}
	passing := verification{kind: "build", runner: "prior", outcome: "pass", passed: -1, failed: -1, total: -1, at: t0}

	known := func(state string) sessionModel {
		s := newAgent(t0)
		s.state = state
		s.hasTaskLabel, s.taskLabel = true, "prior"
		s.hasPrompted = true
		s.lastActivity = "edit"
		return sessionModel{exists: true, st: s}
	}

	// Waiting on the user, a failing run to fix, and every helper of the
	// backlog already live and waiting too.
	needsYou := known("needs_you")
	needsYou.st.waitKind, needsYou.st.waitTool = "permission", "command"
	needsYou.st.hasVerification, needsYou.st.lastVerification = true, failing
	needsYou.helpers = map[string]helperRow{}
	for i, id := range helperIDs {
		if i == maxChains {
			break
		}
		h := newHelper(t0)
		h.state = "needs_you"
		h.waitKind = "question"
		h.hasVerification, h.lastVerification = true, failing
		needsYou.helpers[id] = helperRow{typ: "prior", ordinal: i + 1, st: h}
	}

	// Paused, then closed.
	paused := known("paused")
	paused.st.pausedCause = "usage_limit"
	paused.st.ended, paused.st.endedAt = true, t0

	// Failed, with every helper of the backlog already stopped.
	failed := known("failed")
	failed.stopped = map[string]bool{}
	for _, id := range helperIDs {
		failed.stopped[id] = true
	}

	// Finished, with a full set of other helpers still running.
	done := known("done")
	done.helpers = map[string]helperRow{}
	for i := range maxChains {
		id := "prior" + string(rune('a'+i))
		done.helpers[id] = helperRow{typ: "prior", ordinal: i + 1, st: newHelper(t0)}
	}

	// Idle after a passing build, no label, nothing prompted.
	idle := known("idle")
	idle.st.hasTaskLabel, idle.st.taskLabel, idle.st.hasPrompted = false, "", false
	idle.st.hasVerification, idle.st.lastVerification = true, passing

	return []sessionModel{{}, needsYou, paused, failed, done, idle}
}
