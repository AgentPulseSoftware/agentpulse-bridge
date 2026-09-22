package classify

import (
	"runtime"
	"strings"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cmdnorm"
)

// suppressionWindow is SPEC 7.2's activity heartbeat: an activity event
// whose category matches the last emitted one is still emitted if this
// long has passed since the last emitted event of any kind.
const suppressionWindow = 5 * time.Minute

// permissionDedupWindow is SPEC 7.2's Notification/PermissionRequest
// dedup window.
const permissionDedupWindow = 5 * time.Second

const unpairedBridgeID = "brg_unpaired"

// Classify maps one Claude Code hook invocation to zero or one normalized
// AgentPulse event, per SPEC 7.2, mutating state to reflect it (counters,
// suppression bookkeeping, and the pending markers a later PostToolUse
// needs). ok is false whenever nothing should be appended to the spool for
// this hook call — either because SPEC 7.2 defines none (most PostToolUse
// calls, an idle_prompt Notification, ...) or an activity event was
// suppressed.
func Classify(input HookInput, state *SessionState) (event *Event, ok bool) {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	switch input.HookEventName {
	case HookSessionStart:
		return classifySessionStart(input, state, now), true
	case HookUserPromptSubmit:
		return classifyUserPromptSubmit(input, state, now), true
	case HookPreToolUse:
		return classifyPreToolUse(input, state, now)
	case HookPostToolUse:
		return classifyPostToolUse(input, state, now)
	case HookPermissionRequest:
		return classifyPermissionRequest(input, state, now), true
	case HookNotification:
		return classifyNotification(input, state, now)
	case HookStop:
		return classifyStop(input, state, now), true
	case HookSessionEnd:
		return classifySessionEnd(input, state, now), true
	case HookSubagentStop, HookPreCompact:
		// Not registered in V1 (SPEC 7.1, BR-08); if one arrives anyway
		// (a future Claude Code version, or an operator's own hook
		// config), the conservative choice is no event at all.
		return nil, false
	default:
		return nil, false
	}
}

// --- envelope construction ---

// buildEvent fills the SPEC 10.1 envelope common to every event type and
// records that an event was actually emitted: it is the single place
// state.LastEmittedAt is updated, so every emit path — including ones that
// never touch category-based suppression — keeps the 5-minute heartbeat
// window correct.
func buildEvent(input HookInput, state *SessionState, now time.Time, eventType string, payload any) *Event {
	state.LastEmittedAt = now
	bridgeID := input.BridgeID
	if bridgeID == "" {
		bridgeID = unpairedBridgeID
	}
	return &Event{
		Schema:    1,
		EventID:   newULID(now),
		BridgeID:  bridgeID,
		SessionID: input.SessionID,
		Project:   deriveProject(input.Cwd),
		TS:        now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Type:      eventType,
		Counters: Counters{
			FilesRead:        len(state.ReadHashes),
			FilesEdited:      len(state.EditHashes),
			VerificationRuns: state.VerificationRuns,
			Commits:          state.Commits,
		},
		Payload: payload,
	}
}

// --- SessionStart, UserPromptSubmit, Stop, SessionEnd: always emitted ---

func classifySessionStart(input HookInput, state *SessionState, now time.Time) *Event {
	return buildEvent(input, state, now, TypeSessionStart, SessionStartPayload{
		Agent:         "claude-code",
		BridgeVersion: input.BridgeVersion,
		OS:            osValue(),
	})
}

func classifyUserPromptSubmit(input HookInput, state *SessionState, now time.Time) *Event {
	payload := PromptSubmittedPayload{}
	if input.TaskLabelOn && state.AwaitingTaskLabel {
		payload.TaskLabel = firstLineTaskLabel(input.Prompt)
	}
	state.AwaitingTaskLabel = false
	return buildEvent(input, state, now, TypePromptSubmitted, payload)
}

func classifyStop(input HookInput, state *SessionState, now time.Time) *Event {
	// BR-17: the next prompt after a stop gets a task_label again.
	state.AwaitingTaskLabel = true
	return buildEvent(input, state, now, TypeStop, EmptyPayload{})
}

func classifySessionEnd(input HookInput, state *SessionState, now time.Time) *Event {
	return buildEvent(input, state, now, TypeSessionEnd, SessionEndPayload{
		Reason: sessionEndReason(input.Reason),
	})
}

