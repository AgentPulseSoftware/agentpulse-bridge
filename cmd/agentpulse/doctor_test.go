package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// pairedCredStore is flush_test.go's fakeCredStore (shared across this
// package's test files) already holding a secret, so doctor's
// cmd-level tests can be about wiring and rendering, not credential
// verdict logic — internal/doctor's own package tests cover that in
// detail.
func pairedCredStore() *fakeCredStore {
	return &fakeCredStore{secret: "s3cr3t", has: true}
}

// writeAllEightHooksRegistered writes ~/.claude/settings.json (under
// setTestXDGDirs' temporary $HOME) with all eight BR-08 events
// registered to the bare command "agentpulse hook" — one of the two
// forms hooks.Owner.Owns recognizes regardless of this test binary's own
// path — so check 2 (settings-file) PASSes without this file needing to
// name the real, resolved path of whatever binary `go test` compiled.
func writeAllEightHooksRegistered(t *testing.T) {
	t.Helper()
	path, err := defaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"hooks":{`)
	for i, event := range claudehooks.BR08Events {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `%q:[{"matcher":"","hooks":[{"type":"command","command":"agentpulse hook","timeout":10}]}]`, event)
	}
	b.WriteString("}}")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil { //nolint:gosec // test-controlled temp path
		t.Fatal(err)
	}
}

func TestRunDoctorListsAllEightChecksByName(t *testing.T) {
	setTestXDGDirs(t)
	writeAllEightHooksRegistered(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: srv.URL, credStore: pairedCredStore(),
	})
	if err != nil {
		t.Errorf("runDoctor() = %v, want nil (exit 0):\n%s", err, out.String())
	}

	text := out.String()
	for _, name := range []string{
		"binary-on-path", "settings-file", "claude-version",
		"credential-store", "relay-reachability", "clock-skew", "spool-health", "bridge-version",
	} {
		if !strings.Contains(text, name) {
			t.Errorf("output does not mention check %q:\n%s", name, text)
		}
	}
	if strings.Contains(text, "PENDING") {
		t.Errorf("output still has a PENDING row:\n%s", text)
	}
}

func TestRunDoctorHealthyExitsZero(t *testing.T) {
	setTestXDGDirs(t)
	writeAllEightHooksRegistered(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: srv.URL, credStore: pairedCredStore(),
	})
	if err != nil {
		t.Errorf("runDoctor() = %v, want nil (exit 0) with a healthy relay and a paired credential:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PASS  credential-store") {
		t.Errorf("output does not show credential-store PASS:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS  settings-file") {
		t.Errorf("output does not show settings-file PASS:\n%s", out.String())
	}
}

func TestRunDoctorSettingsFileMissingExitsOne(t *testing.T) {
	setTestXDGDirs(t)
	// No settings.json written: check 2 must FAIL and doctor must exit 1
	// even though checks 4 through 8 are otherwise healthy.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: srv.URL, credStore: pairedCredStore(),
	})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runDoctor() = %v, want errAlreadyReported (settings-file FAIL means exit 1)", err)
	}
	if !strings.Contains(out.String(), "FAIL  settings-file") {
		t.Errorf("output does not show settings-file FAIL:\n%s", out.String())
	}
}

func TestRunDoctorRelayUnreachableExitsOne(t *testing.T) {
	setTestXDGDirs(t)
	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: "http://127.0.0.1:1", credStore: pairedCredStore(),
	})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runDoctor() = %v, want errAlreadyReported (a FAIL means exit 1)", err)
	}
	if !strings.Contains(out.String(), "FAIL  relay-reachability") {
		t.Errorf("output does not show relay-reachability FAIL:\n%s", out.String())
	}
}

// TestRunDoctorFinishesQuicklyAgainstADeadRelayAndMissingClaude puts $PATH
// at an empty temp directory so exec.LookPath finds neither "claude" nor
// "agentpulse" no matter what happens to be installed on the machine
// running this test, then checks the whole command still returns well
// under ten seconds against a dead relay (3s health timeout + 3s
// claude-version timeout, worst case, plus local file reads).
func TestRunDoctorFinishesQuicklyAgainstADeadRelayAndMissingClaude(t *testing.T) {
	setTestXDGDirs(t)
	t.Setenv("PATH", t.TempDir())
	_, out := testCmd("")
	began := time.Now()
	_ = runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: "http://127.0.0.1:1", credStore: pairedCredStore(),
	})
	if elapsed := time.Since(began); elapsed > 8*time.Second {
		t.Errorf("runDoctor() took %v against a dead relay and a missing claude, want well under 10s", elapsed)
	}
	if !strings.Contains(out.String(), "WARN  claude-version") {
		t.Errorf("output does not show claude-version WARN with claude off PATH:\n%s", out.String())
	}
}

func TestRunDoctorKeepAwakeOffPrintsPlainLine(t *testing.T) {
	setTestXDGDirs(t)
	_, out := testCmd("")
	_ = runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: "http://127.0.0.1:1", credStore: pairedCredStore(),
	})
	if !strings.Contains(out.String(), "Keep-awake: off") {
		t.Errorf("output does not show the plain off line:\n%s", out.String())
	}
}

func TestRunDoctorKeepAwakeOnPrintsAVerdictRow(t *testing.T) {
	setTestXDGDirs(t)
	if err := config.Save(xdgpaths.ConfigPath(), config.Config{BridgeID: "brg_test", KeepAwake: true}); err != nil {
		t.Fatal(err)
	}
	_, out := testCmd("")
	_ = runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: "http://127.0.0.1:1", credStore: pairedCredStore(),
	})
	text := out.String()
	if strings.Contains(text, "Keep-awake: off") {
		t.Errorf("output still shows the off line with keep-awake on:\n%s", text)
	}
	if !strings.Contains(text, "keep-awake — on") {
		t.Errorf("output does not show a keep-awake verdict row:\n%s", text)
	}
}

func TestRenderCheckOmitsRemedyLineWhenEmpty(t *testing.T) {
	_, out := testCmd("")
	if err := renderCheck(out, "PASS", "example", "all good", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "remedy:") {
		t.Errorf("output has a remedy line for an empty remedy:\n%s", out.String())
	}
}

func TestRenderCheckIncludesRemedyLineWhenSet(t *testing.T) {
	_, out := testCmd("")
	if err := renderCheck(out, "FAIL", "example", "it broke", "fix it"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "remedy: fix it") {
		t.Errorf("output is missing the remedy line:\n%s", out.String())
	}
}
