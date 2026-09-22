package classify

import "time"

// SessionState is the per-session local counters and bookkeeping SPEC 7.2
// requires (BR-05: persisted at
// $XDG_STATE_HOME/agentpulse/sessions/<session_id>.json, mode 0600, by
// internal/scratch). Classify only ever mutates the fields relevant to the
// hook it is classifying; internal/scratch owns loading, saving, deleting,
// and sweeping this struct to and from disk.
//
// Every field here is either a small integer, a boolean, a timestamp, or a
// SHA-256 hash — never a path, a command, tool output, or prompt text
// (BR-19's spirit applies to local storage too, even though scratch is
// never transmitted: see PendingVerification, which stores whether a
// command's "-v" flag was seen rather than the command itself).
type SessionState struct {
	// ReadHashes and EditHashes are the SHA-256 hex digests of distinct
	// file (or directory, for Glob/Grep/LS) paths read or edited this
	// session — never the paths themselves. Counters.FilesRead/FilesEdited
	// on every emitted event is len(ReadHashes)/len(EditHashes).
	ReadHashes map[string]bool `json:"read_hashes,omitempty"`
	EditHashes map[string]bool `json:"edit_hashes,omitempty"`

	VerificationRuns int `json:"verification_runs"`
	Commits          int `json:"commits"`

	// LastEmittedCategory and LastEmittedAt implement SPEC 7.2's
	// suppression rule: an `activity` event is emitted only when its
	// category differs from LastEmittedCategory, or 5 minutes have passed
	// since LastEmittedAt (the last time ANY event — of any type — was
	// actually emitted, not merely classified). Only a successfully
	// emitted `activity` event updates LastEmittedCategory; every emitted
	// event of any type updates LastEmittedAt.
	LastEmittedCategory string    `json:"last_emitted_category,omitempty"`
	LastEmittedAt       time.Time `json:"last_emitted_at,omitempty"`

	// LastNeedsInputAt is the time of the last needs_input event this
	// session emitted, of any kind. Kept for future needs-you bookkeeping
	// (BR-05 lists it as a required scratch field); nothing in this package
	// consults it beyond keeping it current.
	LastNeedsInputAt time.Time `json:"last_needs_input_at,omitempty"`

	// LastPermissionRequestAt is set only when a PermissionRequest hook is
	// classified, and is the specific dedup key SPEC 7.2 names: a
	// Notification permission_prompt within 5 seconds of this timestamp is
	// treated as the same underlying prompt and produces no event.
	LastPermissionRequestAt time.Time `json:"last_permission_request_at,omitempty"`

	// PRPending and CommitPending mark that the immediately preceding
	// PreToolUse was a `gh pr create` / `git commit` Bash call, for the
	// following PostToolUse to consult (SPEC 7.2). Reset at the start of
	// every PreToolUse and consumed (cleared) by the next PostToolUse.
	PRPending     bool `json:"pr_pending,omitempty"`
	CommitPending bool `json:"commit_pending,omitempty"`

	// PendingVerification and PendingNeedsInput carry what the immediately
	// preceding PreToolUse was, for the following PostToolUse to resolve
	// into verification_finished / input_resolved. Exactly one of
	// {PendingVerification, PendingNeedsInput, PRPending, CommitPending}
	// is set after any given PreToolUse (SPEC 7.2's rows are mutually
	// exclusive by tool name), and all four are reset at the start of
	// every PreToolUse.
	PendingVerification *PendingVerification `json:"pending_verification,omitempty"`
	PendingNeedsInput   *PendingNeedsInput   `json:"pending_needs_input,omitempty"`

	// AwaitingTaskLabel implements BR-17's "once per session on the first
	// prompt and again after a stop": true for a brand new session, set
	// false once a prompt_submitted event includes task_label, and set
	// true again whenever a `stop` is classified.
	AwaitingTaskLabel bool `json:"awaiting_task_label"`

	// KeepAwakePID is BR-16's duplicate-spawn guard: the PID of the
	// caffeinate/systemd-inhibit process "agentpulse hook" already
	// spawned for this session's SessionStart, or 0 if it hasn't (keep-
	// awake off, or unsupported on this platform). Local scratch
	// bookkeeping only — never part of the wire event schema (SPEC
	// 10.1), so no ADR is needed for it.
	KeepAwakePID int `json:"keep_awake_pid,omitempty"`
}

// PendingVerification records what a PreToolUse Bash verification call
// needs the matching PostToolUse to know. GoVerbose is whether the
// command's first pipeline segment contained a "-v" token (SPEC 7.5: Go's
// per-test counts are only parsed when -v was present) — the command text
// itself is deliberately never stored, only this one derived boolean.
type PendingVerification struct {
	Runner RunnerID `json:"runner"`
	Kind   string   `json:"kind"`

	GoVerbose bool `json:"go_verbose,omitempty"`
}

// PendingNeedsInput records that a PreToolUse was AskUserQuestion or
// ExitPlanMode, for the matching PostToolUse to resolve into
// input_resolved (SPEC 7.2).
type PendingNeedsInput struct {
	Kind string `json:"kind"`
}

// NewSessionState returns the state a session starts in: no counters yet,
// and awaiting a task label on its first prompt (BR-17).
func NewSessionState() *SessionState {
	return &SessionState{AwaitingTaskLabel: true}
}
