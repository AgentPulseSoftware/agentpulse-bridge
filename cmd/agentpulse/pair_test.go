package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// testSecret is the secret the fake relay hands out. No test ever writes
// it to a real credential store: every one of them injects fakeCredStore
// and additionally forces internal/cred's own Default() to the file
// fallback inside a temporary XDG_CONFIG_HOME, so neither the macOS
// Keychain nor a Linux Secret Service is ever touched.
const testSecret = "s3cr3t-not-a-real-secret"

// pairingRelay is a fake relay covering the two endpoints pair uses and
// the one unpair uses, recording what it saw.
type pairingRelay struct {
	*httptest.Server
	createdName string
	createdOS   string
	createdArch string
	revokedAuth string
	revokes     int
	bridgeID    string
	deviceName  string
}

func newPairingRelay(t *testing.T, bridgeID, deviceName string) *pairingRelay {
	t.Helper()
	r := &pairingRelay{bridgeID: bridgeID, deviceName: deviceName}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/bridges", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Name    string `json:"name"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
			Version string `json:"bridge_version"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("decoding POST /v1/bridges: %v", err)
		}
		r.createdName, r.createdOS, r.createdArch = body.Name, body.OS, body.Arch
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"bridge_id":"` + r.bridgeID + `","bridge_secret":"` + testSecret +
			`","pairing_code":"7KMPQ2VZ","expires_at":"2099-01-01T00:00:00Z"}`))
	})
	mux.HandleFunc("GET /v1/bridges/me/pairing", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{"paired":true,"device_name":"` + r.deviceName + `"}`))
	})
	mux.HandleFunc("DELETE /v1/bridges/me", func(w http.ResponseWriter, req *http.Request) {
		r.revokes++
		r.revokedAuth = req.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})
	r.Server = httptest.NewServer(mux)
	t.Cleanup(r.Close)
	return r
}

// pairingHome sets up an isolated machine: a temporary $HOME for
// ~/.claude/settings.json and temporary XDG directories for the bridge's
// own config and state.
func pairingHome(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("AGENTPULSE_TEST_CRED_BACKEND", "file")
	return home
}

// testCmd is a cobra.Command wired to buffers, standing in for the real
// one so applyPlan and confirm can be driven without a terminal.
func testCmd(stdin string) (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd, &out
}

func settingsHooks(t *testing.T, path string) map[string][]struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
} {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v\n%s", path, err, data)
	}
	return doc.Hooks
}

// TestPairRegistersTheEightBR08Entries is the happy path: pair
// --yes against a fake relay writes exactly the eight BR-08 entries with
// an empty matcher and a 10 second timeout, stores the secret, and
// records the identity in config.json.
func TestPairRegistersTheEightBR08Entries(t *testing.T) {
	home := pairingHome(t)
	relay := newPairingRelay(t, "brg_new", "Sam's iPhone")
	store := &fakeCredStore{}
	cmd, out := testCmd("")

	if err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, yes: true, credStore: store,
	}); err != nil {
		t.Fatalf("runPair: %v", err)
	}

	hooksByEvent := settingsHooks(t, filepath.Join(home, ".claude", "settings.json"))
	if len(hooksByEvent) != len(claudehooks.BR08Events) {
		t.Fatalf("settings has %d hook events, want the %d BR-08 events: %v", len(hooksByEvent), len(claudehooks.BR08Events), hooksByEvent)
	}
	binary, err := currentBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range claudehooks.BR08Events {
		groups, ok := hooksByEvent[event]
		if !ok {
			t.Errorf("no hook registered for %s", event)
			continue
		}
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Errorf("%s has %d group(s), want exactly one with one command", event, len(groups))
			continue
		}
		if groups[0].Matcher != "" {
			t.Errorf("%s matcher = %q, want empty (BR-08)", event, groups[0].Matcher)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != hookCommand(binary) || h.Timeout != 10 {
			t.Errorf("%s entry = %+v, want type command, %q, timeout 10", event, h, hookCommand(binary))
		}
	}

	if store.secret != testSecret {
		t.Errorf("stored secret = %q, want the one the relay minted", store.secret)
	}
	cfg, err := config.Load(xdgpaths.ConfigPath())
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.BridgeID != "brg_new" || cfg.DeviceName != "Sam's iPhone" || cfg.PairedAt == "" {
		t.Errorf("config = %+v, want the new bridge id, the phone's name, and a paired_at", cfg)
	}

	printed := out.String()
	if strings.Contains(printed, testSecret) {
		t.Error("the bridge secret was printed (SEC-02)")
	}
	if !strings.Contains(printed, "7KMPQ2VZ") {
		t.Error("the pairing code was not printed as text (BR-07)")
	}
	if !strings.Contains(printed, "█") && !strings.Contains(printed, "▀") {
		t.Error("no QR code was printed (BR-07)")
	}
	if !strings.Contains(printed, "Sam's iPhone") {
		t.Error("the closing summary does not name the phone")
	}
	if relay.createdOS == "" || relay.createdArch == "" || relay.createdName == "" {
		t.Errorf("POST /v1/bridges body was incomplete: name=%q os=%q arch=%q", relay.createdName, relay.createdOS, relay.createdArch)
	}
}

// TestPairPreservesAnotherToolsHook is the settings-merge rule at the
// command level: BR-07 preserves all existing settings and hooks.
func TestPairPreservesAnotherToolsHook(t *testing.T) {
	home := pairingHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	original := `{
  "model": "claude-sonnet-4",
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "notify-send done", "timeout": 3}
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	relay := newPairingRelay(t, "brg_new", "iPhone")
	cmd, out := testCmd("")
	if err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, yes: true, credStore: &fakeCredStore{},
	}); err != nil {
		t.Fatalf("runPair: %v", err)
	}

	data, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model": "claude-sonnet-4"`) {
		t.Errorf("an unrelated top-level setting was lost:\n%s", data)
	}
	if !strings.Contains(string(data), "notify-send done") {
		t.Errorf("another tool's hook was lost:\n%s", data)
	}
	stop := settingsHooks(t, settingsPath)["Stop"]
	if len(stop) != 2 {
		t.Errorf("Stop has %d groups, want the other tool's plus ours", len(stop))
	}
}

