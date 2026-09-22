package scrub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postToolUseFixture builds a realistic PostToolUse hook document for a Bash
// verification command, the shape "agentpulse record" would actually
// capture.
func postToolUseFixture(cwd, command, stdout, stderr string) map[string]any {
	return map[string]any{
		"session_id":      "sess_01J8QK7VZC9X8Y2Z3A4B5C6D7E",
		"transcript_path": "/Users/sam/.claude/projects/-Users-sam-notesapp/sess.jsonl",
		"cwd":             cwd,
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command":     command,
			"description": "Run the test suite",
		},
		"tool_response": map[string]any{
			"stdout":      stdout,
			"stderr":      stderr,
			"interrupted": false,
		},
	}
}

// runnerFixture is one table entry: a realistic verification command and
// response for a SPEC 7.5 runner, with the summary text that must survive
// scrubbing.
type runnerFixture struct {
	runner         string
	command        string
	stdout         string
	mustSurvive    []string // substrings that must remain in the scrubbed stdout
	mustNotSurvive []string // substrings that must NOT appear anywhere in the scrubbed document
}

func runnerFixtures() []runnerFixture {
	return []runnerFixture{
		{
			runner:  "pytest",
			command: "pytest tests/test_api.py -v",
			stdout: "collected 12 items\n" +
				"tests/test_api.py::test_login FAILED\n" +
				"/Users/sam/notesapp/tests/test_api.py:42: AssertionError\n" +
				"========================= 1 failed, 11 passed in 2.31s =========================",
			mustSurvive:    []string{"1 failed, 11 passed"},
			mustNotSurvive: []string{"/Users/sam", "test_api.py:42"},
		},
		{
			runner:  "jest",
			command: "npx jest src/components/Button.test.tsx --coverage",
			stdout: "PASS src/components/Button.test.tsx\n" +
				"  ● renders correctly\n" +
				"/Users/sam/notesapp/src/components/Button.tsx:12\n" +
				"Tests:       2 failed, 1 skipped, 17 passed, 20 total",
			mustSurvive:    []string{"2 failed, 1 skipped, 17 passed, 20 total"},
			mustNotSurvive: []string{"/Users/sam", "Button.tsx:12"},
		},
		{
			runner:  "vitest",
			command: "vitest run src/foo.test.ts",
			stdout: " Test Files  1 failed | 4 passed (5)\n" +
				" Tests  2 failed | 43 passed (45)\n" +
				" FAIL  /Users/sam/notesapp/src/foo.test.ts > adds numbers",
			mustSurvive:    []string{"1 failed, 4 passed", "2 failed | 43 passed (45)"},
			mustNotSurvive: []string{"/Users/sam"},
		},
		{
			runner:  "mocha",
			command: "npx mocha test/unit/*.spec.js --timeout 5000",
			stdout: "  Auth\n" +
				"    1) rejects bad password at /Users/sam/notesapp/test/unit/auth.spec.js:20\n" +
				"  12 passing (45ms)\n" +
				"  1 failing",
			mustSurvive:    []string{"12 passing", "1 failing"},
			mustNotSurvive: []string{"/Users/sam", "auth.spec.js:20"},
		},
		{
			runner:  "go test",
			command: "go test ./internal/hooks/... -v -run TestFoo",
			stdout: "=== RUN   TestFoo\n" +
				"    hooks_test.go:42: at /Users/sam/notesapp/bridge/internal/hooks/hooks_test.go\n" +
				"--- FAIL: TestFoo (0.00s)\n" +
				"FAIL",
			// SPEC 7.5 only ever needs the
			// bare --- PASS / --- FAIL / FAIL / ok markers counted, never
			// the test name or package path that normally follows them on
			// the same line, so those must NOT survive scrubbing.
			mustSurvive:    []string{"--- FAIL", "FAIL"},
			mustNotSurvive: []string{"/Users/sam", "hooks_test.go:42", "TestFoo"},
		},
		{
			runner:  "cargo test",
			command: "cargo test --package agentpulse -- --nocapture",
			stdout: "running 8 tests\n" +
				"thread 'it_fails' panicked at /Users/sam/notesapp/src/lib.rs:10:5\n" +
				"test result: FAILED. 7 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out",
			mustSurvive:    []string{"test result: FAILED. 7 passed; 1 failed"},
			mustNotSurvive: []string{"/Users/sam", "lib.rs:10"},
		},
		{
			runner:  "swift test",
			command: "swift test --filter MyTests",
			stdout: "Test Suite 'All tests' started.\n" +
				"/Users/sam/notesapp/Tests/MyTests.swift:15: error: failed\n" +
				"Executed 42 tests, with 2 failures (0 unexpected) in 1.234 (1.240) seconds",
			mustSurvive:    []string{"Executed 42 tests, with 2 failures"},
			mustNotSurvive: []string{"/Users/sam", "MyTests.swift:15"},
		},
		{
			runner:  "xcodebuild",
			command: "xcodebuild -scheme AgentPulse -destination 'platform=iOS Simulator,name=iPhone 16' test",
			stdout: "/Users/sam/notesapp/AgentPulseTests/LoginTests.swift:15: error: XCTAssertEqual failed\n" +
				"Executed 10 tests, with 1 failure (0 unexpected) in 3.100 (3.120) seconds\n" +
				"** TEST FAILED **",
			mustSurvive:    []string{"Executed 10 tests, with 1 failure", "** TEST FAILED **"},
			mustNotSurvive: []string{"/Users/sam", "LoginTests.swift:15"},
		},
	}
}

