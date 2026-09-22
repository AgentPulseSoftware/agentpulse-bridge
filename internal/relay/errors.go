package relay

import (
	"fmt"
	"time"
)

// Kind classifies why PostEvents stopped short of delivering every event,
// matching SPEC section 14's ERR-03 through ERR-06 rows one for one.
type Kind int

// Kind values. KindUnknown should never actually reach a caller; it exists
// so the zero value of Kind is visibly not one of the real cases.
const (
	KindUnknown Kind = iota
	// KindBadRequest is a 400 the client could not attribute to one
	// specific event (ERR-03's fallback: the offending event, when
	// identifiable, is reported via Response.Dropped instead and PostEvents
	// still returns this Kind so the caller knows the chunk stopped).
	KindBadRequest
	// KindUnauthorized is a 401 (ERR-04).
	KindUnauthorized
	// KindRateLimited is a 429 (ERR-05).
	KindRateLimited
	// KindServerError is a 5xx (ERR-06).
	KindServerError
	// KindNetwork is a transport-level failure — connection refused, DNS,
	// TLS, or similar — that never got as far as an HTTP status (ERR-06).
	KindNetwork
	// KindTimeout is the caller's context expiring, whether before a
	// request even starts or while waiting on a hung server (ERR-06,
	// NFR-02's "never blocks longer than the budget").
	KindTimeout
)

func (k Kind) String() string {
	switch k {
	case KindBadRequest:
		return "bad_request"
	case KindUnauthorized:
		return "unauthorized"
	case KindRateLimited:
		return "rate_limited"
	case KindServerError:
		return "server_error"
	case KindNetwork:
		return "network"
	case KindTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// StatusError is the error PostEvents returns when it stops before
// delivering every event in one chunk. The accompanying Response (PostEvents
// returns both) says how much progress was made across every chunk before
// this one.
//
// StatusError deliberately carries no response body text: CLAUDE.md
// and SPEC section 14 forbid logging a relay response body, and
// StatusError.Error() must be safe to pass to a debug logger. The one
// exception is RequiredBridgeVersion, a specific structured field ERR-03
// explicitly allows the bridge to record for "doctor" to
// report.
type StatusError struct {
	Kind       Kind
	StatusCode int // 0 for KindNetwork and KindTimeout, which never got a status

	RequiredBridgeVersion string

	// RetryAfter and RetryAfterValid are set for a KindRateLimited error
	// whose Retry-After header parsed as a delta-seconds value (ERR-05).
	RetryAfter      time.Duration
	RetryAfterValid bool

	// eventIndex is the index, within the chunk that failed, of the event
	// a 400 named as invalid (ERR-03) — or -1 when no single event could
	// be identified. It is unexported: PostEvents translates it into a
	// Response.DroppedEvent (a global index into the caller's own events
	// slice) before returning, which is the only form callers outside
	// this package ever see.
	eventIndex int
}

func (e *StatusError) Error() string {
	if e.StatusCode == 0 {
		return fmt.Sprintf("relay request failed (%s)", e.Kind)
	}
	return fmt.Sprintf("relay returned HTTP %d (%s)", e.StatusCode, e.Kind)
}
