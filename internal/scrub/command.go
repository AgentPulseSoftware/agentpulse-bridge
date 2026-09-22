package scrub

import (
	"regexp"
	"strings"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cmdnorm"
)

// scrubCommand implements the tool_input.command rule (SPEC 7.5):
// after stripping leading environment assignments and "cd ... &&" prefixes
// (SPEC 7.5), and taking only the first pipeline segment (internal/cmdnorm,
// shared with internal/classify's live verification detection), it keeps:
//   - "gh pr create" verbatim (three words) when the
//     command is a `gh pr create` invocation,
//   - "git commit" verbatim when the command is a `git commit` invocation,
//   - just the runner tokens and flags when the segment matches a
//     verification pattern from SPEC 7.5 (paths and other positional
//     arguments are dropped),
//   - "scrubbed-command" otherwise.
func scrubCommand(raw string) string {
	seg := cmdnorm.Normalize(raw)

	if ghPRCreatePattern.MatchString(seg) {
		return "gh pr create"
	}
	if gitCommitPattern.MatchString(seg) {
		return "git commit"
	}
	if matchesAnyPattern(seg, verificationCommandPatterns) {
		return keepRunnerTokensAndFlags(seg)
	}
	return "scrubbed-command"
}

func matchesAnyPattern(s string, patterns []*regexp.Regexp) bool {
	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// keepRunnerTokensAndFlags tokenizes a verification command by whitespace
// and keeps only tokens that name the runner itself (from runnerTokens) or
// look like a flag (start with "-"), dropping file paths, test-name
// filters, and any other positional argument. A kept flag's value — the
// part after a "=", as in "--apiKey=sk_live_x" — is dropped too, keeping
// only the flag's own name; SPEC 7.5 never needs a flag's value, and one
// could easily be a secret (an API key, a customer name baked into
// -ldflags, a real path). A flag can also carry a path glued directly onto
// its name with no "=" at all, e.g. "-o/Users/sam/out"; a flag whose name
// (after the "=" split above) still contains "/" or "\" is dropped
// entirely rather than partially kept, since there is no separator left to
// cleanly cut the path away from.
func keepRunnerTokensAndFlags(seg string) string {
	fields := strings.Fields(seg)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		switch {
		case strings.HasPrefix(f, "-"):
			name := flagNameOnly(f)
			if strings.ContainsAny(name, `/\`) {
				continue
			}
			kept = append(kept, name)
		case runnerTokens[f]:
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return "scrubbed-command"
	}
	return strings.Join(kept, " ")
}

// flagNameOnly returns token up to (but not including) its first "=", so
// "--apiKey=sk_live_x" becomes "--apiKey" and "-ldflags=-X=main.customer=Acme"
// becomes "-ldflags" (only the first "=" matters: everything after it,
// including any further "=" signs, is the value).
func flagNameOnly(token string) string {
	if i := strings.IndexByte(token, '='); i >= 0 {
		return token[:i]
	}
	return token
}