func TestDocumentScrubsRunnerFixtures(t *testing.T) {
	for _, rf := range runnerFixtures() {
		t.Run(rf.runner, func(t *testing.T) {
			doc := postToolUseFixture("/Users/sam/notesapp", rf.command, rf.stdout, "")
			ctx := newContext()
			scrubbed := ctx.document(doc)

			out, err := json.Marshal(scrubbed)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			whole := string(out)

			for _, want := range rf.mustSurvive {
				if !strings.Contains(whole, want) {
					t.Errorf("scrubbed document lost the result line %q:\n%s", want, whole)
				}
			}
			for _, bad := range rf.mustNotSurvive {
				if strings.Contains(whole, bad) {
					t.Errorf("scrubbed document still contains %q:\n%s", bad, whole)
				}
			}

			// Structural fields the classifier needs must survive verbatim.
			respMap := scrubbed["tool_response"].(map[string]any)
			if _, ok := respMap["interrupted"].(bool); !ok {
				t.Errorf("boolean tool_response field was not preserved: %v", respMap["interrupted"])
			}
			if scrubbed["hook_event_name"] != "PostToolUse" {
				t.Errorf("hook_event_name was scrubbed: %v", scrubbed["hook_event_name"])
			}
			if scrubbed["tool_name"] != "Bash" {
				t.Errorf("tool_name was scrubbed: %v", scrubbed["tool_name"])
			}

			// tool_input.description is a free-text field: it must not survive
			// verbatim, only as a length placeholder.
			inputMap := scrubbed["tool_input"].(map[string]any)
			if desc, _ := inputMap["description"].(string); desc == "Run the test suite" {
				t.Errorf("tool_input.description was not scrubbed: %v", desc)
			} else if !strings.HasPrefix(desc, "scrubbed (") {
				t.Errorf("tool_input.description = %q, want a scrubbed(<n> chars) placeholder", desc)
			}
		})
	}
}

func TestDocumentScrubsPromptCwdAndTranscriptPath(t *testing.T) {
	doc := map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "sess_1",
		"cwd":             "/Users/sam/notesapp",
		"transcript_path": "/Users/sam/.claude/projects/x/transcript.jsonl",
		"prompt":          "Please fix the login bug in auth.go, my API key is sk-abc123",
	}
	ctx := newContext()
	got := ctx.document(doc)

	promptOut, _ := got["prompt"].(string)
	if !strings.HasPrefix(promptOut, "scrubbed prompt (") {
		t.Errorf("prompt = %q, want a scrubbed-prompt placeholder", promptOut)
	}
	if strings.Contains(promptOut, "sk-abc123") || strings.Contains(promptOut, "login bug") {
		t.Errorf("prompt placeholder leaked content: %q", promptOut)
	}

	cwdOut, _ := got["cwd"].(string)
	if !strings.HasPrefix(cwdOut, "/scrubbed/") || strings.Contains(cwdOut, "Users") {
		t.Errorf("cwd = %q, want /scrubbed/<hash>", cwdOut)
	}

	if got["transcript_path"] != "/scrubbed/transcript.jsonl" {
		t.Errorf("transcript_path = %v, want /scrubbed/transcript.jsonl", got["transcript_path"])
	}
}

