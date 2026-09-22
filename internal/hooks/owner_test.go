package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOwnerOwns is BR-09's ownership rule. The adversarial rows are the
// point: a plain substring match on "agentpulse" would have deleted every
// one of them.
func TestOwnerOwns(t *testing.T) {
	owner := NewOwner(testBinary)

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"our own installed entry", testBinary + " hook", true},
		{"our own entry, single-quoted path", "'" + testBinary + "' hook", true},
		{"our own entry, double-quoted path", `"` + testBinary + `" hook`, true},
		{"bare name on PATH", "agentpulse hook", true},
		{"dev record shape", "env AGENTPULSE_RECORD_DIR='/tmp/rec' '" + testBinary + "' hook", true},
		{"the binary after a package upgrade moved it", "/opt/homebrew/bin/agentpulse hook", true},
		{"extra arguments after hook", testBinary + " hook --debug", true},

		{"a wrapper with our name inside its own", "/usr/local/bin/my-agentpulse-wrapper hook", false},
		{"a program whose name merely starts the same", "notagentpulse hook", false},
		{"a command that only mentions us", "echo agentpulse", false},
		{"a third-party hook that mentions us in its arguments", "/usr/bin/notify --title 'agentpulse hook ran'", false},
		{"our binary with a different subcommand", testBinary + " flush", false},
		{"our name as an argument to something else", "/usr/bin/env agentpulse hook", false},
		{"a relative path ending in our name", "./agentpulse hook", false},
		{"a sibling binary in our own directory", "/opt/agentpulse/bin/agentpulse-next hook", false},
		{"empty command", "", false},
		{"our binary with no subcommand", testBinary, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := owner.Owns(tc.command); got != tc.want {
				t.Errorf("Owns(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestSplitCommandWordsBackslashEscape covers a binary
// path containing a single quote round-tripping through the exact escaping
// shellQuoteIfNeeded (settings.go) writes for it, so a re-pair recognizes
// its own previously installed entry instead of treating it as a stray
// backslash and a mismatched path.
func TestSplitCommandWordsBackslashEscape(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{"embedded apostrophe, shellQuoteSingle's escaping", `'/Users/O'\''Brien/agentpulse' hook`, []string{"/Users/O'Brien/agentpulse", "hook"}},
		{"backslash-escaped space outside quotes", `/opt/my\ dir/agentpulse hook`, []string{"/opt/my dir/agentpulse", "hook"}},
		{"trailing lone backslash is dropped, not appended literally", `agentpulse\`, []string{"agentpulse"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCommandWords(tc.command)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCommandWords(%q) = %q, want %q", tc.command, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitCommandWords(%q)[%d] = %q, want %q", tc.command, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPlanUninstallRemovesOnlyOwnedEntries proves the rule end to end:
// one settings file holding our entry next to every adversarial neighbour,
// and exactly one entry gone afterwards (BR-09).
func TestPlanUninstallRemovesOnlyOwnedEntries(t *testing.T) {
	neighbours := []string{
		"/usr/local/bin/my-agentpulse-wrapper hook",
		"notagentpulse hook",
		"echo agentpulse",
		"/usr/bin/notify --title 'agentpulse hook ran'",
		"/opt/agentpulse/bin/agentpulse flush",
		"./agentpulse hook",
	}

	hooksArray := []map[string]any{
		{"matcher": "", "hooks": []map[string]any{{"type": "command", "command": testBinary + " hook", "timeout": 10}}},
	}
	for _, cmd := range neighbours {
		hooksArray = append(hooksArray, map[string]any{
			"matcher": "",
			"hooks":   []map[string]any{{"type": "command", "command": cmd, "timeout": 5}},
		})
	}
	doc := map[string]any{"hooks": map[string]any{"PreToolUse": hooksArray}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settingsPath, canonical(t, string(raw)), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanUninstall(settingsPath, NewOwner(testBinary))
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if !plan.Changed {
		t.Fatal("PlanUninstall made no change, want our one entry removed")
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var after struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	data, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatalf("settings no longer parse: %v\n%s", err, data)
	}

	var got []string
	for _, group := range after.Hooks["PreToolUse"] {
		for _, h := range group.Hooks {
			got = append(got, h.Command)
		}
	}
	if len(got) != len(neighbours) {
		t.Fatalf("after uninstall there are %d entries, want %d:\n%s", len(got), len(neighbours), data)
	}
	for i, want := range neighbours {
		if got[i] != want {
			t.Errorf("entry %d = %q, want %q (order and content must be untouched)", i, got[i], want)
		}
	}
}
