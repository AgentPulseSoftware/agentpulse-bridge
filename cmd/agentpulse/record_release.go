//go:build !dev

package main

import "github.com/spf13/cobra"

// registerDevCommands is a no-op in release builds (built without the "dev"
// tag): "make build" must produce a binary with no "record" command, since
// fixture recording writes raw, unscrubbed session data to disk and must
// never ship to end users. See record_dev.go for the
// development-build implementation.
func registerDevCommands(root *cobra.Command) {}