func TestDocumentCwdHashIsStableAndDistinctPerProject(t *testing.T) {
	ctx := newContext()
	a1 := ctx.document(map[string]any{"cwd": "/Users/sam/notesapp"})["cwd"]
	a2 := ctx.document(map[string]any{"cwd": "/Users/sam/notesapp"})["cwd"]
	b := ctx.document(map[string]any{"cwd": "/Users/sam/other-project"})["cwd"]

	if a1 != a2 {
		t.Errorf("same cwd produced different slugs: %v vs %v", a1, a2)
	}
	if a1 == b {
		t.Errorf("different projects produced the same slug: %v", a1)
	}
}

func TestDocumentPathFieldsGetStablePerRunMapping(t *testing.T) {
	ctx := newContext()

	editDoc := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Edit",
		"tool_input": map[string]any{
			"file_path":  "/Users/sam/notesapp/internal/auth.go",
			"old_string": "func Login() bool { return false }",
			"new_string": "func Login() bool { return checkCredentials() }",
		},
	}
	readDoc := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Read",
		"tool_input": map[string]any{
			"file_path": "/Users/sam/notesapp/internal/auth.go", // same file again
		},
	}
	otherDoc := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Read",
		"tool_input": map[string]any{
			"file_path": "/Users/sam/notesapp/internal/config.go",
		},
	}

	editOut := ctx.document(editDoc)
	readOut := ctx.document(readDoc)
	otherOut := ctx.document(otherDoc)

	editPath := editOut["tool_input"].(map[string]any)["file_path"]
	readPath := readOut["tool_input"].(map[string]any)["file_path"]
	otherPath := otherOut["tool_input"].(map[string]any)["file_path"]

	if editPath != readPath {
		t.Errorf("the same original path mapped to two different replacements: %v vs %v", editPath, readPath)
	}
	if editPath == otherPath {
		t.Errorf("two different original paths mapped to the same replacement: %v", editPath)
	}
	if !strings.HasSuffix(editPath.(string), ".go") {
		t.Errorf("path replacement lost the original extension: %v", editPath)
	}
	if strings.Contains(fmt.Sprint(editOut), "/Users/sam") {
		t.Errorf("edited document still contains the real path: %v", editOut)
	}

	// old_string / new_string are file contents: must be length-only.
	inputMap := editOut["tool_input"].(map[string]any)
	oldStr, _ := inputMap["old_string"].(string)
	newStr, _ := inputMap["new_string"].(string)
	if !strings.HasPrefix(oldStr, "scrubbed (") || !strings.HasPrefix(newStr, "scrubbed (") {
		t.Errorf("old_string/new_string were not scrubbed to length placeholders: %q / %q", oldStr, newStr)
	}
}

