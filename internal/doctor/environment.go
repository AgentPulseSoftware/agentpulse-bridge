package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
)

// resolvePath returns path with any symlinks resolved (the same rule
// cmd/agentpulse/settings.go's currentBinaryPath() applies to the
// running binary), or path itself if it can't be resolved — a stale
// registered path may point at a binary a Homebrew upgrade already
// removed, and that's exactly the condition check 1 exists to report,
// not a reason to error out of the check.
func resolvePath(path string) string {
	if path == "" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// commandBinary extracts the program path from a hook command string of
// the shape hookCommand (cmd/agentpulse/settings.go) writes: either a
// bare path, or one single-quoted with shellQuoteSingle's escaping for a
// path containing a space or shell metacharacter. It is not a general
// shell-word splitter — every command this function is ever handed
// already came back from hooks.InstalledCommands, which only returns
// commands hooks.Owner already recognized as this bridge's own, in one
// of these two exact forms.
func commandBinary(command string) string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, "'") {
		// Scan for the closing quote, treating shellQuoteSingle's own
		// escape sequence for an embedded quote ('\'') as a literal
		// quote character rather than the end of the word — otherwise a
		// registered path like "/Users/O'Brien/agentpulse" would be cut
		// off at the first apostrophe.
		var sb strings.Builder
		i := 1
		for i < len(command) {
			if strings.HasPrefix(command[i:], `'\''`) {
				sb.WriteByte('\'')
				i += 4
				continue
			}
			if command[i] == '\'' {
				return sb.String()
			}
			sb.WriteByte(command[i])
			i++
		}
		return sb.String() // unterminated quote: best effort, whole remainder
	}
	if i := strings.IndexAny(command, " \t"); i >= 0 {
		return command[:i]
	}
	return command
}

// registeredBinaries returns the distinct, symlink-resolved binary paths
// named across every command InstalledCommands returned, in
// claudehooks.BR08Events order and first-seen order within that (not map
// iteration order, which Go leaves unspecified).
func registeredBinaries(installed map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, event := range claudehooks.BR08Events {
		for _, command := range installed[event] {
			bin := resolvePath(commandBinary(command))
			if bin == "" || seen[bin] {
				continue
			}
			seen[bin] = true
			out = append(out, bin)
		}
	}
	return out
}

// brokenRegisteredBinaries returns, in claudehooks.BR08Events order, one
// description per distinct registered hook binary path (not symlink-
// resolved — the point is to stat the exact path Claude Code would run)
// that does not exist, is a directory, or lacks any executable bit, e.g.
// "/opt/homebrew/Cellar/agentpulse/1.2.0/bin/agentpulse does not exist".
// A bare command name registered without a path (BR-09's second
// ownership clause, e.g. plain "agentpulse" on PATH) is never reported
// here: it is not a path this function can stat meaningfully, and check
// 1's own PATH lookup already covers whether "agentpulse" resolves to
// anything. stat == nil (no Deps.StatBinary wired up) skips this check
// entirely rather than reporting every registered path broken.
func brokenRegisteredBinaries(stat func(string) (fs.FileInfo, error), installed map[string][]string) []string {
	if stat == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, event := range claudehooks.BR08Events {
		for _, command := range installed[event] {
			path := commandBinary(command)
			if !filepath.IsAbs(path) || seen[path] {
				continue
			}
			seen[path] = true
			if reason, bad := binaryProblem(stat, path); bad {
				out = append(out, fmt.Sprintf("%s %s", path, reason))
			}
		}
	}
	return out
}

// binaryProblem reports why path is unusable as a hook binary, or
// bad == false when it is a plain, executable file.
func binaryProblem(stat func(string) (fs.FileInfo, error), path string) (reason string, bad bool) {
	info, err := stat(path)
	if err != nil {
		return "does not exist", true
	}
	if info.IsDir() {
		return "is a directory, not an executable file", true
	}
	if info.Mode()&0o111 == 0 {
		return "is not executable", true
	}
	return "", false
}

