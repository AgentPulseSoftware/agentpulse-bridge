// Package claudehooks holds the small pieces of Claude Code's own hook
// vocabulary that more than one bridge package needs to agree on exactly.
// It is not a general model of Claude Code hooks; it exists only to stop
// duplicate copies of the same fixed strings from drifting apart.
package claudehooks

// Notification message prefixes Claude Code itself uses (as of the
// version recorded in COMPATIBILITY.md), per SPEC 7.2's
// permission_prompt / idle_prompt notification kinds. Neither the real
// hook payload nor the recorded fixtures carry a separate "notification
// type" field, so both internal/scrub (which keeps only a matched prefix
// when scrubbing a Notification's "message" field) and internal/classify
// (which uses the same prefixes to tell a permission prompt from an idle
// prompt) key off this exact text. Anthropic changing the wording is
// exactly the kind of drift COMPATIBILITY.md exists to catch;
// keeping the strings in one place means both packages notice it the same
// way, not silently apart.
const (
	NotificationPermissionPromptPrefix = "Claude needs your permission"
	NotificationIdlePromptPrefix       = "Claude is waiting for your input"
)

// BR08Events are the eight Claude Code hook events AgentPulse registers
// (BR-08), in the order they are written to settings.json: SessionStart,
// UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
// Notification, Stop, SessionEnd.
//
// This is the single source of that list. cmd/agentpulse/settings.go's
// hookEntries, internal/doctor's settings-file and compatibility-table
// checks, and internal/doctor/compat.json all agree with this slice by
// construction — internal/doctor's compat_test.go fails if compat.json
// or COMPATIBILITY.md drift from it, rather than letting three
// copies of the same eight names quietly disagree.
var BR08Events = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PermissionRequest", "Notification", "Stop", "SessionEnd",
}
