package classify

import (
	"encoding/json"
	"testing"
	"time"
)

// baseInput returns a minimal, valid HookInput for sessionID at fixedNow,
// with the context fields a real "agentpulse hook" invocation would set.
// Cwd deliberately points somewhere outside any git work tree (a temp-ish,
// non-existent path) so project derivation is deterministic across test
// machines regardless of where the repository checkout happens to sit.
func baseInput(sessionID, hookEvent string) HookInput {
	return HookInput{
		HookEventName: hookEvent,
		SessionID:     sessionID,
		Cwd:           "/nonexistent/agentpulse-test-project",
		Now:           fixedNow,
		BridgeID:      "brg_test0001",
		BridgeVersion: "0.0.0-test",
	}
}

var fixedNow = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func toolInputJSON(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func toolResponseJSON(t *testing.T, stdout, stderr string) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"stdout": stdout, "stderr": stderr, "interrupted": false})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// --- SPEC 7.2 table: one test per row ---

func TestSessionStartAlwaysEmitsSessionStart(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookSessionStart)

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("SessionStart should always emit an event")
	}
	if ev.Type != TypeSessionStart {
		t.Errorf("Type = %q, want %q", ev.Type, TypeSessionStart)
	}
	payload, ok := ev.Payload.(SessionStartPayload)
	if !ok {
		t.Fatalf("Payload type = %T, want SessionStartPayload", ev.Payload)
	}
	if payload.Agent != "claude-code" {
		t.Errorf("Agent = %q, want claude-code", payload.Agent)
	}
	if payload.OS != "darwin" && payload.OS != "linux" {
		t.Errorf("OS = %q, want darwin or linux", payload.OS)
	}
	if payload.BridgeVersion != "0.0.0-test" {
		t.Errorf("BridgeVersion = %q, want 0.0.0-test", payload.BridgeVersion)
	}
	assertValidEvent(t, sch, ev)
}

func TestUserPromptSubmitAlwaysEmitsPromptSubmitted(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookUserPromptSubmit)
	input.Prompt = "fix the login bug"

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("UserPromptSubmit should always emit an event")
	}
	if ev.Type != TypePromptSubmitted {
		t.Errorf("Type = %q, want %q", ev.Type, TypePromptSubmitted)
	}
	assertValidEvent(t, sch, ev)
}

func TestUserPromptSubmitIncludesTaskLabelOnlyWhenEnabled(t *testing.T) {
	sch := loadEventSchema(t)

	// Disabled: no task_label even on the first prompt.
	state := NewSessionState()
	input := baseInput("sess_1", HookUserPromptSubmit)
	input.Prompt = "fix the login bug"
	input.TaskLabelOn = false
	ev, _ := Classify(input, state)
	payload := ev.Payload.(PromptSubmittedPayload)
	if payload.TaskLabel != "" {
		t.Errorf("TaskLabel = %q, want empty when disabled", payload.TaskLabel)
	}
	assertValidEvent(t, sch, ev)

	// Enabled: first prompt gets it.
	state2 := NewSessionState()
	input.TaskLabelOn = true
	ev2, _ := Classify(input, state2)
	payload2 := ev2.Payload.(PromptSubmittedPayload)
	if payload2.TaskLabel != "fix the login bug" {
		t.Errorf("TaskLabel = %q, want %q", payload2.TaskLabel, "fix the login bug")
	}
	assertValidEvent(t, sch, ev2)

	// Second prompt in the same session (no stop in between): no label.
	input3 := baseInput("sess_1", HookUserPromptSubmit)
	input3.Prompt = "now fix the logout bug"
	input3.TaskLabelOn = true
	ev3, _ := Classify(input3, state2)
	payload3 := ev3.Payload.(PromptSubmittedPayload)
	if payload3.TaskLabel != "" {
		t.Errorf("TaskLabel = %q, want empty on a second prompt without an intervening stop", payload3.TaskLabel)
	}

	// After a stop, the next prompt gets a label again (BR-17).
	stopInput := baseInput("sess_1", HookStop)
	Classify(stopInput, state2)
	input4 := baseInput("sess_1", HookUserPromptSubmit)
	input4.Prompt = "  now   fix\nthe next bug please and thanks and then some more words to exceed eighty characters for sure"
	input4.TaskLabelOn = true
	ev4, _ := Classify(input4, state2)
	payload4 := ev4.Payload.(PromptSubmittedPayload)
	if payload4.TaskLabel == "" {
		t.Error("expected a task_label on the first prompt after a stop")
	}
	if len([]rune(payload4.TaskLabel)) > 80 {
		t.Errorf("TaskLabel length = %d, want <= 80", len([]rune(payload4.TaskLabel)))
	}
	if payload4.TaskLabel != "now fix" {
		t.Errorf("TaskLabel = %q, want %q (first line, whitespace collapsed)", payload4.TaskLabel, "now fix")
	}
}

