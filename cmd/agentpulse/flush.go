package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/lockfile"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/scratch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// flushBudget is BR-03's total time budget for one "agentpulse flush" run,
// covering every retry.
const flushBudget = 5 * time.Second

// scratchSweepInterval is how old a session's scratch file must be before
// runFlush's sweep removes it (BR-05: "after 24 hours"). Moved here from
// "agentpulse hook": hook has a 50ms budget it cannot spare
// for a directory scan on every invocation.
const scratchSweepInterval = 24 * time.Hour

// pairingGracePeriod is ERR-04's amendment: for this long
// after config.PairedAt, a 401 is treated as retryable (ERR-06) rather
// than counted toward the 3-strike unpair rule, because the relay can
// briefly lag in recognising a new bridge right after pairing.
const pairingGracePeriod = 60 * time.Second

// backoffSchedule is BR-03's retry backoff for 5xx, network, timeout, and
// (inside the pairing grace period) 401 errors: 1s, then 2s, then 4s. A
// retry that cannot complete inside the remaining budget is not started.
var backoffSchedule = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

// maxRateLimitRetries bounds how many times one flush run will honor a 429's
// Retry-After before giving up, purely as a safety net against a
// misbehaving relay that always answers 429 with a tiny delay — the 5
// second budget already bounds real runs long before this would matter.
const maxRateLimitRetries = 5

// defaultRelayBaseURL is the compiled-in production relay host (SPEC
// 11.5). It can be overridden with --relay or AGENTPULSE_RELAY, which is
// what the tests and any local relay use.
const defaultRelayBaseURL = "https://relay.agentpulse.app"

func newFlushCmd() *cobra.Command {
	var relayFlag string
	cmd := &cobra.Command{
		Use:                   "flush",
		Short:                 "Drain the local event spool to the relay",
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			debug := os.Getenv("AGENTPULSE_DEBUG") == "1"
			defer func() {
				if r := recover(); r != nil {
					debugf(cmd.ErrOrStderr(), debug, "agentpulse flush: recovered from panic: %v", r)
				}
				err = nil // always exit 0 (BR-03's spirit, matching "hook"), even after a recovered panic
			}()
			runFlush(flushDeps{
				relayFlag: relayFlag,
				budget:    flushBudget,
				debug:     debug,
				stderr:    cmd.ErrOrStderr(),
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&relayFlag, "relay", "", "override the relay base URL (development only)")
	return cmd
}

// flushDeps are runFlush's dependencies, overridable so tests can shrink
// the budget (rather than waiting out a real 5 seconds), capture debug
// output without touching the real clock or os.Stderr, and — via
// credStore — supply a fake credential backend so no test ever touches
// the real macOS Keychain or Linux Secret Service. credStore
// defaults to cred.Default() (the real, once-probed-and-cached backend)
// when left nil, which is what production use always does.
type flushDeps struct {
	relayFlag string
	budget    time.Duration
	debug     bool
	stderr    io.Writer
	now       func() time.Time
	credStore cred.Store
}

// runFlush is "agentpulse flush"'s body as a plain function, so it can be
// unit tested without exec'ing a binary — the same pattern hook.go's
// runHook uses. It never returns an error and never panics past this
// function (newFlushCmd's recover() backstops that): every failure is
// swallowed and, only if debug is set, logged.
func runFlush(deps flushDeps) {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.stderr == nil {
		deps.stderr = io.Discard
	}

	// BR-03: only one flusher runs per user. If another is already
	// draining, exit 0 immediately and silently — this is the expected,
	// common outcome of a burst of hook calls each spawning a flush, not
	// a failure.
	unlock, ok, err := lockfile.TryLock(flushLockPath())
	if err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: acquiring lock: %v", err)
		return
	}
	if !ok {
		return
	}
	defer unlock()

	// BR-05's session scratch sweep runs here, once per flush, rather than
	// from "agentpulse hook" (which has a 50ms budget it cannot spare for
	// a directory scan): deletes sessions/*.json older than 24 hours,
	// regardless of whether this bridge ends up paired below.
	_ = scratch.Sweep(xdgpaths.StateDir(), scratchSweepInterval)

	cfg, cfgErr := config.Load(xdgpaths.ConfigPath())
	if cfgErr != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: loading config: %v", cfgErr)
	}
	if cfg.BridgeID == "" || cfg.BridgeID == config.UnpairedBridgeID {
		// Not paired: nothing to authenticate a flush with. This is the
		// normal state before "agentpulse pair" exists or has
		// run, and must cost nothing beyond the lock check and sweep above.
		recordUnpairedFlush(deps)
		return
	}

	credStore := deps.credStore
	if credStore == nil {
		credStore = cred.Default()
	}
	secret, secretErr := credStore.Get()
	if secretErr != nil || secret == "" {
		// No credential stored (never paired, or cleared by ERR-04's
		// 3-strike unpair): same as unpaired above, nothing to
		// authenticate a flush with.
		if secretErr != nil && !errors.Is(secretErr, cred.ErrNotFound) {
			debugf(deps.stderr, deps.debug, "agentpulse flush: reading credential: %v", secretErr)
		}
		recordUnpairedFlush(deps)
		return
	}

	baseURL, err := resolveRelayBaseURL(deps.relayFlag)
	if err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: resolving relay URL: %v", err)
		return
	}
	client, err := relay.NewClient(relay.Config{
		BaseURL:      baseURL,
		BridgeID:     cfg.BridgeID,
		BridgeSecret: secret,
		Version:      version,
	})
	if err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: building relay client: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), deps.budget)
	defer cancel()

	batch, discarded, commit, err := spool.Drain(xdgpaths.SpoolPath())
	if err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: draining spool: %v", err)
		return
	}
	if discarded > 0 {
		debugf(deps.stderr, deps.debug, "agentpulse flush: discarded %d malformed spool line(s)", discarded)
	}
	if len(batch) == 0 {
		_ = commit(nil)
		return
	}

	wlFile := watch.Load(xdgpaths.WatchListPath())
	fstate := state.Load(xdgpaths.StatePath())
	cur := batch
	retryAttempt := 0
	rateLimitRetries := 0
	outcome := state.OutcomeOK
	httpStatus := 0

