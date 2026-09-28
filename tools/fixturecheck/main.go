// Command fixturecheck is the "make fixtures" tool (SPEC 9.4): it
// replays every scenario under testdata/fixtures/ through
// the real, compiled agentpulse binary — feeding each recorded (or, for
// now, hand-authored synthetic) hook document to "agentpulse hook" on
// stdin, in order, against one temporary $XDG_STATE_HOME/$XDG_CONFIG_HOME
// per scenario — and compares the resulting spool to that scenario's
// expected.json.
//
// Every scenario is replayed twice (ADR-005, card P5-18): once as the
// bridge normally runs, with subagent hooks classified as their own chains
// (compared to "subagent_events" when the scenario has them, otherwise to
// "events"), and once with subagent classification paused the way the
// flush fallback pauses it (compared to "events", the pre-subagent
// baseline, with no event allowed to carry `subagent`).
//
// This is a development tool, not part of the shipped bridge (BR-20's
// GoReleaser config builds only ./cmd/agentpulse): it lives under tools/
// rather than cmd/ to keep that distinction visible.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// expectedEvent is one entry in expected.json's "events" array (see
// testdata/fixtures/README.md). Payload is matched as a subset: a
// field present here must equal the actual event's field; a field the
// actual event has but expected.json omits is not checked.
//
// Subagent, unlike Payload, is strict: an expected event without it
// requires the actual event to have none.
type expectedEvent struct {
	Type     string         `json:"type"`
	Payload  map[string]any `json:"payload"`
	Subagent map[string]any `json:"subagent"`
}

// expected is one scenario's hand-authored answer key. Of its four fields,
// this tool enforces Events (compareEvents) and, when present, FinalState
// (computeFinalState, final_state.go) — the two fields a bridge-only tool
// can actually check from the classifier's own output. RecapOK and
// Synthetic are carried through as metadata only: RecapOK records a human
// reviewer's judgment call about generated recap text (SPEC 9.3), which is
// relay-rendered and reviewed by a human, not something this tool
// computes. Synthetic just distinguishes a hand-authored scenario from a
// recorded one, for later bookkeeping.
type expected struct {
	Events         []expectedEvent `json:"events"`
	SubagentEvents []expectedEvent `json:"subagent_events"`
	FinalState     string          `json:"final_state"`
	RecapOK        bool            `json:"recap_ok"`
	Synthetic      bool            `json:"synthetic"`
}

// actualEvent is the shape this tool needs from a spooled event line: just
// enough to compare against expectedEvent, without importing
// internal/classify's concrete payload types (which would require a type
// switch per event type here for no benefit — a generic map compares just
// as well for this purpose).
type actualEvent struct {
	Type     string         `json:"type"`
	Payload  map[string]any `json:"payload"`
	Subagent map[string]any `json:"subagent"`
}

var hookFilePattern = regexp.MustCompile(`^\d+-[A-Za-z]+\.json$`)

func main() {
	binPath := flag.String("bin", "bin/agentpulse", "path to the compiled agentpulse binary")
	fixturesDir := flag.String("fixtures", "testdata/fixtures", "path to the fixture corpus directory")
	flag.Parse()

	abs, err := filepath.Abs(*binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixturecheck: resolving %s: %v\n", *binPath, err)
		os.Exit(1)
	}
	if _, err := os.Stat(abs); err != nil {
		fmt.Fprintf(os.Stderr, "fixturecheck: %s not found (run \"make build\" first): %v\n", abs, err)
		os.Exit(1)
	}

	scenarios, err := listScenarios(*fixturesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixturecheck: %v\n", err)
		os.Exit(1)
	}
	if len(scenarios) == 0 {
		fmt.Fprintf(os.Stderr, "fixturecheck: no scenarios found under %s\n", *fixturesDir)
		os.Exit(1)
	}

	failed := 0
	for _, name := range scenarios {
		dir := filepath.Join(*fixturesDir, name)
		if err := runScenario(abs, dir); err != nil {
			fmt.Printf("FAIL %s: %v\n", name, err)
			failed++
			continue
		}
		fmt.Printf("PASS %s\n", name)
	}

	fmt.Printf("\n%d/%d scenarios passed\n", len(scenarios)-failed, len(scenarios))
	if failed > 0 {
		os.Exit(1)
	}
}

