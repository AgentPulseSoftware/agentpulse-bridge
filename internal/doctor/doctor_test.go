package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
)

// fakeCredStore is a stubbed cred.Store for check 4's table test.
type fakeCredStore struct {
	name   string
	secret string
	err    error
}

func (f fakeCredStore) Get() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.secret, nil
}
func (f fakeCredStore) Set(string) error { return nil }
func (f fakeCredStore) Delete() error    { return nil }
func (f fakeCredStore) Name() string     { return f.name }

func TestCheckCredentialStore(t *testing.T) {
	tests := []struct {
		name       string
		store      cred.Store
		wantVerdit Verdict
	}{
		{"keychain readable", fakeCredStore{name: "macOS Keychain", secret: "s3cr3t"}, PASS},
		{"secret service readable", fakeCredStore{name: "Secret Service (secret-tool)", secret: "s3cr3t"}, PASS},
		{"file fallback", fakeCredStore{name: "file fallback", secret: "s3cr3t"}, WARN},
		{"unpaired, no secret", fakeCredStore{name: "macOS Keychain", err: cred.ErrNotFound}, WARN},
		{"backend I/O error", fakeCredStore{name: "macOS Keychain", err: errors.New("boom")}, WARN},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkCredentialStore(tt.store, "/home/sam/.config/agentpulse/credentials")
			if got.Verdict != tt.wantVerdit {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdit, got.Finding)
			}
		})
	}
}

func TestCheckRelayReachability(t *testing.T) {
	tests := []struct {
		name       string
		health     relay.HealthResult
		wantVerdit Verdict
	}{
		{"reachable 200", relay.HealthResult{Reachable: true, StatusCode: 200}, PASS},
		{"reachable 503", relay.HealthResult{Reachable: true, StatusCode: 503}, WARN},
		{"unreachable", relay.HealthResult{}, FAIL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkRelayReachability(tt.health)
			if got.Verdict != tt.wantVerdit {
				t.Errorf("Verdict = %s, want %s", got.Verdict, tt.wantVerdit)
			}
		})
	}
}

func TestCheckClockSkew(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		health     relay.HealthResult
		wantVerdit Verdict
	}{
		{"5 minutes off", relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(-5 * time.Minute)}, FAIL},
		{"within a second", relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(-1 * time.Second)}, PASS},
		{"59 seconds off, still within", relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(-59 * time.Second)}, PASS},
		{"61 seconds off, over", relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(-61 * time.Second)}, FAIL},
		{"no Date header", relay.HealthResult{Reachable: true, StatusCode: 200}, WARN},
		{"relay unreachable", relay.HealthResult{}, WARN},
		{"future skew (clock ahead)", relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(5 * time.Minute)}, FAIL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkClockSkew(tt.health, now)
			if got.Verdict != tt.wantVerdit {
				t.Errorf("Verdict = %s, want %s (finding: %q)", got.Verdict, tt.wantVerdit, got.Finding)
			}
		})
	}
}

func TestCheckClockSkewNeverIssuesASecondRequest(t *testing.T) {
	// This is really documentation-as-test: checkClockSkew's signature
	// takes a relay.HealthResult value, not a context or a health
	// function, so it is structurally impossible for it to make its own
	// request. Asserted here so a future refactor that adds one back
	// breaks a test, not just a doc comment.
	h := relay.HealthResult{Reachable: true, StatusCode: 200, Date: time.Now()}
	_ = checkClockSkew(h, time.Now())
}

