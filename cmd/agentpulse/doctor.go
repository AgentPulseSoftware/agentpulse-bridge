package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/doctor"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/keepawake"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

func newDoctorCmd() *cobra.Command {
	var relayFlag string
	cmd := &cobra.Command{
		Use:                   "doctor",
		Short:                 "Diagnose why AgentPulse isn't working (BR-15); read-only, changes nothing",
		Args:                  cobra.NoArgs,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd.Context(), doctorDeps{out: cmd.OutOrStdout(), relayFlag: relayFlag})
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	return cmd
}

type doctorDeps struct {
	out       io.Writer
	relayFlag string
	now       func() time.Time
	credStore cred.Store
}

// runDoctor is BR-15's body as a plain function (the same pattern
// status.go and projects.go use): read-only, prints one line (plus an
// optional remedy line) per check, then the informational keep-awake
// row, and returns errAlreadyReported (exit 1) exactly when one of the
// eight checks is a FAIL. Checks 1 and 3 can only PASS or WARN (SPEC
// 7.4), and neither can the keep-awake row, so none of those can cause a
// non-zero exit on their own.
func runDoctor(ctx context.Context, d doctorDeps) error {
	if d.now == nil {
		d.now = time.Now
	}
	credStore := d.credStore
	if credStore == nil {
		credStore = cred.Default()
	}

	// A failure to resolve the settings path or this binary's own path
	// must not abort "doctor" — checks 1 and 2 report the empty string
	// themselves (environment.go), and checks 4 through 8 do not depend
	// on either, so they still run.
	settingsPath, _ := defaultSettingsPath()
	binaryPath, _ := currentBinaryPath()
	owner := hooks.NewOwner(binaryPath)

	deps := doctor.Deps{
		BinaryName:   "agentpulse",
		BinaryPath:   binaryPath,
		SettingsPath: settingsPath,
		LookPath:     exec.LookPath,
		InstalledCommands: func() (map[string][]string, error) {
			return hooks.InstalledCommands(settingsPath, owner)
		},
		ClaudeVersion:   doctor.ClaudeVersionRunner(exec.LookPath),
		Now:             d.now,
		Health:          doctorHealthFunc(d.relayFlag),
		CredStore:       credStore,
		CredentialsPath: xdgpaths.CredentialsPath(),
		SpoolPath:       xdgpaths.SpoolPath(),
		State:           state.Load(xdgpaths.StatePath()),
		Version:         version,
	}
	results := doctor.RunChecks(ctx, deps)
	for _, r := range results {
		if err := renderCheck(d.out, string(r.Verdict), r.Name, r.Finding, r.Remedy); err != nil {
			return err
		}
	}

	cfg, _ := config.Load(xdgpaths.ConfigPath())
	if err := renderKeepAwakeRow(d.out, cfg.KeepAwake); err != nil {
		return err
	}

	if doctor.AnyFail(results) {
		return errAlreadyReported
	}
	return nil
}

// doctorHealthFunc resolves the relay base URL once and returns the
// function doctor.RunChecks calls with its own timeout-bounded context
// (internal/doctor's healthTimeout) — this function must never add a
// second timeout on top of that one, and must never be called more than
// the one time RunChecks calls it (checks 5 and 6 both read its single
// result).
func doctorHealthFunc(relayFlag string) func(context.Context) relay.HealthResult {
	return func(ctx context.Context) relay.HealthResult {
		baseURL, err := resolveRelayBaseURL(relayFlag)
		if err != nil {
			return relay.HealthResult{}
		}
		client, err := relay.NewClient(relay.Config{BaseURL: baseURL, Version: version})
		if err != nil {
			return relay.HealthResult{}
		}
		return client.Health(ctx)
	}
}

// renderCheck is "doctor"'s one shared line renderer, used for every row
// it prints: all eight BR-15 checks, and the on-and-supported/
// on-and-unsupported forms of the keep-awake row. remedy is printed as a
// second line only when non-empty — every non-PASS doctor.Result always
// has one.
func renderCheck(out io.Writer, verdict, name, finding, remedy string) error {
	if _, err := fmt.Fprintf(out, "%s  %s — %s\n", verdict, name, finding); err != nil {
		return err
	}
	if remedy == "" {
		return nil
	}
	_, err := fmt.Fprintf(out, "  remedy: %s\n", remedy)
	return err
}

// renderKeepAwakeRow is the informational keep-awake row: it can
// never fail the command (not part of doctor.AnyFail's input), so "off"
// prints as a plain line rather than through renderCheck's PASS/WARN/FAIL
// shape, matching "status"'s own "Keep-awake: on/off" line style.
func renderKeepAwakeRow(out io.Writer, on bool) error {
	if !on {
		_, err := fmt.Fprintln(out, "Keep-awake: off")
		return err
	}
	if keepawake.Supported() {
		return renderCheck(out, string(doctor.PASS), "keep-awake", "on, and supported on this platform", "")
	}
	return renderCheck(out, string(doctor.WARN), "keep-awake",
		"on, but not supported on this platform",
		"systemd-inhibit and tail are required on Linux")
}