func sessionEndReason(reason string) string {
	switch reason {
	case SessionEndClear, SessionEndLogout, SessionEndPromptInputExit:
		return reason
	default:
		return SessionEndOther
	}
}

const maxTaskLabelRunes = 80

// firstLineTaskLabel implements BR-17: the first line of prompt, with
// internal whitespace collapsed to single spaces, truncated to 80
// characters.
func firstLineTaskLabel(prompt string) string {
	line := prompt
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.Join(strings.Fields(line), " ")
	r := []rune(line)
	if len(r) > maxTaskLabelRunes {
		line = string(r[:maxTaskLabelRunes])
	}
	return line
}

func osValue() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	default:
		// BR-20 ships darwin and linux only; anything else (a dev/test
		// platform) conservatively reports linux rather than an invalid
		// enum value.
		return "linux"
	}
}

// --- PreToolUse ---

func classifyPreToolUse(input HookInput, state *SessionState, now time.Time) (*Event, bool) {
	// Reset every pending marker: SPEC 7.2's PreToolUse rows are mutually
	// exclusive by tool name, so at most one of these gets set again below,
	// and the matching PostToolUse is the only thing that should ever
	// consult them.
	state.PendingVerification = nil
	state.PendingNeedsInput = nil
	state.PRPending = false
	state.CommitPending = false

	switch input.ToolName {
	case "AskUserQuestion":
		state.PendingNeedsInput = &PendingNeedsInput{Kind: NeedsInputQuestion}
		return needsInputEvent(input, state, now, NeedsInputQuestion, ""), true
	case "ExitPlanMode":
		state.PendingNeedsInput = &PendingNeedsInput{Kind: NeedsInputPlan}
		return needsInputEvent(input, state, now, NeedsInputPlan, ""), true
	case "Read", "Glob", "Grep", "LS", "WebFetch", "WebSearch":
		updateReadCounters(input, state)
		return activityEvent(input, state, now, CategoryRead)
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		updateEditCounters(input, state)
		return activityEvent(input, state, now, CategoryEdit)
	case "Bash":
		return classifyPreToolUseBash(input, state, now)
	default:
		return activityEvent(input, state, now, CategoryOther)
	}
}

func classifyPreToolUseBash(input HookInput, state *SessionState, now time.Time) (*Event, bool) {
	f := decodeToolInput(input.ToolInput)
	seg := cmdnorm.Normalize(f.Command)

	// gh pr create / git commit are checked BEFORE DetectRunner,
	// deliberately: both are anchored at "^" (pr_commit.go), so they only
	// ever match when the command word itself is "gh"/"git" — but
	// DetectRunner's own patterns are anchored at the command word too
	// (runners.go), not at "gh"/"git", so without this ordering a commit
	// message that happens to contain a runner-shaped word — `git commit
	// -m "fix go build on linux"`, `git commit -m "make lint pass"` —
	// would never reach DetectRunner's patterns anyway (they start with
	// "git", not "go"/"make"). This ordering exists for defense in depth
	// and to keep the two checks' precedence explicit and tested, not
	// because DetectRunner could otherwise misfire on these exact
	// examples.
	if ghPRCreatePattern.MatchString(seg) {
		state.PRPending = true
		return activityEvent(input, state, now, CategoryOther)
	}
	if gitCommitPattern.MatchString(seg) {
		state.CommitPending = true
		return activityEvent(input, state, now, CategoryOther)
	}

	if runner, kind, goVerbose, ok := DetectRunner(f.Command); ok {
		state.PendingVerification = &PendingVerification{Runner: runner, Kind: kind, GoVerbose: goVerbose}
		// Never suppressed (SPEC 7.2).
		return buildEvent(input, state, now, TypeVerificationStarted, VerificationStartedPayload{
			Kind:   kind,
			Runner: string(runner),
		}), true
	}

	return activityEvent(input, state, now, CategoryOther)
}

func updateReadCounters(input HookInput, state *SessionState) {
	f := decodeToolInput(input.ToolInput)
	path, ok := readPathFor(input.ToolName, f)
	if !ok {
		return
	}
	if state.ReadHashes == nil {
		state.ReadHashes = map[string]bool{}
	}
	state.ReadHashes[hashPath(path)] = true
}

