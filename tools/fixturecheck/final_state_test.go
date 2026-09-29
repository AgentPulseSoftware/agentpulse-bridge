package main

import "testing"

func TestComputeFinalState(t *testing.T) {
	ev := func(typ string, payload map[string]any) actualEvent {
		return actualEvent{Type: typ, Payload: payload}
	}
	paused := ev("paused", map[string]any{"cause": "usage_limit"})
	needs := ev("needs_input", map[string]any{"kind": "permission"})
	read := ev("activity", map[string]any{"category": "read"})

	tests := []struct {
		name   string
		events []actualEvent
		want   string
	}{
		{"input_resolved clears needs_you", []actualEvent{needs, ev("input_resolved", nil)}, "working"},
		{"input_resolved leaves reading alone", []actualEvent{read, ev("input_resolved", nil)}, "reading"},
		{"input_resolved leaves paused alone", []actualEvent{paused, ev("input_resolved", nil)}, "paused"},
		{"pr_created leaves paused alone", []actualEvent{paused, ev("pr_created", nil)}, "paused"},
		{"commit leaves paused alone", []actualEvent{paused, ev("commit", nil)}, "paused"},
		{"commit clears needs_you", []actualEvent{needs, ev("commit", nil)}, "working"},
		{"prompt leaves paused", []actualEvent{paused, ev("prompt_submitted", nil)}, "working"},
		{"session_end other after paused ends it", []actualEvent{paused, ev("session_end", map[string]any{"reason": "other"})}, "ended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeFinalState(tt.events); got != tt.want {
				t.Errorf("computeFinalState = %q, want %q", got, tt.want)
			}
		})
	}
}
