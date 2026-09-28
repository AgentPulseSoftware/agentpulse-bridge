package main

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// runHooksOnly is "agentpulse pair --hooks-only" (BR-07 as amended by
// ADR-005 section 3): on a paired machine it registers whichever BR-08
// hooks are missing, showing the exact diff and asking first (D29) unless
// yes is set. It never contacts the relay and never reads or changes the
// credential or config.json, so the pairing stays exactly as it was.
//
// An event is left alone when it already holds exactly this binary's
// hook command, so a machine paired before the subagent hooks existed
// sees only those two entries added. Any other event is merged as
// "agentpulse pair" would merge it (BR-07): that also repoints an entry
// left behind by an upgrade that moved the binary.
func runHooksOnly(cmd *cobra.Command, yes bool) error {
	out := cmd.OutOrStdout()
	cfg, _ := config.Load(xdgpaths.ConfigPath()) // unreadable reads as unpaired
	if cfg.BridgeID == "" || cfg.BridgeID == config.UnpairedBridgeID {
		_, _ = fmt.Fprintln(out, "This machine is not paired. Run `agentpulse pair`.")
		return errAlreadyReported
	}

	settingsPath, err := defaultSettingsPath()
	if err != nil {
		return err
	}
	binary, err := currentBinaryPath()
	if err != nil {
		return err
	}
	owner := hooks.NewOwner(binary)
	installed, err := hooks.InstalledCommands(settingsPath, owner)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		_, _ = fmt.Fprintf(out, "Your Claude Code settings could not be read: %v\nNothing was written to it.\n", err)
		return errAlreadyReported
	}
	var missing []hooks.Entry
	for _, e := range hookEntries(hookCommand(binary)) {
		if !slices.Equal(installed[e.Event], []string{e.Command}) {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		_, err := fmt.Fprintf(out, "All %d Claude Code hooks are already registered in %s. Nothing to do.\n",
			len(claudehooks.BR08Events), settingsPath)
		return err
	}

	plan, err := hooks.PlanInstall(settingsPath, owner, missing)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Your Claude Code settings could not be read: %v\nNothing was written to it.\n", err)
		return errAlreadyReported
	}
	written, err := applyPlan(cmd, plan, yes, "install")
	if err != nil || !written {
		return err
	}
	_, err = fmt.Fprintf(out, "\nDone. All %d Claude Code hooks are registered; your pairing is unchanged.\n",
		len(claudehooks.BR08Events))
	return err
}
