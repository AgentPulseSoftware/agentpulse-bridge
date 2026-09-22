package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func lookPathReturning(path string, err error) func(string) (string, error) {
	return func(string) (string, error) {
		return path, err
	}
}

func TestCheckBinaryOnPath(t *testing.T) {
	notOnPath := errors.New("exec: \"agentpulse\": executable file not found in $PATH")

	tests := []struct {
		name          string
		lookPath      func(string) (string, error)
		installed     map[string][]string
		installedErr  error
		wantVerdict   Verdict
		wantInFinding string
	}{
		{
			name:          "absent from PATH",
			lookPath:      lookPathReturning("", notOnPath),
			wantVerdict:   WARN,
			wantInFinding: "agentpulse",
		},
		{
			name:     "present and matching",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			installed: map[string][]string{
				"Stop": {"/opt/agentpulse/agentpulse hook"},
			},
			wantVerdict:   PASS,
			wantInFinding: "/opt/agentpulse/agentpulse",
		},
		{
			name:     "registered command points elsewhere",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			installed: map[string][]string{
				"Stop": {"/old/homebrew/path/agentpulse hook"},
			},
			wantVerdict:   WARN,
			wantInFinding: "/old/homebrew/path/agentpulse",
		},
		{
			name:        "nothing registered at all",
			lookPath:    lookPathReturning("/opt/agentpulse/agentpulse", nil),
			wantVerdict: WARN,
		},
		{
			name:          "settings unreadable",
			lookPath:      lookPathReturning("/opt/agentpulse/agentpulse", nil),
			installedErr:  errors.New("boom"),
			wantVerdict:   WARN,
			wantInFinding: "could not compare",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBinaryOnPath("agentpulse", "", tt.lookPath, tt.installed, tt.installedErr)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdict, got.Finding)
			}
			if got.Verdict != PASS && got.Remedy == "" {
				t.Errorf("non-PASS result has empty Remedy (finding: %q)", got.Finding)
			}
			if tt.wantInFinding != "" && !strings.Contains(got.Finding, tt.wantInFinding) {
				t.Errorf("Finding = %q, want it to contain %q", got.Finding, tt.wantInFinding)
			}
		})
	}
}

func TestCheckBinaryOnPathNeverFails(t *testing.T) {
	// Documentation-as-test: check 1 (SPEC 7.4's "keeps working with less
	// convenience" case) has no path to FAIL, whatever combination of
	// inputs it's given.
	cases := []Result{
		checkBinaryOnPath("agentpulse", "", lookPathReturning("", errors.New("no")), nil, nil),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), nil, errors.New("boom")),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), map[string][]string{"Stop": {"/y/agentpulse hook"}}, nil),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), map[string][]string{"Stop": {"/x/agentpulse hook"}}, nil),
	}
	for _, r := range cases {
		if r.Verdict == FAIL {
			t.Errorf("checkBinaryOnPath returned FAIL (finding: %q); check 1 must never FAIL", r.Finding)
		}
	}
}

func TestCheckSettingsFile(t *testing.T) {
	allEight := func() map[string][]string {
		m := map[string][]string{}
		for _, e := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "SessionEnd"} {
			m[e] = []string{"/opt/agentpulse hook"}
		}
		return m
	}

	tests := []struct {
		name          string
		settingsPath  string
		installed     map[string][]string
		installedErr  error
		wantVerdict   Verdict
		wantInFinding string
	}{
		{
			name:          "missing",
			settingsPath:  "/home/sam/.claude/settings.json",
			installedErr:  fmt.Errorf("reading /home/sam/.claude/settings.json: %w", fs.ErrNotExist),
			wantVerdict:   FAIL,
			wantInFinding: "/home/sam/.claude/settings.json",
		},
		{
			name:          "unparseable",
			settingsPath:  "/home/sam/.claude/settings.json",
			installedErr:  errors.New("line 3: unexpected token"),
			wantVerdict:   FAIL,
			wantInFinding: "line 3",
		},
		{
			name:         "complete",
			settingsPath: "/home/sam/.claude/settings.json",
			installed:    allEight(),
			wantVerdict:  PASS,
		},
		{
			name:         "missing three events",
			settingsPath: "/home/sam/.claude/settings.json",
			installed: map[string][]string{
				"SessionStart":     {"/opt/agentpulse hook"},
				"UserPromptSubmit": {"/opt/agentpulse hook"},
				"PreToolUse":       {"/opt/agentpulse hook"},
				"PostToolUse":      {"/opt/agentpulse hook"},
				"Stop":             {"/opt/agentpulse hook"},
			},
			wantVerdict:   FAIL,
			wantInFinding: "PermissionRequest, Notification, SessionEnd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkSettingsFile(tt.settingsPath, tt.installed, tt.installedErr)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdict, got.Finding)
			}
			if got.Verdict != PASS && got.Remedy == "" {
				t.Errorf("non-PASS result has empty Remedy (finding: %q)", got.Finding)
			}
			if tt.wantInFinding != "" && !strings.Contains(got.Finding, tt.wantInFinding) {
				t.Errorf("Finding = %q, want it to contain %q", got.Finding, tt.wantInFinding)
			}
		})
	}
}

func TestCommandBinary(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"/opt/agentpulse hook", "/opt/agentpulse"},
		{"'/opt/My Recordings/agentpulse' hook", "/opt/My Recordings/agentpulse"},
		{"'/opt/O'\\''Brien/agentpulse' hook", "/opt/O'Brien/agentpulse"},
	}
	for _, tt := range tests {
		if got := commandBinary(tt.command); got != tt.want {
			t.Errorf("commandBinary(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}