// TestSafeExtension covers a path's extension only being kept when it
// looks like a genuine short extension and the file isn't
// itself a dotfile (whose "extension", per filepath.Ext, is its whole
// sensitive name).
func TestSafeExtension(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/Users/sam/.aws-prod-credentials", ""},
		{"/Users/sam/.env.acmebank.production", ""},
		{"/Users/sam/ProjectCodenameJuniper.xcodeproj", ".xcodeproj"},
		{"/Users/sam/notesapp/main.go", ".go"},
		// A purely numeric "extension" (no letters) must never survive: it
		// could be a Social Security number fragment or a date, not a real
		// file extension.
		{"/Users/sam/records/ssn.123456789", ""},
		{"/Users/sam/backups/dbdump.20260913", ""},
		{"/Users/sam/notesapp/dist/x.tar.gz", ".gz"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := safeExtension(tc.path); got != tc.want {
				t.Errorf("safeExtension(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestDocumentScrubsPathsWithHardCases drives the same four paths through
// the full document scrubber (not just safeExtension directly), confirming
// the ".xcodeproj" case becomes exactly "f3.xcodeproj" and the two dotfiles
// carry no extension — and, most importantly, that none of the four
// original path text (including the sensitive parts of the dotfile names)
// appears anywhere in the output.
func TestDocumentScrubsPathsWithHardCases(t *testing.T) {
	ctx := newContext()
	paths := []string{
		"/Users/sam/.aws-prod-credentials",
		"/Users/sam/.env.acmebank.production",
		"/Users/sam/ProjectCodenameJuniper.xcodeproj",
		"/Users/sam/notesapp/main.go",
	}

	var got []string
	for i, p := range paths {
		doc := ctx.document(map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Read",
			"tool_input":      map[string]any{"file_path": p},
		})
		mapped := doc["tool_input"].(map[string]any)["file_path"].(string)
		got = append(got, mapped)

		whole := fmt.Sprint(doc)
		if strings.Contains(whole, "aws-prod-credentials") || strings.Contains(whole, "acmebank") ||
			strings.Contains(whole, "Juniper") || strings.Contains(whole, "/Users/sam") {
			t.Errorf("path %d: scrubbed document still leaks the original path: %v", i, doc)
		}
	}

	want := []string{"/scrubbed/f1", "/scrubbed/f2", "/scrubbed/f3.xcodeproj", "/scrubbed/f4.go"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDocumentScrubsPrCreatedAndCommitCommands(t *testing.T) {
	ctx := newContext()

	prDoc := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command": `gh pr create --title "Fix login bug" --body "Closes #42, see /Users/sam/notes.txt"`,
		},
	}
	got := ctx.document(prDoc)
	cmd := got["tool_input"].(map[string]any)["command"]
	if cmd != "gh pr create" {
		t.Errorf("command = %v, want exactly \"gh pr create\"", cmd)
	}

	commitDoc := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input": map[string]any{
			"command": `git commit -m "fix: internal bug in /Users/sam/notesapp/internal/auth.go"`,
		},
	}
	got = ctx.document(commitDoc)
	cmd = got["tool_input"].(map[string]any)["command"]
	if cmd != "git commit" {
		t.Errorf("command = %v, want exactly \"git commit\"", cmd)
	}
}

func TestDocumentUnknownFieldsDefaultToScrubbed(t *testing.T) {
	ctx := newContext()
	doc := map[string]any{
		"hook_event_name":  "SomeFutureEvent",
		"a_future_field":   "this looks like it could be free text or a secret",
		"a_future_number":  42,
		"a_future_boolean": true,
	}
	got := ctx.document(doc)

	if got["a_future_field"] != "scrubbed" {
		t.Errorf("unknown string field was not scrubbed by default: %v", got["a_future_field"])
	}
	if got["a_future_number"] != 42 {
		t.Errorf("unknown numeric field was not preserved: %v", got["a_future_number"])
	}
	if got["a_future_boolean"] != true {
		t.Errorf("unknown boolean field was not preserved: %v", got["a_future_boolean"])
	}
	// hook_event_name is an allow-listed structural field.
	if got["hook_event_name"] != "SomeFutureEvent" {
		t.Errorf("hook_event_name should always survive: %v", got["hook_event_name"])
	}
}

func TestDocumentScrubsMessageField(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "known permission prefix kept, appended detail dropped",
			message: "Claude needs your permission to run: rm -rf /Users/sam/secret-project",
			want:    "Claude needs your permission",
		},
		{
			name:    "known idle prefix kept verbatim",
			message: "Claude is waiting for your input",
			want:    "Claude is waiting for your input",
		},
		{
			name:    "unrecognized message defaults to a length placeholder",
			message: "Something Claude Code never told us about happened at /Users/sam/project",
			want:    "scrubbed (",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newContext()
			got := ctx.document(map[string]any{"hook_event_name": "Notification", "message": tc.message})
			msg, _ := got["message"].(string)
			if !strings.HasPrefix(msg, tc.want) {
				t.Errorf("message = %q, want prefix %q", msg, tc.want)
			}
			if strings.Contains(msg, "/Users/") || strings.Contains(msg, "secret") {
				t.Errorf("message leaked content: %q", msg)
			}
		})
	}
}

