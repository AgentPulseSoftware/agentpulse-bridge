package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func lookPathReturning(path string, err error) func(string) (string, error) {
	return func(string) (string, error) {
		return path, err
	}
}

// fakeFileInfo is a minimal fs.FileInfo stub for the stat-based part of
// check 1: only Mode and IsDir are ever consulted.
type fakeFileInfo struct {
	mode  fs.FileMode
	isDir bool
}

func (f fakeFileInfo) Name() string           { return "" }
func (f fakeFileInfo) Size() int64            { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode      { return f.mode }
func (f fakeFileInfo) ModTime() (t time.Time) { return t }
func (f fakeFileInfo) IsDir() bool            { return f.isDir }
func (f fakeFileInfo) Sys() any               { return nil }

// statAlwaysExecutable is a stat stub reporting every path as a plain,
// executable file — used by table cases that are not about P5-27's
// existence check at all, so a fictitious path (e.g. "/x/agentpulse")
// used to exercise some other branch does not trip it.
func statAlwaysExecutable(string) (fs.FileInfo, error) {
	return fakeFileInfo{mode: 0o755}, nil
}

func TestCheckBinaryOnPath(t *testing.T) {
	notOnPath := errors.New("exec: \"agentpulse\": executable file not found in $PATH")

	tests := []struct {
		name          string
		lookPath      func(string) (string, error)
		stat          func(string) (fs.FileInfo, error)
		installed     map[string][]string
		installedErr  error
		wantVerdict   Verdict
		wantInFinding string
		wantRemedy    string
	}{
		{
			name:          "absent from PATH",
			lookPath:      lookPathReturning("", notOnPath),
			stat:          statAlwaysExecutable,
			wantVerdict:   WARN,
			wantInFinding: "agentpulse",
		},
		{
			name:     "present and matching",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:     statAlwaysExecutable,
			installed: map[string][]string{
				"Stop": {"/opt/agentpulse/agentpulse hook"},
			},
			wantVerdict:   PASS,
			wantInFinding: "/opt/agentpulse/agentpulse",
		},
		{
			name:     "registered command points elsewhere",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:     statAlwaysExecutable,
			installed: map[string][]string{
				"Stop": {"/old/homebrew/path/agentpulse hook"},
			},
			wantVerdict:   WARN,
			wantInFinding: "/old/homebrew/path/agentpulse",
		},
		{
			name:        "nothing registered at all",
			lookPath:    lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:        statAlwaysExecutable,
			wantVerdict: WARN,
		},
		{
			name:          "settings unreadable",
			lookPath:      lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:          statAlwaysExecutable,
			installedErr:  errors.New("boom"),
			wantVerdict:   WARN,
			wantInFinding: "could not compare",
		},
		{
			// P5-27: the exact condition a Homebrew upgrade leaves
			// behind — the registered path is the old, now-deleted
			// Cellar path.
			name:     "registered binary does not exist (Homebrew upgrade)",
			lookPath: lookPathReturning("/opt/homebrew/bin/agentpulse", nil),
			stat:     func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
			installed: map[string][]string{
				"Stop": {"/opt/homebrew/Cellar/agentpulse/1.2.0/bin/agentpulse hook"},
			},
			wantVerdict:   FAIL,
			wantInFinding: "/opt/homebrew/Cellar/agentpulse/1.2.0/bin/agentpulse does not exist",
			wantRemedy:    "agentpulse pair --hooks-only",
		},
		{
			name:     "registered binary is not executable",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:     func(string) (fs.FileInfo, error) { return fakeFileInfo{mode: 0o644}, nil },
			installed: map[string][]string{
				"Stop": {"/opt/agentpulse/agentpulse hook"},
			},
			wantVerdict:   FAIL,
			wantInFinding: "is not executable",
			wantRemedy:    "agentpulse pair --hooks-only",
		},
		{
			// stat unset entirely (Deps.StatBinary not wired up): the
			// existence check is skipped, not treated as broken.
			name:     "no stat function wired up",
			lookPath: lookPathReturning("/opt/agentpulse/agentpulse", nil),
			stat:     nil,
			installed: map[string][]string{
				"Stop": {"/opt/agentpulse/agentpulse hook"},
			},
			wantVerdict: PASS,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBinaryOnPath("agentpulse", "", tt.lookPath, tt.stat, tt.installed, tt.installedErr)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdict, got.Finding)
			}
			if got.Verdict != PASS && got.Remedy == "" {
				t.Errorf("non-PASS result has empty Remedy (finding: %q)", got.Finding)
			}
			if tt.wantInFinding != "" && !strings.Contains(got.Finding, tt.wantInFinding) {
				t.Errorf("Finding = %q, want it to contain %q", got.Finding, tt.wantInFinding)
			}
			if tt.wantRemedy != "" && !strings.Contains(got.Remedy, tt.wantRemedy) {
				t.Errorf("Remedy = %q, want it to contain %q", got.Remedy, tt.wantRemedy)
			}
		})
	}
}

