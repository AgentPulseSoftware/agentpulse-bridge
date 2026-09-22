package scrub

import (
	"fmt"
	"strings"
)

// filterResponseLines implements the tool_response line rule: a line
// survives only as a *canonical
// reconstruction* built exclusively from a recognizer's own regex capture
// groups (or a fixed constant, when the whole recognized text is fixed);
// nothing outside a capture group ever reaches the output. Every other
// line is replaced by a single "…" line, with consecutive collapsed lines
// merged into one.
//
// This matters because the naive version of this rule — "keep the whole
// line if it matches a pattern" — is unsafe: a real pytest failure summary
// can share a line with a file path, and a bare `\d+ errors?` match can
// occur inside an arbitrary sentence that happens to mention a number and
// the word "error" right next to a real path (e.g. "1 error found in
// ~/secret/notes.txt"). Reconstructing only from captured digits and a
// small closed set of known words means even a false-positive match can
// never leak anything, because there is nothing to leak: the output is
// built, not copied.
func filterResponseLines(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	collapsing := false

	for _, line := range lines {
		if canon, ok := reconstructLine(line); ok {
			out = append(out, canon)
			collapsing = false
			continue
		}
		if !collapsing {
			out = append(out, "…")
			collapsing = true
		}
	}
	return strings.Join(out, "\n")
}

// reconstructLine tries each recognizer in turn and returns the first
// canonical reconstruction, or ("", false) if nothing recognizes the line.
func reconstructLine(line string) (string, bool) {
	for _, r := range lineReconstructors {
		if canon, ok := r(line); ok {
			return canon, true
		}
	}
	return "", false
}

var lineReconstructors = []func(string) (string, bool){
	reconstructPullURL,
	reconstructGoOK,
	reconstructGoFAIL,
	reconstructGoVerboseMarker,
	reconstructJest,
	reconstructVitest,
	reconstructCargo,
	reconstructExecuted,
	reconstructTestMarker,
	reconstructBuildMarker,
	reconstructGenericCounts, // catch-all: pytest, Mocha, and anything else shaped like "<n> <known word>"
}

// reconstructPullURL keeps a PR/issue URL reduced to "pull/<number>",
// dropping the host, org, repo, and scheme entirely.
func reconstructPullURL(line string) (string, bool) {
	m := pullURLPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return "pull/" + m[1], true
}

// reconstructGoOK and reconstructGoFAIL recognize Go's non-verbose summary
// markers. Anchored to the start of the line (per SPEC 7.5's own `^ok\s` /
// `^FAIL`), and — critically — followed by whitespace or end of line, so
// "FAIL" doesn't match a pytest "FAILED tests/..." line, whose next
// character is "E", not whitespace. The output is the bare word only:
// never the package path or timing that normally follows it on the same
// line.
func reconstructGoOK(line string) (string, bool) {
	if goOKPattern.MatchString(line) {
		return "ok", true
	}
	return "", false
}

func reconstructGoFAIL(line string) (string, bool) {
	if goFAILPattern.MatchString(line) {
		return "FAIL", true
	}
	return "", false
}

// reconstructGoVerboseMarker recognizes `go test -v`'s per-test "--- PASS:"
// / "--- FAIL:" lines, keeping only the marker — never the test name or
// timing that follows it (SPEC 7.5: these are counted, not read).
func reconstructGoVerboseMarker(line string) (string, bool) {
	switch {
	case goVerboseFailPattern.MatchString(line):
		return "--- FAIL", true
	case goVerbosePassPattern.MatchString(line):
		return "--- PASS", true
	}
	return "", false
}

// reconstructJest rebuilds Jest's "Tests: ..." summary line from its own
// capture groups only.
func reconstructJest(line string) (string, bool) {
	m := jestPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	var parts []string
	if m[1] != "" {
		parts = append(parts, m[1]+" failed")
	}
	if m[2] != "" {
		parts = append(parts, m[2]+" skipped")
	}
	parts = append(parts, m[3]+" passed", m[4]+" total")
	return "Tests: " + strings.Join(parts, ", "), true
}

// reconstructVitest rebuilds Vitest's "Tests ..." summary line from its own
// capture groups only.
func reconstructVitest(line string) (string, bool) {
	m := vitestPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	var parts []string
	if m[1] != "" {
		parts = append(parts, m[1]+" failed")
	}
	parts = append(parts, m[2]+" passed")
	return fmt.Sprintf("Tests %s (%s)", strings.Join(parts, " | "), m[3]), true
}

// reconstructCargo rebuilds Cargo's "test result: ..." line.
func reconstructCargo(line string) (string, bool) {
	m := cargoPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return fmt.Sprintf("test result: %s. %s passed; %s failed", m[1], m[2], m[3]), true
}

// reconstructExecuted rebuilds Swift/xcodebuild's "Executed N tests, with M
// failures" line, always in plural form regardless of the original
// singular/plural text, since only the digits are ever read from the
// original line.
func reconstructExecuted(line string) (string, bool) {
	m := executedPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return fmt.Sprintf("Executed %s tests, with %s failures", m[1], m[2]), true
}

// reconstructTestMarker rebuilds xcodebuild's "** TEST SUCCEEDED/FAILED **"
// marker line.
func reconstructTestMarker(line string) (string, bool) {
	m := testMarkerPattern.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return "** TEST " + m[1] + " **", true
}

// reconstructBuildMarker recognizes xcodebuild's "BUILD SUCCEEDED"/"BUILD
// FAILED" markers. These have no variable part to capture — the whole
// recognized text is a fixed constant — so returning that constant (never
// any surrounding text from the line) is safe by construction.
func reconstructBuildMarker(line string) (string, bool) {
	switch {
	case strings.Contains(line, "BUILD SUCCEEDED"):
		return "BUILD SUCCEEDED", true
	case strings.Contains(line, "BUILD FAILED"):
		return "BUILD FAILED", true
	}
	return "", false
}

// reconstructGenericCounts is the catch-all for pytest ("1 failed, 11
// passed"), Mocha ("12 passing", "1 failing"), and any other line shaped
// like "<digits> <one of a small closed set of words>". It finds every
// non-overlapping match and joins them in the order they appear, using
// only the digits and the matched word itself — never any of the
// surrounding text, so "1 error found in ~/secret/notes.txt" becomes just
// "1 error", not the path that followed it.
func reconstructGenericCounts(line string) (string, bool) {
	matches := countWordPattern.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(matches))
	for _, m := range matches {
		parts = append(parts, m[1]+" "+m[2])
	}
	return strings.Join(parts, ", "), true
}
