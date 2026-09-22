package pair

import (
	"context"
	"errors"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
)

// DefaultPollInterval is SPEC 10.3 step 2's "every 2 s". The relay's
// pairing-poll budget refills at two requests a second, so this rate never
// approaches it even with several machines pairing from one address.
const DefaultPollInterval = 2 * time.Second

// DefaultAuthGrace is how long after the bridge was created a 401 from the
// poll is treated as retryable rather than fatal. The relay can briefly
// lag in recognising a new bridge, so for a moment right after the phone
// redeems the code a poll can be refused; "agentpulse
// flush" tolerates the same window for the same reason (ERR-04's
// amendment).
const DefaultAuthGrace = 60 * time.Second

// pollRequestTimeout bounds one poll request, so a relay that accepts a
// connection and then never answers costs one interval, not the whole
// ten minutes.
const pollRequestTimeout = 10 * time.Second

// ErrExpired is returned when the pairing code's expiry passes before a
// phone redeems it (SPEC 10.3 step 1: codes live 10 minutes).
var ErrExpired = errors.New("the pairing code expired")

// ErrRejected is returned when the relay refuses this bridge's credential
// outside the grace window — the code was redeemed by nobody and has since
// been forgotten, or the bridge was revoked from the phone (SEC-08).
var ErrRejected = errors.New("the relay no longer recognizes this pairing")

// StatusPoller is the one relay call Poll makes; *relay.Client implements
// it. It is an interface so the poll loop can be tested against a fake
// relay without a socket.
type StatusPoller interface {
	PairingStatus(ctx context.Context) (relay.PairingStatus, error)
}

// PollOptions configures Poll. Every field has a working default except
// Deadline, which the caller takes from the relay's expires_at.
type PollOptions struct {
	// Deadline is when the pairing code expires.
	Deadline time.Time
	// Interval is the gap between polls (DefaultPollInterval when zero).
	Interval time.Duration
	// AuthGrace is how long from Start a 401 is retried
	// (DefaultAuthGrace when zero).
	AuthGrace time.Duration
	// Start is when the pairing code was issued; the grace window is
	// measured from it. Defaults to the first call to Now.
	Start time.Time

	// Now and Sleep are the clock, injected so tests run instantly.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	// Progress, when set, is called before each wait with the time left
	// before the code expires. The caller redraws one status line with it
	// (BR-07: "a single updating status line, not a new line per poll").
	Progress func(remaining time.Duration)
	// Debug, when set, receives one line per retryable failure. It must
	// never be handed a secret or a response body.
	Debug func(format string, args ...any)
}

// Poll runs SPEC 10.3 step 2: poll until the phone has redeemed the code,
// the code expires, the relay rejects the credential outright, or ctx is
// cancelled (Ctrl-C). It returns the phone's device name on success.
//
// Retryable failures — a 429 (whose Retry-After is honored), a 5xx, a
// network error, a request timeout, and a 401 inside the grace window —
// are logged through Debug and tried again until the deadline. Nothing
// about them is printed, because the person is looking at a QR code and a
// countdown, not a log.
func Poll(ctx context.Context, poller StatusPoller, opts PollOptions) (deviceName string, err error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	grace := opts.AuthGrace
	if grace <= 0 {
		grace = DefaultAuthGrace
	}
	start := opts.Start
	if start.IsZero() {
		start = now()
	}
	debug := opts.Debug
	if debug == nil {
		debug = func(string, ...any) {}
	}

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !now().Before(opts.Deadline) {
			return "", ErrExpired
		}

		wait := interval
		status, err := pollOnce(ctx, poller)
		switch {
		case err == nil && status.Paired:
			return status.DeviceName, nil
		case err == nil:
			// Still waiting for the phone to redeem the code.
		default:
			retryAfter, fatal := classify(err, now().Sub(start) < grace)
			if fatal != nil {
				return "", fatal
			}
			debug("pairing poll failed, retrying: %v", err)
			if retryAfter > wait {
				wait = retryAfter
			}
		}

		remaining := opts.Deadline.Sub(now())
		if opts.Progress != nil {
			opts.Progress(remaining)
		}
		if remaining <= 0 {
			return "", ErrExpired
		}
		if remaining < wait {
			wait = remaining
		}
		if err := sleep(ctx, wait); err != nil {
			return "", err
		}
	}
}

// pollOnce makes one request with its own timeout, so one hung response
// cannot stall the loop.
func pollOnce(ctx context.Context, poller StatusPoller) (relay.PairingStatus, error) {
	reqCtx, cancel := context.WithTimeout(ctx, pollRequestTimeout)
	defer cancel()
	return poller.PairingStatus(reqCtx)
}

// classify decides what a failed poll means: an extra delay before the
// next attempt, or the error to give up with. A 401 is fatal only outside
// the grace window; everything else is retried until the code expires,
// which is the bound the whole loop already has.
func classify(err error, inGrace bool) (retryAfter time.Duration, fatal error) {
	var status *relay.StatusError
	if !errors.As(err, &status) {
		return 0, nil // an unexpected error shape: retry, the deadline bounds us
	}
	switch status.Kind {
	case relay.KindUnauthorized:
		if inGrace {
			return 0, nil
		}
		return 0, ErrRejected
	case relay.KindRateLimited:
		if status.RetryAfterValid {
			return status.RetryAfter, nil
		}
		return 0, nil
	default:
		return 0, nil
	}
}

// sleepCtx waits for d, or returns ctx's error if the person pressed
// Ctrl-C first. The timer is always stopped, so a cancelled pair leaves no
// runaway goroutine behind.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
