package main

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// subagentFallbackPeriod is how long subagent marking stays off after the
// relay rejected an event carrying the `subagent` field (ADR-005 section
// 8: a relay rolled back to a version that does not know the field).
// ADR-005 originally ended the pause when the relay's `subagents` setting
// arrived again; D72 removed that setting, so the bridge tries again after
// this long instead, costing at most one rejected request per period.
const subagentFallbackPeriod = time.Hour

// subagentKey is how the `subagent` field appears in a marshaled event.
// Only the field itself can put this exact byte sequence in a spooled
// line (the type names subagent_start and subagent_stop continue past the
// word, and nothing else in the closed schema holds free text), so it is
// a safe fast path before decoding.
var subagentKey = []byte(`"subagent"`)

// subagentsOn is what "agentpulse hook" sets HookInput.SubagentsOn to:
// true unless the fallback period is running.
func subagentsOn(now time.Time) bool {
	return !subagentsPaused(state.Load(xdgpaths.StatePath()), now)
}

func subagentsPaused(s state.State, now time.Time) bool {
	if s.SubagentsOffUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, s.SubagentsOffUntil)
	return err == nil && now.Before(until)
}

// splitSubagentRejections separates, among the events a relay 400 named
// as invalid, those that carry `subagent`. They are not dropped: the
// caller strips the field and sends them once more (ADR-005 section 8).
// The rest are returned to be dropped as ERR-03 requires.
func splitSubagentRejections(cur [][]byte, dropped []relay.DroppedEvent) (rest []relay.DroppedEvent, rejected int) {
	for _, d := range dropped {
		if d.Index >= 0 && d.Index < len(cur) && bytes.Contains(cur[d.Index], subagentKey) {
			rejected++
			continue
		}
		rest = append(rest, d)
	}
	return rest, rejected
}

// stripSubagents returns lines with the `subagent` field removed and
// every subagent_start and subagent_stop line dropped (they mean nothing
// without it). A line without the field is kept byte for byte, and so is
// one that does not decode (the relay's ERR-03 handling deals with it).
func stripSubagents(lines [][]byte) [][]byte {
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if !bytes.Contains(line, subagentKey) {
			out = append(out, line)
			continue
		}
		var ev map[string]json.RawMessage
		if err := json.Unmarshal(line, &ev); err != nil {
			out = append(out, line)
			continue
		}
		var typ string
		_ = json.Unmarshal(ev["type"], &typ)
		if typ == classify.TypeSubagentStart || typ == classify.TypeSubagentStop {
			continue
		}
		delete(ev, "subagent")
		stripped, err := json.Marshal(ev)
		if err != nil {
			continue // cannot happen: every value is already valid JSON
		}
		out = append(out, stripped)
	}
	return out
}
