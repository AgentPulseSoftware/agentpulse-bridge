package classify

import (
	"encoding/json"
	"time"
)

// HookInput is Classify's first argument. Its top half is parsed straight
// from the Claude Code hook JSON document on stdin (ParseHookInput); its
// bottom half is context the caller ("agentpulse hook") fills in before
// calling Classify — the current time and the bridge's own local
// configuration — so that Classify's signature can stay exactly
// `Classify(HookInput, *SessionState)` while remaining a pure,
// deterministic function tests can drive without touching the clock, the
// filesystem, or environment variables.
//
// Per BR-19, ToolInput and ToolResponse are kept only as opaque
// json.RawMessage: nothing in this package ever re-exposes their decoded
// contents on an Event. They are decoded narrowly, in tool_input.go, only
// to read the small set of fields SPEC 7.2/7.5 classification needs
// (a tool's command, or which field holds a path to hash locally) — never
// to send, log, or persist their values.
type HookInput struct {
	// --- parsed from Claude Code's hook JSON (ParseHookInput) ---
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	Prompt        string          `json:"prompt,omitempty"`
	Message       string          `json:"message,omitempty"`
	Reason        string          `json:"reason,omitempty"`

	// --- caller-supplied context; never present in Claude Code's JSON ---
	Now           time.Time `json:"-"`
	BridgeID      string    `json:"-"`
	BridgeVersion string    `json:"-"`
	TaskLabelOn   bool      `json:"-"`
}

// ParseHookInput decodes raw as one Claude Code hook JSON document.
// Unrecognized fields are ignored (Claude Code's hook payloads carry many
// fields this package never needs); a document that isn't a JSON object at
// all (malformed input, or valid JSON of the wrong shape) is reported as an
// error so the caller can skip classification for it, exactly as it would
// skip an empty or unreadable stdin — never as a reason "agentpulse hook"
// itself fails (BR-02).
func ParseHookInput(raw []byte) (HookInput, error) {
	var doc struct {
		HookEventName string          `json:"hook_event_name"`
		SessionID     string          `json:"session_id"`
		Cwd           string          `json:"cwd"`
		ToolName      string          `json:"tool_name"`
		ToolInput     json.RawMessage `json:"tool_input"`
		ToolResponse  json.RawMessage `json:"tool_response"`
		Prompt        string          `json:"prompt"`
		Message       string          `json:"message"`
		Reason        string          `json:"reason"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return HookInput{}, err
	}
	return HookInput{
		HookEventName: doc.HookEventName,
		SessionID:     doc.SessionID,
		Cwd:           doc.Cwd,
		ToolName:      doc.ToolName,
		ToolInput:     doc.ToolInput,
		ToolResponse:  doc.ToolResponse,
		Prompt:        doc.Prompt,
		Message:       doc.Message,
		Reason:        doc.Reason,
	}, nil
}

// Claude Code hook event names this package recognizes (SPEC 7.2, BR-08).
const (
	HookSessionStart      = "SessionStart"
	HookUserPromptSubmit  = "UserPromptSubmit"
	HookPreToolUse        = "PreToolUse"
	HookPostToolUse       = "PostToolUse"
	HookPermissionRequest = "PermissionRequest"
	HookNotification      = "Notification"
	HookStop              = "Stop"
	HookSessionEnd        = "SessionEnd"

	// Not registered in V1 (BR-08, SPEC 7.1: "SubagentStop hooks are not
	// registered in V1"; PreCompact is likewise absent from BR-08's list).
	// Named here only so Classify can recognize and safely ignore them if
	// a future Claude Code version, or an operator's own hook config,
	// ever sends one anyway.
	HookSubagentStop = "SubagentStop"
	HookPreCompact   = "PreCompact"
)
