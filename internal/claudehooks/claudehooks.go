// Package claudehooks holds the small pieces of Claude Code's own hook
// vocabulary that more than one bridge package needs to agree on exactly.
// It is not a general model of Claude Code hooks; it exists only to stop
// duplicate copies of the same fixed strings from drifting apart.
package claudehooks

// Notification message prefixes Claude Code itself uses (as of the
// version recorded in COMPATIBILITY.md), per SPEC 7.2's
// permission_prompt / idle_prompt notification kinds. The recorded
// fixtures these two paths were written against carry no separate
// "notification type" field, so both internal/scrub (which keeps only a
// matched prefix when scrubbing a Notification's "message" field) and
// internal/classify (which uses the same prefixes to tell a permission
// prompt from an idle prompt) key off this exact text. (Only the
// usage-limit notifications are read from "notification_type", ADR-006
// section 2; moving these two onto it is a separate change.) Anthropic changing the wording is
// exactly the kind of drift COMPATIBILITY.md exists to catch;
// keeping the strings in one place means both packages notice it the same
// way, not silently apart.
const (
	NotificationPermissionPromptPrefix = "Claude needs your permission"
	NotificationIdlePromptPrefix       = "Claude is waiting for your input"
)

// BR08Events are the eleven Claude Code hook events AgentPulse registers
// (BR-08, ADR-005 section 3, ADR-006 section 6), in the order they are
// written to settings.json: SessionStart, UserPromptSubmit, PreToolUse,
// PostToolUse, PermissionRequest, Notification, Stop, SessionEnd,
// StopFailure, and then the two subagent hooks, SubagentStart and
// SubagentStop.
//
// This is the single source of that list. cmd/agentpulse/settings.go's
// hookEntries, internal/doctor's settings-file and compatibility-table
// checks, and internal/doctor/compat.json all agree with this slice by
// construction — internal/doctor's compat_test.go fails if compat.json
// or COMPATIBILITY.md drift from it, rather than letting three
// copies of the same eleven names quietly disagree.
var BR08Events = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "Notification", "Stop", "SessionEnd",
	StopFailure, SubagentStart, SubagentStop,
}

// The two subagent hooks (ADR-005 section 3). A machine paired before
// they were registered lacks them until "agentpulse pair --hooks-only"
// adds them; subagents still show without them, only with a less precise
// start and stop, so their absence is a warning rather than a failure.
const (
	SubagentStart = "SubagentStart"
	SubagentStop  = "SubagentStop"
)

// StopFailure is the hook Claude Code runs instead of Stop when a turn
// ends on an API error, such as a usage limit (ADR-006 section 6). A
// machine paired before it was registered lacks it until "agentpulse
// pair --hooks-only" adds it; without it a usage limit shows as lost
// contact, as it always has, so its absence is a warning rather than a
// failure.
const StopFailure = "StopFailure"

// IsOptionalEvent reports whether event is one of the hooks a machine
// can lack and still work, only with less information: the two subagent
// hooks and StopFailure. "agentpulse doctor" warns, rather than fails,
// when only these are missing.
func IsOptionalEvent(event string) bool {
	return event == SubagentStart || event == SubagentStop || event == StopFailure
}
