package classify

import (
	"regexp"
	"strconv"
)

// VerificationResult is the outcome and (optional) counts ParseResult
// derives from a verification command's tool_response text, per SPEC 7.5.
type VerificationResult struct {
	Outcome string
	Passed  *int
	Failed  *int
	Total   *int
}

// finalizeCounts applies SPEC 7.5's shared count rule: passed, failed, and
// total are attached to the result together, or not at all. total is
// derived as passed+failed when the caller didn't capture one; when the
// caller did capture one, it must be at least passed+failed, or the counts
// are dropped (outcome is always kept regardless).
func finalizeCounts(outcome string, passed, failed int, total *int) VerificationResult {
	t := passed + failed
	if total != nil {
		if *total < passed+failed {
			return VerificationResult{Outcome: outcome}
		}
		t = *total
	}
	p, f, tt := passed, failed, t
	return VerificationResult{Outcome: outcome, Passed: &p, Failed: &f, Total: &tt}
}

// matchInt searches s for re's first submatch and parses it as an integer.
// A match whose captured digits fail to parse — including overflowing int
// (a 20-digit count is not physically possible test output, but nothing
// stops a parser from being pointed at one) — is treated the same as no
// match at all: false, never a fabricated 0.
func matchInt(re *regexp.Regexp, s string) (int, bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// findCount searches s for re's first submatch, for callers (pytest,
// Mocha) that run several independent, individually-optional searches over
// the same text rather than one composite regex. It separates two
// questions a caller needs answered differently:
//   - found: did the pattern match at all? This is a text-level signal —
//     pytest only prints "N failed" when there was at least one failure —
//     and is used to decide outcome (pass vs. fail) independently of
//     whether the number itself could be trusted.
//   - ok: is value safe to use? true when the pattern didn't match at all
//     (value is a legitimate 0, nothing to distrust) OR matched and its
//     digits parsed cleanly; false when it matched but the digits failed
//     to parse (e.g. overflowed int) — a case the caller must treat as
//     "we can't trust any of the counts", never silently substitute 0 and
//     keep going.
func findCount(re *regexp.Regexp, s string) (found bool, value int, ok bool) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return false, 0, true
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return true, 0, false
	}
	return true, n, true
}

