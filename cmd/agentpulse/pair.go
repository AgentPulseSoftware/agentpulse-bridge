package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/hooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/pair"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// errAlreadyReported ends the process with status 1 without main printing
// anything further: the command has already said, in plain language, what
// happened (an expired code, a Ctrl-C, a settings file that needs fixing).
var errAlreadyReported = errors.New("already reported")

// pairingCodeLifetime is the fallback expiry used when the relay's
// expires_at cannot be parsed. SPEC 10.3 step 1 fixes the real lifetime at
// 10 minutes.
const pairingCodeLifetime = 10 * time.Minute

// revokeTimeout bounds the best-effort revoke of a superseded bridge
// identity (BR-09 uses the same budget for unpair).
const revokeTimeout = 3 * time.Second

func newPairCmd() *cobra.Command {
	var (
		relayFlag string
		yes       bool
		invert    bool
	)
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Connect this machine to the AgentPulse app on your phone",
		Long: "Registers this machine with the relay, prints a QR code to scan with the " +
			"AgentPulse app, and — once your phone has scanned it — shows the exact change " +
			"it wants to make to ~/.claude/settings.json and asks for your confirmation.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ctrl-C (and SIGTERM) cancels the poll loop rather than
			// killing the process mid-write: the pairing is abandoned,
			// but nothing half-written is left behind.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runPair(ctx, pairDeps{
				out:       cmd.OutOrStdout(),
				in:        cmd.InOrStdin(),
				cmd:       cmd,
				relayFlag: relayFlag,
				yes:       yes,
				invert:    invert,
				debug:     os.Getenv("AGENTPULSE_DEBUG") == "1",
				stderr:    cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation question before writing ~/.claude/settings.json")
	cmd.Flags().BoolVar(&invert, "qr-invert", false, "draw the QR code inverted, for a scanner that will not read light-on-dark")
	return cmd
}

// pairDeps are runPair's inputs, gathered in one place so tests can drive
// the whole command without a terminal. credStore defaults to
// cred.Default() and now to time.Now.
type pairDeps struct {
	out       io.Writer
	in        io.Reader
	cmd       *cobra.Command
	relayFlag string
	yes       bool
	invert    bool
	debug     bool
	stderr    io.Writer

	credStore    cred.Store
	now          func() time.Time
	pollInterval time.Duration
}

// runPair is BR-07 and SPEC 10.3 end to end.
func runPair(ctx context.Context, d pairDeps) error {
	if d.now == nil {
		d.now = time.Now
	}
	if d.stderr == nil {
		d.stderr = io.Discard
	}
	store := d.credStore
	if store == nil {
		store = cred.Default()
	}

	baseURL, err := resolveRelayBaseURL(d.relayFlag)
	if err != nil {
		return err
	}

	cfgPath := xdgpaths.ConfigPath()
	cfg, loadErr := config.Load(cfgPath)
	if loadErr != nil {
		fmt.Fprintf(d.out, "Note: %s could not be read (%v); pairing will rewrite it.\n\n", cfgPath, loadErr) //nolint:errcheck // a failed status line must not abort pairing
	}

	oldSecret, credErr := store.Get()
	if credErr != nil && !errors.Is(credErr, cred.ErrNotFound) {
		debugf(d.stderr, d.debug, "agentpulse pair: reading the stored secret failed: %v", credErr)
	}
	alreadyPaired := cfg.BridgeID != "" && cfg.BridgeID != config.UnpairedBridgeID && oldSecret != ""

	if alreadyPaired {
		keepGoing, err := confirmRepair(d, cfg)
		if err != nil || !keepGoing {
			return err
		}
	}

	// SPEC 10.3 step 1. This is the only relay call the bridge ever makes
	// without a credential, so it goes through a client built without one.
	anon, err := relay.NewClient(relay.Config{BaseURL: baseURL, Version: version})
	if err != nil {
		return err
	}
	createCtx, cancelCreate := context.WithTimeout(ctx, 15*time.Second)
	created, err := anon.CreateBridge(createCtx, relay.CreateBridgeRequest{
		Name:    pair.MachineName(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Version: sanitizeVersion(version),
	})
	cancelCreate()
	if err != nil {
		return describeRelayError(err, baseURL)
	}

	// Re-pairing means a brand new identity, so the one being replaced is
	// revoked before its credential is overwritten — best effort, exactly
	// like unpair's (BR-09, SEC-08).
	if alreadyPaired {
		revokeQuietly(ctx, baseURL, cfg.BridgeID, oldSecret, d)
	}

	// SPEC 10.3 step 3, before anything is printed: an interrupted pair
	// then leaves a secret the next "agentpulse pair" simply overwrites,
	// rather than a secret the relay knows and this machine does not.
	if err := store.Set(created.Secret); err != nil {
		return fmt.Errorf("storing the bridge secret in the %s: %w", store.Name(), err)
	}

	deadline := expiryOf(created.ExpiresAt, d.now())
	if err := printPairingCode(d, created.Code, deadline); err != nil {
		return err
	}

	client, err := relay.NewClient(relay.Config{
		BaseURL:      baseURL,
		BridgeID:     created.BridgeID,
		BridgeSecret: created.Secret,
		Version:      version,
	})
	if err != nil {
		return err
	}

	progress, clear := newProgressLine(d)
	deviceName, err := pair.Poll(ctx, client, pair.PollOptions{
		Deadline: deadline,
		Interval: d.pollInterval,
		Start:    d.now(),
		Now:      d.now,
		Progress: progress,
		Debug: func(format string, args ...any) {
			debugf(d.stderr, d.debug, "agentpulse pair: "+format, args...)
		},
	})
	clear()
	if err != nil {
		return describePollError(err, d)
	}

	// The secret is stored a second time on purpose: while a re-pair was
	// waiting, a "agentpulse flush" running with the *previous* bridge id
	// could have collected ERR-04's three 401s and cleared the credential
	// store out from under us. Writing it again costs nothing and makes a
	// successful pair always end with a usable credential.
	if err := store.Set(created.Secret); err != nil {
		return fmt.Errorf("storing the bridge secret in the %s: %w", store.Name(), err)
	}

	cfg.BridgeID = created.BridgeID
	cfg.PairedAt = d.now().UTC().Format(time.RFC3339)
	cfg.DeviceName = deviceName
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("saving %s: %w", cfgPath, err)
	}

	return installHooks(d, deviceName)
}

