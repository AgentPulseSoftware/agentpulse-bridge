package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/keepawake"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/lockfile"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/logging"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/scratch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/spool"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// classifyAndSpool is the rest of BR-02's "agentpulse hook" body, beyond
// the optional dev-only recording in hook_dev.go/hook_release.go: parse
// the hook document, classify it per SPEC 7.2, and append at most one
// normalized event to the local spool; draining the spool to
// the relay happens elsewhere. Every failure here is returned for optional debug
// logging by the caller and otherwise swallowed — like every other step of
// "hook", this must never be the reason the command blocks or exits
// non-zero (BR-02).
func classifyAndSpool(raw []byte) error {
	input, err := classify.ParseHookInput(raw)
	if err != nil {
		return fmt.Errorf("parsing hook input: %w", err)
	}
	if input.SessionID == "" || input.HookEventName == "" {
		return nil // nothing to classify without these
	}

	cfg, cfgErr := config.Load(xdgpaths.ConfigPath())
	input.BridgeID = cfg.BridgeID
	input.BridgeVersion = version
	input.TaskLabelOn = cfg.TaskLabel
	input.Now = time.Now().UTC()

	stateDir := xdgpaths.StateDir()
	// Load, Classify, and Save below are a read-modify-write sequence
	// that is not atomic as a whole. Two concurrent "agentpulse hook"
	// invocations for the SAME session_id (Claude Code can fire hooks
	// close together — a fast PreToolUse/PostToolUse pair, or two tool
	// calls racing) could both Load the same on-disk scratch, classify
	// against it independently, and Save: the second Save wins and
	// silently drops whatever the first call's classification decided (a
	// counter increment, a pending marker such as PRPending/
	// CommitPending/PendingVerification, ...). spool.Append's exclusive
	// flock on the spool file prevents two events from corrupting each
	// other's bytes on that shared file, but it does not, and was never
	// meant to, serialize THIS sequence — that needs a lock scoped to one
	// session's scratch file, not the spool.
	//
	// internal/lockfile.Lock (generalized out of spool.Append's own
	// inline flock) provides exactly that: a per-session lock
	// file so two concurrent hook calls for one session serialize instead
	// of racing. Held only across this load/classify/save/delete
	// sequence, never across the network calls "agentpulse flush" makes,
	// so it cannot push a hook invocation anywhere near its 50ms budget
	// under normal conditions — the worst case is waiting out another
	// hook call's own equally-short critical section.
	// A sibling directory to sessions/, not sessions/ itself: scratch's
	// own sweep and BR-05's "scratch is gone after session_end" only ever
	// look at the scratch state files, and must keep working exactly as
	// they do today without needing to know a lock file might also live
	// there.
	sessionLockPath := filepath.Join(xdgpaths.StateDir(), "session-locks", scratch.SanitizeSessionID(input.SessionID)+".lock")
	unlockSession, lockErr := lockfile.Lock(sessionLockPath)
	if lockErr == nil {
		defer unlockSession()
	}
	// A lock error here is an I/O problem (can't create the sessions
	// directory, can't open the lock file), not contention — contention
	// just blocks Lock until the other holder finishes. Proceeding
	// unlocked in that case reverts to the earlier racy behavior rather
	// than making a local filesystem hiccup the reason "agentpulse hook"
	// drops an event (BR-02).

	state := scratch.Load(stateDir, input.SessionID)

	// BR-05's stale-scratch sweep (sessions/*.json older than 24 hours)
	// runs from "agentpulse flush", not here: "hook" has a
	// 50ms budget (BR-02) it cannot spare for a directory scan on every
	// invocation.

	event, ok := classify.Classify(input, state)

	keepAwakeErr := maybeStartKeepAwake(input, cfg, state)

	saveErr := scratch.Save(stateDir, input.SessionID, state)

	var deleteErr error
	if input.HookEventName == classify.HookSessionEnd {
		deleteErr = scratch.Delete(stateDir, input.SessionID)
	}

	var watchErr error
	if ok {
		ok, watchErr = applyWatchDecision(event, input.Now)
	}

	var spoolErr error
	if ok {
		line, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			spoolErr = fmt.Errorf("marshaling event: %w", marshalErr)
		} else {
			spoolErr = spool.Append(xdgpaths.SpoolPath(), line)
		}
	}

	return firstError(cfgErr, keepAwakeErr, saveErr, deleteErr, watchErr, spoolErr)
}

// startKeepAwake is keepawake.Start, overridable by tests (per hook.go's
// own selfExecutable pattern) so the hook integration tests can assert
// exactly when a spawn happens without starting a real process.
var startKeepAwake = keepawake.Start

// maybeStartKeepAwake is BR-16, wired inside classifyAndSpool's existing
// per-session lock so a duplicate SessionStart for the same session (the
// lock's caller already serializes those) can never race its own guard:
// spawn keep-awake only on SessionStart, only when the operator turned it
// on (cfg.KeepAwake), and only once per session — state.KeepAwakePID != 0
// is that guard, set here before the scratch.Save call that follows,
// exactly like the rest of this function's read-modify-write sequence.
//
// The PID passed is os.Getppid() and nothing else (SEC-09: no value from
// hook input). A platform or environment where keep-awake cannot run
// (keepawake.ErrNotSupported: an OS other than darwin/linux, or a Linux
// box missing systemd-inhibit/tail) is not an error — it is logged once,
// at debug level, and the session simply never gets a KeepAwakePID.
// Any other Start failure is returned like every other local failure on
// this path, folded into firstError by the caller; "hook" still exits 0
// either way (BR-02).
func maybeStartKeepAwake(input classify.HookInput, cfg config.Config, state *classify.SessionState) error {
	if input.HookEventName != classify.HookSessionStart || !cfg.KeepAwake || state.KeepAwakePID != 0 {
		return nil
	}
	pid, err := startKeepAwake(os.Getppid())
	if err == nil {
		state.KeepAwakePID = pid
		return nil
	}
	if errors.Is(err, keepawake.ErrNotSupported) {
		logging.Get().Debug("agentpulse hook: keep-awake not supported on this platform", "session_id", input.SessionID)
		return nil
	}
	return fmt.Errorf("starting keep-awake: %w", err)
}

// applyWatchDecision is BR-10, wired between classification and the spool
// append: an unwatched project's event is dropped entirely, or downgraded
// to a throttled `project_seen` (mutating event in place, keeping its
// envelope but replacing type and payload), at most once every six hours.
// A watched (or unknown, watch-new-projects-on) project's event passes
// through unchanged. Returns whether the caller should still spool event.
//
// Loading and, only when the decision needs it, saving watchlist.json is
// this function's entire local I/O cost beyond what classifyAndSpool
// already pays — BR-02's 50ms budget, cheapest exactly on the Drop path
// (internal/watch.Decide's own doc comment).
func applyWatchDecision(event *classify.Event, now time.Time) (keep bool, err error) {
	path := xdgpaths.WatchListPath()
	f := watch.Load(path)
	decision := f.Decide(event.Project.KeyHash, event.Project.Name, now)
	switch decision {
	case watch.DecisionDrop:
		return false, nil
	case watch.DecisionEmitProjectSeen:
		event.Type = classify.TypeProjectSeen
		event.Payload = classify.EmptyPayload{}
	}
	return true, watch.Save(path, f)
}

// firstError returns the first non-nil error among errs, or nil. Every
// step of classifyAndSpool runs regardless of an earlier step's failure
// (saving scratch and appending to the spool are independent concerns),
// so this exists only to give the caller one representative error to log.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
