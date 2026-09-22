// Command agentpulse is the AgentPulse bridge CLI. It runs on the developer's
// machine, watches Claude Code hooks, and relays session events to the
// AgentPulse relay so the iPhone app can show what agents are doing.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=...". It
// defaults to "dev" for local, non-release builds.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// A command that has already explained itself in plain language
		// (an expired pairing code, a Ctrl-C, a settings file that needs
		// fixing) returns errAlreadyReported, so the failure is not
		// restated as a second, terser line.
		if !errors.Is(err, errAlreadyReported) {
			fmt.Fprintln(os.Stderr, "Error:", err) //nolint:errcheck // best-effort; we're already exiting non-zero
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "agentpulse",
		Short: "AgentPulse bridge: connects Claude Code to the AgentPulse iPhone app",
		// Cobra's own error/usage printing is silenced at the root so every
		// subcommand inherits it (cobra checks the root's flag OR the
		// specific command's own flag; setting it here covers all of
		// them, "scrub" included) and main() above is the single place
		// that prints a failure — just "Error: <message>", never the full
		// usage/help block dumped after it by default.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newVersionCmd())
	root.AddCommand(newPairCmd())
	root.AddCommand(newUnpairCmd())
	root.AddCommand(newHookCmd())
	root.AddCommand(newFlushCmd())
	root.AddCommand(newScrubCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newProjectsCmd())
	root.AddCommand(newDoctorCmd())
	registerDevCommands(root)

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the agentpulse version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	}
}
