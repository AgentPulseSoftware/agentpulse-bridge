package classify

import "testing"

func intp(n int) *int { return &n }

type parseCase struct {
	name       string
	runner     RunnerID
	verbose    bool
	output     string
	wantOut    string
	wantPassed *int
	wantFailed *int
	wantTotal  *int
}

func runParseCases(t *testing.T, cases []parseCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseResult(tc.runner, tc.verbose, tc.output)
			if got.Outcome != tc.wantOut {
				t.Errorf("Outcome = %q, want %q", got.Outcome, tc.wantOut)
			}
			assertIntPtrEqual(t, "Passed", got.Passed, tc.wantPassed)
			assertIntPtrEqual(t, "Failed", got.Failed, tc.wantFailed)
			assertIntPtrEqual(t, "Total", got.Total, tc.wantTotal)
		})
	}
}

func assertIntPtrEqual(t *testing.T, field string, got, want *int) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, derefOrNil(got), derefOrNil(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func derefOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestParsePytest(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "failure",
			runner:     RunnerPytest,
			output:     "collected 12 items\ntests/test_api.py::test_login FAILED\n1 failed, 11 passed in 2.31s",
			wantOut:    OutcomeFail,
			wantPassed: intp(11), wantFailed: intp(1), wantTotal: intp(12),
		},
		{
			name:       "all passing",
			runner:     RunnerPytest,
			output:     "12 passed in 1.02s",
			wantOut:    OutcomePass,
			wantPassed: intp(12), wantFailed: intp(0), wantTotal: intp(12),
		},
		{
			name:       "errors count as failures",
			runner:     RunnerPytest,
			output:     "2 passed, 1 error in 0.5s",
			wantOut:    OutcomeFail,
			wantPassed: intp(2), wantFailed: intp(1), wantTotal: intp(3),
		},
		{
			name:    "unparseable output",
			runner:  RunnerPytest,
			output:  "some unrelated build tool output with no recognizable summary",
			wantOut: OutcomeUnknown,
		},
		{
			// A count too large to parse as an int (here, 20 digits) must
			// drop ALL counts, not silently become 0: outcome is still
			// "fail" because "failed" appeared in the text at all, but no
			// passed/failed/total is reported.
			name:    "count overflows int, drops all counts but keeps outcome from the text",
			runner:  RunnerPytest,
			output:  "1 failed, 12345678901234567890 passed in 2.31s",
			wantOut: OutcomeFail,
		},
	})
}

func TestParseJest(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "failures and skipped",
			runner:     RunnerJest,
			output:     "PASS src/x.test.tsx\nTests:       2 failed, 1 skipped, 17 passed, 20 total",
			wantOut:    OutcomeFail,
			wantPassed: intp(17), wantFailed: intp(2), wantTotal: intp(20),
		},
		{
			name:       "all passing, no failed/skipped groups",
			runner:     RunnerJest,
			output:     "Tests:       20 passed, 20 total",
			wantOut:    OutcomePass,
			wantPassed: intp(20), wantFailed: intp(0), wantTotal: intp(20),
		},
		{
			name:    "unparseable output",
			runner:  RunnerJest,
			output:  "no summary line here",
			wantOut: OutcomeUnknown,
		},
		{
			name:    "count overflows int, drops all counts but keeps outcome from the text",
			runner:  RunnerJest,
			output:  "Tests:       2 failed, 12345678901234567890 passed, 12345678901234567892 total",
			wantOut: OutcomeFail,
		},
	})
}