runLoop:
	for len(cur) > 0 {
		if ctx.Err() != nil {
			outcome = state.OutcomeBudgetCutOff
			break
		}

		resp, sendErr := client.PostEvents(ctx, cur, wlFile.Version())
		// BR-11: whole, atomic replacement, only when newer — see
		// internal/watch.ApplyRelayList.
		wlFile.ApplyRelayList(resp.WatchList)
		if resp.Settings != nil {
			wlFile.Settings = resp.Settings
		}
		if resp.Sent > 0 {
			// ERR-04: "any successful request resets the counter to 0."
			fstate.Consecutive401 = 0
			if le := lastEventFromBatch(cur[:resp.Sent]); le != nil {
				fstate.LastEvent = le
			}
		}
		cur = removeResolved(cur, resp.Sent, resp.Dropped)

		if sendErr == nil {
			outcome = state.OutcomeOK
			break
		}

		var se *relay.StatusError
		if !errors.As(sendErr, &se) {
			debugf(deps.stderr, deps.debug, "agentpulse flush: unexpected error shape: %v", sendErr)
			outcome = state.OutcomeNetworkError
			break
		}
		httpStatus = se.StatusCode

		switch se.Kind {
		case relay.KindBadRequest:
			// ERR-03: the offending event, if the relay named one, is
			// already excluded from cur via resp.Dropped above — retry
			// the rest right away, this isn't a transient condition a
			// backoff would help with.
			outcome = state.OutcomeSchemaError
			if se.RequiredBridgeVersion != "" {
				fstate.RequiredBridgeVersion = se.RequiredBridgeVersion
			}
			// ERR-03: "log with event type only" — resp.Dropped carries
			// nothing but the index and DroppedEvent.Type (a
			// best-effort peek at the "type" field, never the payload;
			// see bestEffortType), so this is the whole event that ever
			// reaches the log.
			for _, dropped := range resp.Dropped {
				debugf(deps.stderr, deps.debug, "agentpulse flush: relay rejected event type %q (schema error), dropping it", dropped.Type)
			}
			if len(resp.Dropped) == 0 {
				// No event could be identified: nothing changed, retrying
				// immediately would just fail the same way. Leave the
				// rest spooled for the next flush run instead of looping.
				debugf(deps.stderr, deps.debug, "agentpulse flush: 400 without an identifiable event")
				break runLoop
			}

		case relay.KindUnauthorized:
			outcome = state.OutcomeUnauthorized
			if withinPairingGrace(cfg.PairedAt, deps.now()) {
				if !waitForRetry(ctx, &retryAttempt) {
					outcome = state.OutcomeBudgetCutOff
					break runLoop
				}
				continue
			}
			fstate.Consecutive401++
			debugf(deps.stderr, deps.debug, "agentpulse flush: 401 (consecutive=%d)", fstate.Consecutive401)
			if fstate.Consecutive401 >= 3 {
				fstate.UnpairedReason = "Unpaired by phone or revoked. Run `agentpulse pair`."
				if err := credStore.Delete(); err != nil {
					debugf(deps.stderr, deps.debug, "agentpulse flush: clearing credential: %v", err)
				}
			}
			break runLoop

		case relay.KindRateLimited:
			outcome = state.OutcomeRateLimited
			rateLimitRetries++
			if rateLimitRetries > maxRateLimitRetries || !se.RetryAfterValid {
				break runLoop
			}
			if !sleepWithinBudget(ctx, se.RetryAfter) {
				break runLoop
			}

		case relay.KindServerError, relay.KindNetwork, relay.KindTimeout:
			if se.Kind == relay.KindServerError {
				outcome = state.OutcomeServerError
			} else {
				outcome = state.OutcomeNetworkError
			}
			if !waitForRetry(ctx, &retryAttempt) {
				break runLoop
			}

		default:
			break runLoop
		}
	}

	fstate.LastFlush = &state.LastFlush{TS: deps.now().UTC().Format(time.RFC3339), Outcome: outcome, HTTPStatus: httpStatus}

	if err := commit(cur); err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: committing spool: %v", err)
	}
	if err := watch.Save(xdgpaths.WatchListPath(), wlFile); err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: saving watch list: %v", err)
	}
	if err := state.Save(xdgpaths.StatePath(), fstate); err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: saving state: %v", err)
	}
}

