package scrub

import "regexp"

// verificationCommandPatterns identifies a Bash command's first pipeline
// segment as a test or build run, per SPEC 7.5. It is used only to decide
// how much of tool_input.command is safe to keep (its runner tokens and
// flags); the scrubber does not need to compute pass/fail outcomes, so
// unlike a future classifier it does not need to distinguish "kind" here.
//
// This mirrors the regex column of the SPEC 7.5 table. It is intentionally
// scoped to this package: SPEC 7.5 assigns the canonical runner table used
// for live detection to internal/classify/runners.go; this table serves
// scrubbing recorded fixtures, a dev-only, offline concern with different
// lifetime and blast radius.
var verificationCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(python3?|py) -m pytest\b`), // pytest
	regexp.MustCompile(`\bpytest\b`),
	regexp.MustCompile(`\buv run pytest\b`),
	regexp.MustCompile(`\bpoetry run pytest\b`),
	regexp.MustCompile(`\bjest\b`), // Jest
	regexp.MustCompile(`\bnpx jest\b`),
	regexp.MustCompile(`\bpnpm jest\b`),
	regexp.MustCompile(`\byarn jest\b`),
	regexp.MustCompile(`\bvitest\b`), // Vitest
	regexp.MustCompile(`\bnpx vitest\b`),
	regexp.MustCompile(`\bpnpm vitest\b`),
	regexp.MustCompile(`\byarn vitest\b`),
	regexp.MustCompile(`\bmocha\b`), // Mocha
	regexp.MustCompile(`\bnpx mocha\b`),
	regexp.MustCompile(`\b(npm|pnpm|yarn|bun) (run )?test\b`), // npm scripts
	regexp.MustCompile(`\bgo test\b`),                         // Go
	regexp.MustCompile(`\bcargo (test|nextest run)\b`),        // Cargo
	regexp.MustCompile(`\bswift test\b`),                      // Swift
	regexp.MustCompile(`\bxcodebuild .* test\b`),              // xcodebuild
	regexp.MustCompile(`\b(npm|pnpm|yarn|bun) run build\b`),   // Build
	regexp.MustCompile(`\bgo build\b`),
	regexp.MustCompile(`\bcargo build\b`),
	regexp.MustCompile(`\bswift build\b`),
	regexp.MustCompile(`\bxcodebuild\b`),
	regexp.MustCompile(`\bmake( |$)`),
}

// runnerTokens are the base-command and sub-command words that identify a
// verification runner invocation. When a command matches
// verificationCommandPatterns, scrubCommand keeps only tokens in this set
// plus flag tokens (leading "-"), dropping every path, filename, and test
// name filter.
var runnerTokens = map[string]bool{
	"python": true, "python3": true, "py": true, "-m": true,
	"pytest": true, "uv": true, "poetry": true, "run": true,
	"jest": true, "npx": true, "pnpm": true, "yarn": true, "bun": true,
	"vitest": true, "mocha": true, "npm": true, "test": true,
	"go": true, "build": true, "cargo": true, "nextest": true,
	"swift": true, "xcodebuild": true, "make": true,
}

// ghPRCreatePattern and gitCommitPattern identify the two commands SPEC 7.2
// tracks by name (pr_pending / commit_pending) without needing the runner
// allow-list treatment: if a command matches `gh pr create` or
// `git commit`, only those words are kept.
var (
	ghPRCreatePattern = regexp.MustCompile(`^gh\s+pr\s+create\b`)
	gitCommitPattern  = regexp.MustCompile(`^git\s+commit\b`)
)

// The patterns below back response.go's reconstructLine recognizers. Each
// one is written so every substring that can vary (a count, an ok/FAILED
// word) is in its own capture group — the reconstructor functions in
// response.go build their output exclusively from those groups (or from a
// fixed constant, for a pattern with nothing to capture), never from the
// matched text as a whole. That is what makes it safe for these patterns
// to be broad enough to actually find real-world result lines.
//
// goOKPattern / goFAILPattern are deliberately anchored to the start of the
// line AND require whitespace or end-of-line right after the word: an
// unanchored `FAIL` would also match pytest's
// "FAILED tests/test_x.py::test_y" summary line (a real pytest failure
// line begins with the word "FAILED", which contains "FAIL" as a prefix).
// Requiring a boundary right after rules that out, since the next
// character there is "E", not whitespace or end of line.
var (
	goOKPattern          = regexp.MustCompile(`^ok(\s|$)`)
	goFAILPattern        = regexp.MustCompile(`^FAIL(\s|$)`)
	goVerbosePassPattern = regexp.MustCompile(`--- PASS:`)
	goVerboseFailPattern = regexp.MustCompile(`--- FAIL:`)
	jestPattern          = regexp.MustCompile(`Tests:\s+(?:(\d+) failed, )?(?:(\d+) skipped, )?(\d+) passed, (\d+) total`)
	vitestPattern        = regexp.MustCompile(`Tests\s+(?:(\d+) failed \| )?(\d+) passed \((\d+)\)`)
	cargoPattern         = regexp.MustCompile(`test result: (ok|FAILED)\. (\d+) passed; (\d+) failed`)
	executedPattern      = regexp.MustCompile(`Executed (\d+) tests?, with (\d+) failures?`)
	testMarkerPattern    = regexp.MustCompile(`\*\* TEST (SUCCEEDED|FAILED) \*\*`)
	pullURLPattern       = regexp.MustCompile(`pull/(\d+)`)
)

// countWordPattern is the catch-all pattern behind reconstructGenericCounts
// (pytest, Mocha, and anything else shaped like "<n> <word>"). The word
// alternation is a small, closed set — never ".*" — precisely so a match
// can only ever yield a digit string and one of these exact words, no
// matter what surrounds it on the line (SPEC 7.5's pytest columns plus
// Mocha's "passing"/"failing" and Jest's "skipped"/"total").
var countWordPattern = regexp.MustCompile(`(\d+)\s+(passed|failed|passing|failing|errors?|skipped|total)`)
