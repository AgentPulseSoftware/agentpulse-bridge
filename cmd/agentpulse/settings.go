package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
)

// This file holds everything both "agentpulse pair"/"unpair"
// and the development-only "agentpulse record install"/"uninstall"
// (built only with -tags dev) need to change ~/.claude/settings.json.
// It is deliberately untagged so there is exactly one definition of the
// confirmation prompt and the settings path, whichever build you are
// looking at. The BR-08 event list itself lives in
// internal/claudehooks.BR08Events, the single copy "agentpulse doctor"
// also reads.

// br08HookTimeout is BR-08's hook timeout in seconds.
const br08HookTimeout = 10

// hookEntries builds the eleven BR-08 entries for command: one per event,
// with an empty matcher and a 10 second timeout.
func hookEntries(command string) []hooks.Entry {
	entries := make([]hooks.Entry, 0, len(claudehooks.BR08Events))
	for _, event := range claudehooks.BR08Events {
		entries = append(entries, hooks.Entry{
			Event:   event,
			Matcher: "",
			Command: command,
			Timeout: br08HookTimeout,
		})
	}
	return entries
}

// hookCommand is the command Claude Code runs for each of those events:
// this exact binary, followed by "hook" (BR-08). The path is quoted only
// when it needs to be, so the diff a person is asked to approve reads as
// plainly as possible in the common case.
func hookCommand(binary string) string {
	return shellQuoteIfNeeded(binary) + " hook"
}

// shellQuoteSingle wraps s in single quotes for use inside a shell command
// string, escaping any embedded single quote the POSIX-shell way (close the
// quote, emit an escaped quote, reopen it). Both the recording directory
// and the binary path are attacker-free but user-chosen filesystem paths
// that can contain spaces (e.g. "~/My Recordings"), and the
// command this builds is handed to Claude Code as a literal shell command
// line, so it must survive `sh -c`.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteIfNeeded is shellQuoteSingle for any string a shell would not
// read as a single plain word, and s itself otherwise.
func shellQuoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\r\"'\\$`&|;<>()*?[]{}#~!") {
		return shellQuoteSingle(s)
	}
	return s
}

// currentBinaryPath returns the path to write into settings.json's hook
// command (P5-27): the absolute, symlink-resolved path to the running
// binary, except when os.Executable() itself is a Homebrew `bin/` or
// `opt/<formula>/bin/` symlink, in which case it returns that unresolved
// invocation path instead. See stableBinaryPath for why.
func currentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the current binary: %w", err)
	}
	return stableBinaryPath(exe)
}

// stableBinaryPath applies P5-27's rule to invocationPath, the path
// os.Executable() reports before any symlink is followed. A Homebrew
// install always runs through a symlink — `$(brew --prefix)/bin/agentpulse`
// (or, if invoked that way, `$(brew --prefix)/opt/agentpulse/bin/agentpulse`)
// — whose target is the versioned `.../Cellar/agentpulse/<version>/bin/agentpulse`
// that `brew upgrade` deletes as soon as a new version is installed, which
// is exactly the path a plain symlink-resolving currentBinaryPath used to
// write into every hook: correct the instant it was written, and silently
// broken after the next upgrade, since Homebrew relinks the symlink to the
// new version but never rewrites ~/.claude/settings.json. Recognizing that
// shape and keeping the symlink path instead means the hook keeps working
// across an upgrade without anyone re-running `agentpulse pair`.
//
// Every other case — a `go install` binary, a plain copied binary, a
// manual symlink to something other than a Cellar path — resolves to the
// real file the same way it always did, because there is no Homebrew
// relinking to rely on for those; if the file itself moves, doctor's
// binary-on-path check (BR-15) is what catches it, and the remedy is
// `agentpulse pair --hooks-only`.
func stableBinaryPath(invocationPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(invocationPath)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", invocationPath, err)
	}
	if resolved != invocationPath && isHomebrewCellarPath(resolved, filepath.Base(invocationPath)) {
		return invocationPath, nil
	}
	return resolved, nil
}

// isHomebrewCellarPath reports whether resolved has the exact shape
// Homebrew gives a formula's installed binary,
// ".../Cellar/<name>/<version>/bin/<name>", for the formula named name
// (this program's own binary name, e.g. "agentpulse"). <version> is not
// validated beyond being a single path component — Homebrew's own version
// strings are not a fixed format — since the four surrounding, fixed
// components ("Cellar", name twice, "bin") are already specific enough
// that nothing else plausibly matches by accident.
func isHomebrewCellarPath(resolved, name string) bool {
	if name == "" || filepath.Base(resolved) != name {
		return false
	}
	binDir := filepath.Dir(resolved)
	if filepath.Base(binDir) != "bin" {
		return false
	}
	versionDir := filepath.Dir(binDir)
	formulaDir := filepath.Dir(versionDir)
	if filepath.Base(formulaDir) != name {
		return false
	}
	cellarDir := filepath.Dir(formulaDir)
	return filepath.Base(cellarDir) == "Cellar"
}

// defaultSettingsPath is ~/.claude/settings.json, honoring $HOME so tests
// (and anyone recording fixtures) can point it at a scratch directory
// instead of a real machine's settings.
func defaultSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// applyPlan prints the diff, asks for confirmation unless yes is set, and
// applies the plan (BR-07: "shows the exact hook JSON diff and asks for
// confirmation before writing"; SPEC section 8: settings are written only
// by pair after a "y" or --yes, and by unpair for removal). It reports
// whether the file was actually written.
func applyPlan(cmd *cobra.Command, plan *hooks.Plan, yes bool, verb string) (written bool, err error) {
	out := cmd.OutOrStdout()

	if !plan.Changed {
		if _, err := fmt.Fprintf(out, "Nothing to %s: %s is already up to date.\n", verb, plan.Path); err != nil {
			return false, err
		}
		return false, nil
	}

	if _, err := fmt.Fprintf(out, "This will change %s:\n\n%s\n", plan.Path, plan.Diff); err != nil {
		return false, err
	}

	if !yes && !confirm(cmd) {
		if _, err := fmt.Fprintln(out, "Aborted; nothing was written."); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := plan.Apply(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(out, "Wrote %s.\n", plan.Path); err != nil {
		return true, err
	}
	return true, nil
}

func confirm(cmd *cobra.Command) bool {
	_, _ = fmt.Fprint(cmd.OutOrStdout(), "Proceed? [y/N] ")
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}
