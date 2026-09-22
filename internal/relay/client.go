// Package relay is the bridge's HTTP client for the AgentPulse relay
// (SPEC 11.3 `POST /v1/bridges/me/events`; RL-03/RL-05/RL-20; SEC-01). It never follows a redirect, never keeps a cookie jar, and
// never exposes anything from a relay response beyond the strict Response
// type in types.go (BR-18): unknown response fields are ignored, and
// nothing here ever executes, evaluates, or opens content the relay sends
// back.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// eventsPath is SPEC 11.3's event batch endpoint, relative to the relay's
// base URL.
const eventsPath = "/v1/bridges/me/events"

// maxResponseBodyBytes bounds how much of a relay response this client
// will ever read: the relay's own bodies are tiny (SPEC 10.1's batch
// response), and a relay (or anything impersonating one) sending an
// unbounded body must never be able to make the bridge allocate without
// limit (BR-18's "never trust a relay response" in spirit).
const maxResponseBodyBytes = 1 << 20 // 1 MiB

// Config configures a Client.
type Config struct {
	// BaseURL is the relay's origin, e.g. "https://relay.agentpulse.app"
	// or "http://localhost:8787" for a relay running locally (SEC-01). Pass it
	// through ValidateBaseURL (or let NewClient do so) before use.
	BaseURL string
	// BridgeID and BridgeSecret form the Authorization header credential:
	// "Bearer <BridgeID>.<BridgeSecret>" (RL-03): bearer credentials are
	// <id>.<secret>, and only the secret is ever hashed at rest.
	BridgeID     string
	BridgeSecret string
	// Version is the bridge's own version, sent as "agentpulse/<Version>"
	// in the User-Agent header.
	Version string
	// HTTPClient, when nil, defaults to one with no redirect-following and
	// no cookie jar (BR-18/SEC-14: the bridge never follows a URL a relay
	// response hands it, and carries no session state across requests).
	HTTPClient *http.Client
}

// Client posts event batches to the relay's events endpoint (SPEC 11.3).
type Client struct {
	baseURL      string
	bridgeID     string
	bridgeSecret string
	userAgent    string
	http         *http.Client
}

// NewClient builds a Client from cfg, validating cfg.BaseURL per
// ValidateBaseURL.
func NewClient(cfg Config) (*Client, error) {
	base, err := ValidateBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{
		baseURL:      base,
		bridgeID:     cfg.BridgeID,
		bridgeSecret: cfg.BridgeSecret,
		userAgent:    "agentpulse/" + cfg.Version,
		http:         httpClient,
	}, nil
}

// ValidateBaseURL parses raw as a relay base URL and rejects any scheme
// but https, except http://localhost and http://127.0.0.1 for a relay
// running locally (SEC-01, SPEC 11.5). It returns raw with any trailing
// slash trimmed.
func ValidateBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid relay URL: %w", err)
	}
	host := u.Hostname()
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1"):
	default:
		return "", fmt.Errorf("relay URL %q must use https (http is only allowed for localhost/127.0.0.1)", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// PostEvents sends events to POST {base}/v1/bridges/me/events (SPEC 11.3),
// chunking into requests of at most MaxEventsPerChunk events and
// MaxChunkBytes of encoded body (RL-05), sending chunks sequentially and
// stopping at the first chunk that does not deliver successfully. See
// Response's doc comment for exactly what "delivered" covers, and errors.go
// for how the returned error (always a *StatusError when non-nil)
// classifies why it stopped.
//
// watchListVersion is sent as every chunk's watch_list_version; the caller
// is expected to have read it from the last WatchList this client (or a
// previous run) returned.
func (c *Client) PostEvents(ctx context.Context, events [][]byte, watchListVersion int) (Response, error) {
	var result Response
	offset := 0
	for offset < len(events) {
		if err := ctx.Err(); err != nil {
			return result, &StatusError{Kind: KindTimeout}
		}

		chunk, consumed, oversized := nextChunk(events[offset:])
		for _, oi := range oversized {
			idx := offset + oi
			result.Dropped = append(result.Dropped, DroppedEvent{Index: idx, Type: bestEffortType(events[idx])})
		}

		if len(chunk) == 0 {
			// Only oversized events were in front of us this round;
			// they're already recorded above. Move past them and keep
			// going — nextChunk guarantees consumed > 0 whenever
			// events[offset:] is non-empty, so this always terminates.
			offset += consumed
			result.Sent = offset
			continue
		}

		resp, err := c.postChunk(ctx, chunk, watchListVersion)
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) && se.Kind == KindBadRequest && se.eventIndex >= 0 && se.eventIndex < len(chunk) {
				badIdx := offset + se.eventIndex
				result.Dropped = append(result.Dropped, DroppedEvent{Index: badIdx, Type: bestEffortType(events[badIdx])})
			}
			return result, err
		}

		offset += consumed
		result.Sent = offset
		result.Accepted += resp.accepted
		result.Duplicates += resp.duplicates
		if resp.watchList != nil {
			result.WatchList = resp.watchList
		}
		if resp.settings != nil {
			result.Settings = resp.settings
		}
	}
	return result, nil
}

