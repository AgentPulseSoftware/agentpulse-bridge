package classify

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// subagentTypePattern and subagentIDPattern are ADR-005 section 1's
// patterns, exactly as the event.v1 schema states them.
var (
	subagentTypePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,39}$`)
	subagentIDPattern   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func TestSubagentType(t *testing.T) {
	forty := strings.Repeat("a", 40)
	tests := []struct {
		name, in, want string
	}{
		{"plain", "reviewer", "reviewer"},
		{"plugin namespace", "my-plugin:db-agent", "my-plugin:db-agent"},
		{"dots and underscores", "v2.code_reviewer", "v2.code_reviewer"},
		{"digit first", "2nd-opinion", "2nd-opinion"},
		{"surrounding whitespace trimmed", " \t reviewer \n", "reviewer"},
		{"exactly 40", forty, forty},
		{"41 cut to 40", forty + "b", forty},
		{"long, cut leaves a valid prefix", forty + " rm -rf", forty},
		{"empty", "", fallbackSubagentType},
		{"only whitespace", "   ", fallbackSubagentType},
		{"absolute path", "/Users/someone/.claude/agents/reviewer.md", fallbackSubagentType},
		{"relative path", "agents/reviewer", fallbackSubagentType},
		{"backslash path", `agents\reviewer`, fallbackSubagentType},
		{"dot first", "../reviewer", fallbackSubagentType},
		{"space", "code reviewer", fallbackSubagentType},
		{"double quote", `review"er`, fallbackSubagentType},
		{"single quote", "review'er", fallbackSubagentType},
		{"shell", "$(whoami)", fallbackSubagentType},
		{"equals", "a=b", fallbackSubagentType},
		{"markup", "<b>reviewer</b>", fallbackSubagentType},
		{"newline inside", "review\ner", fallbackSubagentType},
		{"non-ASCII letter", "réviseur", fallbackSubagentType},
		{"emoji", "reviewer🤖", fallbackSubagentType},
		{"hyphen first", "-reviewer", fallbackSubagentType},
		{"NUL", "review\x00er", fallbackSubagentType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubagentType(tt.in)
			if got != tt.want {
				t.Errorf("SubagentType(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !subagentTypePattern.MatchString(got) {
				t.Errorf("SubagentType(%q) = %q does not match the schema pattern", tt.in, got)
			}
		})
	}
}

func TestSubagentID(t *testing.T) {
	// sha256("a1b2c3d4e5f60001") begins 3a1f0d3f34eb433f.
	if got, want := SubagentID("a1b2c3d4e5f60001"), "3a1f0d3f34eb433f"; got != want {
		t.Errorf("SubagentID = %q, want %q", got, want)
	}
	for _, in := range []string{"", "x", strings.Repeat("z", 4096), "agent with spaces/and/slashes"} {
		got := SubagentID(in)
		if !subagentIDPattern.MatchString(got) {
			t.Errorf("SubagentID(%q) = %q does not match ^[0-9a-f]{16}$", in, got)
		}
		if in != "" && strings.Contains(got, in) {
			t.Errorf("SubagentID(%q) = %q contains its input", in, got)
		}
	}
}

// subInput is baseInput inside subagent agentID, with classification on.
func subInput(hookEvent, agentID, agentType string) HookInput {
	in := baseInput("sess_sub", hookEvent)
	in.AgentID = agentID
	in.AgentType = agentType
	in.SubagentsOn = true
	return in
}

func TestSubagentEventsCarryTheEnvelope(t *testing.T) {
	sch := loadEventSchema(t)
	state := NewSessionState()
	want := &Subagent{ID: SubagentID("agent-1"), Type: "reviewer"}

	pre := subInput(HookPreToolUse, "agent-1", "reviewer")
	pre.ToolName = "Bash"
	pre.ToolInput = toolInputJSON(t, map[string]any{"command": "pytest -q"})
	post := subInput(HookPostToolUse, "agent-1", "reviewer")
	post.ToolResponse = toolResponseJSON(t, "3 passed in 0.1s", "")
	perm := subInput(HookPermissionRequest, "agent-1", "reviewer")
	perm.ToolName = "Bash"
	notify := subInput(HookNotification, "agent-1", "reviewer")
	notify.Message = "Claude needs your permission to use Bash"
	notify.Now = fixedNow.Add(time.Minute) // outside the dedup window

	steps := []struct {
		in       HookInput
		wantType string
	}{
		{subInput(HookSubagentStart, "agent-1", "reviewer"), TypeSubagentStart},
		{pre, TypeVerificationStarted},
		{post, TypeVerificationFinished},
		{perm, TypeNeedsInput},
		{notify, TypeNeedsInput},
		{subInput(HookSubagentStop, "agent-1", "reviewer"), TypeSubagentStop},
	}
	for _, s := range steps {
		ev, ok := Classify(s.in, state)
		if !ok {
			t.Fatalf("%s: no event, want %s", s.in.HookEventName, s.wantType)
		}
		if ev.Type != s.wantType {
			t.Errorf("%s: Type = %q, want %q", s.in.HookEventName, ev.Type, s.wantType)
		}
		if ev.Subagent == nil || *ev.Subagent != *want {
			t.Errorf("%s: Subagent = %+v, want %+v", s.in.HookEventName, ev.Subagent, want)
		}
		if ev.SessionID != "sess_sub" {
			t.Errorf("%s: SessionID = %q, want the parent's", s.in.HookEventName, ev.SessionID)
		}
		assertValidEvent(t, sch, ev)
	}
	if len(state.Subagents) != 0 {
		t.Errorf("chain not deleted on subagent_stop: %d left", len(state.Subagents))
	}
}

