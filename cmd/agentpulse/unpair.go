package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

func newUnpairCmd() *cobra.Command {
	var (
		relayFlag string
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "unpair",
		Short: "Disconnect this machine: remove the hooks, revoke it at the relay, delete local data",
		Long: "Removes only the hook entries this bridge added to ~/.claude/settings.json, asks the " +
			"relay to forget this machine, and deletes every file the bridge owns except its log.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnpair(cmd.Context(), unpairDeps{
				out:       cmd.OutOrStdout(),
				cmd:       cmd,
				relayFlag: relayFlag,
				yes:       yes,
				debug:     os.Getenv("AGENTPULSE_DEBUG") == "1",
				stderr:    cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation question before changing ~/.claude/settings.json")
	return cmd
}

type unpairDeps struct {
	out       io.Writer
	cmd       *cobra.Command
	relayFlag string
	yes       bool
	debug     bool
	stderr    io.Writer

	credStore cred.Store
}

// runUnpair is BR-09, in this order: hooks first (the part that
// affects the person's own file and needs their consent), then the relay,
// then local data. Each step runs even if the one before it failed, and
// the exit status reports only the hook removal, which is the step whose
// failure leaves something behind that matters. Declining that step
// counts as a failure too: exit 0 is reserved for the case where the hook
// removal succeeded, and declining did not remove anything.
func runUnpair(ctx context.Context, d unpairDeps) error {
	if d.stderr == nil {
		d.stderr = io.Discard
	}
	store := d.credStore
	if store == nil {
		store = cred.Default()
	}

	// Read the identity before anything deletes it; the revoke below needs
	// both halves of the credential: the bridge ID and the secret.
	cfgPath := xdgpaths.ConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		debugf(d.stderr, d.debug, "agentpulse unpair: reading %s failed: %v", cfgPath, err)
	}
	secret, credErr := store.Get()
	if credErr != nil && !errors.Is(credErr, cred.ErrNotFound) {
		debugf(d.stderr, d.debug, "agentpulse unpair: reading the stored secret failed: %v", credErr)
	}

	hooksRemoved, nothingToRemove, cancelled, hooksErr := removeHooks(d)
	if cancelled {
		if _, err := fmt.Fprintln(d.out, "Nothing was changed; this machine is still paired."); err != nil {
			return err
		}
		return errAlreadyReported
	}

	revoked := revokeAtRelay(ctx, d, cfg.BridgeID, secret)
	// An unreadable config.json makes cfg.BridgeID come
	// back as the unpaired placeholder (config.Load's tolerant contract),
	// which revokeAtRelay cannot tell apart from a machine that was
	// genuinely never paired — so it silently skips the revoke either
	// way. There is no bridge_id to authenticate a revoke with in this
	// case (config.json is the only place it lives), so the revoke stays
	// skipped; what changes here is the summary telling the person why,
	// instead of the generic message that reads as "there was nothing to
	// revoke."
	configUnreadable := err != nil && secret != ""
	deleted, deleteFailures := deleteLocalData(d, store)

	return printUnpairSummary(d, hooksRemoved, nothingToRemove, hooksErr, revoked, configUnreadable, deleted, deleteFailures)
}

// removeHooks is BR-09 step 1: plan the removal, show it, ask, apply.
// nothingToRemove is its own outcome, distinct from removed: a machine
// with no AgentPulse hooks has nothing to confirm or write, but that is
// not the same thing as having just removed them.
func removeHooks(d unpairDeps) (removed, nothingToRemove, cancelled bool, err error) {
	settingsPath, err := defaultSettingsPath()
	if err != nil {
		return false, false, false, err
	}
	binary, binErr := currentBinaryPath()
	if binErr != nil {
		// Without the path, Owner still recognizes "agentpulse hook" and
		// any absolute path ending in the binary's own name, which covers
		// every entry this bridge writes.
		debugf(d.stderr, d.debug, "agentpulse unpair: resolving this binary's path failed: %v", binErr)
	}

	plan, err := hooks.PlanUninstall(settingsPath, hooks.NewOwner(binary))
	if err != nil {
		// ERR-07's message shape: the file and the line, nothing written.
		return false, false, false, err
	}
	if !plan.Changed {
		if _, err := fmt.Fprintf(d.out, "No AgentPulse hooks found in %s.\n", plan.Path); err != nil {
			return false, false, false, err
		}
		return false, true, false, nil
	}

	written, err := applyPlan(d.cmd, plan, d.yes, "uninstall")
	if err != nil {
		return false, false, false, err
	}
	if !written {
		// The person said no to the only step that touches a file of
		// theirs. Treat that as cancelling the whole command rather than
		// deleting the credential behind their back.
		return false, false, true, nil
	}
	return true, false, false, nil
}

