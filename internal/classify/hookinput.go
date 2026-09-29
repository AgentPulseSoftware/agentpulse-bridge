package classify

import (
	"encoding/json"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
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

	// Error is StopFailure's cause (ADR-006 section 2), a short code such
	// as "rate_limit"; NotificationType is Notification's kind, read only
	// for the usage-limit types. Classify compares each against a fixed
	// table and sends only the table's own code, never the value itself.
	// StopFailure's error_details and last_assistant_message, which are
	// message text, deliberately have no field here or in ParseHookInput
	// (ADR-006 section 7): they are never decoded at all.
	Error            string `json:"error,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`

	// AgentID and AgentType are present only on hooks that fire for, or
	// inside, a Claude Code subagent (COMPATIBILITY.md, "Subagent hooks";
	// ADR-005 section 1). AgentID is used only as the input to SubagentID's
	// one-way hash and is never sent, logged, or persisted; AgentType is
	// sent only after SubagentType has cleaned it.
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`

	// --- caller-supplied context; never present in Claude Code's JSON ---
	Now           time.Time `json:"-"`
	BridgeID      string    `json:"-"`
	BridgeVersion string    `json:"-"`
	TaskLabelOn   bool      `json:"-"`
	// SubagentsOn is whether subagent hooks are classified as their own
	// chains (ADR-005, D72). "agentpulse hook" sets it true unless the
	// flush fallback has switched marking off for a while after a relay
	// rejected the subagent field (ADR-005 section 8). When false,
	// AgentID and AgentType are ignored and classification is exactly
	// what it was before subagents were recognized.
	SubagentsOn bool `json:"-"`

	// subagent is set by Classify from AgentID and AgentType, once per
	// hook call, for every event it builds.
	subagent *Subagent
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
		HookEventName    string          `json:"hook_event_name"`
		SessionID        string          `json:"session_id"`
		Cwd              string          `json:"cwd"`
		ToolName         string          `json:"tool_name"`
		ToolInput        json.RawMessage `json:"tool_input"`
		ToolResponse     json.RawMessage `json:"tool_response"`
		Prompt           string          `json:"prompt"`
		Message          string          `json:"message"`
		Reason           string          `json:"reason"`
		Error            string          `json:"error"`
		NotificationType string          `json:"notification_type"`
		AgentID          string          `json:"agent_id"`
		AgentType        string          `json:"agent_type"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return HookInput{}, err
	}
	return HookInput{
		HookEventName:    doc.HookEventName,
		SessionID:        doc.SessionID,
		Cwd:              doc.Cwd,
		ToolName:         doc.ToolName,
		ToolInput:        doc.ToolInput,
		ToolResponse:     doc.ToolResponse,
		Prompt:           doc.Prompt,
		Message:          doc.Message,
		Reason:           doc.Reason,
		Error:            doc.Error,
		NotificationType: doc.NotificationType,
		AgentID:          doc.AgentID,
		AgentType:        doc.AgentType,
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

	// StopFailure runs instead of Stop when a turn ends on an API error;
	// it becomes paused (ADR-006 section 2). "agentpulse pair" registers
	// it (BR-08).
	HookStopFailure = claudehooks.StopFailure

	// SubagentStart and SubagentStop become subagent_start and
	// subagent_stop when SubagentsOn is true, and nothing otherwise
	// (ADR-005 section 1). "agentpulse pair" registers both (BR-08).
	HookSubagentStart = claudehooks.SubagentStart
	HookSubagentStop  = claudehooks.SubagentStop

	// Not registered (BR-08). Named here only so Classify can recognize
	// and safely ignore it if an operator's own hook config ever sends
	// one anyway.
	HookPreCompact = "PreCompact"
)