func TestExcludedEventTypesInsideASubagentEmitNothing(t *testing.T) {
	for _, hook := range []string{HookSessionStart, HookUserPromptSubmit, HookStop, HookSessionEnd} {
		t.Run(hook, func(t *testing.T) {
			state := NewSessionState()
			state.AwaitingTaskLabel = false
			if ev, ok := Classify(subInput(hook, "agent-1", "reviewer"), state); ok {
				t.Errorf("emitted %s, want nothing", ev.Type)
			}
			if state.AwaitingTaskLabel || state.Project != nil {
				t.Errorf("excluded hook changed the session scratch: %+v", state)
			}
		})
	}
}

func TestSubagentsOffIgnoresAgentFields(t *testing.T) {
	for _, hook := range []string{HookSubagentStart, HookSubagentStop} {
		in := subInput(hook, "agent-1", "reviewer")
		in.SubagentsOn = false
		if ev, ok := Classify(in, NewSessionState()); ok {
			t.Errorf("%s with subagents off emitted %s, want nothing", hook, ev.Type)
		}
	}
	in := subInput(HookStop, "agent-1", "reviewer")
	in.SubagentsOn = false
	ev, ok := Classify(in, NewSessionState())
	if !ok || ev.Type != TypeStop || ev.Subagent != nil {
		t.Errorf("Stop with subagents off = %+v, %v; want today's stop without subagent", ev, ok)
	}
}

func readInput(hookEvent, agentID string, now time.Time) HookInput {
	in := baseInput("sess_sub", hookEvent)
	if agentID != "" {
		in = subInput(hookEvent, agentID, "reviewer")
	}
	in.ToolName = "Read"
	in.Now = now
	return in
}

func TestSuppressionIsPerChain(t *testing.T) {
	state := NewSessionState()
	if _, ok := Classify(readInput(HookPreToolUse, "", fixedNow), state); !ok {
		t.Fatal("main-chain read suppressed, want emitted")
	}
	ev, ok := Classify(readInput(HookPreToolUse, "agent-1", fixedNow.Add(time.Second)), state)
	if !ok || ev.Subagent == nil {
		t.Fatal("subagent read in the same window suppressed by the main chain's, want emitted")
	}
	if _, ok := Classify(readInput(HookPreToolUse, "", fixedNow.Add(2*time.Second)), state); ok {
		t.Error("second main-chain read emitted, want suppressed by its own chain")
	}
	if _, ok := Classify(readInput(HookPreToolUse, "agent-1", fixedNow.Add(3*time.Second)), state); ok {
		t.Error("second subagent read emitted, want suppressed by its own chain")
	}
}

func TestInputResolvedIsPerChain(t *testing.T) {
	state := NewSessionState()
	ask := subInput(HookPreToolUse, "agent-a", "planner")
	ask.ToolName = "AskUserQuestion"
	if ev, ok := Classify(ask, state); !ok || ev.Type != TypeNeedsInput {
		t.Fatalf("A's AskUserQuestion = %v, %v; want needs_input", ev, ok)
	}
	if ev, ok := Classify(subInput(HookPostToolUse, "agent-b", "reviewer"), state); ok {
		t.Errorf("B's PostToolUse emitted %s, want nothing: it must not resolve A's question", ev.Type)
	}
	if ev, ok := Classify(baseInput("sess_sub", HookPostToolUse), state); ok {
		t.Errorf("main chain's PostToolUse emitted %s, want nothing", ev.Type)
	}
	ev, ok := Classify(subInput(HookPostToolUse, "agent-a", "planner"), state)
	if !ok || ev.Type != TypeInputResolved || ev.Subagent == nil || ev.Subagent.ID != SubagentID("agent-a") {
		t.Errorf("A's PostToolUse = %+v, %v; want input_resolved for A", ev, ok)
	}
}

