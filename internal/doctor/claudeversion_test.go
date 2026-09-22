package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCheckClaudeVersion(t *testing.T) {
	tests := []struct {
		name          string
		output        string
		runErr        error
		wantVerdict   Verdict
		wantInFinding []string
	}{
		{
			name:          "absent",
			runErr:        errors.New("not found on PATH"),
			wantVerdict:   WARN,
			wantInFinding: []string{"not found on PATH"},
		},
		{
			name:          "current baseline version",
			output:        "2.1.261 (Claude Code)\n",
			wantVerdict:   PASS,
			wantInFinding: []string{"2.1.261"},
		},
		{
			name:          "older version",
			output:        "2.0.9 (Claude Code)\n",
			wantVerdict:   WARN,
			wantInFinding: []string{"PermissionRequest", "Needs You comes only from AskUserQuestion and ExitPlanMode"},
		},
		{
			name:          "garbage",
			output:        "not a version at all\n",
			wantVerdict:   WARN,
			wantInFinding: []string{"could not determine"},
		},
		{
			name:          "non-zero exit",
			runErr:        errors.New("exited with an error"),
			wantVerdict:   WARN,
			wantInFinding: []string{"could not determine"},
		},
		{
			name:          "timeout",
			runErr:        context.DeadlineExceeded,
			wantVerdict:   WARN,
			wantInFinding: []string{"timed out"},
		},
		{
			name:          "empty output",
			output:        "",
			wantVerdict:   WARN,
			wantInFinding: []string{"could not determine"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkClaudeVersion(tt.output, tt.runErr)
			if got.Verdict != tt.wantVerdict {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdict, got.Finding)
			}
			if got.Verdict == FAIL {
				t.Errorf("checkClaudeVersion returned FAIL; check 3 must never FAIL (finding: %q)", got.Finding)
			}
			if got.Verdict != PASS && got.Remedy == "" {
				t.Errorf("non-PASS result has empty Remedy (finding: %q)", got.Finding)
			}
			for _, want := range tt.wantInFinding {
				if !strings.Contains(got.Finding, want) {
					t.Errorf("Finding = %q, want it to contain %q", got.Finding, want)
				}
			}
		})
	}
}

// TestRunClaudeVersionCheckHangingStubTimesOut is a "use a stub function,
// not a real process" timeout case: a Deps.ClaudeVersion that
// simply blocks past the deadline, driven the way RunChecks drives it —
// through its own claudeVersionTimeout-bounded context — asserting it
// returns promptly (not after a full compat-check-style wait) and WARNs
// with a "timed out" Finding.
func TestRunClaudeVersionCheckHangingStubTimesOut(t *testing.T) {
	hang := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	began := time.Now()
	cctx, cancel := context.WithTimeout(context.Background(), claudeVersionTimeout)
	defer cancel()
	output, err := hang(cctx)
	elapsed := time.Since(began)

	if elapsed > 4*time.Second {
		t.Errorf("hanging stub took %v to return, want at or just over %v", elapsed, claudeVersionTimeout)
	}
	got := checkClaudeVersion(output, err)
	if got.Verdict != WARN {
		t.Errorf("Verdict = %s, want WARN (finding: %q)", got.Verdict, got.Finding)
	}
	if !strings.Contains(got.Finding, "timed out") {
		t.Errorf("Finding = %q, want it to mention timing out", got.Finding)
	}
}

// writeStubClaude writes an executable shell script named "claude" into
// dir that runs script, and returns dir. The stub also appends its own
// argv (minus argv[0]) to an "argv.txt" file in dir, one line per
// invocation, so tests can assert ClaudeVersionRunner passed exactly the
// fixed argv and nothing else.
func writeStubClaude(t *testing.T, script string) (dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs are POSIX-only")
	}
	dir = t.TempDir()
	path := filepath.Join(dir, "claude")
	contents := "#!/bin/sh\necho \"$@\" >> \"" + filepath.Join(dir, "argv.txt") + "\"\n" + script
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil { //nolint:gosec // test fixture, deliberately executable
		t.Fatalf("writing stub claude: %v", err)
	}
	return dir
}

// lookPathOn returns a lookPath func that resolves name only within dir,
// the way exec.LookPath would if $PATH were exactly dir — used instead
// of mutating the real process $PATH so these tests never see whatever
// "claude" (if any) is actually installed on the machine running them.
func lookPathOn(dir string) func(string) (string, error) {
	return func(name string) (string, error) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
}

func TestClaudeVersionRunnerPrintsAVersion(t *testing.T) {
	dir := writeStubClaude(t, "echo '2.1.261 (Claude Code)'\n")
	run := ClaudeVersionRunner(lookPathOn(dir))

	output, err := run(context.Background())
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output, "2.1.261") {
		t.Errorf("output = %q, want it to contain 2.1.261", output)
	}
	assertFixedArgv(t, dir)
}

func TestClaudeVersionRunnerNonZeroExit(t *testing.T) {
	dir := writeStubClaude(t, "exit 1\n")
	run := ClaudeVersionRunner(lookPathOn(dir))

	if _, err := run(context.Background()); err == nil {
		t.Fatal("run() error = nil, want non-nil for a non-zero exit")
	}
	assertFixedArgv(t, dir)
}

func TestClaudeVersionRunnerAbsent(t *testing.T) {
	dir := t.TempDir() // no "claude" written into it
	run := ClaudeVersionRunner(lookPathOn(dir))

	if _, err := run(context.Background()); err == nil {
		t.Fatal("run() error = nil, want non-nil when claude is not on PATH")
	}
}

// assertFixedArgv checks the stub's argv.txt recorded exactly one
// invocation with exactly the single argument "--version" — SEC-09's
// "fixed argv, nothing derived from hook input or config", and no shell
// operators or extra words snuck onto the command line.
func assertFixedArgv(t *testing.T, dir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "argv.txt"))
	if err != nil {
		t.Fatalf("reading argv.txt: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("claude stub invoked %d times, want exactly 1: %v", len(lines), lines)
	}
	if lines[0] != "--version" {
		t.Errorf("argv (minus argv[0]) = %q, want exactly %q", lines[0], "--version")
	}
}