// revokeAtRelay is BR-09 step 2: best effort, 3 seconds, every failure
// ignored — including the 404 or 405 from a relay deployed before
// DELETE /v1/bridges/me existed.
func revokeAtRelay(ctx context.Context, d unpairDeps, bridgeID, secret string) bool {
	if bridgeID == "" || bridgeID == config.UnpairedBridgeID || secret == "" {
		return false
	}
	baseURL, err := resolveRelayBaseURL(d.relayFlag)
	if err != nil {
		debugf(d.stderr, d.debug, "agentpulse unpair: %v", err)
		return false
	}
	client, err := relay.NewClient(relay.Config{
		BaseURL:      baseURL,
		BridgeID:     bridgeID,
		BridgeSecret: secret,
		Version:      version,
	})
	if err != nil {
		debugf(d.stderr, d.debug, "agentpulse unpair: building the relay client failed: %v", err)
		return false
	}
	revokeCtx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	if err := client.RevokeSelf(revokeCtx); err != nil {
		debugf(d.stderr, d.debug, "agentpulse unpair: revoking at the relay failed (ignored): %v", err)
		return false
	}
	return true
}

// deleteLocalData is BR-09 step 3: the credential and every file the
// bridge owns, except bridge.log (BR-04), which is deliberately left so a
// person can still read what happened.
func deleteLocalData(d unpairDeps, store cred.Store) (deleted int, failures []string) {
	if err := store.Delete(); err != nil {
		debugf(d.stderr, d.debug, "agentpulse unpair: deleting the stored secret failed: %v", err)
		failures = append(failures, "the stored secret ("+store.Name()+")")
	} else {
		deleted++
	}

	paths := []string{
		xdgpaths.ConfigPath(),
		// The fallback credential file, in case a previous run used it
		// while a platform store is active now (BR-06).
		xdgpaths.CredentialsPath(),
		xdgpaths.StatePath(),
		xdgpaths.WatchListPath(),
		xdgpaths.SpoolPath(),
		filepath.Join(xdgpaths.StateDir(), "flush.lock"),
		xdgpaths.SessionsDir(),
		// One <session-id>.lock per session hook_classify.go has ever
		// processed (internal/lockfile); names are session ids.
		filepath.Join(xdgpaths.StateDir(), "session-locks"),
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err != nil {
			continue // already gone: not a failure, and not something to report
		}
		if err := os.RemoveAll(path); err != nil {
			debugf(d.stderr, d.debug, "agentpulse unpair: removing %s failed: %v", path, err)
			failures = append(failures, path)
			continue
		}
		deleted++
	}
	return deleted, failures
}

func printUnpairSummary(d unpairDeps, hooksRemoved, nothingToRemove bool, hooksErr error, revoked, configUnreadable bool, deleted int, failures []string) error {
	out := d.out
	if _, err := fmt.Fprintln(out, "\nSummary:"); err != nil {
		return err
	}

	switch {
	case hooksErr != nil:
		fmt.Fprintf(out, "  - Claude Code hooks: NOT removed — %v\n", hooksErr)   //nolint:errcheck // summary lines are best effort
		fmt.Fprintln(out, "    Fix that file and run `agentpulse unpair` again.") //nolint:errcheck
	case hooksRemoved:
		fmt.Fprintln(out, "  - Claude Code hooks: removed (other tools' hooks untouched)") //nolint:errcheck
	case nothingToRemove:
		fmt.Fprintln(out, "  - Claude Code hooks: nothing to remove") //nolint:errcheck
	default:
		fmt.Fprintln(out, "  - Claude Code hooks: not removed") //nolint:errcheck
	}

	switch {
	case revoked:
		fmt.Fprintln(out, "  - Relay: this machine was revoked") //nolint:errcheck
	case configUnreadable:
		fmt.Fprintln(out, "  - Relay: not revoked — this machine's local identity could not be read; remove it from the phone instead") //nolint:errcheck
	default:
		fmt.Fprintln(out, "  - Relay: not revoked (it will reject this machine's events anyway once the phone removes it)") //nolint:errcheck
	}

	fmt.Fprintf(out, "  - Local data: %d item(s) deleted\n", deleted) //nolint:errcheck
	for _, f := range failures {
		fmt.Fprintf(out, "    could not delete %s\n", f) //nolint:errcheck
	}
	fmt.Fprintf(out, "  - Kept: %s, so you can still read what happened\n", xdgpaths.LogPath()) //nolint:errcheck

	if hooksErr != nil || (!hooksRemoved && !nothingToRemove) {
		return errAlreadyReported
	}
	return nil
}
