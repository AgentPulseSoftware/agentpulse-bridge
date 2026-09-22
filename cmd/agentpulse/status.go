package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/doctor"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// statusHealthTimeout is BR-14's "one HEAD request" budget: status must
// never hang on a dead relay for more than a few seconds.
const statusHealthTimeout = 3 * time.Second

func newStatusCmd() *cobra.Command {
	var relayFlag string
	cmd := &cobra.Command{
		Use:                   "status",
		Short:                 "Show whether AgentPulse is paired, reachable, and reporting",
		Args:                  cobra.NoArgs,
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), statusDeps{out: cmd.OutOrStdout(), relayFlag: relayFlag})
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	return cmd
}

type statusDeps struct {
	out       io.Writer
	relayFlag string
	now       func() time.Time
}

// runStatus is BR-14's body as a plain function, testable without exec'ing
// a binary (the same pattern every other command in this package uses).
// It returns errAlreadyReported (exit 1) whenever the printed status is
// itself the failure — unpaired, revoked, or unreachable — never a plain
// Go error for those; a real error return is reserved for a write to
// d.out failing.
func runStatus(ctx context.Context, d statusDeps) error {
	if d.now == nil {
		d.now = time.Now
	}

	cfg, _ := config.Load(xdgpaths.ConfigPath())
	st := state.Load(xdgpaths.StatePath())
	wl := watch.Load(xdgpaths.WatchListPath())
	paired := cfg.BridgeID != "" && cfg.BridgeID != config.UnpairedBridgeID

	// ERR-04: three consecutive 401s cleared the credential and recorded
	// exactly this reason; it takes priority over every other line.
	if st.UnpairedReason != "" {
		if _, err := fmt.Fprintln(d.out, st.UnpairedReason); err != nil {
			return err
		}
		return errAlreadyReported
	}

	if _, err := fmt.Fprintf(d.out, "Paired: %s\n", pairedLine(paired, cfg.DeviceName)); err != nil {
		return err
	}

	health := checkHealth(ctx, d)
	if _, err := fmt.Fprintf(d.out, "Relay: %s\n", healthLine(health)); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(d.out, "Spool: %s\n", spoolLine(xdgpaths.SpoolPath())); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(d.out, "Watched projects: %d\n", watchedCount(wl)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(d.out, "Keep-awake: %s\n", onOff(cfg.KeepAwake)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(d.out, "Last event sent: %s\n", lastEventLine(st.LastEvent)); err != nil {
		return err
	}

	if paired && health.Reachable {
		return nil
	}
	return errAlreadyReported
}

func pairedLine(paired bool, deviceName string) string {
	if !paired {
		return "no (run `agentpulse pair`)"
	}
	if deviceName == "" {
		return "yes"
	}
	return "yes, to " + deviceName
}

// checkHealth resolves the relay base URL and performs BR-14's single
// HEAD /v1/health within statusHealthTimeout. A URL that fails to
// resolve (an invalid --relay/AGENTPULSE_RELAY override) is reported the
// same way as an unreachable relay: unauthenticated, so no credential is
// needed to ask whether the relay itself is up.
func checkHealth(ctx context.Context, d statusDeps) relay.HealthResult {
	baseURL, err := resolveRelayBaseURL(d.relayFlag)
	if err != nil {
		return relay.HealthResult{}
	}
	client, err := relay.NewClient(relay.Config{BaseURL: baseURL, Version: version})
	if err != nil {
		return relay.HealthResult{}
	}
	hctx, cancel := context.WithTimeout(ctx, statusHealthTimeout)
	defer cancel()
	return client.Health(hctx)
}

func healthLine(h relay.HealthResult) string {
	if !h.Reachable {
		return "unreachable"
	}
	return fmt.Sprintf("reachable (HTTP %d)", h.StatusCode)
}

// spoolLine reports ERR-02's "status ... report FAIL with the path": a
// spool directory a real "agentpulse hook" cannot write to (disk full,
// permissions) must show up here, named, even when spoolPath itself does
// not exist yet. spool.Depth alone cannot tell that apart from "empty and
// healthy" — it only ever opens for reading, so a missing file reads as
// zero regardless of whether the directory would accept a new one — so
// this checks doctor.SpoolWritable (the same probe "agentpulse doctor"
// runs for its own spool-health check) first.
func spoolLine(spoolPath string) string {
	if err := doctor.SpoolWritable(spoolPath); err != nil {
		return fmt.Sprintf("FAIL — cannot write %s: %v", spoolPath, err)
	}
	depth, err := spool.Depth(spoolPath)
	if err != nil {
		return fmt.Sprintf("FAIL — cannot read %s: %v", spoolPath, err)
	}
	return fmt.Sprintf("%d event(s) queued", depth)
}

func watchedCount(f watch.File) int {
	if f.WatchList == nil {
		return 0
	}
	n := 0
	for _, p := range f.WatchList.Projects {
		if p.Watched {
			n++
		}
	}
	return n
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func lastEventLine(le *state.LastEvent) string {
	if le == nil {
		return "none yet"
	}
	return fmt.Sprintf("%s at %s", le.Type, le.TS)
}