func TestParseVitest(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "failures",
			runner:     RunnerVitest,
			output:     " Tests  2 failed | 43 passed (45)",
			wantOut:    OutcomeFail,
			wantPassed: intp(43), wantFailed: intp(2), wantTotal: intp(45),
		},
		{
			name:       "all passing",
			runner:     RunnerVitest,
			output:     " Tests  45 passed (45)",
			wantOut:    OutcomePass,
			wantPassed: intp(45), wantFailed: intp(0), wantTotal: intp(45),
		},
		{
			name:    "unparseable output",
			runner:  RunnerVitest,
			output:  "no summary line here",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseMocha(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "failures",
			runner:     RunnerMocha,
			output:     "  12 passing (42ms)\n  1 failing",
			wantOut:    OutcomeFail,
			wantPassed: intp(12), wantFailed: intp(1), wantTotal: intp(13),
		},
		{
			name:       "all passing",
			runner:     RunnerMocha,
			output:     "  13 passing (42ms)",
			wantOut:    OutcomePass,
			wantPassed: intp(13), wantFailed: intp(0), wantTotal: intp(13),
		},
		{
			name:    "unparseable output",
			runner:  RunnerMocha,
			output:  "no summary line here",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseNpmTriesEveryParser(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "delegates to pytest",
			runner:     RunnerNpm,
			output:     "1 failed, 11 passed in 2.31s",
			wantOut:    OutcomeFail,
			wantPassed: intp(11), wantFailed: intp(1), wantTotal: intp(12),
		},
		{
			name:       "delegates to jest",
			runner:     RunnerNpm,
			output:     "Tests:       2 failed, 17 passed, 19 total",
			wantOut:    OutcomeFail,
			wantPassed: intp(17), wantFailed: intp(2), wantTotal: intp(19),
		},
		{
			name:       "delegates to vitest",
			runner:     RunnerNpm,
			output:     " Tests  45 passed (45)",
			wantOut:    OutcomePass,
			wantPassed: intp(45), wantFailed: intp(0), wantTotal: intp(45),
		},
		{
			name:       "delegates to mocha",
			runner:     RunnerNpm,
			output:     "  13 passing (42ms)",
			wantOut:    OutcomePass,
			wantPassed: intp(13), wantFailed: intp(0), wantTotal: intp(13),
		},
		{
			name:    "none match",
			runner:  RunnerNpm,
			output:  "some custom test runner output with no known shape",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseGo(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:    "non-verbose pass",
			runner:  RunnerGo,
			verbose: false,
			output:  "ok  \tgithub.com/agentpulsesoftware/agentpulse-bridge/internal/classify\t0.386s",
			wantOut: OutcomePass,
		},
		{
			name:    "non-verbose fail, no counts even though FAIL present",
			runner:  RunnerGo,
			verbose: false,
			output:  "--- FAIL: TestFoo (0.00s)\nFAIL\tgithub.com/x/y\t0.10s",
			wantOut: OutcomeFail,
		},
		{
			name:       "verbose pass with counts",
			runner:     RunnerGo,
			verbose:    true,
			output:     "--- PASS: TestFoo (0.00s)\n--- PASS: TestBar (0.00s)\nok  \tgithub.com/x/y\t0.10s",
			wantOut:    OutcomePass,
			wantPassed: intp(2), wantFailed: intp(0), wantTotal: intp(2),
		},
		{
			name:       "verbose fail with counts",
			runner:     RunnerGo,
			verbose:    true,
			output:     "--- PASS: TestFoo (0.00s)\n--- FAIL: TestBar (0.00s)\nFAIL\tgithub.com/x/y\t0.10s",
			wantOut:    OutcomeFail,
			wantPassed: intp(1), wantFailed: intp(1), wantTotal: intp(2),
		},
		{
			name:    "unparseable output",
			runner:  RunnerGo,
			output:  "some unrelated output",
			wantOut: OutcomeUnknown,
		},
		{
			name:    "pytest FAILED line must not false-positive as go FAIL",
			runner:  RunnerGo,
			output:  "FAILED tests/test_x.py::test_y",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseCargo(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "ok",
			runner:     RunnerCargo,
			output:     "test result: ok. 42 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out",
			wantOut:    OutcomePass,
			wantPassed: intp(42), wantFailed: intp(0), wantTotal: intp(42),
		},
		{
			name:       "failed",
			runner:     RunnerCargo,
			output:     "test result: FAILED. 40 passed; 2 failed; 0 ignored; 0 measured; 0 filtered out",
			wantOut:    OutcomeFail,
			wantPassed: intp(40), wantFailed: intp(2), wantTotal: intp(42),
		},
		{
			name:    "unparseable output",
			runner:  RunnerCargo,
			output:  "compiling agentpulse v0.1.0",
			wantOut: OutcomeUnknown,
		},
		{
			name:    "count overflows int, drops all counts but keeps outcome from the text",
			runner:  RunnerCargo,
			output:  "test result: FAILED. 12345678901234567890 passed; 12345678901234567891 failed",
			wantOut: OutcomeFail,
		},
	})
}

func TestParseSwift(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "all passing",
			runner:     RunnerSwift,
			output:     "Executed 10 tests, with 0 failures (0 unexpected) in 1.234 seconds",
			wantOut:    OutcomePass,
			wantPassed: intp(10), wantFailed: intp(0), wantTotal: intp(10),
		},
		{
			name:       "some failing",
			runner:     RunnerSwift,
			output:     "Executed 10 tests, with 2 failures (0 unexpected) in 1.234 seconds",
			wantOut:    OutcomeFail,
			wantPassed: intp(8), wantFailed: intp(2), wantTotal: intp(10),
		},
		{
			name:    "unparseable output",
			runner:  RunnerSwift,
			output:  "Compiling...",
			wantOut: OutcomeUnknown,
		},
		{
			// swift test prints one "Executed N, with M failures" line per
			// test suite/bundle, then a final one for the whole run. An
			// early passing suite must not paper over the run's own total.
			name:       "multi-suite: early suite passes, total fails",
			runner:     RunnerSwift,
			output:     "Executed 5 tests, with 0 failures (0 unexpected) in 0.5 seconds\nExecuted 15 tests, with 2 failures (0 unexpected) in 3.0 seconds",
			wantOut:    OutcomeFail,
			wantPassed: intp(13), wantFailed: intp(2), wantTotal: intp(15),
		},
		{
			name:    "count overflows int, no text fallback so outcome is unknown",
			runner:  RunnerSwift,
			output:  "Executed 12345678901234567890 tests, with 2 failures (0 unexpected) in 1.0 seconds",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseXcodebuild(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:       "executed line present",
			runner:     RunnerXcodebuild,
			output:     "Executed 5 tests, with 1 failure (0 unexpected) in 3.0 seconds\n** TEST FAILED **",
			wantOut:    OutcomeFail,
			wantPassed: intp(4), wantFailed: intp(1), wantTotal: intp(5),
		},
		{
			// Same multi-suite concern as Swift: xcodebuild prints one
			// "Executed N, with M failures" per test bundle, then a final
			// total. An early bundle passing must not hide the total
			// failing.
			name:       "multi-suite: early suite passes, total fails",
			runner:     RunnerXcodebuild,
			output:     "Executed 3 tests, with 0 failures (0 unexpected) in 0.2 seconds\nExecuted 12 tests, with 1 failures (0 unexpected) in 1.0 seconds\n** TEST FAILED **",
			wantOut:    OutcomeFail,
			wantPassed: intp(11), wantFailed: intp(1), wantTotal: intp(12),
		},
		{
			name:    "marker only, succeeded",
			runner:  RunnerXcodebuild,
			output:  "** TEST SUCCEEDED **",
			wantOut: OutcomePass,
		},
		{
			name:    "marker only, failed",
			runner:  RunnerXcodebuild,
			output:  "** TEST FAILED **",
			wantOut: OutcomeFail,
		},
		{
			name:    "unparseable output",
			runner:  RunnerXcodebuild,
			output:  "note: Building targets in dependency order",
			wantOut: OutcomeUnknown,
		},
	})
}