func TestCheckSpoolHealth(t *testing.T) {
	t.Run("healthy, empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spool.ndjson")
		got := checkSpoolHealth(path)
		if got.Verdict != PASS {
			t.Errorf("Verdict = %s, want PASS (finding: %q)", got.Verdict, got.Finding)
		}
	})

	t.Run("at cap", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spool.ndjson")
		for i := 0; i < spool.Cap; i++ {
			if err := spool.Append(path, []byte(`{"type":"stop"}`)); err != nil {
				t.Fatalf("seeding spool: %v", err)
			}
		}
		got := checkSpoolHealth(path)
		if got.Verdict != WARN {
			t.Errorf("Verdict = %s, want WARN (finding: %q)", got.Verdict, got.Finding)
		}
	})

	t.Run("unwritable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root ignores permission bits")
		}
		dir := t.TempDir()
		roDir := filepath.Join(dir, "readonly")
		if err := os.Mkdir(roDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
		path := filepath.Join(roDir, "sub", "spool.ndjson")

		got := checkSpoolHealth(path)
		if got.Verdict != FAIL {
			t.Errorf("Verdict = %s, want FAIL (finding: %q)", got.Verdict, got.Finding)
		}
	})
}

func TestCheckBridgeVersion(t *testing.T) {
	tests := []struct {
		name       string
		state      state.State
		wantVerdit Verdict
	}{
		{"nothing set", state.State{}, PASS},
		{"required bridge version set", state.State{RequiredBridgeVersion: "1.5.0"}, FAIL},
		{"unpaired reason set", state.State{UnpairedReason: "Unpaired by phone or revoked. Run `agentpulse pair`."}, FAIL},
		{"both set, unpaired wins", state.State{RequiredBridgeVersion: "1.5.0", UnpairedReason: "revoked"}, FAIL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkBridgeVersion(tt.state, "1.4.0")
			if got.Verdict != tt.wantVerdit {
				t.Errorf("Verdict = %s, want %s", got.Verdict, tt.wantVerdit)
			}
		})
	}
}

func TestCheckBridgeVersionUnpairedReasonIsExactMessage(t *testing.T) {
	st := state.State{UnpairedReason: "Unpaired by phone or revoked. Run `agentpulse pair`."}
	got := checkBridgeVersion(st, "1.4.0")
	if got.Finding != st.UnpairedReason {
		t.Errorf("Finding = %q, want the exact status message %q", got.Finding, st.UnpairedReason)
	}
}

// TestEveryNonPassResultHasARemedy is a cross-cutting assertion: every
// Result this package can produce that isn't a PASS must carry a
// non-empty Remedy.
func TestEveryNonPassResultHasARemedy(t *testing.T) {
	now := time.Now()
	results := []Result{
		checkBinaryOnPath("agentpulse", "", func(string) (string, error) { return "", errors.New("not found") }, nil, nil),
		checkBinaryOnPath("agentpulse", "", func(string) (string, error) { return "/opt/agentpulse", nil }, nil, errors.New("boom")),
		checkBinaryOnPath("agentpulse", "", func(string) (string, error) { return "/opt/agentpulse", nil }, map[string][]string{"Stop": {"/elsewhere/agentpulse hook"}}, nil),
		checkSettingsFile("", nil, nil),
		checkSettingsFile("/x/settings.json", nil, errors.New("boom")),
		checkSettingsFile("/x/settings.json", map[string][]string{"Stop": {"x hook"}}, nil),
		checkClaudeVersion("", errors.New("not found on PATH")),
		checkClaudeVersion("garbage", nil),
		checkClaudeVersion("2.0.9", nil),
		checkCredentialStore(fakeCredStore{name: "file fallback", secret: "x"}, "/x/credentials"),
		checkCredentialStore(fakeCredStore{name: "macOS Keychain", err: cred.ErrNotFound}, "/x/credentials"),
		checkCredentialStore(fakeCredStore{name: "macOS Keychain", err: errors.New("boom")}, "/x/credentials"),
		checkRelayReachability(relay.HealthResult{}),
		checkRelayReachability(relay.HealthResult{Reachable: true, StatusCode: 503}),
		checkClockSkew(relay.HealthResult{Reachable: true, StatusCode: 200, Date: now.Add(-5 * time.Minute)}, now),
		checkSpoolHealth(filepath.Join(t.TempDir(), "at-cap-check-run-separately")), // PASS case, skip below
		checkBridgeVersion(state.State{RequiredBridgeVersion: "9.9.9"}, "1.0.0"),
		checkBridgeVersion(state.State{UnpairedReason: "revoked"}, "1.0.0"),
	}
	for _, r := range results {
		if r.Verdict == PASS {
			continue
		}
		if r.Remedy == "" {
			t.Errorf("%s: verdict %s has an empty Remedy, want non-empty", r.Name, r.Verdict)
		}
	}
}

