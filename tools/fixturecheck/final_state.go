package main

// computeFinalState applies SPEC 7.3's relay-side state machine to a flat
// sequence of bridge events, purely to check a fixture scenario's own
// hand-authored "final_state" against what its events actually imply — it
// is not, and must not become, the relay's own implementation, which is
// written from the same spec section independently.
//
// Only the event-driven rows of SPEC 7.3's table are modeled: idle,
// working, reading, fixing, testing, needs_you, and done, plus the
// session_end reason mapping. The table's time-based rows (state changes
// triggered purely by elapsed wall-clock time) do not apply here: a
// fixture replay has no meaningful elapsed time between events, so those
// rows would never fire during one run of "make fixtures" regardless.
//
// A scenario spanning two sessions (two-concurrent-sessions) still gets a
// single flat run through this function in file order, exactly like
// compareEvents does for the events themselves: the two sessions'
// transitions interleave, but as long as each transition on its own maps
// to the same state SPEC 7.3 would give it — which they do for every
// scenario in this corpus so far, read/edit/stop being independent of
// which session emitted them — the flattened result matches. A future
// scenario that genuinely needs its two sessions to diverge in final
// state would need this tool to track state per session_id instead; not
// needed yet.
func computeFinalState(events []actualEvent) string {
	state := "idle"
	lastVerificationOutcome := ""

	for _, ev := range events {
		switch ev.Type {
		case "session_start":
			state = "idle"
		case "prompt_submitted":
			state = "working"
		case "activity":
			category, _ := ev.Payload["category"].(string)
			switch category {
			case "read":
				state = "reading"
			case "edit":
				if lastVerificationOutcome == "fail" {
					state = "fixing"
				} else {
					state = "working"
				}
			default:
				state = "working"
			}
		case "verification_started":
			state = "testing"
		case "verification_finished":
			state = "working"
			if outcome, ok := ev.Payload["outcome"].(string); ok {
				lastVerificationOutcome = outcome
			}
		case "needs_input":
			state = "needs_you"
		case "input_resolved":
			state = "working"
		case "pr_created", "commit":
			// Unchanged state; history only (SPEC 7.3).
		case "stop":
			state = "done"
		case "session_end":
			reason, _ := ev.Payload["reason"].(string)
			switch reason {
			case "clear", "logout", "prompt_input_exit":
				state = "ended"
			default: // "other" or missing
				switch state {
				case "working", "reading", "testing", "fixing", "needs_you":
					state = "failed"
				default: // done, idle
					state = "ended"
				}
			}
		}
	}

	return state
}