func TestDocumentScrubsMatcherField(t *testing.T) {
	cases := []struct {
		name    string
		matcher string
		want    string
	}{
		{"empty matcher (BR-08) kept", "", ""},
		{"known tool name kept", "Bash", "Bash"},
		{"another known tool name kept", "MultiEdit", "MultiEdit"},
		{"an operator-authored regex is scrubbed", "Edit|Write|.*secret.*", "scrubbed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newContext()
			got := ctx.document(map[string]any{"hook_event_name": "PreToolUse", "matcher": tc.matcher})
			if got["matcher"] != tc.want {
				t.Errorf("matcher = %v, want %v", got["matcher"], tc.want)
			}
		})
	}
}

// TestDocumentScrubsToolNameField covers the "tool_name" allow-list.
func TestDocumentScrubsToolNameField(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		want     string
	}{
		{"built-in tool kept verbatim", "Bash", "Bash"},
		{"another built-in tool kept verbatim", "WebFetch", "WebFetch"},
		{"an MCP tool becomes a constant placeholder", "mcp__filesystem__read_file", "mcp__scrubbed"},
		{"an MCP tool naming a customer becomes the same placeholder", "mcp__acme_bank_internal__query", "mcp__scrubbed"},
		{"an unrecognized future tool defaults to scrubbed", "SomeFutureTool", "scrubbed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newContext()
			got := ctx.document(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tc.toolName})
			if got["tool_name"] != tc.want {
				t.Errorf("tool_name = %v, want %v", got["tool_name"], tc.want)
			}
			if strings.Contains(fmt.Sprint(got), "acme_bank") || strings.Contains(fmt.Sprint(got), "filesystem") {
				t.Errorf("tool_name leaked the MCP server/tool name: %v", got)
			}
		})
	}
}

func TestScrubDirEndToEnd(t *testing.T) {
	inDir := t.TempDir()
	outDir := t.TempDir()

	docs := []map[string]any{
		{
			"hook_event_name": "SessionStart",
			"session_id":      "sess_1",
			"cwd":             "/Users/sam/notesapp",
			"transcript_path": "/Users/sam/.claude/projects/x/t.jsonl",
			"source":          "startup",
		},
		postToolUseFixture(
			"/Users/sam/notesapp",
			"pytest -q",
			"collected 3 items\n========================= 3 passed in 0.50s =========================",
			"",
		),
	}
	for i, d := range docs {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal fixture %d: %v", i, err)
		}
		name := fmt.Sprintf("%03d-%s.json", i+1, d["hook_event_name"])
		if err := os.WriteFile(filepath.Join(inDir, name), raw, 0o600); err != nil {
			t.Fatalf("writing fixture %d: %v", i, err)
		}
	}

	report, err := ScrubDir(inDir, outDir)
	if err != nil {
		t.Fatalf("ScrubDir: %v", err)
	}
	if report.FilesScrubbed != len(docs) {
		t.Errorf("FilesScrubbed = %d, want %d", report.FilesScrubbed, len(docs))
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("reading outDir: %v", err)
	}
	if len(entries) != len(docs) {
		t.Fatalf("outDir has %d files, want %d", len(entries), len(docs))
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(outDir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if strings.Contains(string(data), "/Users/") {
			t.Errorf("%s still contains an absolute path:\n%s", e.Name(), data)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if len(line) > 200 {
				t.Errorf("%s has a line over 200 characters", e.Name())
			}
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(outDir, entries[1].Name())), "3 passed") {
		t.Errorf("scrubbed fixture lost the pytest summary line")
	}
}

func TestScrubDirSelfCheckCatchesALeak(t *testing.T) {
	inDir := t.TempDir()
	outDir := t.TempDir()

	// A field name the scrubber does not know about, carrying a value that
	// happens to already look scrubbed-adjacent but is actually a raw path
	// embedded where the scrubber cannot see it: this simulates a would-be
	// leak by writing directly into outDir after a normal scrub, standing
	// in for "a future bug reintroduces a raw value".
	doc := map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "sess_1",
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(inDir, "001-SessionStart.json"), raw, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := ScrubDir(inDir, outDir); err != nil {
		t.Fatalf("ScrubDir: %v", err)
	}

	// Now plant a leak directly, as if scrubbing had a bug, and confirm the
	// self-check function itself detects it.
	leaky := filepath.Join(outDir, "002-Leaky.json")
	if err := os.WriteFile(leaky, []byte(`{"note":"/Users/sam/secret"}`), 0o600); err != nil {
		t.Fatalf("writing leaky file: %v", err)
	}
	ctx := newContext()
	if err := ctx.selfCheck(outDir, outDir); err == nil {
		t.Fatal("expected selfCheck to fail on a planted /Users/ leak")
	} else if !strings.Contains(err.Error(), "002-Leaky.json") {
		t.Errorf("selfCheck error does not name the offending file: %v", err)
	}
}

