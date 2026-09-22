package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/scrub"
)

// newScrubCmd builds "agentpulse scrub <in-dir> <out-dir>" (SPEC 9.4, 18):
// it turns a directory of raw recorded hook JSON documents into a
// fixture-safe copy, then self-checks the result for leaked paths or
// oversized lines before reporting success.
func newScrubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scrub <in-dir> <out-dir>",
		Short: "Scrub a directory of recorded hook JSON into fixture-safe copies",
		Args:  cobra.ExactArgs(2),
		// A scrub failure (most likely the self-check catching a leak)
		// should print only its message, not cobra's usage block. Also set
		// at the root, which alone would cover this; set again here to be
		// explicit for the one command whose failure mode (a possible
		// privacy leak) most needs a clean, unambiguous message.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			inDir, outDir := args[0], args[1]
			report, err := scrub.ScrubDir(inDir, outDir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "Scrubbed %d file(s) from %s into %s.\n",
				report.FilesScrubbed, inDir, outDir); err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, "Self-check passed: no /Users/, /home/, recorded cwd, or line over 200 characters found.")
			return err
		},
	}
}
