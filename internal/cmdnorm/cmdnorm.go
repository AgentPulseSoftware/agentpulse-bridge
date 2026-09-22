// Package cmdnorm implements the one piece of Bash-command preprocessing
// SPEC 7.5 defines once but two packages need identically: internal/scrub
// (deciding how much of a recorded command is safe to keep in a fixture)
// and internal/classify (deciding whether a live Bash command is a
// verification run, SPEC 7.2/7.5). Both must strip the same leading noise
// before looking at what the command actually runs, so that logic lives
// here instead of twice.
package cmdnorm

import (
	"regexp"
	"strings"
)

// envAssignmentPattern matches one leading "NAME=value " environment
// assignment at the start of a command.
var envAssignmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*\s+`)

// cdPrefixPattern matches a leading "cd <path> && " (or ";").
var cdPrefixPattern = regexp.MustCompile(`^cd\s+\S+\s*(&&|;)\s*`)

// Normalize strips any number of leading "VAR=value" assignments and one
// leading "cd <path> &&"/"cd <path>;" prefix (in either order), then
// returns only the first top-level pipeline segment (the text before a
// "|"), per SPEC 7.5: "after stripping leading environment assignments and
// cd ... && prefixes ... its first pipeline segment".
func Normalize(cmd string) string {
	return firstPipelineSegment(stripLeadingEnvAndCd(cmd))
}

// stripLeadingEnvAndCd removes any number of leading "VAR=val" assignments
// and one leading "cd <path> &&"/"cd <path>;" prefix, in either order.
func stripLeadingEnvAndCd(cmd string) string {
	s := strings.TrimSpace(cmd)
	for {
		if m := envAssignmentPattern.FindString(s); m != "" {
			s = strings.TrimSpace(s[len(m):])
			continue
		}
		if m := cdPrefixPattern.FindString(s); m != "" {
			s = strings.TrimSpace(s[len(m):])
			continue
		}
		break
	}
	return s
}

// firstPipelineSegment returns the text before the first top-level "|".
func firstPipelineSegment(cmd string) string {
	seg, _, _ := strings.Cut(cmd, "|")
	return strings.TrimSpace(seg)
}