func updateEditCounters(input HookInput, state *SessionState) {
	f := decodeToolInput(input.ToolInput)
	path, ok := editPathFor(input.ToolName, f)
	if !ok {
		return
	}
	if state.EditHashes == nil {
		state.EditHashes = map[string]bool{}
	}
	state.EditHashes[hashPath(path)] = true
}

// --- PostToolUse ---

func classifyPostToolUse(input HookInput, state *SessionState, now time.Time) (*Event, bool) {
	if pv := state.PendingVerification; pv != nil {
		state.PendingVerification = nil
		state.VerificationRuns++
		output := extractResponseText(input.ToolResponse)
		result := ParseResult(pv.Runner, pv.GoVerbose, output)
		return buildEvent(input, state, now, TypeVerificationFinished, VerificationFinishedPayload{
			Kind:    pv.Kind,
			Runner:  string(pv.Runner),
			Outcome: result.Outcome,
			Passed:  result.Passed,
			Failed:  result.Failed,
			Total:   result.Total,
		}), true
	}

	if state.PRPending {
		state.PRPending = false
		output := extractResponseText(input.ToolResponse)
		if number, ok := parsePRNumber(output); ok {
			n := number
			return buildEvent(input, state, now, TypePRCreated, PRCreatedPayload{Number: &n}), true
		}
		return nil, false
	}

	if state.CommitPending {
		state.CommitPending = false
		state.Commits++
		return buildEvent(input, state, now, TypeCommit, EmptyPayload{}), true
	}

	if state.PendingNeedsInput != nil {
		state.PendingNeedsInput = nil
		return buildEvent(input, state, now, TypeInputResolved, EmptyPayload{}), true
	}

	return nil, false
}

func parsePRNumber(output string) (int, bool) {
	return matchInt(pullURLPattern, output)
}

// --- PermissionRequest, Notification: needs_input(permission) ---

func classifyPermissionRequest(input HookInput, state *SessionState, now time.Time) *Event {
	state.LastPermissionRequestAt = now
	return needsInputEvent(input, state, now, NeedsInputPermission, toolCategory(input.ToolName))
}

func classifyNotification(input HookInput, state *SessionState, now time.Time) (*Event, bool) {
	switch {
	case strings.HasPrefix(input.Message, claudehooks.NotificationPermissionPromptPrefix):
		if !state.LastPermissionRequestAt.IsZero() && now.Sub(state.LastPermissionRequestAt) <= permissionDedupWindow {
			return nil, false // deduplicated against a recent PermissionRequest (SPEC 7.2)
		}
		// tool_category is omitted: a Notification carries no tool name to
		// categorize (SPEC 7.2 only assigns tool_category from the
		// PermissionRequest hook's own tool_name).
		return needsInputEvent(input, state, now, NeedsInputPermission, ""), true
	case strings.HasPrefix(input.Message, claudehooks.NotificationIdlePromptPrefix):
		return nil, false
	default:
		return nil, false
	}
}

func toolCategory(toolName string) string {
	switch toolName {
	case "Bash":
		return ToolCategoryCommand
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "Read":
		return ToolCategoryFile
	case "WebFetch", "WebSearch":
		return ToolCategoryWeb
	default:
		return ToolCategoryOther
	}
}

func needsInputEvent(input HookInput, state *SessionState, now time.Time, kind, toolCat string) *Event {
	state.LastNeedsInputAt = now
	return buildEvent(input, state, now, TypeNeedsInput, NeedsInputPayload{
		Kind:         kind,
		ToolCategory: toolCat,
	})
}

// --- activity suppression (SPEC 7.2) ---

func activityEvent(input HookInput, state *SessionState, now time.Time, category string) (*Event, bool) {
	if !shouldEmitActivity(state, category, now) {
		return nil, false
	}
	ev := buildEvent(input, state, now, TypeActivity, ActivityPayload{Category: category})
	state.LastEmittedCategory = category
	return ev, true
}

func shouldEmitActivity(state *SessionState, category string, now time.Time) bool {
	if state.LastEmittedCategory != category {
		return true
	}
	if state.LastEmittedAt.IsZero() {
		return true
	}
	return now.Sub(state.LastEmittedAt) >= suppressionWindow
}