// TestSelfCheckNamesTheInputFileNotTheTempPath covers: selfCheck is
// called by ScrubDir with a scratch (temp) directory to read
// from but the original input directory to report — because the scratch
// directory is deleted the instant self-check fails, so a message pointing
// at it would send the operator to a path that no longer exists, while the
// input file is stable and still there.
func TestSelfCheckNamesTheInputFileNotTheTempPath(t *testing.T) {
	scratchDir := t.TempDir() // stands in for ScrubDir's deleted tempOut
	reportDir := t.TempDir()  // stands in for ScrubDir's inDir

	leaky := filepath.Join(scratchDir, "001-Leaky.json")
	if err := os.WriteFile(leaky, []byte(`{"note":"/Users/sam/secret"}`), 0o600); err != nil {
		t.Fatalf("writing leaky file: %v", err)
	}

	ctx := newContext()
	err := ctx.selfCheck(scratchDir, reportDir)
	if err == nil {
		t.Fatal("expected selfCheck to fail on a planted /Users/ leak")
	}
	if strings.Contains(err.Error(), scratchDir) {
		t.Errorf("selfCheck error names the (about to be deleted) scratch path:\n%v", err)
	}
	wantPath := filepath.Join(reportDir, "001-Leaky.json")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("selfCheck error does not name the input file %s:\n%v", wantPath, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// TestSelfCheckDetectsExpandedPatterns covers the self-check's
// forbidden-substring and generic-path-shape coverage, and confirms
// scrub's own "/scrubbed/..." placeholders don't
// trip the generic path heuristic (they'd otherwise look exactly like a
// two-segment path ending in a file extension).
func TestSelfCheckDetectsExpandedPatterns(t *testing.T) {
	cases := []struct {
		name          string
		content       string
		wantViolation bool
	}{
		{"home tilde path", "config lives at ~/dotfiles/config.yaml", true},
		{"windows backslash Users path", `see \Users\sam\config`, true},
		{"windows drive path", `C:\Windows\System32\config.sys`, true},
		{"file url", "file:///Users/sam/project/file.txt", true},
		{"opt path", "installed under /opt/homebrew/bin/go", true},
		{"var path", "logs at /var/log/system.log", true},
		{"workspaces path", "mounted at /workspaces/project/src", true},
		{"generic two-segment path with extension", "look in src/internal/auth.go for the bug", true},
		{"our own scrubbed file placeholder is not a false positive", "/scrubbed/f1.py", false},
		{"our own transcript placeholder is not a false positive", "/scrubbed/transcript.jsonl", false},
		{"our own cwd hash placeholder is not a false positive", "/scrubbed/a1b2c3d4", false},
		{"safe short text", "3 passed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := map[string]any{"note": tc.content}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "001-Test.json"), raw, 0o600); err != nil {
				t.Fatalf("writing: %v", err)
			}
			ctx := newContext()
			err = ctx.selfCheck(dir, dir)
			if tc.wantViolation && err == nil {
				t.Errorf("expected selfCheck to flag %q, but it passed", tc.content)
			}
			if !tc.wantViolation && err != nil {
				t.Errorf("selfCheck false-positived on %q: %v", tc.content, err)
			}
		})
	}
}

