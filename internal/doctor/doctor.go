// Package doctor implements the checks behind "agentpulse doctor" (BR-15,
// SPEC 7.4, NFR-11): a read-only diagnostic that tells the operator why
// the bridge isn't working, one PASS/WARN/FAIL verdict per check, with a
// one-line remedy on anything that isn't a PASS.
//
// This package is deliberately split from cmd/agentpulse/doctor.go the
// same way every other command in this repository is: everything here is
// a plain function over a Deps struct of already-resolved inputs (a
// clock, a credential store, a relay health result, local state), so
// every check is testable without touching a real machine, a real
// keychain, or a real network. Rendering — turning a []Result into
// output text and an exit code — is the cmd package's job, not this
// one's, matching status.go and projects.go.
//
// RunChecks returns all eight of BR-15's checks, in this order: binary on
// PATH, the settings file's registered hooks, the installed Claude Code
// version against the embedded compatibility table (checks 1 through 3),
// then the credential store, relay reachability, clock skew, spool
// health, and bridge version (checks 4 through 8). Checks 1 through 3
// never import internal/hooks directly —
// cmd/agentpulse/doctor.go closes over internal/hooks.InstalledCommands
// and internal/hooks.Owner and hands this package only the resulting
// func() (map[string][]string, error), the same way it hands check 5 a
// bare func(context.Context) relay.HealthResult instead of an
// internal/relay.Client.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
)

// Verdict is one check's outcome.
type Verdict string

// The three verdicts BR-15 defines. There is no fourth "unknown" verdict
// in this package: checks 1 and 3 can never determine enough to FAIL
// (SPEC 7.4 treats an old or undetectable Claude Code as a degradation,
// not a fault), so an inconclusive result is WARN with a Finding that
// says why, never a separate verdict.
const (
	PASS Verdict = "PASS"
	WARN Verdict = "WARN"
	FAIL Verdict = "FAIL"
)

// Result is one check's outcome: a short machine-stable Name (used only
// for logs and tests, never printed on its own without Finding), the
// Verdict, a one-line human-readable Finding, and — required on every
// non-PASS result — a one-line Remedy naming the next action.
type Result struct {
	Name    string
	Verdict Verdict
	Finding string
	Remedy  string
}

// Deps is every input a check needs, resolved once by
// cmd/agentpulse/doctor.go so no check here touches a real clock,
// filesystem, keychain, or network directly.
type Deps struct {
	// BinaryName is this bridge's own program name, always "agentpulse"
	// in production; a Deps field (rather than a hardcoded literal in
	// check 1) only so a test can use a different name without touching
	// a real PATH entry named "agentpulse".
	BinaryName string
	// BinaryPath is currentBinaryPath()'s result: the absolute,
	// symlink-resolved path to the copy of the bridge currently running
	// "doctor". Empty if it could not be resolved — check 1 still runs,
	// it just can't say which copy of "agentpulse" this process is.
	BinaryPath string
	// SettingsPath is ~/.claude/settings.json (defaultSettingsPath()).
	SettingsPath string
	// LookPath resolves a program name on $PATH (exec.LookPath in
	// production), for check 1's "is agentpulse on PATH at all".
	LookPath func(name string) (string, error)
	// InstalledCommands returns, per Claude Code hook event present in
	// SettingsPath, the commands in that event this bridge owns
	// (hooks.InstalledCommands(SettingsPath, hooks.NewOwner(BinaryPath))
	// in production). Checks 1 and 2 share the one result RunChecks gets
	// from calling this exactly once, the same way checks 5 and 6 share
	// one Health result.
	InstalledCommands func() (map[string][]string, error)
	// ClaudeVersion runs `claude --version` and returns its raw output
	// (doctor.ClaudeVersionRunner(exec.LookPath) in production). Check 3
	// gets its own healthTimeout-equivalent, claudeVersionTimeout,
	// derived from RunChecks' own ctx.
	ClaudeVersion func(context.Context) (string, error)
	// Now is time.Now, overridable so the clock-skew check (6) is
	// deterministic in tests.
	Now func() time.Time
	// Health performs the one HEAD /v1/health request checks 5 and 6
	// both read from — never called twice.
	Health func(context.Context) relay.HealthResult
	// CredStore is the active credential backend (cred.Default() in
	// production).
	CredStore cred.Store
	// CredentialsPath is the fallback credential file's path
	// (xdgpaths.CredentialsPath()), named in check 4's remedy when that
	// backend is the one in use.
	CredentialsPath string
	// SpoolPath is the event spool's path (xdgpaths.SpoolPath()).
	SpoolPath string
	// State is the bridge's already-loaded runtime state
	// (state.Load(xdgpaths.StatePath())): check 8 reads
	// RequiredBridgeVersion and UnpairedReason from it.
	State state.State
	// Version is this running binary's own version string, for check 8's
	// "the relay requires X, this is Y" message.
	Version string
}