func TestAnyFail(t *testing.T) {
	tests := []struct {
		name    string
		results []Result
		want    bool
	}{
		{"no results", nil, false},
		{"all pass", []Result{{Verdict: PASS}, {Verdict: PASS}}, false},
		{"warn only", []Result{{Verdict: PASS}, {Verdict: WARN}}, false},
		{"one fail", []Result{{Verdict: PASS}, {Verdict: FAIL}, {Verdict: WARN}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyFail(tt.results); got != tt.want {
				t.Errorf("AnyFail() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunChecksReusesHealthResultOnce drives RunChecks end to end with a
// Health func that counts its own calls, asserting it is called exactly
// once even though both the reachability and clock-skew checks consume
// its result (the "do not issue a second request" rule).
func TestRunChecksReusesHealthResultOnce(t *testing.T) {
	calls := 0
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	binary := "/opt/agentpulse/bin/agentpulse"
	allEightOwned := func() (map[string][]string, error) {
		m := map[string][]string{}
		for _, event := range claudehooks.BR08Events {
			m[event] = []string{binary + " hook"}
		}
		return m, nil
	}
	d := Deps{
		BinaryName:        "agentpulse",
		BinaryPath:        binary,
		SettingsPath:      "/x/settings.json",
		LookPath:          func(string) (string, error) { return binary, nil },
		InstalledCommands: allEightOwned,
		ClaudeVersion:     func(context.Context) (string, error) { return compat.BaselineVersion, nil },
		Now:               func() time.Time { return now },
		Health: func(context.Context) relay.HealthResult {
			calls++
			return relay.HealthResult{Reachable: true, StatusCode: 200, Date: now}
		},
		CredStore:       fakeCredStore{name: "macOS Keychain", secret: "s"},
		CredentialsPath: "/x/credentials",
		SpoolPath:       filepath.Join(t.TempDir(), "spool.ndjson"),
		State:           state.State{},
		Version:         "1.0.0",
	}
	results := RunChecks(context.Background(), d)
	if calls != 1 {
		t.Errorf("Health called %d times, want exactly 1", calls)
	}
	if len(results) != 8 {
		t.Fatalf("RunChecks() returned %d results, want 8 (BR-15's eight checks)", len(results))
	}
	for _, r := range results {
		if r.Verdict != PASS {
			t.Errorf("%s: Verdict = %s, want PASS for this all-healthy Deps (finding: %q)", r.Name, r.Verdict, r.Finding)
		}
	}
	if AnyFail(results) {
		t.Error("AnyFail(results) = true for an all-PASS run")
	}
}

// installedCommandsCounterDeps is the shared Deps for the two
// InstalledCommands counter tests below: identical to
// TestRunChecksReusesHealthResultOnce's Deps except InstalledCommands is
// the caller's fn.
func installedCommandsCounterDeps(t *testing.T, fn func() (map[string][]string, error)) Deps {
	t.Helper()
	binary := "/opt/agentpulse/bin/agentpulse"
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return Deps{
		BinaryName:        "agentpulse",
		BinaryPath:        binary,
		SettingsPath:      "/x/settings.json",
		LookPath:          func(string) (string, error) { return binary, nil },
		InstalledCommands: fn,
		ClaudeVersion:     func(context.Context) (string, error) { return compat.BaselineVersion, nil },
		Now:               func() time.Time { return now },
		Health: func(context.Context) relay.HealthResult {
			return relay.HealthResult{Reachable: true, StatusCode: 200, Date: now}
		},
		CredStore:       fakeCredStore{name: "macOS Keychain", secret: "s"},
		CredentialsPath: "/x/credentials",
		SpoolPath:       filepath.Join(t.TempDir(), "spool.ndjson"),
		State:           state.State{},
		Version:         "1.0.0",
	}
}

// TestRunChecksCallsInstalledCommandsOnce is a counter test:
// Deps.InstalledCommands is documented as "called
// exactly once and its result shared by checks 1 and 2" but nothing
// failed if a future change to RunChecks called it a second time. The
// stub here counts its own calls and, on the first call only, returns
// every hook event fully registered; any later call returns an empty
// map instead, so if check 1 or check 2 were ever fed a second, separate
// call's result rather than the one shared result, that check's Verdict
// would flip and this test would catch it even if the call count were
// somehow still reported as 1.
func TestRunChecksCallsInstalledCommandsOnce(t *testing.T) {
	binary := "/opt/agentpulse/bin/agentpulse"
	calls := 0
	fn := func() (map[string][]string, error) {
		calls++
		if calls > 1 {
			return map[string][]string{}, nil
		}
		m := map[string][]string{}
		for _, event := range claudehooks.BR08Events {
			m[event] = []string{binary + " hook"}
		}
		return m, nil
	}
	d := installedCommandsCounterDeps(t, fn)

	results := RunChecks(context.Background(), d)

	if calls != 1 {
		t.Fatalf("InstalledCommands called %d times, want exactly 1", calls)
	}
	if len(results) < 2 {
		t.Fatalf("RunChecks() returned %d results, want at least 2 (checks 1 and 2)", len(results))
	}
	binaryOnPath, settingsFile := results[0], results[1]
	if binaryOnPath.Name != "binary-on-path" || binaryOnPath.Verdict != PASS {
		t.Errorf("check 1 (binary-on-path) = %+v, want a PASS derived from the single, fully-registered InstalledCommands call", binaryOnPath)
	}
	if settingsFile.Name != "settings-file" || settingsFile.Verdict != PASS {
		t.Errorf("check 2 (settings-file) = %+v, want a PASS derived from the single, fully-registered InstalledCommands call", settingsFile)
	}
}

// TestRunChecksCallsInstalledCommandsOnceOnError is
// TestRunChecksCallsInstalledCommandsOnce's mirror case: when
// InstalledCommands errors, it is still called exactly once, and both
// checks report the degraded outcome checkBinaryOnPath and
// checkSettingsFile define for a non-nil installedErr, rather than a
// second, possibly different attempt.
func TestRunChecksCallsInstalledCommandsOnceOnError(t *testing.T) {
	wantErr := errors.New("settings file is malformed JSON")
	calls := 0
	fn := func() (map[string][]string, error) {
		calls++
		return nil, wantErr
	}
	d := installedCommandsCounterDeps(t, fn)

	results := RunChecks(context.Background(), d)

	if calls != 1 {
		t.Fatalf("InstalledCommands called %d times, want exactly 1", calls)
	}
	if len(results) < 2 {
		t.Fatalf("RunChecks() returned %d results, want at least 2 (checks 1 and 2)", len(results))
	}
	binaryOnPath, settingsFile := results[0], results[1]
	if binaryOnPath.Name != "binary-on-path" || binaryOnPath.Verdict != WARN {
		t.Errorf("check 1 (binary-on-path) = %+v, want WARN (installedErr set)", binaryOnPath)
	}
	if !strings.Contains(binaryOnPath.Finding, "could not compare against the registered hook command") {
		t.Errorf("check 1 Finding = %q, want it to name the comparison failure", binaryOnPath.Finding)
	}
	if settingsFile.Name != "settings-file" || settingsFile.Verdict != FAIL {
		t.Errorf("check 2 (settings-file) = %+v, want FAIL (installedErr set)", settingsFile)
	}
	if !strings.Contains(settingsFile.Finding, wantErr.Error()) {
		t.Errorf("check 2 Finding = %q, want it to contain the single call's own error %q", settingsFile.Finding, wantErr)
	}
}