// chunkResult is postChunk's success return: one HTTP request's worth of
// the batch response body (types.go's okBody), unpacked.
type chunkResult struct {
	accepted   int
	duplicates int
	watchList  *WatchList
	settings   *Settings
}

// postChunk sends exactly one HTTP request for chunk (which the caller
// guarantees already satisfies MaxEventsPerChunk/MaxChunkBytes) and
// classifies the result.
func (c *Client) postChunk(ctx context.Context, chunk [][]byte, watchListVersion int) (chunkResult, error) {
	raw := make([]json.RawMessage, len(chunk))
	for i, e := range chunk {
		raw[i] = json.RawMessage(e)
	}
	body, err := json.Marshal(struct {
		Events           []json.RawMessage `json:"events"`
		WatchListVersion int               `json:"watch_list_version"`
	}{Events: raw, WatchListVersion: watchListVersion})
	if err != nil {
		return chunkResult{}, fmt.Errorf("encoding event batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+eventsPath, bytes.NewReader(body))
	if err != nil {
		return chunkResult{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.bridgeID+"."+c.bridgeSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return chunkResult{}, &StatusError{Kind: KindTimeout}
		}
		return chunkResult{}, &StatusError{Kind: KindNetwork}
	}
	defer func() { _ = resp.Body.Close() }()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var ok okBody
		if readErr == nil {
			_ = json.Unmarshal(data, &ok) // tolerant: a malformed 2xx body just yields zero counts, never an error
		}
		return chunkResult{accepted: ok.Accepted, duplicates: ok.Duplicates, watchList: ok.WatchList, settings: ok.Settings}, nil
	}

	var eb errorBody
	if readErr == nil {
		_ = json.Unmarshal(data, &eb)
	}

	switch resp.StatusCode {
	case http.StatusBadRequest:
		se := &StatusError{Kind: KindBadRequest, StatusCode: resp.StatusCode, RequiredBridgeVersion: eb.RequiredBridgeVersion, eventIndex: -1}
		if idx, ok := parseEventIndex(eb.Detail); ok {
			se.eventIndex = idx
		}
		return chunkResult{}, se
	case http.StatusUnauthorized:
		return chunkResult{}, &StatusError{Kind: KindUnauthorized, StatusCode: resp.StatusCode}
	case http.StatusTooManyRequests:
		d, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
		return chunkResult{}, &StatusError{Kind: KindRateLimited, StatusCode: resp.StatusCode, RetryAfter: d, RetryAfterValid: ok}
	default:
		return chunkResult{}, &StatusError{Kind: KindServerError, StatusCode: resp.StatusCode}
	}
}

// parseRetryAfter parses an HTTP Retry-After header as a delta-seconds
// value (ERR-05: "when it is a delta-seconds value"; the HTTP-date form is
// not honored — the relay only ever sends delta-seconds for RL-04's rate
// limit, so this is not a gap in practice).
func parseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// bestEffortType extracts an event's "type" field for logging a dropped
// event without ever logging the event itself (BR-19/SEC-06 in spirit):
// just enough structure to say what kind of event was lost, nothing else.
// A parse failure yields "" rather than an error — this is a best-effort
// diagnostic, never load-bearing.
func bestEffortType(event []byte) string {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(event, &v)
	return v.Type
}