// checkBinaryOnPath is BR-15 check 1. It FAILs when a registered hook's
// binary path does not exist or is not executable (P5-27): that is
// exactly what a Homebrew upgrade leaves behind, since `brew upgrade`
// deletes the old versioned Cellar path as soon as the new one is
// linked, and a hook pointing at a deleted file cannot run at all — that
// is a broken bridge, not a degradation, so it outranks every other
// outcome this check can report. Every other outcome — no PATH entry, or
// a registered path that exists and runs but differs from the copy of
// the bridge currently running "doctor" — can only WARN: the bridge
// still runs Claude Code's hooks via whatever absolute path is in
// settings.json regardless of what a shell's $PATH resolves "agentpulse"
// to.
func checkBinaryOnPath(binaryName, runningBinaryPath string, lookPath func(string) (string, error), stat func(string) (fs.FileInfo, error), installed map[string][]string, installedErr error) Result {
	if lookPath == nil {
		lookPath = func(string) (string, error) { return "", errors.New("no PATH lookup available") }
	}

	if installedErr == nil {
		if broken := brokenRegisteredBinaries(stat, installed); len(broken) > 0 {
			return Result{
				Name: "binary-on-path", Verdict: FAIL,
				Finding: fmt.Sprintf("registered hook binary %s", strings.Join(broken, "; ")),
				Remedy:  "run `agentpulse pair --hooks-only`",
			}
		}
	}

	found, lookErr := lookPath(binaryName)
	if lookErr != nil {
		finding := fmt.Sprintf("%s is not on PATH", binaryName)
		if runningBinaryPath != "" {
			finding = fmt.Sprintf("%s is not on PATH (this doctor is running from %s)", binaryName, runningBinaryPath)
		}
		return Result{
			Name: "binary-on-path", Verdict: WARN,
			Finding: finding,
			Remedy:  "add the directory holding `agentpulse` to your PATH, or run `agentpulse pair` again from the copy you want Claude Code to use",
		}
	}
	resolvedFound := resolvePath(found)

	if installedErr != nil {
		return Result{
			Name: "binary-on-path", Verdict: WARN,
			Finding: fmt.Sprintf("%s is on PATH at %s, but could not compare against the registered hook command", binaryName, resolvedFound),
			Remedy:  "run `agentpulse pair`",
		}
	}

	registered := registeredBinaries(installed)
	if len(registered) == 1 && registered[0] == resolvedFound {
		return Result{
			Name: "binary-on-path", Verdict: PASS,
			Finding: fmt.Sprintf("%s (on PATH) matches the registered hook command", resolvedFound),
		}
	}

	finding := fmt.Sprintf("%s on PATH is %s, but no registered hook command points at it", binaryName, resolvedFound)
	if len(registered) > 0 {
		finding = fmt.Sprintf("%s on PATH is %s; the registered hook command uses %s instead",
			binaryName, resolvedFound, strings.Join(registered, ", "))
	}
	return Result{
		Name: "binary-on-path", Verdict: WARN,
		Finding: finding,
		Remedy:  "run `agentpulse pair` again",
	}
}

// checkSettingsFile is BR-15 check 2 (BR-08). Unlike check 1, this one
// can FAIL: a missing, unparseable, or incomplete settings file means
// Claude Code is not actually calling this bridge at all, which is a
// broken setup, not a degradation.
func checkSettingsFile(settingsPath string, installed map[string][]string, installedErr error) Result {
	if settingsPath == "" {
		return Result{
			Name: "settings-file", Verdict: FAIL,
			Finding: "could not resolve the Claude Code settings file path",
			Remedy:  "run `agentpulse pair`",
		}
	}
	if installedErr != nil {
		if errors.Is(installedErr, fs.ErrNotExist) {
			return Result{
				Name: "settings-file", Verdict: FAIL,
				Finding: fmt.Sprintf("%s does not exist", settingsPath),
				Remedy:  "run `agentpulse pair`",
			}
		}
		return Result{
			Name: "settings-file", Verdict: FAIL,
			Finding: fmt.Sprintf("%s: %v", settingsPath, installedErr),
			Remedy:  "fix the JSON syntax in that file, then run `agentpulse pair`",
		}
	}

	var missing []string
	for _, event := range claudehooks.BR08Events {
		if len(installed[event]) == 0 {
			missing = append(missing, event)
		}
	}
	if len(missing) == 0 {
		return Result{
			Name: "settings-file", Verdict: PASS,
			Finding: fmt.Sprintf("all %d hook events registered", len(claudehooks.BR08Events)),
		}
	}
	if onlyOptionalEvents(missing) {
		// Each optional hook only adds information: subagents still show
		// from their first tool call, only without a precise start and
		// stop (ADR-005 section 3), and without StopFailure a usage limit
		// shows as lost contact, as it always has (ADR-006 section 6).
		// Claude Code sends a subagent's hooks with its parent's
		// session_id (COMPATIBILITY.md), so no parent mapping depends on
		// SubagentStart and this never needs to be a FAIL.
		return Result{
			Name: "settings-file", Verdict: WARN,
			Finding: fmt.Sprintf("%s is missing optional hooks: %s", settingsPath, strings.Join(missing, ", ")),
			Remedy:  optionalHooksRemedy,
		}
	}
	return Result{
		Name: "settings-file", Verdict: FAIL,
		Finding: fmt.Sprintf("%s is missing hook registrations for: %s", settingsPath, strings.Join(missing, ", ")),
		Remedy:  "run `agentpulse pair`",
	}
}

// optionalHooksRemedy is the remedy when only optional hooks are
// missing. The app's setup instructions name the same command.
const optionalHooksRemedy = "Run `agentpulse pair --hooks-only` to add the missing hooks (keeps your pairing)."

func onlyOptionalEvents(events []string) bool {
	for _, e := range events {
		if !claudehooks.IsOptionalEvent(e) {
			return false
		}
	}
	return true
}
