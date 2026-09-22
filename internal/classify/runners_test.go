package classify

import "testing"

// TestDetectRunnerCommandPatterns covers SPEC 7.5's command-pattern column,
// one case per runner (plus the env/cd-stripping and pipeline rules
// DetectRunner shares via internal/cmdnorm).
func TestDetectRunnerCommandPatterns(t *testing.T) {
	cases := []struct {
		name    string
		command string
		wantID  RunnerID
		wantKnd string
	}{
		{"pytest bare", "pytest -q", RunnerPytest, KindTest},
		{"pytest python -m", "python3 -m pytest tests/", RunnerPytest, KindTest},
		{"pytest py -m", "py -m pytest tests/", RunnerPytest, KindTest},
		{"pytest uv run", "uv run pytest tests/", RunnerPytest, KindTest},
		{"pytest poetry run", "poetry run pytest tests/", RunnerPytest, KindTest},

		{"jest bare", "jest src/", RunnerJest, KindTest},
		{"jest npx", "npx jest src/", RunnerJest, KindTest},
		{"jest pnpm", "pnpm jest", RunnerJest, KindTest},
		{"jest yarn", "yarn jest", RunnerJest, KindTest},

		{"vitest bare", "vitest run", RunnerVitest, KindTest},
		{"vitest npx", "npx vitest run", RunnerVitest, KindTest},
		{"vitest pnpm", "pnpm vitest", RunnerVitest, KindTest},
		{"vitest yarn", "yarn vitest", RunnerVitest, KindTest},

		{"mocha bare", "mocha test/", RunnerMocha, KindTest},
		{"mocha npx", "npx mocha test/", RunnerMocha, KindTest},

		{"npm test", "npm test", RunnerNpm, KindTest},
		{"npm run test", "npm run test", RunnerNpm, KindTest},
		{"pnpm test", "pnpm test", RunnerNpm, KindTest},
		{"yarn test", "yarn test", RunnerNpm, KindTest},
		{"bun test", "bun test", RunnerNpm, KindTest},

		{"go test", "go test ./...", RunnerGo, KindTest},

		{"cargo test", "cargo test --package agentpulse", RunnerCargo, KindTest},
		{"cargo nextest", "cargo nextest run --workspace", RunnerCargo, KindTest},

		{"swift test", "swift test --filter MyTests", RunnerSwift, KindTest},

		{"xcodebuild test", "xcodebuild -scheme AgentPulse -destination 'x' test", RunnerXcodebuild, KindTest},
		{"xcodebuild test immediately after", "xcodebuild test -scheme AgentPulse -destination 'x'", RunnerXcodebuild, KindTest},

		{"npm run build", "npm run build", RunnerBuild, KindBuild},
		{"go build", "go build ./...", RunnerBuild, KindBuild},
		{"cargo build", "cargo build --release", RunnerBuild, KindBuild},
		{"swift build", "swift build -c release", RunnerBuild, KindBuild},
		{"xcodebuild bare", "xcodebuild -scheme AgentPulse build", RunnerBuild, KindBuild},
		{"make bare", "make", RunnerBuild, KindBuild},
		{"make target", "make all", RunnerBuild, KindBuild},

		// env / cd stripping, and pipeline (shared cmdnorm behavior)
		{"env assignment stripped", "CI=true pytest -q", RunnerPytest, KindTest},
		{"cd stripped", "cd /repo && go test ./...", RunnerGo, KindTest},
		{"pipeline only first segment", "go test ./... -v | grep FAIL", RunnerGo, KindTest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, kind, _, ok := DetectRunner(tc.command)
			if !ok {
				t.Fatalf("DetectRunner(%q) matched nothing, want %s/%s", tc.command, tc.wantID, tc.wantKnd)
			}
			if id != tc.wantID {
				t.Errorf("DetectRunner(%q) id = %q, want %q", tc.command, id, tc.wantID)
			}
			if kind != tc.wantKnd {
				t.Errorf("DetectRunner(%q) kind = %q, want %q", tc.command, kind, tc.wantKnd)
			}
		})
	}
}

func TestDetectRunnerXcodebuildTestTakesPrecedenceOverBuild(t *testing.T) {
	// SPEC 7.5's Build pattern excludes "xcodebuild ... test" via a
	// negative lookahead RE2 can't express; runnerTable's ordering
	// (xcodebuild-test checked before Build) must reproduce that exclusion.
	id, kind, _, ok := DetectRunner("xcodebuild -scheme AgentPulse -destination 'x' test")
	if !ok || id != RunnerXcodebuild || kind != KindTest {
		t.Errorf("DetectRunner(xcodebuild ... test) = %q/%q/%v, want xcodebuild/test/true", id, kind, ok)
	}
}

func TestDetectRunnerGoVerboseFlag(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"go test ./... -v", true},
		{"go test -v ./...", true},
		{"go test ./...", false},
		{"go test -run TestFoo ./...", false},
	}
	for _, tc := range cases {
		_, _, verbose, ok := DetectRunner(tc.command)
		if !ok {
			t.Fatalf("DetectRunner(%q) matched nothing", tc.command)
		}
		if verbose != tc.want {
			t.Errorf("DetectRunner(%q) verbose = %v, want %v", tc.command, verbose, tc.want)
		}
	}
}

func TestDetectRunnerNoMatch(t *testing.T) {
	cases := []string{
		"ls -la",
		"rm -rf build",
		"echo hello",
		"cat package.json",
		"git status",
		"npm install",
		"npm run lint",
	}
	for _, cmd := range cases {
		if _, _, _, ok := DetectRunner(cmd); ok {
			t.Errorf("DetectRunner(%q) unexpectedly matched a runner", cmd)
		}
	}
}

// TestDetectRunnerAnchoredAtCommandWord proves patterns are anchored at
// the command word of the (env/cd-stripped) first pipeline segment, not
// searched anywhere in the line: a runner-shaped word inside a quoted
// argument or message must never look like a verification run just
// because the word appears somewhere on the line.
func TestDetectRunnerAnchoredAtCommandWord(t *testing.T) {
	cases := []string{
		`echo "run pytest later" >> NOTES.md`,
		`grep -rn "go test" Makefile`,
		`cat "notes about go build failures"`,
		`echo "remember to run make later"`,
	}
	for _, cmd := range cases {
		if id, kind, _, ok := DetectRunner(cmd); ok {
			t.Errorf("DetectRunner(%q) = %s/%s, want no match (runner word isn't the command)", cmd, id, kind)
		}
	}
}