func TestParseBuild(t *testing.T) {
	runParseCases(t, []parseCase{
		{
			name:    "build succeeded marker",
			runner:  RunnerBuild,
			output:  "** BUILD SUCCEEDED **",
			wantOut: OutcomePass,
		},
		{
			name:    "build failed marker",
			runner:  RunnerBuild,
			output:  "** BUILD FAILED **",
			wantOut: OutcomeFail,
		},
		{
			name:    "generic error line, no marker",
			runner:  RunnerBuild,
			output:  "main.go:10:2: undefined: error in package",
			wantOut: OutcomeFail,
		},
		{
			name:    "clean output, no marker, no error line",
			runner:  RunnerBuild,
			output:  "go: downloading module\nbuild complete",
			wantOut: OutcomeUnknown,
		},
	})
	// Build never gets counts, even when the output happens to contain
	// count-shaped text.
	got := ParseResult(RunnerBuild, false, "BUILD SUCCEEDED, 4 passed, 0 failed")
	if got.Passed != nil || got.Failed != nil || got.Total != nil {
		t.Errorf("build kind should never carry counts, got %+v", got)
	}
}

// TestFinalizeCountsDropsInconsistentTotal exercises SPEC 7.5's
// "passed + failed <= total" rule directly: Jest's captured total can, in
// principle, be smaller than passed+failed if the summary line is
// malformed or truncated, and counts must be omitted entirely in that
// case (outcome is still kept).
func TestFinalizeCountsDropsInconsistentTotal(t *testing.T) {
	got := finalizeCounts(OutcomeFail, 5, 5, intp(3)) // 5+5=10 > 3
	if got.Outcome != OutcomeFail {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeFail)
	}
	if got.Passed != nil || got.Failed != nil || got.Total != nil {
		t.Errorf("expected counts to be dropped when total < passed+failed, got %+v", got)
	}
}

func TestFinalizeCountsDerivesTotalWhenAbsent(t *testing.T) {
	got := finalizeCounts(OutcomePass, 5, 0, nil)
	if got.Total == nil || *got.Total != 5 {
		t.Errorf("Total = %v, want 5 (derived from passed+failed)", got.Total)
	}
}
