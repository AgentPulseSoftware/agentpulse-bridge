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
	writeBareHooksRegistered(t, claudehooks.BR08Events)
}

// writeBareHooksRegistered is writeAllEightHooksRegistered for exactly
// events, e.g. the ten a machine paired before StopFailure holds.
func writeBareHooksRegistered(t *testing.T, events []string) {
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
	for i, event := range events {
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

// writeHooksRegisteredAt writes ~/.claude/settings.json with all eleven
// BR-08 events registered to hookCommand(binary), for P5-27's doctor
// tests, which care about the exact registered path.
func writeHooksRegisteredAt(t *testing.T, binary string) {
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
		fmt.Fprintf(&b, `%q:[{"matcher":"","hooks":[{"type":"command","command":%q,"timeout":10}]}]`, event, hookCommand(binary))
	}
	b.WriteString("}}")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil { //nolint:gosec // test-controlled temp path
		t.Fatal(err)
	}
}

// TestRunDoctorFailsWhenAHomebrewUpgradeDeletedTheRegisteredBinary is
// P5-27's doctor requirement end to end: settings.json still names the
// versioned Cellar path a `brew upgrade` deleted, so "doctor" must FAIL
// with the `agentpulse pair --hooks-only` remedy rather than PASS.
func TestRunDoctorFailsWhenAHomebrewUpgradeDeletedTheRegisteredBinary(t *testing.T) {
	setTestXDGDirs(t)
	deletedCellarPath := filepath.Join(realTempDir(t), "Cellar", "agentpulse", "1.0.0", "bin", "agentpulse")
	writeHooksRegisteredAt(t, deletedCellarPath) // never created on disk

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
		t.Fatalf("runDoctor() = %v, want errAlreadyReported (a deleted registered binary is a FAIL)", err)
	}
	text := out.String()
	if !strings.Contains(text, "FAIL  binary-on-path") {
		t.Errorf("output does not show binary-on-path FAIL:\n%s", text)
	}
	if !strings.Contains(text, deletedCellarPath) || !strings.Contains(text, "does not exist") {
		t.Errorf("output does not name the missing path:\n%s", text)
	}
	if !strings.Contains(text, "agentpulse pair --hooks-only") {
		t.Errorf("output does not show the --hooks-only remedy:\n%s", text)
	}
}

// TestRunDoctorPassesThroughALiveHomebrewSymlinkAfterUpgrade simulates a
// Homebrew Cellar/opt layout and a `brew upgrade` in a scratch HOME:
// settings.json names the stable bin/ symlink path (what P5-27's
// currentBinaryPath now writes), the symlink is relinked to a new
// version the way `brew upgrade` relinks it, and "doctor" must not FAIL
// — the path Claude Code actually runs still resolves and is
// executable.
func TestRunDoctorPassesThroughALiveHomebrewSymlinkAfterUpgrade(t *testing.T) {
	setTestXDGDirs(t)
	prefix := realTempDir(t)
	cellarV1 := filepath.Join(prefix, "Cellar", "agentpulse", "1.0.0", "bin", "agentpulse")
	writeExecutable(t, cellarV1)
	binLink := filepath.Join(prefix, "bin", "agentpulse")
	if err := os.MkdirAll(filepath.Dir(binLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarV1, binLink); err != nil {
		t.Fatal(err)
	}
	writeHooksRegisteredAt(t, binLink)

	// brew upgrade: delete the old version, install the new one, relink.
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(cellarV1))); err != nil {
		t.Fatal(err)
	}
	cellarV2 := filepath.Join(prefix, "Cellar", "agentpulse", "1.1.0", "bin", "agentpulse")
	writeExecutable(t, cellarV2)
	if err := os.Remove(binLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarV2, binLink); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: srv.URL, credStore: pairedCredStore(),
	})
	text := out.String()
	if strings.Contains(text, "FAIL  binary-on-path") {
		t.Errorf("binary-on-path FAILed after the upgrade through a live symlink:\n%s", text)
	}
	if err != nil && strings.Contains(text, "FAIL  binary-on-path") {
		t.Errorf("runDoctor() = %v because of binary-on-path:\n%s", err, text)
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

// TestRunDoctorWarnsWhenPairedBeforeStopFailure is a scratch machine
// paired before ADR-006: its settings hold the ten earlier events but not
// StopFailure. Doctor still exits 0 and shows a WARN naming StopFailure
// with the exact --hooks-only remedy line.
func TestRunDoctorWarnsWhenPairedBeforeStopFailure(t *testing.T) {
	setTestXDGDirs(t)
	var ten []string
	for _, e := range claudehooks.BR08Events {
		if e != claudehooks.StopFailure {
			ten = append(ten, e)
		}
	}
	writeBareHooksRegistered(t, ten)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runDoctor(context.Background(), doctorDeps{
		out: out, relayFlag: srv.URL, credStore: pairedCredStore(),
	})
	text := out.String()
	if err != nil {
		t.Errorf("runDoctor() = %v, want nil (a missing StopFailure is a WARN):\n%s", err, text)
	}
	if !strings.Contains(text, "WARN  settings-file") || !strings.Contains(text, "missing optional hooks: StopFailure") {
		t.Errorf("output does not show the settings-file WARN naming StopFailure:\n%s", text)
	}
	if !strings.Contains(text, "Run `agentpulse pair --hooks-only` to add the missing hooks (keeps your pairing).") {
		t.Errorf("output does not show the exact remedy line:\n%s", text)
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
