package coalesce

import (
	"maps"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

// This file is a model of what the relay keeps about one session, written
// from SPEC 7.3's event rows, ADR-005 section 4 (helper chains, as amended
// by D71 and D73) and ADR-006 section 3 (paused). Coalescing uses it for
// one question only: "does the relay end up in the same state without this
// event?". It is not the relay's implementation, and it leaves out
// everything that does not depend on which events arrive: the timers of
// SPEC 7.3 (lost contact, Done to Idle and the rest), which the relay
// applies to the final state the same way either way.

// maxChains is ADR-005 section 4's limit of helper chains per session.
const maxChains = 8

// verification is SPEC 7.3's "records outcome and counts".
type verification struct {
	kind, runner, outcome string
	passed, failed, total int // -1 when absent
	at                    int64
}

// agentState is one chain's state: the session row for the main chain, or
// one helper's row. Every field is something the relay stores and the
// roster, a bubble, an alert or a later transition reads. Every field is
// comparable, so two states compare with ==.
type agentState struct {
	state             string
	ended             bool
	endedAt           int64
	waitKind          string
	waitTool          string
	pausedCause       string
	startedAt         int64
	lastEventAt       int64
	lastStateChangeAt int64
	hasVerification   bool
	lastVerification  verification
	hasVerifyStarted  bool
	lastVerifyStarted int64
	counters          classify.Counters
	hasTaskLabel      bool
	taskLabel         string
	hasPrompted       bool
	lastActivity      string
}

// helperRow is a live helper chain (ADR-005 section 4).
type helperRow struct {
	typ     string
	ordinal int
	st      agentState
}

// sessionModel is everything the relay keeps about one session that the
// session's own events decide.
type sessionModel struct {
	exists  bool
	st      agentState
	helpers map[string]helperRow
	// stopped holds the ids of helpers whose subagent_stop was applied
	// (D73: later events for them are history only).
	stopped map[string]bool
}

func newAgent(at int64) agentState {
	return agentState{state: "idle", startedAt: at, lastEventAt: at, lastStateChangeAt: at}
}

// newHelper is a helper chain's first state: working (ADR-005 section 4).
func newHelper(at int64) agentState {
	s := newAgent(at)
	s.state = "working"
	return s
}

func (m sessionModel) clone() sessionModel {
	m.helpers = maps.Clone(m.helpers)
	m.stopped = maps.Clone(m.stopped)
	return m
}

func (m sessionModel) equal(o sessionModel) bool {
	return m.exists == o.exists && m.st == o.st &&
		maps.Equal(m.helpers, o.helpers) && maps.Equal(m.stopped, o.stopped)
}

// apply advances the model by one event.
func (m *sessionModel) apply(e *event) {
	if e.chain != "" {
		m.applyHelper(e)
		return
	}
	before := newAgent(e.at)
	if m.exists {
		before = m.st
	}
	after := transition(before, e, max(e.at, before.lastEventAt))
	if clearsHelpers(before, after) {
		m.helpers = nil
	}
	m.exists = true
	m.st = after
}

// applyHelper applies an event carrying `subagent`: it moves that helper's
// chain and keeps the session in contact without moving its state (D71).
// A start creates the chain and a stop deletes it and remembers its id;
// neither touches the session. An event for a stopped helper, and a start
// or stop while the session is ended, change nothing.
func (m *sessionModel) applyHelper(e *event) {
	lifecycle := e.typ == classify.TypeSubagentStart || e.typ == classify.TypeSubagentStop
	ended := m.exists && m.st.ended
	stopped := m.exists && m.stopped[e.chain]
	if !m.exists {
		m.exists = true
		m.st = newAgent(e.at)
	}
	if stopped || (ended && lifecycle) {
		return
	}
	if e.typ == classify.TypeSubagentStop {
		delete(m.helpers, e.chain)
		if m.stopped == nil {
			m.stopped = map[string]bool{}
		}
		m.stopped[e.chain] = true
		return
	}

	row, live := m.helpers[e.chain]
	if !live && len(m.helpers) >= maxChains {
		m.evictStalest()
	}
	room := live || len(m.helpers) < maxChains
	ordinal := row.ordinal
	if !live || row.typ != e.chainType {
		ordinal = m.lowestFreeOrdinal(e.chain, e.chainType)
	}
	if m.helpers == nil {
		m.helpers = map[string]helperRow{}
	}

	if e.typ == classify.TypeSubagentStart {
		if !live && room {
			m.helpers[e.chain] = helperRow{typ: e.chainType, ordinal: ordinal, st: newHelper(e.at)}
		}
		return
	}

	// touch: contact and counters follow the event, the state does not.
	at := max(e.at, m.st.lastEventAt)
	if m.st.ended {
		m.st.ended, m.st.endedAt, m.st.lastStateChangeAt = false, 0, at
	}
	m.st.counters = e.counters
	m.st.lastEventAt = max(m.st.lastEventAt, at)
	if !room {
		return
	}

	chainBefore := newHelper(e.at)
	if live {
		chainBefore = row.st
	}
	chainAfter := transition(chainBefore, e, max(e.at, chainBefore.lastEventAt))
	m.helpers[e.chain] = helperRow{typ: e.chainType, ordinal: ordinal, st: chainAfter}
}

// evictStalest removes the helper with the earliest last event that is not
// waiting on the user (D73 (2)); ties go to the smaller id so the model is
// deterministic.
func (m *sessionModel) evictStalest() {
	victim := ""
	for id, row := range m.helpers {
		if row.st.state == "needs_you" {
			continue
		}
		if victim == "" {
			victim = id
			continue
		}
		v := m.helpers[victim].st.lastEventAt
		if row.st.lastEventAt < v || (row.st.lastEventAt == v && id < victim) {
			victim = id
		}
	}
	if victim != "" {
		delete(m.helpers, victim)
	}
}

// lowestFreeOrdinal is the helper label's number: the lowest positive one
// no other live helper of the same type holds.
func (m *sessionModel) lowestFreeOrdinal(id, typ string) int {
	held := map[int]bool{}
	for other, row := range m.helpers {
		if other != id && row.typ == typ {
			held[row.ordinal] = true
		}
	}
	n := 1
	for held[n] {
		n++
	}
	return n
}

// clearsHelpers is ADR-005 section 4's list of main-chain changes that
// delete every helper chain: the session ends, restarts, fails or goes
// idle.
func clearsHelpers(before, after agentState) bool {
	return after.ended ||
		after.startedAt != before.startedAt ||
		(before.state != "failed" && after.state == "failed") ||
		(before.state != "idle" && after.state == "idle")
}

func isActive(state string) bool {
	switch state {
	case "working", "reading", "testing", "fixing", "needs_you":
		return true
	}
	return false
}

// isFixing is SPEC 7.3's "last_verification_outcome == fail and no
// verification since".
func isFixing(s agentState) bool {
	if !s.hasVerification || s.lastVerification.outcome != "fail" {
		return false
	}
	return !s.hasVerifyStarted || s.lastVerifyStarted <= s.lastVerification.at
}

// toState sets a state and clears Needs You.
func toState(s *agentState, state string) {
	s.state = state
	s.waitKind, s.waitTool = "", ""
}

// transition applies one event to one chain: SPEC 7.3's event rows with
// ADR-006 section 3's paused rows. at is the event's effective instant.
func transition(before agentState, e *event, at int64) agentState {
	s := before
	// ERR-08: an event for an ended session re-opens it.
	s.ended, s.endedAt = false, 0

	switch e.typ {
	case classify.TypeSessionStart:
		toState(&s, "idle")
		s.hasVerification, s.lastVerification = false, verification{}
		s.hasVerifyStarted, s.lastVerifyStarted = false, 0
		s.startedAt = at
		s.hasPrompted = false
		s.lastActivity = ""
	case classify.TypePromptSubmitted:
		// ADR-006 section 3: a continuation without a label keeps the
		// stored one.
		if !(s.state == "paused" && e.taskLabel == nil) {
			s.hasTaskLabel = e.taskLabel != nil
			s.taskLabel = ""
			if e.taskLabel != nil {
				s.taskLabel = *e.taskLabel
			}
		}
		toState(&s, "working")
		s.hasPrompted = true
	case classify.TypeActivity:
		switch e.category {
		case "read":
			toState(&s, "reading")
		case "edit":
			if isFixing(s) {
				toState(&s, "fixing")
			} else {
				toState(&s, "working")
			}
		default:
			toState(&s, "working")
		}
		s.lastActivity = e.category
	case classify.TypeVerificationStarted:
		toState(&s, "testing")
		s.hasVerifyStarted, s.lastVerifyStarted = true, at
	case classify.TypeVerificationFinished:
		toState(&s, "working")
		s.hasVerification = true
		s.lastVerification = verification{
			kind: e.kind, runner: e.runner, outcome: e.outcome,
			passed: e.passed, failed: e.failed, total: e.total, at: at,
		}
	case classify.TypeNeedsInput:
		s.state = "needs_you"
		s.waitKind, s.waitTool = e.kind, e.toolCategory
	case classify.TypeInputResolved, classify.TypePRCreated, classify.TypeCommit:
		// Out of needs_you these clear Needs You (SPEC 7.2's clearing rule
		// for pr_created and commit); anywhere else, paused included, they
		// change nothing.
		if s.state == "needs_you" {
			toState(&s, "working")
		}
	case classify.TypeStop:
		toState(&s, "done")
	case classify.TypePaused:
		toState(&s, "paused")
		s.pausedCause = e.cause
	case classify.TypeSessionEnd:
		switch {
		case e.reason != "other":
			s.ended, s.endedAt = true, at
		case isActive(s.state):
			// Failed; the relay's 2-hour timer ends it later.
			s.state = "failed"
		default:
			// done, idle, failed and paused end at once.
			s.ended, s.endedAt = true, at
		}
	}
	if s.state != "paused" {
		s.pausedCause = ""
	}

	// Every event carries the cumulative counters (SPEC 10.1).
	s.counters = e.counters
	s.lastEventAt = max(s.lastEventAt, at)
	if s.state != before.state || s.ended != before.ended {
		s.lastStateChangeAt = at
	}
	return s
}