// listScenarios returns the scenario directory names under fixturesDir,
// sorted: every subdirectory that contains an expected.json (skipping
// README.md and anything else that isn't a scenario).
func listScenarios(fixturesDir string) ([]string, error) {
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", fixturesDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(fixturesDir, e.Name(), "expected.json")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// runScenario replays one scenario's hook documents through binPath, in
// both modes, and compares each resulting spool against its expected.json.
func runScenario(binPath, dir string) error {
	exp, err := loadExpected(filepath.Join(dir, "expected.json"))
	if err != nil {
		return fmt.Errorf("loading expected.json: %w", err)
	}

	files, err := hookFiles(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no NNN-<hook>.json files found")
	}

	off, err := replay(binPath, files, true)
	if err != nil {
		return fmt.Errorf("subagents paused: %w", err)
	}
	if err := compareEvents(exp.Events, off); err != nil {
		return fmt.Errorf("subagents paused: %w", err)
	}
	if exp.FinalState != "" {
		if got := computeFinalState(off); got != exp.FinalState {
			return fmt.Errorf("subagents paused: final_state = %q, want %q", got, exp.FinalState)
		}
	}

	on, err := replay(binPath, files, false)
	if err != nil {
		return err
	}
	want := exp.Events
	if exp.SubagentEvents != nil {
		want = exp.SubagentEvents
	}
	if err := compareEvents(want, on); err != nil {
		return err
	}
	if exp.FinalState != "" {
		// The session's own state follows its main chain only (D71).
		var mainChain []actualEvent
		for _, ev := range on {
			if ev.Subagent == nil {
				mainChain = append(mainChain, ev)
			}
		}
		if got := computeFinalState(mainChain); got != exp.FinalState {
			return fmt.Errorf("final_state = %q, want %q", got, exp.FinalState)
		}
	}
	return nil
}

// replay feeds files to "binPath hook" in order against a fresh state and
// config directory and returns the spooled events. With pauseSubagents it
// first writes the state.json the flush fallback writes (ADR-005 section
// 8), so the hook classifies subagent hooks as it did before subagents
// were recognized.
func replay(binPath string, files []string, pauseSubagents bool) ([]actualEvent, error) {
	stateDir, err := os.MkdirTemp("", "agentpulse-fixture-state-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp state dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stateDir) }()
	configDir, err := os.MkdirTemp("", "agentpulse-fixture-config-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp config dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(configDir) }()
	if pauseSubagents {
		if err := writePausedState(filepath.Join(stateDir, "agentpulse")); err != nil {
			return nil, err
		}
	}

	env := append(os.Environ(),
		"XDG_STATE_HOME="+stateDir,
		"XDG_CONFIG_HOME="+configDir,
		"AGENTPULSE_DEBUG=0",
	)
	// AGENTPULSE_RECORD_DIR must never be set here: fixturecheck replays
	// through "agentpulse hook" exactly as Claude Code would invoke it in
	// production, and recording is a dev-only capability of a separate
	// build (hook_dev.go/hook_release.go) this binary may or may not even
	// have compiled in.

	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // fixture path from the repo's own testdata
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		cmd := exec.Command(binPath, "hook") //nolint:gosec // binPath is our own just-built binary
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(raw)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("running %s hook on %s: %w (stderr: %s)", binPath, filepath.Base(f), err, stderr.String())
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			return nil, fmt.Errorf("%s printed output (violates BR-02/BR-04): stdout=%q stderr=%q", filepath.Base(f), stdout.String(), stderr.String())
		}
	}

	return readSpool(filepath.Join(stateDir, "agentpulse", "spool.ndjson"))
}

// writePausedState writes a state.json whose subagents_off_until is an
// hour from now, far longer than any replay takes.
func writePausedState(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	doc, err := json.Marshal(map[string]string{"subagents_off_until": until})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), doc, 0o600)
}

func loadExpected(path string) (expected, error) {
	var e expected
	data, err := os.ReadFile(path) //nolint:gosec // fixture path from the repo's own testdata
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return e, err
	}
	return e, nil
}

// hookFiles returns dir's "NNN-<hook>.json" files, sorted by name (which
// sorts by counter since NNN is zero-padded).
func hookFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !hookFilePattern.MatchString(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

// readSpool parses every line of the spool.ndjson at path (missing file =
// no events, a valid outcome for a scenario that emits nothing).
func readSpool(path string) ([]actualEvent, error) {
	f, err := os.Open(path) //nolint:gosec // path built from our own temp state dir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening spool %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var events []actualEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev actualEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("spool line is not valid JSON: %w (%q)", err, string(line))
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading spool %s: %w", path, err)
	}
	return events, nil
}

// compareEvents checks that actual matches want event-for-event, in
// order: same count, same type, and — per
// testdata/fixtures/README.md — every payload field want lists must
// equal the corresponding actual field; a field actual has that want
// doesn't mention is not checked.
func compareEvents(want []expectedEvent, actual []actualEvent) error {
	if len(want) != len(actual) {
		return fmt.Errorf("got %d events, want %d\n  got:  %s\n  want: %s",
			len(actual), len(want), summarizeActual(actual), summarizeExpected(want))
	}
	for i := range want {
		if actual[i].Type != want[i].Type {
			return fmt.Errorf("event %d: type = %q, want %q", i, actual[i].Type, want[i].Type)
		}
		if !jsonEqual(actual[i].Subagent, want[i].Subagent) {
			return fmt.Errorf("event %d (%s): subagent = %v, want %v", i, want[i].Type, actual[i].Subagent, want[i].Subagent)
		}
		for key, wantVal := range want[i].Payload {
			gotVal, ok := actual[i].Payload[key]
			if !ok {
				return fmt.Errorf("event %d (%s): payload missing field %q, want %v", i, want[i].Type, key, wantVal)
			}
			if !jsonEqual(gotVal, wantVal) {
				return fmt.Errorf("event %d (%s): payload.%s = %v, want %v", i, want[i].Type, key, gotVal, wantVal)
			}
		}
	}
	return nil
}

// jsonEqual compares two values decoded from JSON via encoding/json into
// `any` (so numbers on both sides are float64, making a direct comparison
// safe without a numeric-precision special case).
func jsonEqual(a, b any) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(aj) == string(bj)
}

func summarizeActual(events []actualEvent) string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return "[" + strings.Join(types, ", ") + "]"
}

func summarizeExpected(events []expectedEvent) string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return "[" + strings.Join(types, ", ") + "]"
}
