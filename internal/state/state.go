// Package state persists the bridge's mutable runtime state (as opposed
// to internal/config's operator-set configuration) at
// $XDG_STATE_HOME/agentpulse/state.json, mode 0600, written atomically.
//
// Consecutive401 and UnpairedReason are written by cmd/agentpulse/flush.go
// and read there, by cmd/agentpulse/status.go, and by internal/doctor;
// RequiredBridgeVersion likewise.
// LastEvent and LastFlush: "agentpulse status" (BR-14) reads them to
// print pairing state and the last event sent.
package state

import (
	"encoding/json"
	"os"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/atomicfile"
)

// LastEvent is the most recently delivered event's identity (BR-14): just
// enough for "agentpulse status" to print something meaningful, never the
// event's payload or counters.
type LastEvent struct {
	TS        string `json:"ts"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// LastFlush is the outcome of the most recent "agentpulse flush" run.
// Outcome is a short, fixed word (see the Outcome* constants), not a free
// text error message — a flush failure must never end up logging or
// storing relay response bodies (BR-19's spirit) or arbitrary Go error
// text that might embed one.
type LastFlush struct {
	TS         string `json:"ts"`
	Outcome    string `json:"outcome"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

// Outcome values for LastFlush.Outcome.
const (
	OutcomeOK           = "ok"            // every spooled event was accepted (or nothing was spooled)
	OutcomeUnpaired     = "unpaired"      // no bridge_id/credential to flush with
	OutcomeUnauthorized = "unauthorized"  // the relay rejected the credential
	OutcomeRateLimited  = "rate_limited"  // 429, gave up within budget
	OutcomeSchemaError  = "schema_error"  // 400 the relay could not accept
	OutcomeServerError  = "server_error"  // 5xx after retries
	OutcomeNetworkError = "network_error" // connection/timeout after retries
	OutcomeBudgetCutOff = "budget_cutoff" // the 5s budget ran out mid-retry
)

// State is the full record of the bridge's mutable runtime state.
type State struct {
	Consecutive401        int        `json:"consecutive_401,omitempty"`
	UnpairedReason        string     `json:"unpaired_reason,omitempty"`
	RequiredBridgeVersion string     `json:"required_bridge_version,omitempty"`
	LastEvent             *LastEvent `json:"last_event,omitempty"`
	LastFlush             *LastFlush `json:"last_flush,omitempty"`
}

// Load reads state.json at path, tolerantly: a missing or unparseable
// file yields the zero value (no 401s yet, no reason, no version
// mismatch, nothing sent yet) rather than an error propagated to a
// caller that must never fail because local state was unreadable —
// matching internal/config.Load's contract.
func Load(path string) State {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the bridge's own XDG state dir, not attacker input
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// Save writes state.json atomically, mode 0600 (BR-05). A failure is
// returned as an *atomicfile.WriteError naming path (ERR-02): callers
// that cannot afford to fail because of it (every "agentpulse flush" call
// site) swallow it, logging at most; "status" and "doctor"
// are the callers that render it as FAIL.
func Save(path string, s State) error {
	return atomicfile.WriteJSON(path, s, 0o600)
}
