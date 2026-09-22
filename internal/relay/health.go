package relay

import (
	"context"
	"net/http"
	"time"
)

// healthPath is SPEC 11.3's unauthenticated uptime check.
const healthPath = "/v1/health"

// HealthResult is what a HEAD /v1/health round trip found, for
// "agentpulse status" (BR-14, reachability only) and "agentpulse doctor"
// (BR-15, which also uses StatusCode and Date for its clock-skew check).
type HealthResult struct {
	// Reachable is true for any HTTP response at all, 2xx or not — a
	// relay that answers, even with an error status, is a reachable
	// relay; only a transport failure (network, timeout) makes this
	// false.
	Reachable  bool
	StatusCode int
	// Date is the relay's own clock, parsed from the response's Date
	// header (RFC 1123, per net/http.ParseTime), zero when absent or
	// unparseable.
	Date time.Time
}

// Health performs one unauthenticated HEAD /v1/health with ctx's
// deadline. It never returns a Go error for an ordinary unreachable
// relay (connection refused, timeout, DNS): that is HealthResult{} with
// Reachable false, which is exactly what both callers above want to
// report rather than handle as an exceptional case.
func (c *Client) Health(ctx context.Context) HealthResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.baseURL+healthPath, nil)
	if err != nil {
		return HealthResult{}
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return HealthResult{}
	}
	defer func() { _ = resp.Body.Close() }()

	result := HealthResult{Reachable: true, StatusCode: resp.StatusCode}
	if raw := resp.Header.Get("Date"); raw != "" {
		if t, err := http.ParseTime(raw); err == nil {
			result.Date = t
		}
	}
	return result
}
