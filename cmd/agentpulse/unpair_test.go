package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// deletedSecret reports whether the fake store is empty, which is what
// BR-09's "keeps no secret after unpair" looks like from outside.
func (f *fakeCredStore) deletedSecret() bool { return !f.has }

// pairedMachine builds the full local state a paired bridge has: the eight
// hook entries plus another tool's, a config file, a spool, a state file,
// a watch list, a lock file, a session scratch file, and a log.
func pairedMachine(t *testing.T) (home string, store *fakeCredStore) {
	t.Helper()
	home = pairingHome(t)

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
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
`), 0o600); err != nil {
		t.Fatal(err)
	}

	binary, err := currentBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := hooks.PlanInstall(settingsPath, hooks.NewOwner(binary), hookEntries(hookCommand(binary)))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.BridgeID = "brg_paired"
	cfg.DeviceName = "Sam's iPhone"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(xdgpaths.SessionsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	sessionLocksDir := filepath.Join(xdgpaths.StateDir(), "session-locks")
	if err := os.MkdirAll(sessionLocksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		xdgpaths.StatePath(),
		xdgpaths.WatchListPath(),
		xdgpaths.SpoolPath(),
		filepath.Join(xdgpaths.StateDir(), "flush.lock"),
		filepath.Join(xdgpaths.SessionsDir(), "sess_1.json"),
		filepath.Join(sessionLocksDir, "sess_1.lock"),
		xdgpaths.LogPath(),
	} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return home, &fakeCredStore{secret: "paired-secret", has: true}
}

// TestUnpairRemovesEverythingItOwns is BR-09 end to end, with a relay that
// answers 404 to the revoke — the state an older relay deployment can be
// in. The teardown still completes and the command still exits 0.
func TestUnpairRemovesEverythingItOwns(t *testing.T) {
	home, store := pairedMachine(t)

	revokeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeCalls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cmd, out := testCmd("")
	if err := runUnpair(context.Background(), unpairDeps{
		out: out, cmd: cmd, relayFlag: srv.URL, yes: true, credStore: store,
	}); err != nil {
		t.Fatalf("runUnpair returned %v, want nil (exit 0) even when the revoke 404s", err)
	}

	if revokeCalls != 1 {
		t.Errorf("the relay saw %d revoke attempt(s), want exactly one", revokeCalls)
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), " hook") {
		t.Errorf("an agentpulse hook entry survived unpair:\n%s", data)
	}
	if !strings.Contains(string(data), "notify-send done") {
		t.Errorf("another tool's hook was removed:\n%s", data)
	}

	for _, path := range []string{
		xdgpaths.ConfigPath(),
		xdgpaths.CredentialsPath(),
		xdgpaths.StatePath(),
		xdgpaths.WatchListPath(),
		xdgpaths.SpoolPath(),
		filepath.Join(xdgpaths.StateDir(), "flush.lock"),
		xdgpaths.SessionsDir(),
		filepath.Join(xdgpaths.StateDir(), "session-locks"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists after unpair", path)
		}
	}
	if _, err := os.Stat(xdgpaths.LogPath()); err != nil {
		t.Errorf("bridge.log was removed; BR-09 leaves it in place: %v", err)
	}
	if !store.deletedSecret() {
		t.Error("the credential survived unpair (SEC-08)")
	}
	if !strings.Contains(out.String(), xdgpaths.LogPath()) {
		t.Errorf("the summary does not say the log was kept:\n%s", out.String())
	}
}

// TestUnpairWithoutConfirmationChangesNothing is SPEC section 8 in
// reverse: answering no leaves the settings file and every local file
// exactly as they were, and reports failure: declining removes nothing,
// so the hook removal did not succeed, and exit 0 is reserved for the
// case where it did.
func TestUnpairWithoutConfirmationChangesNothing(t *testing.T) {
	home, store := pairedMachine(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	before, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}

	cmd, out := testCmd("n\n")
	err = runUnpair(context.Background(), unpairDeps{
		out: out, cmd: cmd, relayFlag: "http://127.0.0.1:1", credStore: store,
	})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runUnpair error = %v, want the already-reported sentinel (exit 1)", err)
	}

	after, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("settings changed after the person declined:\n%s", after)
	}
	if store.deletedSecret() {
		t.Error("the credential was deleted after the person declined")
	}
	if _, err := os.Stat(xdgpaths.ConfigPath()); err != nil {
		t.Errorf("config.json was deleted after the person declined: %v", err)
	}
	if !strings.Contains(out.String(), "still paired") {
		t.Errorf("the person was not told nothing changed:\n%s", out.String())
	}
}

// TestUnpairOnAnUnpairedMachineIsHarmless: nothing to remove, nothing to
// revoke, and still exit 0.
func TestUnpairOnAnUnpairedMachineIsHarmless(t *testing.T) {
	pairingHome(t)
	cmd, out := testCmd("")

	if err := runUnpair(context.Background(), unpairDeps{
		out: out, cmd: cmd, relayFlag: "http://127.0.0.1:1", yes: true, credStore: &fakeCredStore{},
	}); err != nil {
		t.Fatalf("runUnpair: %v", err)
	}
	if !strings.Contains(out.String(), "No AgentPulse hooks found") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing to remove") {
		t.Errorf("the summary does not say there was nothing to remove:\n%s", out.String())
	}
	if strings.Contains(out.String(), "hooks: removed") {
		t.Errorf("the summary claims hooks were removed when there were none to begin with:\n%s", out.String())
	}
}

// TestUnpairWithUnreadableConfigSkipsRevokeAndSaysWhy covers the case
// where config.json exists but fails to parse, so there is no bridge_id to
// revoke with even though the credential store still holds a secret. The
// revoke is still skipped (nothing can authenticate it), but the summary
// must say why rather than the generic "nothing to revoke" line, which
// would read as though this machine was never paired at all.
func TestUnpairWithUnreadableConfigSkipsRevokeAndSaysWhy(t *testing.T) {
	_, store := pairedMachine(t)
	if err := os.WriteFile(xdgpaths.ConfigPath(), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	revokeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cmd, out := testCmd("")
	if err := runUnpair(context.Background(), unpairDeps{
		out: out, cmd: cmd, relayFlag: srv.URL, yes: true, credStore: store,
	}); err != nil {
		t.Fatalf("runUnpair returned %v, want nil (exit 0)", err)
	}
	if revokeCalls != 0 {
		t.Errorf("the relay saw %d revoke attempt(s) with no readable bridge_id, want zero", revokeCalls)
	}
	if !strings.Contains(out.String(), "local identity could not be read") {
		t.Errorf("the summary does not say why the revoke was skipped:\n%s", out.String())
	}
	if !store.deletedSecret() {
		t.Error("the credential survived unpair despite the unreadable config")
	}
}

// TestUnpairReportsInvalidSettingsAndStillClearsLocalData is ERR-07 on the
// removal side: the file is named, nothing is written to it, the rest of
// the teardown still runs, and the exit status is 1.
func TestUnpairReportsInvalidSettingsAndStillClearsLocalData(t *testing.T) {
	home, store := pairedMachine(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	broken := "{\n  \"hooks\": { oops\n}\n"
	if err := os.WriteFile(settingsPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, out := testCmd("")
	err := runUnpair(context.Background(), unpairDeps{
		out: out, cmd: cmd, relayFlag: "http://127.0.0.1:1", yes: true, credStore: store,
	})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runUnpair error = %v, want the already-reported sentinel (exit 1)", err)
	}

	after, err := os.ReadFile(settingsPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Errorf("settings were rewritten despite the parse failure:\n%s", after)
	}
	if !strings.Contains(out.String(), "line 2") {
		t.Errorf("the summary does not name the line (ERR-07):\n%s", out.String())
	}
	if !store.deletedSecret() {
		t.Error("the credential survived; local teardown must run even when the hook removal fails")
	}
	if _, err := os.Stat(xdgpaths.ConfigPath()); !os.IsNotExist(err) {
		t.Error("config.json survived; local teardown must run even when the hook removal fails")
	}
}