// healthTimeout bounds check 5's HEAD /v1/health, matching "status"'s own
// budget (cmd/agentpulse/status.go's statusHealthTimeout) so "doctor"
// finishes in well under ten seconds against a dead relay.
const healthTimeout = 3 * time.Second

// RunChecks runs all eight of BR-15's checks in order and returns one
// Result per check. d.InstalledCommands is called exactly once and its
// result shared between checks 1 and 2, the same way check 5's
// relay.HealthResult is shared with check 6 rather than requesting
// either twice.
func RunChecks(ctx context.Context, d Deps) []Result {
	installed, installedErr := callInstalledCommands(d.InstalledCommands)
	binaryOnPath := checkBinaryOnPath(d.BinaryName, d.BinaryPath, d.LookPath, installed, installedErr)
	settingsFile := checkSettingsFile(d.SettingsPath, installed, installedErr)

	vctx, vcancel := context.WithTimeout(ctx, claudeVersionTimeout)
	defer vcancel()
	claudeOutput, claudeErr := callClaudeVersion(vctx, d.ClaudeVersion)
	claudeVersion := checkClaudeVersion(claudeOutput, claudeErr)

	credential := checkCredentialStore(d.CredStore, d.CredentialsPath)

	hctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	health := d.Health(hctx)
	reachability := checkRelayReachability(health)
	clockSkew := checkClockSkew(health, d.Now())

	spoolHealth := checkSpoolHealth(d.SpoolPath)
	bridgeVersion := checkBridgeVersion(d.State, d.Version)

	return []Result{
		binaryOnPath, settingsFile, claudeVersion,
		credential, reachability, clockSkew, spoolHealth, bridgeVersion,
	}
}

// callInstalledCommands calls fn if set, or reports the same "not
// configured" condition checkBinaryOnPath and checkSettingsFile already
// know how to render as a WARN/FAIL — so a Deps built without this field
// (a stale caller, or a test that only cares about checks 4-8) degrades
// the same way a missing settings file would, rather than panicking.
func callInstalledCommands(fn func() (map[string][]string, error)) (map[string][]string, error) {
	if fn == nil {
		return nil, errors.New("doctor: Deps.InstalledCommands is not set")
	}
	return fn()
}

// callClaudeVersion is callInstalledCommands's counterpart for check 3.
func callClaudeVersion(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
	if fn == nil {
		return "", errors.New("doctor: Deps.ClaudeVersion is not set")
	}
	return fn(ctx)
}

// AnyFail reports whether any result FAILed — "doctor"'s exit code rule:
// exit 0 with no FAIL, 1 otherwise, WARNs alone still exit 0.
func AnyFail(results []Result) bool {
	for _, r := range results {
		if r.Verdict == FAIL {
			return true
		}
	}
	return false
}

// checkCredentialStore is BR-15 check 4 (BR-06). Order of the cases
// matters: an unreadable/absent secret is reported as "not paired"
// regardless of which backend is active, since that is the more
// actionable fact; a readable secret in the fallback file is WARN (not
// FAIL — SPEC 14/ERR handling never fails the bridge just for lacking a
// platform keychain) naming the file so the operator can find it.
func checkCredentialStore(store cred.Store, fallbackPath string) Result {
	name := store.Name()
	_, err := store.Get()
	switch {
	case errors.Is(err, cred.ErrNotFound):
		return Result{
			Name: "credential-store", Verdict: WARN,
			Finding: "no credential stored (not paired)",
			Remedy:  "run `agentpulse pair`",
		}
	case err != nil:
		return Result{
			Name: "credential-store", Verdict: WARN,
			Finding: fmt.Sprintf("%s: cannot read the stored credential: %v", name, err),
			Remedy:  "run `agentpulse pair` again",
		}
	case name == cred.NameFileFallback:
		return Result{
			Name: "credential-store", Verdict: WARN,
			Finding: "using the file fallback, not a platform credential store (" + name + ")",
			Remedy:  fmt.Sprintf("no platform keychain/Secret Service was available; the secret is at %s", fallbackPath),
		}
	default:
		return Result{
			Name: "credential-store", Verdict: PASS,
			Finding: name + " is readable",
		}
	}
}

// checkRelayReachability is BR-15 check 5, the one unauthenticated
// HEAD /v1/health "status" already performs.
func checkRelayReachability(h relay.HealthResult) Result {
	if !h.Reachable {
		return Result{
			Name: "relay-reachability", Verdict: FAIL,
			Finding: "relay is unreachable",
			Remedy:  "check your network connection and try again",
		}
	}
	if h.StatusCode < 200 || h.StatusCode >= 300 {
		return Result{
			Name: "relay-reachability", Verdict: WARN,
			Finding: fmt.Sprintf("relay responded with HTTP %d", h.StatusCode),
			Remedy:  "the relay may be degraded; try again shortly",
		}
	}
	return Result{
		Name: "relay-reachability", Verdict: PASS,
		Finding: fmt.Sprintf("reachable (HTTP %d)", h.StatusCode),
	}
}

