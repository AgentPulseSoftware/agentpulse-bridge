package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// claudeVersionTimeout bounds check 3's `claude --version`, matching
// healthTimeout's 3-second budget so "doctor" finishes in well under ten
// seconds even when "claude" hangs. ClaudeVersionRunner applies it to
// the process it runs; RunChecks applies it again to whatever
// Deps.ClaudeVersion turns out to be, so a test-supplied stub is bounded
// the same way a real process is.
const claudeVersionTimeout = 3 * time.Second

// ClaudeVersionRunner returns the func(context.Context) (string, error)
// production Deps.ClaudeVersion uses: it resolves "claude" via lookPath
// (exec.LookPath in production), then runs it with the fixed two-word
// argv {"claude", "--version"} — no shell, and no value from
// settings.json, hook input, or config ever reaches this command line
// (SEC-09) — under its own claudeVersionTimeout, and returns whatever it
// wrote to stdout, raw.
func ClaudeVersionRunner(lookPath func(string) (string, error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		path, err := lookPath("claude")
		if err != nil {
			return "", errors.New("not found on PATH")
		}

		cctx, cancel := context.WithTimeout(ctx, claudeVersionTimeout)
		defer cancel()

		cmd := exec.CommandContext(cctx, path, "--version")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if runErr := cmd.Run(); runErr != nil {
			if cctx.Err() != nil {
				return "", context.DeadlineExceeded
			}
			return "", errors.New("exited with an error")
		}
		if stdout.Len() == 0 {
			return "", errors.New("produced no output")
		}
		return stdout.String(), nil
	}
}

// firstLine returns s up to its first line break, or s itself if it has
// none — `claude --version`'s own output is one line, but a stub or a
// misbehaving build might print more, and only the first line is ever
// considered.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// formatVersion renders parseVersion's three integers back as "X.Y.Z"
// for a Finding or Remedy.
func formatVersion(v [3]int) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// describeClaudeVersionError turns an error from Deps.ClaudeVersion into
// the one-clause reason checkClaudeVersion's Finding names. It only
// needs to recognize the timeout specially — every other error
// ClaudeVersionRunner returns ("not found on PATH", "exited with an
// error", "produced no output") is already a short, plain-English
// clause on its own.
func describeClaudeVersionError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// claudeVersionConsequence renders SPEC 7.4's degradation list in plain
// words for exactly the events unsupported names, in the order SPEC 7.4
// states them: no PermissionRequest, no PermissionRequest and no
// Notification together, then no SessionEnd. Events other than these
// three (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop)
// carry no SPEC 7.4 consequence of their own, so they are only ever
// named in the Finding's own event list, not here.
func claudeVersionConsequence(unsupported []string) string {
	has := map[string]bool{}
	for _, e := range unsupported {
		has[e] = true
	}

	var parts []string
	switch {
	case has["PermissionRequest"] && has["Notification"]:
		parts = append(parts, "no PermissionRequest and no Notification means Needs You comes only from AskUserQuestion and ExitPlanMode")
	case has["PermissionRequest"]:
		parts = append(parts, "no PermissionRequest means permission prompts are seen only through Notification")
	case has["Notification"]:
		parts = append(parts, "no Notification means a permission prompt is seen only through PermissionRequest, with no fallback if that is missed")
	}
	if has["SessionEnd"] {
		parts = append(parts, "no SessionEnd means sessions end only on the idle timer")
	}
	if len(parts) == 0 {
		return "the bridge keeps working with less information from Claude Code"
	}
	return strings.Join(parts, "; ")
}

// checkClaudeVersion is BR-15 check 3 (SPEC 7.4, NFR-11). It can never
// FAIL: an old Claude Code, or one "doctor" simply could not query, is a
// documented degradation the bridge keeps working through, not a fault
// (see the doc comment on the PASS/WARN/FAIL const block).
func checkClaudeVersion(output string, runErr error) Result {
	if runErr != nil {
		return Result{
			Name: "claude-version", Verdict: WARN,
			Finding: fmt.Sprintf("could not determine the installed Claude Code version (%s)", describeClaudeVersionError(runErr)),
			Remedy:  "check `claude --version` runs in your shell",
		}
	}

	version, ok := parseVersion(firstLine(output))
	if !ok {
		return Result{
			Name: "claude-version", Verdict: WARN,
			Finding: "could not determine the installed Claude Code version (unrecognized output)",
			Remedy:  "check `claude --version` runs in your shell",
		}
	}

	versionStr := formatVersion(version)
	unsupported := unsupportedEvents(version)
	if len(unsupported) == 0 {
		return Result{
			Name: "claude-version", Verdict: PASS,
			Finding: fmt.Sprintf("Claude Code %s supports all 8 hook events", versionStr),
		}
	}
	return Result{
		Name: "claude-version", Verdict: WARN,
		Finding: fmt.Sprintf("Claude Code %s does not send %s: %s",
			versionStr, strings.Join(unsupported, ", "), claudeVersionConsequence(unsupported)),
		Remedy: fmt.Sprintf("upgrade Claude Code to %s or later", compat.BaselineVersion),
	}
}
