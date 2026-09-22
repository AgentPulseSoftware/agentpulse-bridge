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

// hookEntries builds the eight BR-08 entries for command: one per event,
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

// currentBinaryPath returns the absolute, symlink-resolved path to the
// running binary, which is what gets written into settings.json's hook
// command so Claude Code always invokes this exact build.
func currentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the current binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", exe, err)
	}
	return resolved, nil
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
