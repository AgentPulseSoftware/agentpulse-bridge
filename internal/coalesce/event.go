package coalesce

import (
	"encoding/json"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

// event is what coalescing reads from one spooled line: exactly the fields
// the relay's state machine reads (SPEC 7.3, ADR-005 section 4, ADR-006
// section 3), nothing else. raw is the line itself, which is what is sent;
// coalescing never re-encodes or edits an event.
type event struct {
	raw       []byte
	sessionID string
	at        int64 // ts, Unix milliseconds
	typ       string
	chain     string // subagent.id; "" for the main chain
	chainType string // subagent.type
	counters  classify.Counters

	category     string // activity
	kind         string // verification_* and needs_input
	runner       string
	outcome      string
	passed       int // -1 when absent
	failed       int // -1 when absent
	total        int // -1 when absent
	toolCategory string  // needs_input
	taskLabel    *string // prompt_submitted
	reason       string  // session_end
	cause        string  // paused
}

// wireEvent is the subset of SPEC 10.1's envelope and payloads that event
// needs. Payload fields that share a name across types (kind) share a
// field here.
type wireEvent struct {
	SessionID string `json:"session_id"`
	TS        string `json:"ts"`
	Type      string `json:"type"`
	Subagent  *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"subagent"`
	Counters classify.Counters `json:"counters"`
	Payload  struct {
		Category     string  `json:"category"`
		Kind         string  `json:"kind"`
		Runner       string  `json:"runner"`
		Outcome      string  `json:"outcome"`
		Passed       *int    `json:"passed"`
		Failed       *int    `json:"failed"`
		Total        *int    `json:"total"`
		ToolCategory string  `json:"tool_category"`
		TaskLabel    *string `json:"task_label"`
		Reason       string  `json:"reason"`
		Cause        string  `json:"cause"`
	} `json:"payload"`
}

// mainOnly are the types that never carry `subagent` (ADR-005 section 1,
// ADR-006 section 1); the relay rejects one that does.
var mainOnly = map[string]bool{
	classify.TypeSessionStart:    true,
	classify.TypePromptSubmitted: true,
	classify.TypeStop:            true,
	classify.TypeSessionEnd:      true,
	classify.TypePaused:          true,
}

// eitherChain are the types allowed with or without `subagent`.
var eitherChain = map[string]bool{
	classify.TypeActivity:             true,
	classify.TypeVerificationStarted:  true,
	classify.TypeVerificationFinished: true,
	classify.TypeNeedsInput:           true,
	classify.TypeInputResolved:        true,
	classify.TypePRCreated:            true,
	classify.TypeCommit:               true,
}

// parseResult says what a spooled line is, for coalescing purposes.
type parseResult int

const (
	// parsedSession is an event of a known type for a known session, in a
	// shape the relay accepts: it takes part in coalescing.
	parsedSession parseResult = iota
	// parsedOpaqueSession belongs to a session but is not something the
	// model understands (a type this bridge does not know, or a shape the
	// relay would reject). Its whole session is then sent as it is.
	parsedOpaqueSession
	// parsedStandalone belongs to no session (project_seen, or a line that
	// does not parse at all). It is always sent as it is.
	parsedStandalone
)

// parse reads one spooled line.
func parse(line []byte) (*event, parseResult) {
	var w wireEvent
	if err := json.Unmarshal(line, &w); err != nil {
		return nil, parsedStandalone
	}
	if w.Type == classify.TypeProjectSeen || w.SessionID == "" {
		return nil, parsedStandalone
	}
	ts, err := time.Parse(time.RFC3339Nano, w.TS)
	if err != nil {
		return &event{sessionID: w.SessionID}, parsedOpaqueSession
	}
	e := &event{
		raw:          line,
		sessionID:    w.SessionID,
		at:           ts.UnixMilli(),
		typ:          w.Type,
		counters:     w.Counters,
		category:     w.Payload.Category,
		kind:         w.Payload.Kind,
		runner:       w.Payload.Runner,
		outcome:      w.Payload.Outcome,
		passed:       intOrAbsent(w.Payload.Passed),
		failed:       intOrAbsent(w.Payload.Failed),
		total:        intOrAbsent(w.Payload.Total),
		toolCategory: w.Payload.ToolCategory,
		taskLabel:    w.Payload.TaskLabel,
		reason:       w.Payload.Reason,
		cause:        w.Payload.Cause,
	}
	if w.Subagent != nil {
		e.chain, e.chainType = w.Subagent.ID, w.Subagent.Type
	}

	switch {
	case mainOnly[e.typ]:
		if w.Subagent != nil {
			return e, parsedOpaqueSession
		}
	case eitherChain[e.typ]:
	case e.typ == classify.TypeSubagentStart || e.typ == classify.TypeSubagentStop:
		if w.Subagent == nil {
			return e, parsedOpaqueSession
		}
	default:
		return e, parsedOpaqueSession
	}
	if w.Subagent != nil && e.chain == "" {
		return e, parsedOpaqueSession
	}
	return e, parsedSession
}

func intOrAbsent(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