// A permission_prompt Notification may arrive without the agent_id of the
// subagent whose PermissionRequest it repeats (COMPATIBILITY.md), so the
// dedup window spans every chain of the session.
func TestPermissionPromptDedupSpansChains(t *testing.T) {
	note := func(at time.Time) HookInput {
		in := baseInput("sess_sub", HookNotification)
		in.Message = "Claude needs your permission to use Bash"
		in.Now = at
		return in
	}
	state := NewSessionState()
	perm := subInput(HookPermissionRequest, "agent-a", "planner")
	perm.ToolName = "Bash"
	if ev, ok := Classify(perm, state); !ok || ev.Type != TypeNeedsInput || ev.Subagent == nil {
		t.Fatalf("A's PermissionRequest = %+v, %v; want needs_input for A", ev, ok)
	}
	if ev, ok := Classify(note(fixedNow.Add(time.Second)), state); ok {
		t.Errorf("permission prompt without agent_id a second after A's PermissionRequest emitted %+v, want nothing", ev)
	}
	if ev, ok := Classify(note(fixedNow.Add(permissionDedupWindow+time.Second)), state); !ok || ev.Type != TypeNeedsInput {
		t.Errorf("permission prompt after the dedup window = %+v, %v; want needs_input", ev, ok)
	}
}

func TestSubagentEventsUseTheParentProject(t *testing.T) {
	state := NewSessionState()
	parent, _ := Classify(baseInput("sess_sub", HookSessionStart), state)
	in := readInput(HookPreToolUse, "agent-1", fixedNow)
	in.Cwd = "/nonexistent/somewhere-else"
	ev, ok := Classify(in, state)
	if !ok {
		t.Fatal("subagent read emitted nothing")
	}
	if ev.Project != parent.Project {
		t.Errorf("subagent Project = %+v, want the parent's %+v", ev.Project, parent.Project)
	}
}

func TestSubagentChainsAreCapped(t *testing.T) {
	state := NewSessionState()
	for i := 0; i < maxSubagentChains+4; i++ {
		id := string(rune('A' + i))
		ev, ok := Classify(readInput(HookPreToolUse, id, fixedNow), state)
		if i < maxSubagentChains && !ok {
			t.Fatalf("chain %d: first read suppressed", i)
		}
		if ok && ev.Subagent == nil {
			t.Errorf("chain %d: folded event lost its subagent envelope", i)
		}
	}
	if len(state.Subagents) != maxSubagentChains {
		t.Errorf("len(Subagents) = %d, want the cap %d", len(state.Subagents), maxSubagentChains)
	}

	// An hour later the idle chains make room for a new one.
	if _, ok := Classify(readInput(HookPreToolUse, "late", fixedNow.Add(idleChainAge)), state); !ok {
		t.Error("new chain after the idle period suppressed, want its own chain")
	}
	if _, ok := state.Subagents[SubagentID("late")]; !ok || len(state.Subagents) != 1 {
		t.Errorf("idle chains not evicted: %d chains", len(state.Subagents))
	}
}

// A subagent beyond the cap must not share the main chain's scratch: its
// PreToolUse would clear the main chain's pending verification and its
// read would suppress the main chain's next read.
func TestOverflowSubagentDoesNotTouchTheMainChain(t *testing.T) {
	state := NewSessionState()
	for i := 0; i < maxSubagentChains; i++ {
		Classify(readInput(HookPreToolUse, string(rune('A'+i)), fixedNow), state)
	}

	goTest := baseInput("sess_sub", HookPreToolUse)
	goTest.ToolName = "Bash"
	goTest.ToolInput = toolInputJSON(t, map[string]any{"command": "go test ./..."})
	goTest.Now = fixedNow.Add(time.Second)
	if ev, ok := Classify(goTest, state); !ok || ev.Type != TypeVerificationStarted {
		t.Fatalf("main chain's go test = %+v, %v; want verification_started", ev, ok)
	}

	if ev, ok := Classify(readInput(HookPreToolUse, "overflow", fixedNow.Add(2*time.Second)), state); !ok || ev.Subagent == nil {
		t.Fatalf("overflow subagent's read = %+v, %v; want activity with its envelope", ev, ok)
	}
	if _, ok := state.Subagents[SubagentID("overflow")]; ok {
		t.Fatal("setup: overflow subagent got its own chain, want it past the cap")
	}

	done := baseInput("sess_sub", HookPostToolUse)
	done.ToolName = "Bash"
	done.ToolResponse = toolResponseJSON(t, "ok  \texample.com/pkg\t0.01s", "")
	done.Now = fixedNow.Add(3 * time.Second)
	if ev, ok := Classify(done, state); !ok || ev.Type != TypeVerificationFinished {
		t.Errorf("main chain's PostToolUse = %+v, %v; want verification_finished", ev, ok)
	}
	if ev, ok := Classify(readInput(HookPreToolUse, "", fixedNow.Add(4*time.Second)), state); !ok || ev.Subagent != nil {
		t.Errorf("main chain's read = %+v, %v; want activity, not suppressed by the overflow subagent's", ev, ok)
	}
}
