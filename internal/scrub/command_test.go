package scrub

import "testing"

func TestScrubCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		// --- one per SPEC 7.5 runner: keep runner tokens/flags, drop paths ---
		{
			name: "pytest with path and node id",
			cmd:  "pytest tests/test_api.py::test_login -v",
			want: "pytest -v",
		},
		{
			name: "python -m pytest with target",
			cmd:  "python3 -m pytest src/tests -k login --maxfail=1",
			want: "python3 -m pytest -k --maxfail",
		},
		{
			name: "uv run pytest",
			cmd:  "uv run pytest tests/",
			want: "uv run pytest",
		},
		{
			name: "jest with pattern",
			cmd:  "npx jest src/components/Button.test.tsx --coverage",
			want: "npx jest --coverage",
		},
		{
			name: "vitest run",
			cmd:  "vitest run src/foo.test.ts",
			want: "vitest run",
		},
		{
			name: "mocha with spec path",
			cmd:  "npx mocha test/unit/*.spec.js --timeout 5000",
			want: "npx mocha --timeout",
		},
		{
			name: "npm test script",
			cmd:  "npm test -- --watch=false",
			want: "npm test -- --watch",
		},
		{
			name: "go test verbose with package path",
			cmd:  "go test ./internal/hooks/... -v -run TestFoo",
			want: "go test -v -run",
		},
		{
			name: "cargo test",
			cmd:  "cargo test --package agentpulse -- --nocapture",
			want: "cargo test --package -- --nocapture",
		},
		{
			name: "cargo nextest",
			cmd:  "cargo nextest run --workspace",
			want: "cargo nextest run --workspace",
		},
		{
			name: "swift test",
			cmd:  "swift test --filter MyTests",
			want: "swift test --filter",
		},
		{
			name: "xcodebuild test",
			cmd:  "xcodebuild -scheme AgentPulse -destination 'platform=iOS Simulator,name=iPhone 16' test",
			want: "xcodebuild -scheme -destination test",
		},

		// --- env / cd stripping ---
		{
			name: "env assignment then cd then pytest",
			cmd:  "CI=true cd /Users/sam/project && pytest -q",
			want: "pytest -q",
		},
		{
			name: "multiple env assignments",
			cmd:  "FOO=1 BAR=baz pytest -q",
			want: "pytest -q",
		},

		// --- pipeline: only first segment matters ---
		{
			name: "pipeline to grep",
			cmd:  "go test ./... -v | grep FAIL",
			want: "go test -v",
		},

		// --- gh pr create / git commit ---
		{
			name: "gh pr create with title and body",
			cmd:  `gh pr create --title "Fix bug" --body "Long description with secrets"`,
			want: "gh pr create",
		},
		{
			name: "git commit with message",
			cmd:  `git commit -m "fix: internal path /Users/sam/secret-project details"`,
			want: "git commit",
		},

		// --- default: anything else ---
		{
			name: "arbitrary shell command",
			cmd:  "rm -rf /Users/sam/project/build",
			want: "scrubbed-command",
		},
		{
			name: "cat a file",
			cmd:  "cat /Users/sam/.ssh/id_rsa",
			want: "scrubbed-command",
		},

		// --- a kept flag's value (after "=") must never survive ---
		{
			name: "npm test with an API key flag value",
			cmd:  "npm run test -- --testPathPattern=billing --apiKey=sk_live_51H8xSECRET",
			want: "npm run test -- --testPathPattern --apiKey",
		},
		{
			name: "go build with a customer name baked into ldflags",
			cmd:  "go build -ldflags=-X=main.customer=AcmeBankPLC",
			want: "go build -ldflags",
		},
		{
			name: "pytest with a real path in --rootdir",
			cmd:  "pytest --rootdir=/Users/x -k billing",
			want: "pytest --rootdir -k",
		},
		{
			// A path glued directly onto a flag with no "=" at all must still be
			// dropped, not partially kept.
			name: "go test with a path glued onto -o with no separator",
			cmd:  "go test -o/Users/sam/clients/acme/out ./...",
			want: "go test",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubCommand(tc.cmd)
			if got != tc.want {
				t.Errorf("scrubCommand(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
			assertNoLeakedPath(t, got)
		})
	}
}

func assertNoLeakedPath(t *testing.T, s string) {
	t.Helper()
	for _, bad := range []string{
		"/Users/", "/home/", ".py", ".ts", ".js", ".rs",
		// Flag values from the kept-flag cases above: none of these must
		// ever survive, however the command is scrubbed.
		"SECRET", "AcmeBankPLC", "billing",
	} {
		if containsSubstring(s, bad) {
			t.Errorf("scrubbed command %q still contains %q", s, bad)
		}
	}
	// A kept flag's value always comes after "=" (flagNameOnly's job is to
	// drop exactly that), so a scrubbed command should never contain "="
	// at all.
	if containsSubstring(s, "=") {
		t.Errorf("scrubbed command %q still contains a flag value after \"=\"", s)
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
