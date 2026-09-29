package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// hooksOnlyMachine is a machine paired before the subagent hooks existed:
// a config.json with a bridge id, and a settings file holding the eight
// original entries plus a hook of the user's own, listed after this
// bridge's entry in PreToolUse. Any request to the relay fails the test.
func hooksOnlyMachine(t *testing.T) (settingsPath string, before []byte) {
	t.Helper()
	home := pairingHome(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("--hooks-only contacted the relay: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(relay.Close)
	t.Setenv("AGENTPULSE_RELAY", relay.URL)

	if err := config.Save(xdgpaths.ConfigPath(), config.Config{BridgeID: "brg_existing", DeviceName: "Sam's iPhone"}); err != nil {
		t.Fatal(err)
	}
	binary, err := currentBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	type spec struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	type group struct {
		Matcher string `json:"matcher"`
		Hooks   []spec `json:"hooks"`
	}
	byEvent := map[string][]group{}
	for _, event := range claudehooks.BR08Events[:8] {
		byEvent[event] = []group{{Hooks: []spec{{"command", hookCommand(binary), 10}}}}
	}
	byEvent["PreToolUse"] = append(byEvent["PreToolUse"], group{Matcher: "Bash", Hooks: []spec{{Type: "command", Command: "my-linter --check"}}})
	before, err = json.MarshalIndent(map[string]any{"model": "opus", "hooks": byEvent}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	settingsPath = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	return settingsPath, before
}

func runPairCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newPairCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func hooksObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing settings: %v\n%s", err, data)
	}
	var h map[string]json.RawMessage
	if err := json.Unmarshal(doc["hooks"], &h); err != nil {
		t.Fatalf("parsing hooks: %v", err)
	}
	if string(doc["model"]) != `"opus"` {
		t.Errorf("model = %s, want the user's setting kept", doc["model"])
	}
	return h
}

func TestHooksOnlyAddsExactlyTheMissingSubagentHooks(t *testing.T) {
	settingsPath, before := hooksOnlyMachine(t)
	configBefore, _ := os.ReadFile(xdgpaths.ConfigPath())

	out, err := runPairCmd(t, "", "--hooks-only", "--yes")
	if err != nil {
		t.Fatalf("pair --hooks-only --yes: %v\n%s", err, out)
	}
	after, _ := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	was, now := hooksObject(t, before), hooksObject(t, after)
	if len(now) != len(was)+2 {
		t.Errorf("settings has %d hook events, want %d (two added)", len(now), len(was)+2)
	}
	for event, raw := range was {
		var a, b bytes.Buffer
		_ = json.Compact(&a, raw)
		_ = json.Compact(&b, now[event])
		if a.String() != b.String() {
			t.Errorf("%s changed:\nbefore %s\nafter  %s", event, a.String(), b.String())
		}
	}
	hooksByEvent := settingsHooks(t, settingsPath)
	for _, event := range []string{"SubagentStart", "SubagentStop"} {
		g := hooksByEvent[event]
		if len(g) != 1 || len(g[0].Hooks) != 1 || g[0].Matcher != "" || g[0].Hooks[0].Timeout != 10 ||
			!strings.HasSuffix(g[0].Hooks[0].Command, " hook") {
			t.Errorf("%s = %+v, want one BR-08 entry", event, g)
		}
		if !regexp.MustCompile(`(?m)^\+\s+"` + event + `": \[$`).MatchString(out) {
			t.Errorf("diff does not show %s being added:\n%s", event, out)
		}
	}
	if strings.Count(out, "\n+") < 2 || strings.Contains(out, "\n-") {
		t.Errorf("diff should only add lines:\n%s", out)
	}
	if !strings.Contains(out, "your pairing is unchanged") {
		t.Errorf("output = %q, want the closing line", out)
	}
	if configAfter, _ := os.ReadFile(xdgpaths.ConfigPath()); !bytes.Equal(configBefore, configAfter) {
		t.Error("config.json changed; --hooks-only must leave the pairing alone")
	}
	if _, err := os.Stat(xdgpaths.CredentialsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a credentials file appeared (%v); --hooks-only must not touch the credential", err)
	}

	// A second run finds everything registered and writes nothing.
	out, err = runPairCmd(t, "", "--hooks-only", "--yes")
	if err != nil || !strings.Contains(out, "already registered") {
		t.Errorf("second run = %q, %v; want the already-registered message", out, err)
	}
	if again, _ := os.ReadFile(settingsPath); !bytes.Equal(again, after) { //nolint:gosec // test-controlled path
		t.Error("second run changed the settings file")
	}
}

func TestHooksOnlyAsksFirst(t *testing.T) {
	settingsPath, before := hooksOnlyMachine(t)
	out, err := runPairCmd(t, "n\n", "--hooks-only")
	if err != nil {
		t.Fatalf("declined run: %v", err)
	}
	if !strings.Contains(out, "Proceed? [y/N]") || !strings.Contains(out, "SubagentStart") {
		t.Errorf("output = %q, want the diff and the question", out)
	}
	if after, _ := os.ReadFile(settingsPath); !bytes.Equal(after, before) { //nolint:gosec // test-controlled path
		t.Error("settings written after the user said no (D29)")
	}
}

func TestHooksOnlyOnAnUnpairedMachine(t *testing.T) {
	home := pairingHome(t)
	out, err := runPairCmd(t, "", "--hooks-only", "--yes")
	if !errors.Is(err, errAlreadyReported) {
		t.Errorf("err = %v, want errAlreadyReported (exit 1)", err)
	}
	if strings.TrimSpace(out) != "This machine is not paired. Run `agentpulse pair`." {
		t.Errorf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("settings.json was created on an unpaired machine")
	}
}

// TestHooksOnlyRepointsAStaleCellarPath is P5-27's Homebrew-upgrade case:
// a machine paired against a versioned Cellar path a `brew upgrade` since
// deleted. hooks.Owner still recognizes that stale path as this bridge's
// own (any absolute path whose base name is "agentpulse"), so
// --hooks-only repoints every entry to the currently running binary's
// path rather than leaving it alone or treating it as someone else's
// hook.
func TestHooksOnlyRepointsAStaleCellarPath(t *testing.T) {
	home := pairingHome(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("--hooks-only contacted the relay: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(relay.Close)
	t.Setenv("AGENTPULSE_RELAY", relay.URL)

	if err := config.Save(xdgpaths.ConfigPath(), config.Config{BridgeID: "brg_existing", DeviceName: "Sam's iPhone"}); err != nil {
		t.Fatal(err)
	}
	const staleCellarPath = "/opt/homebrew/Cellar/agentpulse/1.0.0/bin/agentpulse"
	type spec struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout,omitempty"`
	}
	type group struct {
		Matcher string `json:"matcher"`
		Hooks   []spec `json:"hooks"`
	}
	byEvent := map[string][]group{}
	for _, event := range claudehooks.BR08Events {
		byEvent[event] = []group{{Hooks: []spec{{"command", hookCommand(staleCellarPath), 10}}}}
	}
	before, err := json.MarshalIndent(map[string]any{"hooks": byEvent}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, before, 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runPairCmd(t, "", "--hooks-only", "--yes")
	if err != nil {
		t.Fatalf("pair --hooks-only --yes: %v\n%s", err, out)
	}

	binary, err := currentBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	hooksByEvent := settingsHooks(t, settingsPath)
	for _, event := range claudehooks.BR08Events {
		g := hooksByEvent[event]
		if len(g) != 1 || len(g[0].Hooks) != 1 {
			t.Fatalf("%s = %+v, want exactly one group with one command", event, g)
		}
		if got := g[0].Hooks[0].Command; got != hookCommand(binary) {
			t.Errorf("%s command = %q, want the repointed %q", event, got, hookCommand(binary))
		}
		if strings.Contains(g[0].Hooks[0].Command, staleCellarPath) {
			t.Errorf("%s still names the deleted Cellar path %q", event, staleCellarPath)
		}
	}
	if !strings.Contains(out, "Done. All") {
		t.Errorf("output = %q, want the completion message", out)
	}
}

func TestHooksOnlyRejectsPairingFlags(t *testing.T) {
	pairingHome(t)
	for _, args := range [][]string{
		{"--hooks-only", "--relay", "https://relay.example"},
		{"--hooks-only", "--qr-invert"},
	} {
		if _, err := runPairCmd(t, "", args...); err == nil || !strings.Contains(err.Error(), "hooks-only") {
			t.Errorf("pair %v: err = %v, want a flag conflict", args, err)
		}
	}
}
