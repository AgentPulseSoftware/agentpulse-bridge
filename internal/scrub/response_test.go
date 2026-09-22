package scrub

import (
	"strings"
	"testing"
)

func TestFilterResponseLines(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "pytest summary reconstructed, traceback collapses",
			input: "============================= test session starts ==============================\n" +
				"collected 12 items\n" +
				"\n" +
				"tests/test_api.py::test_login FAILED\n" +
				"\n" +
				"================================== FAILURES ===================================\n" +
				"\n" +
				"    def test_login():\n" +
				">       assert response.status_code == 200\n" +
				"E       AssertionError: assert 401 == 200\n" +
				"\n" +
				"/Users/sam/project/tests/test_api.py:42: AssertionError\n" +
				"========================= 1 failed, 11 passed in 2.31s =========================",
			want: "…\n1 failed, 11 passed",
		},
		{
			name: "jest summary reconstructed from capture groups only",
			input: "PASS src/components/Button.test.tsx\n" +
				"Tests:       2 failed, 1 skipped, 17 passed, 20 total\n" +
				"Snapshots:   0 total\n" +
				"Time:        3.412s",
			// "Snapshots:   0 total" also matches the generic count-word
			// catch-all ("total" is in its word list, for Jest's own
			// summary line) and reconstructs harmlessly to "0 total".
			want: "…\nTests: 2 failed, 1 skipped, 17 passed, 20 total\n0 total\n…",
		},
		{
			name: "vitest summary reconstructed from capture groups only",
			input: " Test Files  1 failed | 4 passed (5)\n" +
				" Tests  2 failed | 43 passed (45)\n" +
				" Duration  1.02s",
			want: "1 failed, 4 passed\nTests 2 failed | 43 passed (45)\n…",
		},
		{
			name: "mocha summary reconstructed from capture groups only",
			input: "  Auth\n" +
				"    \u2713 logs in\n" +
				"    1) rejects bad password\n" +
				"\n" +
				"  12 passing (45ms)\n" +
				"  1 failing",
			want: "…\n12 passing\n1 failing",
		},
		{
			name: "go test verbose: markers survive, test names and paths do not",
			input: "=== RUN   TestFoo\n" +
				"--- PASS: TestFoo (0.00s)\n" +
				"=== RUN   TestChargeCard\n" +
				"    bar_test.go:12: unexpected value at /Users/sam/project/bar.go\n" +
				"--- FAIL: TestChargeCard (0.00s)\n" +
				"FAIL\n" +
				"FAIL\tgithub.com/acme/private/pkg/billing\t0.004s",
			want: "…\n--- PASS\n…\n--- FAIL\nFAIL\nFAIL",
		},
		{
			name:  "go test non-verbose ok: bare marker only, no module path",
			input: "ok  \tgithub.com/agentpulsesoftware/agentpulse-bridge/internal/hooks\t0.468s",
			want:  "ok",
		},
		{
			name: "cargo test summary reconstructed from capture groups only",
			input: "running 8 tests\n" +
				"test tests::it_fails ... FAILED\n" +
				"\n" +
				"failures:\n" +
				"\n" +
				"---- tests::it_fails stdout ----\n" +
				"thread 'tests::it_fails' panicked at /Users/sam/project/src/lib.rs:10:5\n" +
				"\n" +
				"test result: FAILED. 7 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out",
			want: "…\ntest result: FAILED. 7 passed; 1 failed",
		},
		{
			name:  "swift test summary reconstructed from capture groups only",
			input: "Test Suite 'All tests' passed.\n\t Executed 42 tests, with 2 failures (0 unexpected) in 1.234 (1.240) seconds",
			want:  "…\nExecuted 42 tests, with 2 failures",
		},
		{
			name: "xcodebuild summary reconstructed from capture groups only",
			input: "Test Suite 'AgentPulseTests' started at 2026-09-12 10:00:00.\n" +
				"/Users/sam/project/AgentPulseTests/LoginTests.swift:15: error: -[LoginTests testLogin] : XCTAssertEqual failed\n" +
				"Executed 10 tests, with 1 failure (0 unexpected) in 3.100 (3.120) seconds\n" +
				"** TEST FAILED **",
			want: "…\nExecuted 10 tests, with 1 failures\n** TEST FAILED **",
		},
		{
			name:  "build success marker reconstructed as a bare constant",
			input: "Compiling agentpulse v0.1.0\n** BUILD SUCCEEDED **\nDone",
			want:  "…\nBUILD SUCCEEDED\n…",
		},
		{
			name:  "pr url line reduced to pull/<number>",
			input: "Creating pull request for feature-branch into main in acme/agentpulse\nhttps://github.com/acme/agentpulse/pull/482\nhttps://github.com/acme/agentpulse/pull/482.diff",
			want:  "…\npull/482\npull/482",
		},
		{
			name:  "unrelated lines fully collapse to one ellipsis",
			input: "/Users/sam/project/src/main.go\nsome random build chatter\nmore chatter",
			want:  "…",
		},

		// --- adversarial samples (all must leak nothing) ---
		{
			name:  "adversarial: pytest FAILED summary line with assertion detail",
			input: `FAILED tests/test_billing.py::test_charge - assert 500 == 401`,
			want:  "…",
		},
		{
			name:  "adversarial: go ok line with a private module path",
			input: `ok  github.com/acme/private/pkg 0.5s`,
			want:  "ok",
		},
		{
			name:  "adversarial: sentence containing a count word next to a real path",
			input: `1 error found in ~/secret/notes.txt`,
			want:  "1 error",
		},
		{
			name:  "adversarial: bare Windows path with no result context",
			input: `C:\Users\sam\AppData\Local\secrets\config.json`,
			want:  "…",
		},
		{
			name:  "adversarial: go verbose FAIL line with a suggestive test name",
			input: `--- FAIL: TestChargeCard (0.02s)`,
			want:  "--- FAIL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterResponseLines(tc.input)
			if got != tc.want {
				t.Errorf("filterResponseLines(%q)\n got:  %q\n want: %q", tc.input, got, tc.want)
			}
			assertNoLeak(t, got)
		})
	}
}

// assertNoLeak checks output for every kind of thing this package must
// never let through: absolute Unix and Windows paths, home-relative paths,
// file URLs, common system directories, test/function names, and package
// import paths that showed up in this file's adversarial inputs.
func assertNoLeak(t *testing.T, got string) {
	t.Helper()
	forbidden := []string{
		"/Users/", "/home/", "~/", `C:\`, `\Users\`, "file://",
		"secret", "notes.txt", "config.json",
		"TestFoo", "TestChargeCard", "TestBar",
		"github.com/acme/private", "github.com/agentpulsesoftware/agentpulse-bridge",
		"billing.py", "test_charge", "500 == 401",
	}
	for _, bad := range forbidden {
		if strings.Contains(got, bad) {
			t.Errorf("filtered output leaked %q: %q", bad, got)
		}
	}
}