// parseGroup interprets one capture group from a regex match already known
// to have succeeded overall: "" means an optional group that didn't
// participate (Jest's optional "N failed, " clause, say) — a legitimate,
// trustworthy 0 — and anything else must parse as a clean int (no
// overflow) or ok is false. Unlike findCount, there is no separate "found"
// question here: the caller already knows the group either has digits or
// is empty by construction of the regex.
func parseGroup(s string) (value int, ok bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// --- pytest: `(\d+) passed`, `(\d+) failed`, `(\d+) error(s)?` ---

var (
	pytestPassedPattern = regexp.MustCompile(`\b(\d+) passed\b`)
	pytestFailedPattern = regexp.MustCompile(`\b(\d+) failed\b`)
	pytestErrorPattern  = regexp.MustCompile(`\b(\d+) errors?\b`)
)

func parsePytest(output string) VerificationResult {
	hasPassed, passed, passedOK := findCount(pytestPassedPattern, output)
	hasFailed, failedN, failedOK := findCount(pytestFailedPattern, output)
	hasError, errN, errorOK := findCount(pytestErrorPattern, output)
	if !hasPassed && !hasFailed && !hasError {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	// Outcome comes from whether "N failed"/"N error(s)" appeared at all
	// (SPEC 7.5: "outcome fail if failed or errors > 0") — a text-level
	// signal, decided before (and independent of) whether those counts
	// themselves parsed cleanly.
	outcome := OutcomePass
	if hasFailed || hasError {
		outcome = OutcomeFail
	}
	if !passedOK || !failedOK || !errorOK {
		return VerificationResult{Outcome: outcome}
	}
	// pytest's summary has no single "N errors" bucket distinct from
	// "failed" in the schema's counts (verification_finished has only
	// passed/failed/total): collected errors count as failures.
	failed := failedN + errN
	return finalizeCounts(outcome, passed, failed, nil)
}

// --- Jest: `Tests:\s+(?:(\d+) failed, )?(?:(\d+) skipped, )?(\d+) passed, (\d+) total` ---

var jestPattern = regexp.MustCompile(`Tests:\s+(?:(\d+) failed, )?(?:(\d+) skipped, )?(\d+) passed, (\d+) total`)

func parseJest(output string) VerificationResult {
	m := jestPattern.FindStringSubmatch(output)
	if m == nil {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	// Jest only includes "N failed, " at all when N >= 1, so the group's
	// mere presence (independent of whether N itself parses) is the
	// outcome signal.
	outcome := OutcomePass
	if m[1] != "" {
		outcome = OutcomeFail
	}
	failed, okF := parseGroup(m[1])
	passed, okP := parseGroup(m[3])
	total, okT := parseGroup(m[4])
	if !okF || !okP || !okT {
		return VerificationResult{Outcome: outcome}
	}
	return finalizeCounts(outcome, passed, failed, &total)
}

// --- Vitest: `Tests\s+(?:(\d+) failed \| )?(\d+) passed \((\d+)\)` ---

var vitestPattern = regexp.MustCompile(`Tests\s+(?:(\d+) failed \| )?(\d+) passed \((\d+)\)`)

func parseVitest(output string) VerificationResult {
	m := vitestPattern.FindStringSubmatch(output)
	if m == nil {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	// Same reasoning as Jest: Vitest only prints "N failed | " when N >= 1.
	outcome := OutcomePass
	if m[1] != "" {
		outcome = OutcomeFail
	}
	failed, okF := parseGroup(m[1])
	passed, okP := parseGroup(m[2])
	total, okT := parseGroup(m[3])
	if !okF || !okP || !okT {
		return VerificationResult{Outcome: outcome}
	}
	return finalizeCounts(outcome, passed, failed, &total)
}

// --- Mocha: `(\d+) passing`, `(\d+) failing` ---

var (
	mochaPassingPattern = regexp.MustCompile(`\b(\d+) passing\b`)
	mochaFailingPattern = regexp.MustCompile(`\b(\d+) failing\b`)
)

func parseMocha(output string) VerificationResult {
	hasPassing, passed, passedOK := findCount(mochaPassingPattern, output)
	hasFailing, failed, failedOK := findCount(mochaFailingPattern, output)
	if !hasPassing && !hasFailing {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	outcome := OutcomePass
	if hasFailing {
		outcome = OutcomeFail
	}
	if !passedOK || !failedOK {
		return VerificationResult{Outcome: outcome}
	}
	return finalizeCounts(outcome, passed, failed, nil)
}

// --- npm scripts: try every parser above (SPEC 7.5), first match wins ---

func parseNpm(output string) VerificationResult {
	for _, f := range []func(string) VerificationResult{parsePytest, parseJest, parseVitest, parseMocha} {
		if r := f(output); r.Outcome != OutcomeUnknown {
			return r
		}
	}
	return VerificationResult{Outcome: OutcomeUnknown}
}

// --- Go: `^ok\s` / `^FAIL` lines; counts only with -v, from `--- PASS`/`--- FAIL` ---

var (
	goOKPattern          = regexp.MustCompile(`(?m)^ok(\s|$)`)
	goFAILPattern        = regexp.MustCompile(`(?m)^FAIL(\s|$)`)
	goVerbosePassPattern = regexp.MustCompile(`--- PASS:`)
	goVerboseFailPattern = regexp.MustCompile(`--- FAIL:`)
)

func parseGo(output string, verbose bool) VerificationResult {
	var outcome string
	switch {
	case goFAILPattern.MatchString(output):
		outcome = OutcomeFail
	case goOKPattern.MatchString(output):
		outcome = OutcomePass
	default:
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	if !verbose {
		return VerificationResult{Outcome: outcome}
	}
	passed := len(goVerbosePassPattern.FindAllStringIndex(output, -1))
	failed := len(goVerboseFailPattern.FindAllStringIndex(output, -1))
	if passed == 0 && failed == 0 {
		return VerificationResult{Outcome: outcome}
	}
	return finalizeCounts(outcome, passed, failed, nil)
}

// --- Cargo: `test result: (ok|FAILED)\. (\d+) passed; (\d+) failed` ---

var cargoPattern = regexp.MustCompile(`test result: (ok|FAILED)\. (\d+) passed; (\d+) failed`)

func parseCargo(output string) VerificationResult {
	m := cargoPattern.FindStringSubmatch(output)
	if m == nil {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	outcome := OutcomeFail
	if m[1] == "ok" {
		outcome = OutcomePass
	}
	passed, okP := parseGroup(m[2])
	failed, okF := parseGroup(m[3])
	if !okP || !okF {
		return VerificationResult{Outcome: outcome}
	}
	return finalizeCounts(outcome, passed, failed, nil)
}

// --- Swift & xcodebuild: `Executed (\d+) tests?, with (\d+) failures?` ---

var executedPattern = regexp.MustCompile(`Executed (\d+) tests?, with (\d+) failures?`)

// parseExecuted takes the LAST "Executed N tests, with M failures" match,
// not the first: both swift test and xcodebuild print one such line per
// test suite/bundle, followed by a final line for the run as a whole, and
// only that last, overall line's counts are the ones SPEC 7.5 wants
// (an early suite passing must not paper over the whole run failing).
func parseExecuted(output string) VerificationResult {
	matches := executedPattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	m := matches[len(matches)-1]
	// Unlike pytest/Jest/Vitest, this pattern has no separate word that
	// signals pass/fail independent of the numbers — "failures" here is
	// only ever a count, never a bare marker — so if that count itself
	// can't be trusted, there is no text-level fallback: outcome is
	// unknown, not a guess.
	total, okT := parseGroup(m[1])
	failed, okF := parseGroup(m[2])
	if !okT || !okF {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	passed := total - failed
	if passed < 0 {
		passed = 0
	}
	outcome := OutcomePass
	if failed > 0 {
		outcome = OutcomeFail
	}
	return finalizeCounts(outcome, passed, failed, &total)
}

// xcodebuild additionally recognizes the `** TEST SUCCEEDED/FAILED **`
// marker when no "Executed N tests" summary is present (e.g. a build-only
// failure before any test ran).
var testMarkerPattern = regexp.MustCompile(`\*\* TEST (SUCCEEDED|FAILED) \*\*`)

func parseXcodebuild(output string) VerificationResult {
	if r := parseExecuted(output); r.Outcome != OutcomeUnknown {
		return r
	}
	m := testMarkerPattern.FindStringSubmatch(output)
	if m == nil {
		return VerificationResult{Outcome: OutcomeUnknown}
	}
	outcome := OutcomeFail
	if m[1] == "SUCCEEDED" {
		outcome = OutcomePass
	}
	return VerificationResult{Outcome: outcome}
}

// --- Build: outcome only, from `BUILD SUCCEEDED`/`BUILD FAILED` or a
// generic error line; never counts (SPEC 7.5: "kind = build", and a build
// has no pass/fail test counts to report). ---

var (
	buildFailedMarkerPattern    = regexp.MustCompile(`BUILD FAILED`)
	buildSucceededMarkerPattern = regexp.MustCompile(`BUILD SUCCEEDED`)
	errorLinePattern            = regexp.MustCompile(`(?im)^.*\berror\b.*$`)
)

func parseBuild(output string) VerificationResult {
	switch {
	case buildFailedMarkerPattern.MatchString(output):
		return VerificationResult{Outcome: OutcomeFail}
	case buildSucceededMarkerPattern.MatchString(output):
		return VerificationResult{Outcome: OutcomePass}
	case errorLinePattern.MatchString(output):
		return VerificationResult{Outcome: OutcomeFail}
	default:
		return VerificationResult{Outcome: OutcomeUnknown}
	}
}

// ParseResult parses output (a verification command's combined
// stdout/stderr text) for runner, applying SPEC 7.5's per-runner rules.
// verbose is only consulted for RunnerGo (whether -v was present at
// command time; see PendingVerification.GoVerbose).
func ParseResult(runner RunnerID, verbose bool, output string) VerificationResult {
	switch runner {
	case RunnerPytest:
		return parsePytest(output)
	case RunnerJest:
		return parseJest(output)
	case RunnerVitest:
		return parseVitest(output)
	case RunnerMocha:
		return parseMocha(output)
	case RunnerNpm:
		return parseNpm(output)
	case RunnerGo:
		return parseGo(output, verbose)
	case RunnerCargo:
		return parseCargo(output)
	case RunnerSwift:
		return parseExecuted(output)
	case RunnerXcodebuild:
		return parseXcodebuild(output)
	case RunnerBuild:
		return parseBuild(output)
	default:
		return VerificationResult{Outcome: OutcomeUnknown}
	}
}
