package classify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The error text a StopFailure fixture or test carries: a path and a
// sentence, neither of which may ever reach an event (ADR-006 section 7).
const (
	secretPath     = "/Users/someone/secret-project/main.go"
	secretSentence = "The build broke while editing the payment module."
)

func stopFailureDoc(t *testing.T, fields map[string]any) HookInput {
	t.Helper()
	doc := map[string]any{
		"hook_event_name":        HookStopFailure,
		"session_id":             "sess_paused",
		"cwd":                    "/nonexistent/agentpulse-test-project",
		"error_details":          "429 Too Many Requests while reading " + secretPath,
		"last_assistant_message": "API Error: " + secretSentence + " " + secretPath,
	}
	for k, v := range fields {
		doc[k] = v
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseHookInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	in.Now = fixedNow
	in.BridgeID = "brg_test0001"
	in.BridgeVersion = "0.0.0-test"
	in.SubagentsOn = true
	return in
}

// TestStopFailureErrorTable is ADR-006 section 2's table: every
// documented error value, an invented one, and an empty one.
func TestStopFailureErrorTable(t *testing.T) {
	sch := loadEventSchema(t)
	tests := []struct {
		errorValue string
		want       string
	}{
		{"rate_limit", CauseUsageLimit},
		{"billing_error", CauseBilling},
		{"account_on_hold", CauseBilling},
		{"authentication_failed", CauseAuth},
		{"oauth_org_not_allowed", CauseAuth},
		{"cloud_credential_error", CauseAuth},
		{"overloaded", CauseOverloaded},
		{"max_output_tokens", CauseOutputLimit},
		{"server_error", CauseAPIError},
		{"invalid_request", CauseAPIError},
		{"model_not_found", CauseAPIError},
		{"unknown", CauseAPIError},
		{"a_value_claude_code_adds_later", CauseAPIError},
		{"", CauseAPIError},
		// Exact comparison only: no case folding, trimming or prefixes.
		{"Rate_Limit", CauseAPIError},
		{" rate_limit", CauseAPIError},
		{"rate_limit_exceeded", CauseAPIError},
	}
	for _, tt := range tests {
		t.Run(tt.errorValue, func(t *testing.T) {
			in := stopFailureDoc(t, map[string]any{"error": tt.errorValue})
			ev, ok := Classify(in, NewSessionState())
			if !ok {
				t.Fatal("StopFailure on the main chain emitted nothing")
			}
			if ev.Type != TypePaused || ev.Subagent != nil {
				t.Errorf("Type = %q, Subagent = %+v; want paused without subagent", ev.Type, ev.Subagent)
			}
			payload, err := json.Marshal(ev.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"cause":"` + tt.want + `"}`; string(payload) != want {
				t.Errorf("payload = %s, want %s", payload, want)
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

// TestStopFailureInSubagentEmitsNothing: a StopFailure carrying agent_id
// sends nothing, with subagent chains on or paused (ADR-006 section 2),
// and leaves the session's own state alone.
func TestStopFailureInSubagentEmitsNothing(t *testing.T) {
	for _, on := range []bool{true, false} {
		in := stopFailureDoc(t, map[string]any{"error": "rate_limit", "agent_id": "a1b2c3", "agent_type": "reviewer"})
		in.SubagentsOn = on
		state := NewSessionState()
		if ev, ok := Classify(in, state); ok || ev != nil {
			t.Errorf("SubagentsOn=%v: emitted %+v, want nothing", on, ev)
		}
		if !state.LastEmittedAt.IsZero() || state.Project != nil {
			t.Errorf("SubagentsOn=%v: session state changed: %+v", on, state.ChainState)
		}
	}
}

// TestStopFailureDoesNotReArmTaskLabel is BR-17 as ADR-006 section 2
// amends it: the prompt that continues after a pause (Claude Code's own
// fixed text) gets no label, where a prompt after a Stop would.
func TestStopFailureDoesNotReArmTaskLabel(t *testing.T) {
	state := NewSessionState()
	prompt := func(text string) PromptSubmittedPayload {
		in := baseInput("sess_paused", HookUserPromptSubmit)
		in.Prompt = text
		in.TaskLabelOn = true
		ev, _ := Classify(in, state)
		return ev.Payload.(PromptSubmittedPayload)
	}
	if got := prompt("fix the login bug"); got.TaskLabel != "fix the login bug" {
		t.Fatalf("first prompt TaskLabel = %q, want the label", got.TaskLabel)
	}
	if _, ok := Classify(stopFailureDoc(t, map[string]any{"error": "rate_limit"}), state); !ok {
		t.Fatal("StopFailure emitted nothing")
	}
	if state.AwaitingTaskLabel {
		t.Error("StopFailure armed the task label")
	}
	if got := prompt("Continue from where you left off."); got.TaskLabel != "" {
		t.Errorf("continuation prompt TaskLabel = %q, want none", got.TaskLabel)
	}

	// A Stop still re-arms it afterwards.
	Classify(baseInput("sess_paused", HookStop), state)
	if got := prompt("now the logout bug"); got.TaskLabel != "now the logout bug" {
		t.Errorf("prompt after Stop TaskLabel = %q, want the label", got.TaskLabel)
	}
}

// TestQuotaNotifications is ADR-006 section 2's Notification table.
func TestQuotaNotifications(t *testing.T) {
	sch := loadEventSchema(t)
	tests := []struct {
		name      string
		notifType string
		message   string
		agentID   string
		wantType  string // "" for no event
		wantCause string
	}{
		{"stale reset pauses with limit_reset", "quota_auto_resume_stale", "Your usage limit reset. Press Enter.", "", TypePaused, CauseLimitReset},
		{"auto-resume fired sends nothing", "quota_auto_resume_fired", "Continuing.", "", "", ""},
		{"auto-resume disabled sends nothing", "quota_auto_resume_disabled", "Not continuing.", "", "", ""},
		{"stale reset inside a subagent sends nothing", "quota_auto_resume_stale", "Your usage limit reset.", "a1b2c3", "", ""},
		{"quota type wins over a permission-looking message", "quota_auto_resume_fired", "Claude needs your permission to continue", "", "", ""},
		{"permission prompt path unchanged", "permission_prompt", "Claude needs your permission to use Bash", "", TypeNeedsInput, ""},
		{"idle prompt path unchanged", "idle_prompt", "Claude is waiting for your input", "", "", ""},
		{"no type, permission message: unchanged", "", "Claude needs your permission to use Bash", "", TypeNeedsInput, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := baseInput("sess_paused", HookNotification)
			in.NotificationType = tt.notifType
			in.Message = tt.message
			in.AgentID = tt.agentID
			in.SubagentsOn = true
			ev, ok := Classify(in, NewSessionState())
			if tt.wantType == "" {
				if ok || ev != nil {
					t.Errorf("emitted %+v, want nothing", ev)
				}
				return
			}
			if !ok || ev.Type != tt.wantType {
				t.Fatalf("emitted %+v (ok=%v), want %s", ev, ok, tt.wantType)
			}
			if tt.wantType == TypePaused {
				if p, _ := ev.Payload.(PausedPayload); p.Cause != tt.wantCause || ev.Subagent != nil {
					t.Errorf("payload = %+v, subagent = %+v; want cause %s, no subagent", ev.Payload, ev.Subagent, tt.wantCause)
				}
				if data, _ := json.Marshal(ev); strings.Contains(string(data), "Press Enter") {
					t.Errorf("event carries the notification message: %s", data)
				}
			}
			assertValidEvent(t, sch, ev)
		})
	}
}

// TestPausedIsNeverSuppressed: repeated pauses a moment apart all emit
// (only activity is ever suppressed, SPEC 7.2).
func TestPausedIsNeverSuppressed(t *testing.T) {
	state := NewSessionState()
	for i := 0; i < 3; i++ {
		in := stopFailureDoc(t, map[string]any{"error": "rate_limit"})
		in.Now = fixedNow.Add(time.Duration(i) * time.Millisecond)
		if _, ok := Classify(in, state); !ok {
			t.Errorf("pause %d was suppressed", i)
		}
	}
}

// TestStopFailureEventCarriesNoInputText is ADR-006 section 7's privacy
// test at the classifier: the emitted bytes contain neither the error
// text nor the raw error value. cmd/agentpulse's
// TestStopFailureNeverWritesErrorText covers the log and every file.
func TestStopFailureEventCarriesNoInputText(t *testing.T) {
	const invented = "zz_invented_error_value"
	for _, errorValue := range []string{"rate_limit", invented} {
		ev, ok := Classify(stopFailureDoc(t, map[string]any{"error": errorValue}), NewSessionState())
		if !ok {
			t.Fatal("no event")
		}
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{secretPath, secretSentence, "secret-project", "Too Many Requests", "API Error", invented, "rate_limit"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("event contains %q: %s", secret, data)
			}
		}
	}
}

func TestParseHookInputReadsOnlyTheCodeFields(t *testing.T) {
	in := stopFailureDoc(t, map[string]any{"error": "rate_limit", "notification_type": "quota_auto_resume_stale"})
	if in.Error != "rate_limit" || in.NotificationType != "quota_auto_resume_stale" {
		t.Errorf("Error = %q, NotificationType = %q", in.Error, in.NotificationType)
	}
	// HookInput has no field that could hold the message text at all;
	// marshaling it (its json tags) must not bring the text back.
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretPath, secretSentence} {
		if strings.Contains(string(data), secret) {
			t.Errorf("HookInput holds %q: %s", secret, data)
		}
	}
}
