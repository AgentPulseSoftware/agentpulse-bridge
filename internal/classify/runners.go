package classify

import (
	"regexp"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cmdnorm"
)

// RunnerID is one of the verification runner identifiers from SPEC 7.5
// (and the `runner` enum in the AgentPulse `event.v1` schema).
type RunnerID string

// Runner identifiers (SPEC 7.5, schema `runner` enum).
const (
	RunnerPytest     RunnerID = "pytest"
	RunnerJest       RunnerID = "jest"
	RunnerVitest     RunnerID = "vitest"
	RunnerMocha      RunnerID = "mocha"
	RunnerNpm        RunnerID = "npm"
	RunnerGo         RunnerID = "go"
	RunnerCargo      RunnerID = "cargo"
	RunnerSwift      RunnerID = "swift"
	RunnerXcodebuild RunnerID = "xcodebuild"
	RunnerBuild      RunnerID = "build"
)

type runnerDef struct {
	id       RunnerID
	kind     string
	patterns []*regexp.Regexp
}

// runnerTable is SPEC 7.5's table as data: the canonical, live-detection
// runner list. internal/scrub keeps a separate,
// boolean-only copy of these same regex alternatives
// (verificationCommandPatterns in internal/scrub/runners.go) rather than
// importing this table — deliberately: that package only ever needs "is
// this command a verification run", a dev-only, offline concern (scrubbing
// a recording) with a different lifetime and blast radius than this
// package's live kind/runner/outcome detection. internal/cmdnorm, the
// actual shared logic (stripping env assignments, a leading `cd ... &&`,
// and taking the first pipeline segment) both packages need byte-for-byte
// identical, IS shared — see that package.
//
// Every pattern below is anchored with "^" at the start of the normalized
// segment (SPEC 7.5, amended: "anchored at the command word"). DetectRunner
// already strips leading env assignments and a `cd ... &&` prefix and takes
// only the first pipeline segment (internal/cmdnorm) before matching, so
// "^" lands exactly on the command word that segment actually runs — never
// on a runner-shaped word appearing later, inside a quoted argument or
// message. Without the anchor, `echo "run pytest later" >> NOTES.md` or
// `grep -rn "go test" Makefile` would wrongly look like verification runs,
// since the word is present in the command line even though it never runs
// as a command.
//
// Table order matters for exactly one pair of rows: xcodebuild-with-test
// must be checked before the generic Build row. SPEC 7.5's own Build
// pattern excludes a test invocation with a negative lookahead —
// `xcodebuild(?! .*test)` — that Go's regexp package (RE2) cannot express.
// Checking the more specific xcodebuild-test row first reproduces the same
// result without it: a command containing "xcodebuild ... test" matches
// that earlier row and DetectRunner never reaches the Build row's bare
// "xcodebuild" alternative.
var runnerTable = []runnerDef{
	{RunnerPytest, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^(python3?|py) -m pytest\b`),
		regexp.MustCompile(`^pytest\b`),
		regexp.MustCompile(`^uv run pytest\b`),
		regexp.MustCompile(`^poetry run pytest\b`),
	}},
	{RunnerJest, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^jest\b`),
		regexp.MustCompile(`^npx jest\b`),
		regexp.MustCompile(`^pnpm jest\b`),
		regexp.MustCompile(`^yarn jest\b`),
	}},
	{RunnerVitest, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^vitest\b`),
		regexp.MustCompile(`^npx vitest\b`),
		regexp.MustCompile(`^pnpm vitest\b`),
		regexp.MustCompile(`^yarn vitest\b`),
	}},
	{RunnerMocha, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^mocha\b`),
		regexp.MustCompile(`^npx mocha\b`),
	}},
	{RunnerNpm, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^(npm|pnpm|yarn|bun) (run )?test\b`),
	}},
	{RunnerGo, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^go test\b`),
	}},
	{RunnerCargo, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^cargo (test|nextest run)\b`),
	}},
	{RunnerSwift, KindTest, []*regexp.Regexp{
		regexp.MustCompile(`^swift test\b`),
	}},
	{RunnerXcodebuild, KindTest, []*regexp.Regexp{
		// Per SPEC 7.5: "xcodebuild .* test"
		// required a token between "xcodebuild" and "test" (a literal
		// space, then ".*", then another literal space before "test"), so
		// "xcodebuild test -scheme X ..." — test immediately following,
		// nothing between them — never matched. "\bxcodebuild\b.*\btest\b"
		// only requires "test" to appear as its own word somewhere after
		// "xcodebuild", adjacent or not.
		regexp.MustCompile(`^xcodebuild\b.*\btest\b`),
	}},
	{RunnerBuild, KindBuild, []*regexp.Regexp{
		regexp.MustCompile(`^(npm|pnpm|yarn|bun) run build\b`),
		regexp.MustCompile(`^go build\b`),
		regexp.MustCompile(`^cargo build\b`),
		regexp.MustCompile(`^swift build\b`),
		regexp.MustCompile(`^xcodebuild\b`),
		regexp.MustCompile(`^make( |$)`),
	}},
}

// goVerboseFlagPattern detects a standalone "-v" token, used only to
// decide whether SPEC 7.5's `go test` per-test counts should be parsed
// (they are only emitted with -v).
var goVerboseFlagPattern = regexp.MustCompile(`(^|\s)-v(\s|$)`)

// DetectRunner reports whether command (the raw, unnormalized
// tool_input.command of a Bash PreToolUse call) is a verification run per
// SPEC 7.5, returning its runner identifier, kind ("test" or "build"), and
// whether -v was present (meaningful only for RunnerGo). ok is false when
// no runner pattern matches.
func DetectRunner(command string) (id RunnerID, kind string, goVerbose bool, ok bool) {
	seg := cmdnorm.Normalize(command)
	for _, r := range runnerTable {
		for _, p := range r.patterns {
			if p.MatchString(seg) {
				return r.id, r.kind, goVerboseFlagPattern.MatchString(seg), true
			}
		}
	}
	return "", "", false, false
}
