// Package coalesce shortens a spool backlog before "agentpulse flush"
// sends it (card P5-26, decision D81; BR-03, SPEC 11.6).
//
// The rule is deliberately small: keep every event, in spool order, except
// an `activity` event that is both older than StaleAfter and not the last
// event of its chain. Every other event type is always kept, because each
// can change what the relay shows or alerts on (a Done, a Needs You, a
// verification result, a helper starting or stopping, a pause), and only
// the relay knows which ones matter. An old `activity` event only records
// that work was happening back then; a later event of the same chain
// carries the same cumulative counters and supersedes it.
//
// The bridge keeps no model of the relay's state machine: a second copy
// would drift from the relay's, and a shorter sequence that reaches the
// same final state can still fire different alerts or write a different
// recap on the way.
package coalesce

import (
	"encoding/json"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

// StaleAfter is the relay's lost-contact window (RL-11): an activity
// event older than this no longer says anything about the present.
const StaleAfter = 10 * time.Minute

// chain identifies one line of work in a session: the main chain
// (helper "") or one helper chain, keyed by `subagent.id` (ADR-005). The
// last event of a session is also the last event of its chain, so keeping
// the last event of every chain keeps the last event of every session.
type chain struct {
	session string
	helper  string
}

// header is the part of a spooled line this package reads: the SPEC 10.1
// envelope fields that say which chain an event belongs to, when it
// happened, and its type. Nothing else is decoded.
type header struct {
	SessionID string `json:"session_id"`
	TS        string `json:"ts"`
	Type      string `json:"type"`
	Subagent  *struct {
		ID string `json:"id"`
	} `json:"subagent"`
}

// DropStaleActivity returns lines in their original order without the
// `activity` events older than StaleAfter at now that are not the last
// event of their chain. A line it cannot read, or whose session or time is
// missing or unparseable, is kept. lines itself is not modified.
func DropStaleActivity(lines [][]byte, now time.Time) [][]byte {
	chains := make([]*chain, len(lines))
	last := make(map[chain]int)
	for i, l := range lines {
		var h header
		if err := json.Unmarshal(l, &h); err != nil || h.SessionID == "" {
			continue
		}
		c := chain{session: h.SessionID}
		if h.Subagent != nil {
			c.helper = h.Subagent.ID
		}
		last[c] = i
		if h.Type != classify.TypeActivity {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, h.TS)
		if err != nil || now.Sub(ts) <= StaleAfter {
			continue
		}
		chains[i] = &c // a stale activity event: dropped unless last below
	}

	out := make([][]byte, 0, len(lines))
	for i, l := range lines {
		if c := chains[i]; c != nil && last[*c] != i {
			continue
		}
		out = append(out, l)
	}
	return out
}