// recordUnpairedFlush sets LastFlush to OutcomeUnpaired and saves it,
// for runFlush's two early-return paths (no bridge_id, or no credential):
// both mean "there was nothing to flush with," which is what "agentpulse
// status" and "doctor" (BR-14) must see rather than whatever
// LastFlush a previous, once-paired run left behind. It is best-effort like every other
// state write in this file: a failure is logged at debug level only.
func recordUnpairedFlush(deps flushDeps) {
	fstate := state.Load(xdgpaths.StatePath())
	fstate.LastFlush = &state.LastFlush{TS: deps.now().UTC().Format(time.RFC3339), Outcome: state.OutcomeUnpaired}
	if err := state.Save(xdgpaths.StatePath(), fstate); err != nil {
		debugf(deps.stderr, deps.debug, "agentpulse flush: saving state: %v", err)
	}
}

// lastEventFromBatch parses just {session_id, ts, type} out of the last
// (chronologically most recent) spooled line in a successfully delivered
// prefix, for BR-14's "last event sent" — never anything else in the
// event, and never the event that failed if only part of the batch sent.
// A line that fails to parse (should not happen; every spooled line was
// itself built and marshaled by this same bridge) yields nil, leaving
// the previous LastEvent, if any, in place.
func lastEventFromBatch(delivered [][]byte) *state.LastEvent {
	if len(delivered) == 0 {
		return nil
	}
	var e struct {
		SessionID string `json:"session_id"`
		TS        string `json:"ts"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(delivered[len(delivered)-1], &e); err != nil {
		return nil
	}
	return &state.LastEvent{TS: e.TS, Type: e.Type, SessionID: e.SessionID}
}

func flushLockPath() string {
	return filepath.Join(xdgpaths.StateDir(), "flush.lock")
}

// resolveRelayBaseURL implements SPEC 11.5's resolution order: --relay
// flag, then AGENTPULSE_RELAY, then the compiled-in default, validated per
// SEC-01 (relay.ValidateBaseURL).
func resolveRelayBaseURL(flagValue string) (string, error) {
	raw := flagValue
	if raw == "" {
		raw = os.Getenv("AGENTPULSE_RELAY")
	}
	if raw == "" {
		raw = defaultRelayBaseURL
	}
	return relay.ValidateBaseURL(raw)
}

// withinPairingGrace reports whether now falls inside pairingGracePeriod
// after pairedAt (RFC 3339, config.PairedAt). An empty or unparseable
// pairedAt — unpaired, or a config predating this field — is never inside
// the grace period, so a 401 is handled normally (ERR-04).
func withinPairingGrace(pairedAt string, now time.Time) bool {
	if pairedAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, pairedAt)
	if err != nil {
		return false
	}
	return now.Sub(t) < pairingGracePeriod
}

// removeResolved returns the subsequence of cur that is neither part of
// the delivered prefix (cur[:sent]) nor permanently dropped, preserving
// relative order — exactly what should remain spooled after one
// PostEvents call.
func removeResolved(cur [][]byte, sent int, dropped []relay.DroppedEvent) [][]byte {
	if sent >= len(cur) {
		return nil
	}
	dropSet := make(map[int]bool, len(dropped))
	for _, d := range dropped {
		dropSet[d.Index] = true
	}
	out := make([][]byte, 0, len(cur)-sent)
	for i := sent; i < len(cur); i++ {
		if dropSet[i] {
			continue
		}
		out = append(out, cur[i])
	}
	return out
}

// waitForRetry sleeps out the next step of backoffSchedule (advancing
// *attempt), returning false without sleeping when the schedule is
// exhausted or the remaining budget can't fit the sleep — "a retry that
// cannot complete inside the remaining budget is not started" (BR-03).
func waitForRetry(ctx context.Context, attempt *int) bool {
	if *attempt >= len(backoffSchedule) {
		return false
	}
	d := backoffSchedule[*attempt]
	*attempt++
	return sleepWithinBudget(ctx, d)
}

// sleepWithinBudget sleeps for d, bounded by ctx: it returns false without
// sleeping at all when d would not fit before ctx's deadline, and false if
// ctx is canceled mid-sleep.
func sleepWithinBudget(ctx context.Context, d time.Duration) bool {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
