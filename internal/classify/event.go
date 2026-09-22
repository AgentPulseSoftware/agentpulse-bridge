// Package classify turns one Claude Code hook document into zero or one
// normalized AgentPulse bridge events, per SPEC 7.2 (hook classification),
// 7.5 (verification detection), 10.1 (the event schema), BR-02
// (classification budget), BR-05 (session scratch), BR-13 (project
// derivation), BR-17 (task labels), and BR-19 (no path, command, output, or
// prompt content ever leaves the machine).
//
// Classify is a pure function of (HookInput, *SessionState): everything it
// needs beyond the hook's own JSON — the current time, the bridge's own id,
// version, and whether task labels are enabled — travels in HookInput as
// caller-supplied context (see hookinput.go), so tests can drive it
// deterministically without touching the filesystem or the clock.
package classify

// Event is one normalized AgentPulse bridge event (SPEC 10.1, schema
// version 1). Every event this package builds is validated in
// classify_test.go (assertValidEvent, schema_test.go) against the
// AgentPulse `event.v1` schema, which sets additionalProperties: false on
// every variant: that is what actually guarantees every field here, and
// nothing more, is what the schema allows. There is deliberately no field
// anywhere in this file, or in any Payload type below, that can carry a
// file path, a shell command, tool output, or prompt text (BR-19) — see
// also TestEventStructHasNoForbiddenFields (br19_test.go) for a narrower,
// reflection-based check on today's known types.
type Event struct {
	Schema    int      `json:"schema"`
	EventID   string   `json:"event_id"`
	BridgeID  string   `json:"bridge_id"`
	SessionID string   `json:"session_id"`
	Project   Project  `json:"project"`
	TS        string   `json:"ts"`
	Type      string   `json:"type"`
	Counters  Counters `json:"counters"`
	Payload   any      `json:"payload"`
}

// Project identifies the repository or directory a session ran in without
// ever naming it (BR-13): only a hash of the derived project key, and a
// display name truncated to what the schema allows.
type Project struct {
	KeyHash string `json:"key_hash"`
	Name    string `json:"name"`
}

// Counters are the cumulative per-session totals every event carries
// (SPEC 7.2's "Counters" paragraph): distinct files read and edited (by
// path hash, never the path itself), verification runs, and commits.
type Counters struct {
	FilesRead        int `json:"files_read"`
	FilesEdited      int `json:"files_edited"`
	VerificationRuns int `json:"verification_runs"`
	Commits          int `json:"commits"`
}

// Event type constants (SPEC 10.1's `type` enum).
const (
	TypeSessionStart         = "session_start"
	TypePromptSubmitted      = "prompt_submitted"
	TypeActivity             = "activity"
	TypeVerificationStarted  = "verification_started"
	TypeVerificationFinished = "verification_finished"
	TypeNeedsInput           = "needs_input"
	TypeInputResolved        = "input_resolved"
	TypePRCreated            = "pr_created"
	TypeCommit               = "commit"
	TypeStop                 = "stop"
	TypeSessionEnd           = "session_end"
	// TypeProjectSeen is never produced by Classify itself: BR-10's
	// throttled substitute for a dropped, unwatched project's event,
	// built by cmd/agentpulse (internal/watch's gating) out of an
	// already-classified event's envelope.
	TypeProjectSeen = "project_seen"
)

// Payload types, one per event `type` (SPEC 10.1's payload table). Each
// mirrors its schema variant exactly — same fields, same optionality — so
// that marshaling a Go struct here can never produce a field the schema's
// `additionalProperties: false` would reject.

// SessionStartPayload is the payload for TypeSessionStart.
type SessionStartPayload struct {
	Agent         string `json:"agent"`
	BridgeVersion string `json:"bridge_version"`
	OS            string `json:"os"`
}

// PromptSubmittedPayload is the payload for TypePromptSubmitted.
type PromptSubmittedPayload struct {
	TaskLabel string `json:"task_label,omitempty"`
}

// Activity categories (SPEC 10.1's `activity` payload `category` enum).
const (
	CategoryRead  = "read"
	CategoryEdit  = "edit"
	CategoryOther = "other"
)

// ActivityPayload is the payload for TypeActivity.
type ActivityPayload struct {
	Category string `json:"category"`
}

// Verification kinds (SPEC 10.1's `kind` enum for verification_started and
// verification_finished).
const (
	KindTest  = "test"
	KindBuild = "build"
)

// Verification outcomes (SPEC 10.1's `outcome` enum).
const (
	OutcomePass    = "pass"
	OutcomeFail    = "fail"
	OutcomeUnknown = "unknown"
)

// VerificationStartedPayload is the payload for TypeVerificationStarted.
type VerificationStartedPayload struct {
	Kind   string `json:"kind"`
	Runner string `json:"runner"`
}

// VerificationFinishedPayload is the payload for TypeVerificationFinished.
// Passed, Failed, and Total are omitted together whenever SPEC 7.5's count
// rule (every captured group parsed as an integer and
// passed+failed <= total) isn't satisfied — see finalizeCounts in
// verify_result.go. duration_ms is never populated: verification
// duration is not measured.
type VerificationFinishedPayload struct {
	Kind    string `json:"kind"`
	Runner  string `json:"runner"`
	Outcome string `json:"outcome"`
	Passed  *int   `json:"passed,omitempty"`
	Failed  *int   `json:"failed,omitempty"`
	Total   *int   `json:"total,omitempty"`
}

// needs_input kinds (SPEC 10.1's `kind` enum).
const (
	NeedsInputPermission = "permission"
	NeedsInputQuestion   = "question"
	NeedsInputPlan       = "plan"
)

// tool_category values (SPEC 10.1's `tool_category` enum).
const (
	ToolCategoryCommand = "command"
	ToolCategoryFile    = "file"
	ToolCategoryWeb     = "web"
	ToolCategoryOther   = "other"
)

// NeedsInputPayload is the payload for TypeNeedsInput. ToolCategory is
// only ever populated for kind == permission (SPEC 7.2): a question or
// plan prompt has no associated tool to categorize.
type NeedsInputPayload struct {
	Kind         string `json:"kind"`
	ToolCategory string `json:"tool_category,omitempty"`
}

// EmptyPayload is the payload for event types the schema defines with no
// properties at all (input_resolved, commit, stop): additionalProperties
// is false and there is nothing to set, so this always marshals to `{}`.
type EmptyPayload struct{}

// PRCreatedPayload is the payload for TypePRCreated.
type PRCreatedPayload struct {
	Number *int `json:"number,omitempty"`
}

// session_end reasons (SPEC 10.1's `reason` enum).
const (
	SessionEndClear           = "clear"
	SessionEndLogout          = "logout"
	SessionEndPromptInputExit = "prompt_input_exit"
	SessionEndOther           = "other"
)

// SessionEndPayload is the payload for TypeSessionEnd.
type SessionEndPayload struct {
	Reason string `json:"reason"`
}
