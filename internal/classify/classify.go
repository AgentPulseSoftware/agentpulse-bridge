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
//
// A hook that fired inside a subagent (input.InSubagent) is classified
// against that subagent's own chain state and its event carries the
// `subagent` envelope (ADR-005 section 1); session_id is always the
// parent's, because Claude Code sends the parent's (COMPATIBILITY.md,
// "Subagent hooks", finding a).
func Classify(input HookInput, state *SessionState) (event *Event, ok bool) {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	chainID := ""
	if input.InSubagent() {
		if excludedInSubagent(input.HookEventName) {
			return nil, false
		}
		chainID = SubagentID(input.AgentID)
	}
	mainChain := &state.ChainState

	switch input.HookEventName {
	case HookSessionStart:
		return classifySessionStart(input, state, mainChain, now), true
	case HookUserPromptSubmit:
		return classifyUserPromptSubmit(input, state, mainChain, now), true
	case HookPreToolUse:
		return classifyPreToolUse(input, state, state.chain(chainID, now), now)
	case HookPostToolUse:
		return classifyPostToolUse(input, state, state.chain(chainID, now), now)
	case HookPermissionRequest:
		return classifyPermissionRequest(input, state, state.chain(chainID, now), now), true
	case HookNotification:
		return classifyNotification(input, state, state.chain(chainID, now), now)
	case HookStop:
		return classifyStop(input, state, mainChain, now), true
	case HookSessionEnd:
		return classifySessionEnd(input, state, mainChain, now), true
	case HookSubagentStart:
		if chainID == "" {
			return nil, false // SubagentsOn is false, or no agent_id to name the chain by
		}
		return buildEvent(input, state, state.chain(chainID, now), now, TypeSubagentStart, EmptyPayload{}), true
	case HookSubagentStop:
		if chainID == "" {
			return nil, false
		}
		// The chain's own state is deleted, so its LastEmittedAt is moot.
		ev := buildEvent(input, state, &ChainState{}, now, TypeSubagentStop, EmptyPayload{})
		delete(state.Subagents, chainID)
		return ev, true
	case HookPreCompact:
		// Not registered (BR-08); if one arrives anyway, the
		// conservative choice is no event at all.
		return nil, false
	default:
		return nil, false
	}
}

// --- envelope construction ---

// buildEvent fills the SPEC 10.1 envelope common to every event type and
// records that an event was actually emitted: it is the single place
// c.LastEmittedAt is updated, so every emit path — including ones that
// never touch category-based suppression — keeps the chain's 5-minute
// heartbeat window correct.
//
// A main-chain event derives its project from cwd (BR-13) and remembers
// it in state.Project; a subagent event reports that remembered project
// instead, and only derives its own when there is none yet.
func buildEvent(input HookInput, state *SessionState, c *ChainState, now time.Time, eventType string, payload any) *Event {
	c.LastEmittedAt = now
	bridgeID := input.BridgeID
	if bridgeID == "" {
		bridgeID = unpairedBridgeID
	}
	sub := subagentFor(input)
	var project Project
	if sub != nil && state.Project != nil {
		project = *state.Project
	} else {
		project = deriveProject(input.Cwd)
		if sub == nil {
			p := project
			state.Project = &p
		}
	}
	return &Event{
		Schema:    1,
		EventID:   newULID(now),
		BridgeID:  bridgeID,
		SessionID: input.SessionID,
		Project:   project,
		TS:        now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Type:      eventType,
		Counters: Counters{
			FilesRead:        len(state.ReadHashes),
			FilesEdited:      len(state.EditHashes),
			VerificationRuns: state.VerificationRuns,
			Commits:          state.Commits,
		},
		Payload:  payload,
		Subagent: sub,
	}
}

// --- SessionStart, UserPromptSubmit, Stop, SessionEnd: always emitted ---

func classifySessionStart(input HookInput, state *SessionState, c *ChainState, now time.Time) *Event {
	return buildEvent(input, state, c, now, TypeSessionStart, SessionStartPayload{
		Agent:         "claude-code",
		BridgeVersion: input.BridgeVersion,
		OS:            osValue(),
	})
}

func classifyUserPromptSubmit(input HookInput, state *SessionState, c *ChainState, now time.Time) *Event {
	payload := PromptSubmittedPayload{}
	if input.TaskLabelOn && state.AwaitingTaskLabel {
		payload.TaskLabel = firstLineTaskLabel(input.Prompt)
	}
	state.AwaitingTaskLabel = false
	return buildEvent(input, state, c, now, TypePromptSubmitted, payload)
}

func classifyStop(input HookInput, state *SessionState, c *ChainState, now time.Time) *Event {
	// BR-17: the next prompt after a stop gets a task_label again.
	state.AwaitingTaskLabel = true
	return buildEvent(input, state, c, now, TypeStop, EmptyPayload{})
}