// confirmRepair implements SPEC 10.3 step 4: re-running pair on a paired
// bridge offers a re-pair or an exit, and never silently reuses the old
// identity. --yes chooses re-pair.
func confirmRepair(d pairDeps, cfg config.Config) (bool, error) {
	with := "your phone"
	if cfg.DeviceName != "" {
		with = cfg.DeviceName
	}
	if _, err := fmt.Fprintf(d.out,
		"This machine is already paired with %s.\nPairing again gives it a new identity and revokes the current one; the phone will need to scan a new code.\n",
		with); err != nil {
		return false, err
	}
	if d.yes {
		return true, nil
	}
	if !confirm(d.cmd) {
		_, err := fmt.Fprintln(d.out, "Left as it is; nothing changed.")
		return false, err
	}
	return true, nil
}

// printPairingCode prints the QR code, the code as plain text, and when it
// expires (BR-07).
func printPairingCode(d pairDeps, code string, deadline time.Time) error {
	art, err := pair.Render(pair.Payload(code), d.invert)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(d.out,
		"\nScan this with the AgentPulse app on your phone:\n\n%s\nOr type this code into the app: %s\nThe code expires at %s (in %s).\n\n",
		art, code, deadline.Local().Format("15:04:05"), roundedDuration(time.Until(deadline)))
	return err
}

// newProgressLine returns Poll's progress callback and a matching clear
// func, sharing the printed line's rune length between them: progress
// rewrites one status line in place with a carriage return, never a new
// line per poll (BR-07), and clear wipes exactly what was last printed.
// Hardcoding a width would leave stray characters on screen whenever
// roundedDuration's text ran longer than the guess, and overshoot on
// every shorter one; measuring instead avoids both.
func newProgressLine(d pairDeps) (progress func(time.Duration), clear func()) {
	var lastLen int
	progress = func(remaining time.Duration) {
		line := fmt.Sprintf("Waiting for your phone… %s left ", roundedDuration(remaining))
		fmt.Fprint(d.out, "\r", line) //nolint:errcheck // a status line is never worth failing the command for
		lastLen = len([]rune(line))
	}
	clear = func() {
		fmt.Fprint(d.out, "\r", strings.Repeat(" ", lastLen), "\r") //nolint:errcheck // cosmetic
	}
	return progress, clear
}