// clockSkewLimit is BR-15's 60 second threshold.
const clockSkewLimit = 60 * time.Second

// checkClockSkew is BR-15 check 6, computed from check 5's own
// HealthResult.Date (never a second request). An unreachable relay, a
// missing Date header, or one that failed to parse are all WARN, never
// FAIL — the relay simply didn't report its clock, which says nothing
// about whether the machine's own clock is wrong.
func checkClockSkew(h relay.HealthResult, now time.Time) Result {
	if !h.Reachable || h.Date.IsZero() {
		return Result{
			Name: "clock-skew", Verdict: WARN,
			Finding: "relay did not report its clock",
		}
	}
	skew := now.Sub(h.Date)
	if skew < 0 {
		skew = -skew
	}
	if skew > clockSkewLimit {
		return Result{
			Name: "clock-skew", Verdict: FAIL,
			Finding: fmt.Sprintf("this machine's clock is off from the relay's by %s", skew.Round(time.Second)),
			Remedy:  "enable automatic time sync; events carry the bridge's own clock, and the relay clamps future timestamps",
		}
	}
	return Result{
		Name: "clock-skew", Verdict: PASS,
		Finding: fmt.Sprintf("within %s of the relay's clock", skew.Round(time.Second)),
	}
}

// SpoolWritable is a minimal writability probe for check 7: it opens (and
// creates, if missing) the spool file for read-write and closes it again
// without writing a byte — the same first step spool.Append itself takes
// — so the check can tell a real permissions or disk problem (ERR-02)
// apart from an empty, healthy spool, without appending a bogus event to
// a spool a real "agentpulse flush" would later try to deliver.
//
// Exported so "agentpulse status" (cmd/agentpulse/status.go) can run the
// same probe: ERR-02 requires both "status" and "doctor" to report FAIL
// naming the path when the spool directory is unwritable, and
// spool.Depth alone cannot tell "unwritable" apart from "empty" when the
// spool file does not exist yet, since Depth only ever opens for reading.
func SpoolWritable(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is the bridge's own XDG state dir, not attacker input
	if err != nil {
		return err
	}
	return f.Close()
}

// checkSpoolHealth is BR-15 check 7 (BR-03, ERR-02).
func checkSpoolHealth(spoolPath string) Result {
	if err := SpoolWritable(spoolPath); err != nil {
		return Result{
			Name: "spool-health", Verdict: FAIL,
			Finding: fmt.Sprintf("cannot write %s: %v", spoolPath, err),
			Remedy:  fmt.Sprintf("check permissions on %s", spoolPath),
		}
	}
	depth, err := spool.Depth(spoolPath)
	if err != nil {
		return Result{
			Name: "spool-health", Verdict: FAIL,
			Finding: fmt.Sprintf("cannot read %s: %v", spoolPath, err),
			Remedy:  fmt.Sprintf("check permissions on %s", spoolPath),
		}
	}
	if depth >= spool.Cap {
		return Result{
			Name: "spool-health", Verdict: WARN,
			Finding: fmt.Sprintf("spool is at its %d-event cap; oldest events are being dropped", spool.Cap),
			Remedy:  "check that `agentpulse status` shows the relay reachable",
		}
	}
	return Result{
		Name: "spool-health", Verdict: PASS,
		Finding: fmt.Sprintf("%d event(s) queued, writable", depth),
	}
}

// checkBridgeVersion is BR-15 check 8 (ERR-03, ERR-04). An unpaired
// reason (ERR-04: cleared after three consecutive 401s) takes priority
// over a version mismatch, since it is the more urgent, more actionable
// fact — a version mismatch cannot even be re-checked usefully until
// pairing is restored.
func checkBridgeVersion(st state.State, currentVersion string) Result {
	if st.UnpairedReason != "" {
		return Result{
			Name: "bridge-version", Verdict: FAIL,
			Finding: st.UnpairedReason,
			Remedy:  "run `agentpulse pair`",
		}
	}
	if st.RequiredBridgeVersion != "" {
		return Result{
			Name: "bridge-version", Verdict: FAIL,
			Finding: fmt.Sprintf("the relay requires bridge version %s, this is %s", st.RequiredBridgeVersion, currentVersion),
			Remedy:  "upgrade the bridge",
		}
	}
	return Result{
		Name: "bridge-version", Verdict: PASS,
		Finding: fmt.Sprintf("this bridge (%s) satisfies the relay's requirement", currentVersion),
	}
}