func TestCheckBinaryOnPathNeverFailsWithoutAStatFunction(t *testing.T) {
	// Documentation-as-test: a nil stat (Deps.StatBinary unset) leaves
	// check 1 exactly as before P5-27 — it can only PASS or WARN.
	cases := []Result{
		checkBinaryOnPath("agentpulse", "", lookPathReturning("", errors.New("no")), nil, nil, nil),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), nil, nil, errors.New("boom")),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), nil, map[string][]string{"Stop": {"/y/agentpulse hook"}}, nil),
		checkBinaryOnPath("agentpulse", "", lookPathReturning("/x/agentpulse", nil), nil, map[string][]string{"Stop": {"/x/agentpulse hook"}}, nil),
	}
	for _, r := range cases {
		if r.Verdict == FAIL {
			t.Errorf("checkBinaryOnPath returned FAIL (finding: %q) with no stat function wired up", r.Finding)
		}
	}
}

func TestCheckSettingsFile(t *testing.T) {
	registered := func(events ...string) map[string][]string {
		m := map[string][]string{}
		for _, e := range events {
			m[e] = []string{"/opt/agentpulse hook"}
		}
		return m
	}
	originalEight := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "SessionEnd"}
	allTen := append(append([]string{}, originalEight...), "SubagentStart", "SubagentStop")

	tests := []struct {
		name          string
		settingsPath  string
		installed     map[string][]string
		installedErr  error
		wantVerdict   Verdict
		wantInFinding string
		wantRemedy    string
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
			installed:    registered(allTen...),
			wantVerdict:  PASS,
		},
		{
			name:          "only the subagent hooks missing",
			settingsPath:  "/home/sam/.claude/settings.json",
			installed:     registered(originalEight...),
			wantVerdict:   WARN,
			wantInFinding: "missing the subagent hooks: SubagentStart, SubagentStop",
			wantRemedy:    "Run `agentpulse pair --hooks-only` to add the subagent hooks (keeps your pairing).",
		},
		{
			name:          "only SubagentStop missing",
			settingsPath:  "/home/sam/.claude/settings.json",
			installed:     registered(append(append([]string{}, originalEight...), "SubagentStart")...),
			wantVerdict:   WARN,
			wantInFinding: "missing the subagent hooks: SubagentStop",
			wantRemedy:    "Run `agentpulse pair --hooks-only` to add the subagent hooks (keeps your pairing).",
		},
		{
			name:          "an original hook missing as well",
			settingsPath:  "/home/sam/.claude/settings.json",
			installed:     registered(originalEight[1:]...),
			wantVerdict:   FAIL,
			wantInFinding: "missing hook registrations for: SessionStart, SubagentStart, SubagentStop",
			wantRemedy:    "run `agentpulse pair`",
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
			if tt.wantRemedy != "" && got.Remedy != tt.wantRemedy {
				t.Errorf("Remedy = %q, want %q", got.Remedy, tt.wantRemedy)
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