func classifySessionEnd(input HookInput, state *SessionState, c *ChainState, now time.Time) *Event {
	return buildEvent(input, state, c, now, TypeSessionEnd, SessionEndPayload{
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

func classifyPreToolUse(input HookInput, state *SessionState, c *ChainState, now time.Time) (*Event, bool) {
	// Reset every pending marker: SPEC 7.2's PreToolUse rows are mutually
	// exclusive by tool name, so at most one of these gets set again below,
	// and the matching PostToolUse is the only thing that should ever
	// consult them.
	c.PendingVerification = nil
	c.PendingNeedsInput = nil
	c.PRPending = false
	c.CommitPending = false
	// An overflow chain is shared by every subagent past the cap, so a
	// marker set there could be reset or consumed by a different
	// subagent's hook. It never tracks one (see SessionState.Overflow).
	track := !state.isOverflow(c)

	switch input.ToolName {
	case "AskUserQuestion":
		if track {
			c.PendingNeedsInput = &PendingNeedsInput{Kind: NeedsInputQuestion}
		}
		return needsInputEvent(input, state, c, now, NeedsInputQuestion, ""), true
	case "ExitPlanMode":
		if track {
			c.PendingNeedsInput = &PendingNeedsInput{Kind: NeedsInputPlan}
		}
		return needsInputEvent(input, state, c, now, NeedsInputPlan, ""), true
	case "Read", "Glob", "Grep", "LS", "WebFetch", "WebSearch":
		updateReadCounters(input, state)
		return activityEvent(input, state, c, now, CategoryRead)
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		updateEditCounters(input, state)
		return activityEvent(input, state, c, now, CategoryEdit)
	case "Bash":
		if !track {
			return activityEvent(input, state, c, now, CategoryOther)
		}
		return classifyPreToolUseBash(input, state, c, now)
	default:
		return activityEvent(input, state, c, now, CategoryOther)
	}
}

func classifyPreToolUseBash(input HookInput, state *SessionState, c *ChainState, now time.Time) (*Event, bool) {
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
		c.PRPending = true
		return activityEvent(input, state, c, now, CategoryOther)
	}
	if gitCommitPattern.MatchString(seg) {
		c.CommitPending = true
		return activityEvent(input, state, c, now, CategoryOther)
	}

	if runner, kind, goVerbose, ok := DetectRunner(f.Command); ok {
		c.PendingVerification = &PendingVerification{Runner: runner, Kind: kind, GoVerbose: goVerbose}
		// Never suppressed (SPEC 7.2).
		return buildEvent(input, state, c, now, TypeVerificationStarted, VerificationStartedPayload{
			Kind:   kind,
			Runner: string(runner),
		}), true
	}

	return activityEvent(input, state, c, now, CategoryOther)
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

func classifyPostToolUse(input HookInput, state *SessionState, c *ChainState, now time.Time) (*Event, bool) {
	if state.isOverflow(c) {
		return nil, false // no markers are tracked there, and a stale one is not trusted
	}
	if pv := c.PendingVerification; pv != nil {
		c.PendingVerification = nil
		state.VerificationRuns++
		output := extractResponseText(input.ToolResponse)
		result := ParseResult(pv.Runner, pv.GoVerbose, output)
		return buildEvent(input, state, c, now, TypeVerificationFinished, VerificationFinishedPayload{
			Kind:    pv.Kind,
			Runner:  string(pv.Runner),
			Outcome: result.Outcome,
			Passed:  result.Passed,
			Failed:  result.Failed,
			Total:   result.Total,
		}), true
	}

	if c.PRPending {
		c.PRPending = false
		output := extractResponseText(input.ToolResponse)
		if number, ok := parsePRNumber(output); ok {
			n := number
			return buildEvent(input, state, c, now, TypePRCreated, PRCreatedPayload{Number: &n}), true
		}
		return nil, false
	}

	if c.CommitPending {
		c.CommitPending = false
		state.Commits++
		return buildEvent(input, state, c, now, TypeCommit, EmptyPayload{}), true
	}

	if c.PendingNeedsInput != nil {
		c.PendingNeedsInput = nil
		return buildEvent(input, state, c, now, TypeInputResolved, EmptyPayload{}), true
	}

	return nil, false
}

func parsePRNumber(output string) (int, bool) {
	return matchInt(pullURLPattern, output)
}

// --- PermissionRequest, Notification: needs_input(permission) ---

func classifyPermissionRequest(input HookInput, state *SessionState, c *ChainState, now time.Time) *Event {
	state.LastPermissionRequestAt = now
	return needsInputEvent(input, state, c, now, NeedsInputPermission, toolCategory(input.ToolName))
}

func classifyNotification(input HookInput, state *SessionState, c *ChainState, now time.Time) (*Event, bool) {
	switch {
	case strings.HasPrefix(input.Message, claudehooks.NotificationPermissionPromptPrefix):
		if !state.LastPermissionRequestAt.IsZero() && now.Sub(state.LastPermissionRequestAt) <= permissionDedupWindow {
			return nil, false // deduplicated against a recent PermissionRequest in any chain (SPEC 7.2)
		}
		// tool_category is omitted: a Notification carries no tool name to
		// categorize (SPEC 7.2 only assigns tool_category from the
		// PermissionRequest hook's own tool_name).
		return needsInputEvent(input, state, c, now, NeedsInputPermission, ""), true
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

func needsInputEvent(input HookInput, state *SessionState, c *ChainState, now time.Time, kind, toolCat string) *Event {
	c.LastNeedsInputAt = now
	return buildEvent(input, state, c, now, TypeNeedsInput, NeedsInputPayload{
		Kind:         kind,
		ToolCategory: toolCat,
	})
}

// --- activity suppression (SPEC 7.2) ---

func activityEvent(input HookInput, state *SessionState, c *ChainState, now time.Time, category string) (*Event, bool) {
	if !shouldEmitActivity(c, category, now) {
		return nil, false
	}
	ev := buildEvent(input, state, c, now, TypeActivity, ActivityPayload{Category: category})
	c.LastEmittedCategory = category
	return ev, true
}

func shouldEmitActivity(c *ChainState, category string, now time.Time) bool {
	if c.LastEmittedCategory != category {
		return true
	}
	if c.LastEmittedAt.IsZero() {
		return true
	}
	return now.Sub(c.LastEmittedAt) >= suppressionWindow
}
