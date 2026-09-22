//go:build dev

package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
)

// registerDevCommands adds the "record" command tree to root. It is only
// linked into development builds ("make build-dev", built with -tags dev):
// recording writes raw, unscrubbed Claude Code session data to disk, so it
// must never be reachable from the binary an end user installs.
func registerDevCommands(root *cobra.Command) {
	root.AddCommand(newRecordCmd())
}

func newRecordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record real Claude Code hook input for building fixtures (development build only)",
	}
	cmd.AddCommand(newRecordInstallCmd())
	cmd.AddCommand(newRecordUninstallCmd())
	return cmd
}

func newRecordInstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install <dir>",
		Short: "Register agentpulse hook in ~/.claude/settings.json to record raw hook JSON into <dir>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			absDir, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("resolving %s: %w", args[0], err)
			}

			binary, err := currentBinaryPath()
			if err != nil {
				return err
			}

			settingsPath, err := defaultSettingsPath()
			if err != nil {
				return err
			}

			command := fmt.Sprintf("env AGENTPULSE_RECORD_DIR=%s %s hook", shellQuoteSingle(absDir), shellQuoteSingle(binary))
			plan, err := hooks.PlanInstall(settingsPath, hooks.NewOwner(binary), hookEntries(command))
			if err != nil {
				return err
			}
			_, err = applyPlan(cmd, plan, yes, "install")
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without asking for confirmation")
	return cmd
}

func newRecordUninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove agentpulse's recording hook entries from ~/.claude/settings.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			settingsPath, err := defaultSettingsPath()
			if err != nil {
				return err
			}
			binary, err := currentBinaryPath()
			if err != nil {
				return err
			}
			plan, err := hooks.PlanUninstall(settingsPath, hooks.NewOwner(binary))
			if err != nil {
				return err
			}
			_, err = applyPlan(cmd, plan, yes, "uninstall")
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without asking for confirmation")
	return cmd
}