// TestPairAbortsOnInvalidSettingsJSON is ERR-07: the message names the
// file and the line, nothing is written to settings, and the pairing
// itself still stands.
func TestPairAbortsOnInvalidSettingsJSON(t *testing.T) {
	home := pairingHome(t)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	broken := "{\n  \"model\": \"claude\",\n  \"hooks\": { oops\n}\n"
	if err := os.WriteFile(settingsPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	relay := newPairingRelay(t, "brg_new", "iPhone")
	store := &fakeCredStore{}
	cmd, out := testCmd("")

	err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, yes: true, credStore: store,
	})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runPair error = %v, want the already-reported sentinel (exit 1)", err)
	}

	printed := out.String()
	if !strings.Contains(printed, settingsPath) {
		t.Errorf("the message does not name the file (ERR-07):\n%s", printed)
	}
	if !strings.Contains(printed, "line 3") {
		t.Errorf("the message does not name the line (ERR-07):\n%s", printed)
	}
	after, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Errorf("settings were written despite the parse failure:\n%s", after)
	}
	if store.secret != testSecret {
		t.Error("the pairing was discarded; it should stay valid so the person can fix the file and re-run")
	}
}

// TestRepairReplacesTheIdentity is SPEC 10.3 step 4: pairing again never
// reuses the old identity, revokes it, and replaces the credential.
func TestRepairReplacesTheIdentity(t *testing.T) {
	pairingHome(t)
	cfgPath := xdgpaths.ConfigPath()
	cfg := config.Default()
	cfg.BridgeID = "brg_old"
	cfg.DeviceName = "Old iPhone"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	relay := newPairingRelay(t, "brg_new", "New iPhone")
	store := &fakeCredStore{secret: "old-secret", has: true}
	cmd, out := testCmd("")

	if err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, yes: true, credStore: store,
	}); err != nil {
		t.Fatalf("runPair: %v", err)
	}

	if relay.revokes != 1 {
		t.Errorf("the relay saw %d revoke(s), want exactly one for the replaced identity", relay.revokes)
	}
	if relay.revokedAuth != "Bearer brg_old.old-secret" {
		t.Errorf("the revoke used %q, want the old credential", relay.revokedAuth)
	}
	if store.secret == "old-secret" {
		t.Error("the old secret survived re-pairing")
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.BridgeID != "brg_new" {
		t.Errorf("bridge_id = %q, want the newly minted identity", got.BridgeID)
	}
	if !strings.Contains(out.String(), "already paired") {
		t.Errorf("the person was not told the machine was already paired:\n%s", out.String())
	}
}

// TestRepairDeclinedChangesNothing is the other half of step 4: answering
// no leaves the existing pairing exactly as it was.
func TestRepairDeclinedChangesNothing(t *testing.T) {
	home := pairingHome(t)
	cfgPath := xdgpaths.ConfigPath()
	cfg := config.Default()
	cfg.BridgeID = "brg_old"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	relay := newPairingRelay(t, "brg_new", "New iPhone")
	store := &fakeCredStore{secret: "old-secret", has: true}
	cmd, out := testCmd("n\n")

	if err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, credStore: store,
	}); err != nil {
		t.Fatalf("runPair: %v", err)
	}

	if relay.createdName != "" {
		t.Error("a new bridge was registered even though the person declined")
	}
	if store.secret != "old-secret" {
		t.Errorf("the stored secret changed to %q, want the original", store.secret)
	}
	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.BridgeID != "brg_old" {
		t.Errorf("bridge_id = %q, want the original", got.BridgeID)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json was created even though the person declined (SPEC section 8)")
	}
}

// TestPairDeclinedWritesNoSettings is SPEC section 8's rule: settings are
// written only after a "y" or --yes.
func TestPairDeclinedWritesNoSettings(t *testing.T) {
	home := pairingHome(t)
	relay := newPairingRelay(t, "brg_new", "iPhone")
	cmd, out := testCmd("n\n")

	if err := runPair(context.Background(), pairDeps{
		out: out, cmd: cmd, relayFlag: relay.URL, credStore: &fakeCredStore{},
	}); err != nil {
		t.Fatalf("runPair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json was written without confirmation (SPEC section 8)")
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("the person was not told nothing was written:\n%s", out.String())
	}
}

func TestSanitizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"dev", "dev"},
		{"1.2.3", "1.2.3"},
		{"v1.2.3-rc.1+build5", "v1.2.3-rc.1+build5"},
		{"1.2.3 (dirty)", "1.2.3--dirty-"},
		{"", "dev"},
		{strings.Repeat("9", 40), strings.Repeat("9", 32)},
	}
	for _, tc := range tests {
		if got := sanitizeVersion(tc.in); got != tc.want {
			t.Errorf("sanitizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