// TestSelfCheckMeasuresDecodedContentLinesNotRawFileLines covers many
// short lines ("ok", 2 characters, well under the 200-char
// cap) JSON-escape onto a single long raw file line (each "\n" becomes the
// two characters backslash-n). A check that measured raw file lines would
// wrongly flag this; decoding the JSON string first and measuring its real
// lines must not.
func TestSelfCheckMeasuresDecodedContentLinesNotRawFileLines(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("ok\n", 100) // ~400 chars once JSON-escaped onto one line; each real line is 2 chars
	doc := map[string]any{"stdout": content}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-Test.json"), raw, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	ctx := newContext()
	if err := ctx.selfCheck(dir, dir); err != nil {
		t.Errorf("selfCheck false-positived on short decoded lines that only look long once JSON-escaped: %v", err)
	}
}

// TestSelfCheckCatchesOverlongDecodedContentLine is the flip side: a
// genuinely long line inside a decoded string value must still be caught,
// wherever it falls inside a multi-line value.
func TestSelfCheckCatchesOverlongDecodedContentLine(t *testing.T) {
	dir := t.TempDir()
	longLine := strings.Repeat("x", 250)
	doc := map[string]any{"stdout": "short\n" + longLine + "\nshort again"}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "001-Test.json"), raw, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	ctx := newContext()
	err = ctx.selfCheck(dir, dir)
	if err == nil {
		t.Fatal("expected selfCheck to catch the overlong decoded content line")
	}
	if !strings.Contains(err.Error(), "250") {
		t.Errorf("error does not report the actual length: %v", err)
	}
}

// TestScrubDirLeavesNoTempDirOrPartialOutputOnFailure proves ScrubDir
// deletes its temp output and leaves nothing partial when a run fails
// early (here, on unparseable input); the self-check-failure path is
// exercised separately above.
func TestScrubDirLeavesNoTempDirOrPartialOutputOnFailure(t *testing.T) {
	inDir := t.TempDir()
	outParent := t.TempDir()
	outDir := filepath.Join(outParent, "scenario")

	good, err := json.Marshal(map[string]any{"hook_event_name": "SessionStart"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inDir, "001-Good.json"), good, 0o600); err != nil {
		t.Fatalf("writing good fixture: %v", err)
	}
	// A JSON array, not an object: ScrubDir must fail on this file, after
	// having already written 001-Good.json's scrubbed copy into its
	// temporary output directory.
	if err := os.WriteFile(filepath.Join(inDir, "002-Bad.json"), []byte(`[1,2,3]`), 0o600); err != nil {
		t.Fatalf("writing bad fixture: %v", err)
	}

	if _, err := ScrubDir(inDir, outDir); err == nil {
		t.Fatal("expected ScrubDir to fail on a non-object JSON file")
	}

	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("outDir should not exist after a failed scrub, stat error = %v", err)
	}

	leftover, err := filepath.Glob(filepath.Join(outParent, ".agentpulse-scrub-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftover) != 0 {
		t.Errorf("temp output directory was not cleaned up: %v", leftover)
	}
}

// TestScrubDirReplacesOutDirWholesaleOnSuccess confirms ScrubDir's
// move-into-place step replaces outDir outright rather than
// merging into it: a stale file from an earlier run must not survive a
// fresh, successful scrub that doesn't produce it again.
//
// (TestSelfCheckDetectsExpandedPatterns and TestScrubDirSelfCheckCatchesALeak
// exercise the self-check's failure-detection logic directly;
// TestScrubDirLeavesNoTempDirOrPartialOutputOnFailure above exercises the
// same temp-dir-cleanup code path via a different, reachable failure.)
func TestScrubDirReplacesOutDirWholesaleOnSuccess(t *testing.T) {
	inDir := t.TempDir()
	outParent := t.TempDir()
	outDir := filepath.Join(outParent, "scenario")

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(outDir, "001-Old.json")
	if err := os.WriteFile(stale, []byte(`{"hook_event_name":"Stop"}`), 0o600); err != nil {
		t.Fatalf("writing stale file: %v", err)
	}

	doc := map[string]any{"hook_event_name": "SessionStart"}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inDir, "001-New.json"), raw, 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := ScrubDir(inDir, outDir); err != nil {
		t.Fatalf("ScrubDir: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("expected the stale output to be replaced wholesale, but 001-Old.json still exists")
	}
	if _, err := os.Stat(filepath.Join(outDir, "001-New.json")); err != nil {
		t.Errorf("expected the new scrubbed output to exist: %v", err)
	}
}