// installHooks is BR-07's second half and BR-08: build the eight entries,
// show the diff, ask, write.
func installHooks(d pairDeps, deviceName string) error {
	if _, err := fmt.Fprintf(d.out, "Paired with %s.\n\n", displayName(deviceName)); err != nil {
		return err
	}

	settingsPath, err := defaultSettingsPath()
	if err != nil {
		return err
	}
	binary, err := currentBinaryPath()
	if err != nil {
		return err
	}
	plan, err := hooks.PlanInstall(settingsPath, hooks.NewOwner(binary), hookEntries(hookCommand(binary)))
	if err != nil {
		// ERR-07: the message already names the file and the line. Nothing
		// has been written to it. The pairing itself stands.
		fmt.Fprintf(d.out, "Your Claude Code settings could not be read: %v\nNothing was written to it. This machine is paired; fix that file and run `agentpulse pair` again to register the hooks.\n", err) //nolint:errcheck // we are already returning a failure
		return errAlreadyReported
	}

	written, err := applyPlan(d.cmd, plan, d.yes, "install")
	if err != nil {
		return err
	}
	if !written && plan.Changed {
		_, err := fmt.Fprintln(d.out, "The hooks were not registered, so nothing will be reported to your phone yet. Run `agentpulse pair` again when you are ready.")
		return err
	}

	_, err = fmt.Fprintf(d.out,
		"\nDone. %d Claude Code hooks registered.\nNext step: start a Claude Code session.\n",
		len(claudehooks.BR08Events))
	return err
}

// revokeQuietly asks the relay to forget a bridge identity this machine is
// replacing (SEC-08). Every failure is a debug line and nothing more:
// BR-09's best-effort rule applies here for the same reason it applies to
// unpair — the local change must go through regardless.
func revokeQuietly(ctx context.Context, baseURL, bridgeID, secret string, d pairDeps) {
	if bridgeID == "" || secret == "" {
		return
	}
	client, err := relay.NewClient(relay.Config{
		BaseURL:      baseURL,
		BridgeID:     bridgeID,
		BridgeSecret: secret,
		Version:      version,
	})
	if err != nil {
		debugf(d.stderr, d.debug, "agentpulse pair: building the revoke client failed: %v", err)
		return
	}
	revokeCtx, cancel := context.WithTimeout(ctx, revokeTimeout)
	defer cancel()
	if err := client.RevokeSelf(revokeCtx); err != nil {
		debugf(d.stderr, d.debug, "agentpulse pair: revoking the previous bridge failed (ignored): %v", err)
	}
}

// describePollError turns Poll's outcome into one plain sentence and, for
// the two expected endings, a printed message plus errAlreadyReported so
// main does not print it a second time.
func describePollError(err error, d pairDeps) error {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(d.out, "\nStopped. This machine is not paired; run `agentpulse pair` to start again.") //nolint:errcheck // already failing
		return errAlreadyReported
	case errors.Is(err, pair.ErrExpired):
		fmt.Fprintln(d.out, "That code expired before your phone scanned it. Run `agentpulse pair` again.") //nolint:errcheck // already failing
		return errAlreadyReported
	case errors.Is(err, pair.ErrRejected):
		fmt.Fprintln(d.out, "The relay no longer recognizes this pairing. Run `agentpulse pair` again.") //nolint:errcheck // already failing
		return errAlreadyReported
	default:
		return err
	}
}

// describeRelayError gives the two failures a person can actually act on a
// sentence of their own, and passes everything else through. It never
// includes a response body (SEC-06's spirit on the bridge side).
func describeRelayError(err error, baseURL string) error {
	var status *relay.StatusError
	if !errors.As(err, &status) {
		return err
	}
	switch status.Kind {
	case relay.KindRateLimited:
		if status.RetryAfterValid {
			return fmt.Errorf("the relay is rate limiting this address; try again in %s", roundedDuration(status.RetryAfter))
		}
		return errors.New("the relay is rate limiting this address; try again in a few minutes")
	case relay.KindNetwork, relay.KindTimeout:
		return fmt.Errorf("could not reach the relay at %s; check your network and try again", baseURL)
	default:
		return fmt.Errorf("registering this machine with the relay failed: %w", err)
	}
}

// sanitizeVersion shapes main.version to the relay's bridge_version schema
// ([0-9A-Za-z.+-], 1 to 32 characters), so a locally built binary with an
// unusual version string cannot turn pairing into a 400.
func sanitizeVersion(v string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			return r
		case r == '.' || r == '+' || r == '-':
			return r
		default:
			return '-'
		}
	}, v)
	if len(cleaned) > 32 {
		cleaned = cleaned[:32]
	}
	if cleaned == "" {
		return "dev"
	}
	return cleaned
}

// expiryOf parses the relay's expires_at, falling back to SPEC 10.3's
// 10-minute lifetime if it is missing or unparseable.
func expiryOf(expiresAt string, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, expiresAt); err == nil {
		return t
	}
	return now.Add(pairingCodeLifetime)
}

// roundedDuration formats a duration the way a person reads a countdown:
// whole seconds, never a negative one.
func roundedDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// displayName is the phone's name, or a neutral stand-in when the relay
// did not send one.
func displayName(deviceName string) string {
	if strings.TrimSpace(deviceName) == "" {
		return "your phone"
	}
	return deviceName
}