func TestPreToolUseAskUserQuestionEmitsNeedsInputQuestion(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "AskUserQuestion"

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	if ev.Type != TypeNeedsInput {
		t.Errorf("Type = %q, want %q", ev.Type, TypeNeedsInput)
	}
	payload := ev.Payload.(NeedsInputPayload)
	if payload.Kind != NeedsInputQuestion {
		t.Errorf("Kind = %q, want %q", payload.Kind, NeedsInputQuestion)
	}
	if payload.ToolCategory != "" {
		t.Errorf("ToolCategory = %q, want empty for a question", payload.ToolCategory)
	}
	if state.PendingNeedsInput == nil || state.PendingNeedsInput.Kind != NeedsInputQuestion {
		t.Errorf("expected PendingNeedsInput to be set to question, got %+v", state.PendingNeedsInput)
	}
	assertValidEvent(t, sch, ev)
}

func TestPreToolUseExitPlanModeEmitsNeedsInputPlan(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "ExitPlanMode"

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(NeedsInputPayload)
	if payload.Kind != NeedsInputPlan {
		t.Errorf("Kind = %q, want %q", payload.Kind, NeedsInputPlan)
	}
	assertValidEvent(t, sch, ev)
}

func TestPreToolUseReadCategoryTools(t *testing.T) {
	sch := loadEventSchema(t)
	for _, tool := range []string{"Read", "Glob", "Grep", "LS", "WebFetch", "WebSearch"} {
		t.Run(tool, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", HookPreToolUse)
			input.ToolName = tool
			input.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go", "path": "/x"})

			ev, ok := Classify(input, state)
			if !ok {
				t.Fatal("expected an event")
			}
			if ev.Type != TypeActivity {
				t.Errorf("Type = %q, want %q", ev.Type, TypeActivity)
			}
			payload := ev.Payload.(ActivityPayload)
			if payload.Category != CategoryRead {
				t.Errorf("Category = %q, want %q", payload.Category, CategoryRead)
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

func TestPreToolUseEditCategoryTools(t *testing.T) {
	sch := loadEventSchema(t)
	for _, tool := range []string{"Edit", "MultiEdit", "Write", "NotebookEdit"} {
		t.Run(tool, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", HookPreToolUse)
			input.ToolName = tool
			input.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go", "notebook_path": "/x/a.ipynb"})

			ev, ok := Classify(input, state)
			if !ok {
				t.Fatal("expected an event")
			}
			payload := ev.Payload.(ActivityPayload)
			if payload.Category != CategoryEdit {
				t.Errorf("Category = %q, want %q", payload.Category, CategoryEdit)
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

func TestPreToolUseBashVerificationEmitsVerificationStarted(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "Bash"
	input.ToolInput = toolInputJSON(t, map[string]any{"command": "pytest -q"})

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	if ev.Type != TypeVerificationStarted {
		t.Errorf("Type = %q, want %q", ev.Type, TypeVerificationStarted)
	}
	payload := ev.Payload.(VerificationStartedPayload)
	if payload.Kind != KindTest || payload.Runner != string(RunnerPytest) {
		t.Errorf("payload = %+v, want kind=test runner=pytest", payload)
	}
	if state.PendingVerification == nil || state.PendingVerification.Runner != RunnerPytest {
		t.Errorf("expected PendingVerification to be set to pytest, got %+v", state.PendingVerification)
	}
	assertValidEvent(t, sch, ev)
}

func TestPreToolUseBashGhPRCreateMarksPendingAndEmitsOther(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "Bash"
	input.ToolInput = toolInputJSON(t, map[string]any{"command": `gh pr create --title "x" --body "y"`})

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(ActivityPayload)
	if payload.Category != CategoryOther {
		t.Errorf("Category = %q, want %q", payload.Category, CategoryOther)
	}
	if !state.PRPending {
		t.Error("expected PRPending to be set")
	}
	assertValidEvent(t, sch, ev)
}

func TestPreToolUseBashGitCommitMarksPendingAndEmitsOther(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "Bash"
	input.ToolInput = toolInputJSON(t, map[string]any{"command": `git commit -m "fix bug"`})

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(ActivityPayload)
	if payload.Category != CategoryOther {
		t.Errorf("Category = %q, want %q", payload.Category, CategoryOther)
	}
	if !state.CommitPending {
		t.Error("expected CommitPending to be set")
	}
	assertValidEvent(t, sch, ev)
}

// TestPreToolUseBashGitCommitWinsOverRunnerWordInMessage proves the
// anchored `^git\s+commit` check is evaluated before DetectRunner: a
// commit message that happens to contain a runner-shaped word ("go
// build", "make") must still be classified as a commit, never as a
// verification_started.
func TestPreToolUseBashGitCommitWinsOverRunnerWordInMessage(t *testing.T) {
	cases := []string{
		`git commit -m "make lint pass"`,
		`git commit -m "fix go build on linux"`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", HookPreToolUse)
			input.ToolName = "Bash"
			input.ToolInput = toolInputJSON(t, map[string]any{"command": cmd})

			ev, ok := Classify(input, state)
			if !ok {
				t.Fatal("expected an event")
			}
			if ev.Type != TypeActivity {
				t.Errorf("Type = %q, want %q (a commit message must never look like a verification run)", ev.Type, TypeActivity)
			}
			if !state.CommitPending {
				t.Error("expected CommitPending to be set")
			}
			if state.PendingVerification != nil {
				t.Errorf("expected no PendingVerification, got %+v", state.PendingVerification)
			}
		})
	}
}

func TestPreToolUseOtherToolEmitsActivityOther(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "Task"

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(ActivityPayload)
	if payload.Category != CategoryOther {
		t.Errorf("Category = %q, want %q", payload.Category, CategoryOther)
	}
	assertValidEvent(t, sch, ev)
}

func TestPreToolUseBashOtherCommandEmitsActivityOther(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookPreToolUse)
	input.ToolName = "Bash"
	input.ToolInput = toolInputJSON(t, map[string]any{"command": "ls -la"})

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(ActivityPayload)
	if payload.Category != CategoryOther {
		t.Errorf("Category = %q, want %q", payload.Category, CategoryOther)
	}
	if state.PRPending || state.CommitPending || state.PendingVerification != nil {
		t.Error("an unrelated Bash command should not set any pending marker")
	}
}

func TestPostToolUseAfterVerificationEmitsVerificationFinished(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "Bash"
	pre.ToolInput = toolInputJSON(t, map[string]any{"command": "pytest -q"})
	if _, ok := Classify(pre, state); !ok {
		t.Fatal("setup: expected verification_started")
	}

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "Bash"
	post.ToolResponse = toolResponseJSON(t, "collected 12 items\n1 failed, 11 passed in 2.31s", "")

	ev, ok := Classify(post, state)
	if !ok {
		t.Fatal("expected verification_finished")
	}
	if ev.Type != TypeVerificationFinished {
		t.Errorf("Type = %q, want %q", ev.Type, TypeVerificationFinished)
	}
	payload := ev.Payload.(VerificationFinishedPayload)
	if payload.Outcome != OutcomeFail {
		t.Errorf("Outcome = %q, want fail", payload.Outcome)
	}
	if payload.Passed == nil || *payload.Passed != 11 || payload.Failed == nil || *payload.Failed != 1 {
		t.Errorf("counts = %+v, want passed=11 failed=1", payload)
	}
	if state.PendingVerification != nil {
		t.Error("PendingVerification should be cleared after PostToolUse")
	}
	if ev.Counters.VerificationRuns != 1 {
		t.Errorf("VerificationRuns = %d, want 1", ev.Counters.VerificationRuns)
	}
	assertValidEvent(t, sch, ev)
}

func TestPostToolUsePRPendingWithURLEmitsPRCreated(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "Bash"
	pre.ToolInput = toolInputJSON(t, map[string]any{"command": "gh pr create --title x --body y"})
	Classify(pre, state)

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "Bash"
	post.ToolResponse = toolResponseJSON(t, "https://github.com/acme/repo/pull/42", "")

	ev, ok := Classify(post, state)
	if !ok {
		t.Fatal("expected pr_created")
	}
	if ev.Type != TypePRCreated {
		t.Errorf("Type = %q, want %q", ev.Type, TypePRCreated)
	}
	payload := ev.Payload.(PRCreatedPayload)
	if payload.Number == nil || *payload.Number != 42 {
		t.Errorf("Number = %v, want 42", payload.Number)
	}
	if state.PRPending {
		t.Error("PRPending should be cleared after PostToolUse")
	}
	assertValidEvent(t, sch, ev)
}

func TestPostToolUsePRPendingWithoutURLEmitsNothing(t *testing.T) {
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "Bash"
	pre.ToolInput = toolInputJSON(t, map[string]any{"command": "gh pr create --title x --body y"})
	Classify(pre, state)

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "Bash"
	post.ToolResponse = toolResponseJSON(t, "error: could not create pull request", "")

	_, ok := Classify(post, state)
	if ok {
		t.Fatal("expected no event when no PR URL is present")
	}
	if state.PRPending {
		t.Error("PRPending should still be cleared even when no event is emitted")
	}
}

func TestPostToolUseCommitPendingEmitsCommit(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "Bash"
	pre.ToolInput = toolInputJSON(t, map[string]any{"command": `git commit -m "fix"`})
	Classify(pre, state)

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "Bash"
	post.ToolResponse = toolResponseJSON(t, "[main abc1234] fix", "")

	ev, ok := Classify(post, state)
	if !ok {
		t.Fatal("expected commit")
	}
	if ev.Type != TypeCommit {
		t.Errorf("Type = %q, want %q", ev.Type, TypeCommit)
	}
	if ev.Counters.Commits != 1 {
		t.Errorf("Commits = %d, want 1", ev.Counters.Commits)
	}
	if state.CommitPending {
		t.Error("CommitPending should be cleared after PostToolUse")
	}
	assertValidEvent(t, sch, ev)
}

func TestPostToolUseAfterNeedsInputEmitsInputResolved(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "AskUserQuestion"
	Classify(pre, state)

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "AskUserQuestion"

	ev, ok := Classify(post, state)
	if !ok {
		t.Fatal("expected input_resolved")
	}
	if ev.Type != TypeInputResolved {
		t.Errorf("Type = %q, want %q", ev.Type, TypeInputResolved)
	}
	if state.PendingNeedsInput != nil {
		t.Error("PendingNeedsInput should be cleared after PostToolUse")
	}
	assertValidEvent(t, sch, ev)
}

func TestPostToolUseOtherwiseEmitsNothing(t *testing.T) {
	state := NewSessionState()
	pre := baseInput("sess_1", HookPreToolUse)
	pre.ToolName = "Read"
	pre.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go"})
	Classify(pre, state)

	post := baseInput("sess_1", HookPostToolUse)
	post.ToolName = "Read"

	_, ok := Classify(post, state)
	if ok {
		t.Fatal("expected no event for a plain PostToolUse after a read")
	}
}

func TestPermissionRequestAlwaysEmitsNeedsInputPermission(t *testing.T) {
	sch := loadEventSchema(t)
	cases := []struct {
		tool string
		want string
	}{
		{"Bash", ToolCategoryCommand},
		{"Edit", ToolCategoryFile},
		{"Write", ToolCategoryFile},
		{"MultiEdit", ToolCategoryFile},
		{"NotebookEdit", ToolCategoryFile},
		{"Read", ToolCategoryFile},
		{"WebFetch", ToolCategoryWeb},
		{"WebSearch", ToolCategoryWeb},
		{"Glob", ToolCategoryOther},
		{"SomeMCPTool", ToolCategoryOther},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", HookPermissionRequest)
			input.ToolName = tc.tool

			ev, ok := Classify(input, state)
			if !ok {
				t.Fatal("PermissionRequest should always emit an event")
			}
			if ev.Type != TypeNeedsInput {
				t.Errorf("Type = %q, want %q", ev.Type, TypeNeedsInput)
			}
			payload := ev.Payload.(NeedsInputPayload)
			if payload.Kind != NeedsInputPermission {
				t.Errorf("Kind = %q, want %q", payload.Kind, NeedsInputPermission)
			}
			if payload.ToolCategory != tc.want {
				t.Errorf("ToolCategory = %q, want %q", payload.ToolCategory, tc.want)
			}
			if state.LastPermissionRequestAt.IsZero() {
				t.Error("expected LastPermissionRequestAt to be set")
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

func TestNotificationPermissionPromptEmitsNeedsInputPermission(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookNotification)
	input.Message = "Claude needs your permission to run this command"

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	payload := ev.Payload.(NeedsInputPayload)
	if payload.Kind != NeedsInputPermission {
		t.Errorf("Kind = %q, want %q", payload.Kind, NeedsInputPermission)
	}
	if payload.ToolCategory != "" {
		t.Errorf("ToolCategory = %q, want empty (Notification carries no tool name)", payload.ToolCategory)
	}
	assertValidEvent(t, sch, ev)
}

func TestNotificationPermissionPromptDedupedWithinFiveSeconds(t *testing.T) {
	state := NewSessionState()
	pr := baseInput("sess_1", HookPermissionRequest)
	pr.ToolName = "Bash"
	Classify(pr, state)

	notif := baseInput("sess_1", HookNotification)
	notif.Message = "Claude needs your permission to run this command"
	notif.Now = fixedNow.Add(3 * time.Second)

	_, ok := Classify(notif, state)
	if ok {
		t.Fatal("expected the Notification to be deduplicated within 5 seconds of the PermissionRequest")
	}
}

func TestNotificationPermissionPromptNotDedupedAfterFiveSeconds(t *testing.T) {
	state := NewSessionState()
	pr := baseInput("sess_1", HookPermissionRequest)
	pr.ToolName = "Bash"
	Classify(pr, state)

	notif := baseInput("sess_1", HookNotification)
	notif.Message = "Claude needs your permission to run this command"
	notif.Now = fixedNow.Add(6 * time.Second)

	_, ok := Classify(notif, state)
	if !ok {
		t.Fatal("expected an event when more than 5 seconds have passed since the PermissionRequest")
	}
}

func TestNotificationIdlePromptEmitsNothing(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookNotification)
	input.Message = "Claude is waiting for your input"

	_, ok := Classify(input, state)
	if ok {
		t.Fatal("idle_prompt should emit nothing")
	}
}

func TestNotificationOtherEmitsNothing(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookNotification)
	input.Message = "Something else entirely"

	_, ok := Classify(input, state)
	if ok {
		t.Fatal("an unrecognized notification should emit nothing")
	}
}

func TestStopAlwaysEmitsStop(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	input := baseInput("sess_1", HookStop)

	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("Stop should always emit an event")
	}
	if ev.Type != TypeStop {
		t.Errorf("Type = %q, want %q", ev.Type, TypeStop)
	}
	assertValidEvent(t, sch, ev)
}

func TestSessionEndReasonMapping(t *testing.T) {
	sch := loadEventSchema(t)
	cases := []struct {
		reason string
		want   string
	}{
		{"clear", SessionEndClear},
		{"logout", SessionEndLogout},
		{"prompt_input_exit", SessionEndPromptInputExit},
		{"other", SessionEndOther},
		{"", SessionEndOther},
		{"something_unrecognized", SessionEndOther},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", HookSessionEnd)
			input.Reason = tc.reason

			ev, ok := Classify(input, state)
			if !ok {
				t.Fatal("SessionEnd should always emit an event")
			}
			payload := ev.Payload.(SessionEndPayload)
			if payload.Reason != tc.want {
				t.Errorf("Reason = %q, want %q", payload.Reason, tc.want)
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

func TestSubagentStopAndPreCompactEmitNothing(t *testing.T) {
	for _, hook := range []string{HookSubagentStop, HookPreCompact} {
		t.Run(hook, func(t *testing.T) {
			state := NewSessionState()
			input := baseInput("sess_1", hook)
			if _, ok := Classify(input, state); ok {
				t.Errorf("%s should emit nothing (not registered in V1)", hook)
			}
		})
	}
}

func TestUnknownHookEventEmitsNothing(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", "SomeFutureHook")
	if _, ok := Classify(input, state); ok {
		t.Error("an unrecognized hook event name should emit nothing")
	}
}

// --- SPEC 7.2 suppression rule ---

func TestActivitySuppressedWhenSameCategoryWithinFiveMinutes(t *testing.T) {
	state := NewSessionState()
	first := baseInput("sess_1", HookPreToolUse)
	first.ToolName = "Read"
	first.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go"})
	if _, ok := Classify(first, state); !ok {
		t.Fatal("first read should emit")
	}

	second := baseInput("sess_1", HookPreToolUse)
	second.ToolName = "Read"
	second.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/b.go"})
	second.Now = fixedNow.Add(1 * time.Minute)
	if _, ok := Classify(second, state); ok {
		t.Error("a second read within 5 minutes of the same category should be suppressed")
	}
}

func TestActivityNotSuppressedWhenCategoryChanges(t *testing.T) {
	state := NewSessionState()
	read := baseInput("sess_1", HookPreToolUse)
	read.ToolName = "Read"
	read.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go"})
	Classify(read, state)

	edit := baseInput("sess_1", HookPreToolUse)
	edit.ToolName = "Edit"
	edit.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go"})
	edit.Now = fixedNow.Add(1 * time.Second)
	if _, ok := Classify(edit, state); !ok {
		t.Error("an edit immediately after a read should not be suppressed (different category)")
	}
}

func TestActivityNotSuppressedAfterFiveMinuteHeartbeat(t *testing.T) {
	state := NewSessionState()
	read1 := baseInput("sess_1", HookPreToolUse)
	read1.ToolName = "Read"
	read1.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/a.go"})
	Classify(read1, state)

	read2 := baseInput("sess_1", HookPreToolUse)
	read2.ToolName = "Read"
	read2.ToolInput = toolInputJSON(t, map[string]any{"file_path": "/x/b.go"})
	read2.Now = fixedNow.Add(5 * time.Minute)
	if _, ok := Classify(read2, state); !ok {
		t.Error("a same-category read after the 5-minute heartbeat window should be emitted")
	}
}

func TestNonActivityEventsAreNeverSuppressed(t *testing.T) {
	state := NewSessionState()
	for i := 0; i < 3; i++ {
		input := baseInput("sess_1", HookStop)
		input.Now = fixedNow.Add(time.Duration(i) * time.Millisecond)
		if _, ok := Classify(input, state); !ok {
			t.Errorf("iteration %d: stop should never be suppressed", i)
		}
	}
}

// --- BR-13 project derivation ---

func TestProjectNameFallsBackWhenEmpty(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookStop)
	input.Cwd = ""

	ev, _ := Classify(input, state)
	if ev.Project.Name != "project" {
		t.Errorf("Project.Name = %q, want fallback %q", ev.Project.Name, "project")
	}
	if len(ev.Project.KeyHash) != 64 {
		t.Errorf("Project.KeyHash length = %d, want 64 (hex sha256)", len(ev.Project.KeyHash))
	}
}

func TestProjectNameUsesCwdBaseName(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookStop)
	input.Cwd = "/nonexistent/agentpulse-test-project"

	ev, _ := Classify(input, state)
	if ev.Project.Name != "agentpulse-test-project" {
		t.Errorf("Project.Name = %q, want %q", ev.Project.Name, "agentpulse-test-project")
	}
}

// --- counters ---

func TestCountersReflectDistinctPathsOnly(t *testing.T) {
	state := NewSessionState()
	for _, path := range []string{"/x/a.go", "/x/a.go", "/x/b.go"} {
		input := baseInput("sess_1", HookPreToolUse)
		input.ToolName = "Edit"
		input.ToolInput = toolInputJSON(t, map[string]any{"file_path": path})
		input.Now = input.Now.Add(6 * time.Minute) // avoid suppression between calls
		Classify(input, state)
	}
	if len(state.EditHashes) != 2 {
		t.Errorf("distinct edit hashes = %d, want 2", len(state.EditHashes))
	}
}

// --- envelope basics every event should satisfy ---

func TestEveryEmittedEventHasWellFormedEnvelope(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookSessionStart)
	ev, ok := Classify(input, state)
	if !ok {
		t.Fatal("expected an event")
	}
	if ev.Schema != 1 {
		t.Errorf("Schema = %d, want 1", ev.Schema)
	}
	if len(ev.EventID) != 26 {
		t.Errorf("EventID length = %d, want 26", len(ev.EventID))
	}
	if ev.BridgeID != "brg_test0001" {
		t.Errorf("BridgeID = %q, want brg_test0001", ev.BridgeID)
	}
	if ev.SessionID != "sess_1" {
		t.Errorf("SessionID = %q, want sess_1", ev.SessionID)
	}
	if ev.TS == "" {
		t.Error("TS should not be empty")
	}
}

func TestBridgeIDFallsBackToPlaceholderWhenUnset(t *testing.T) {
	state := NewSessionState()
	input := baseInput("sess_1", HookSessionStart)
	input.BridgeID = ""
	ev, _ := Classify(input, state)
	if ev.BridgeID != "brg_unpaired" {
		t.Errorf("BridgeID = %q, want brg_unpaired", ev.BridgeID)
	}
}
