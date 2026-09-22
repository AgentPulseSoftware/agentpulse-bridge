package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The three pairing endpoints from SPEC 11.3, relative to the relay's base
// URL. SEC-14: these paths are compiled in and joined to the configured
// base URL; no URL is ever taken from a relay response.
const (
	bridgesPath    = "/v1/bridges"
	pairingPath    = "/v1/bridges/me/pairing"
	revokeSelfPath = "/v1/bridges/me"
)

// CreateBridgeRequest is the body of SPEC 10.3 step 1's unauthenticated
// POST /v1/bridges. Every field is validated by the relay's schema: Name is
// 1 to 64 characters with no path separator, OS is "darwin" or "linux",
// Arch is "amd64" or "arm64", and Version matches [0-9A-Za-z.+-]{1,32}.
type CreateBridgeRequest struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"bridge_version"`
}

// CreateBridgeResponse is SPEC 10.3 step 1's 201 body. Secret is the only
// copy of the bridge secret that will ever exist outside the operator's
// credential store (SEC-02: "shown once"), so the caller must store it
// before doing anything else that can fail.
type CreateBridgeResponse struct {
	BridgeID  string `json:"bridge_id"`
	Secret    string `json:"bridge_secret"`
	Code      string `json:"pairing_code"`
	ExpiresAt string `json:"expires_at"`
}

// PairingStatus is SPEC 10.3 step 2's poll response: either
// {"paired":false,"expires_at":...} while the code is still waiting to be
// redeemed, or {"paired":true,"device_name":...} once a phone has redeemed
// it. DeviceName is free text chosen on the phone; callers print it but
// never act on it (BR-18).
type PairingStatus struct {
	Paired     bool   `json:"paired"`
	DeviceName string `json:"device_name"`
	ExpiresAt  string `json:"expires_at"`
}

// CreateBridge performs SPEC 10.3 step 1. It is the one relay call the
// bridge makes without a credential, so it is served by a Client built
// with an empty BridgeID and BridgeSecret; no Authorization header is sent.
//
// A 429 (RL-04's IP limit) comes back as a *StatusError with
// KindRateLimited and, when the relay sent a delta-seconds Retry-After, the
// delay it named.
func (c *Client) CreateBridge(ctx context.Context, req CreateBridgeRequest) (CreateBridgeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return CreateBridgeResponse{}, fmt.Errorf("encoding bridge registration: %w", err)
	}
	var out CreateBridgeResponse
	if err := c.do(ctx, http.MethodPost, bridgesPath, body, false, &out); err != nil {
		return CreateBridgeResponse{}, err
	}
	if out.BridgeID == "" || out.Secret == "" || out.Code == "" {
		return CreateBridgeResponse{}, fmt.Errorf("relay returned an incomplete bridge registration")
	}
	return out, nil
}

// PairingStatus performs SPEC 10.3 step 2's poll with bridge auth. A 401 is
// returned as a *StatusError with KindUnauthorized; the caller decides
// whether that is fatal (see internal/pair's grace period).
func (c *Client) PairingStatus(ctx context.Context) (PairingStatus, error) {
	var out PairingStatus
	if err := c.do(ctx, http.MethodGet, pairingPath, nil, true, &out); err != nil {
		return PairingStatus{}, err
	}
	return out, nil
}

// RevokeSelf performs DELETE /v1/bridges/me (SEC-08), which invalidates
// this bridge's secret at the relay. "agentpulse unpair" calls it
// best-effort: every error, including the 404 or 405 an older relay
// deployment answers with, is the caller's to log and ignore.
func (c *Client) RevokeSelf(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, revokeSelfPath, nil, true, nil)
}

// watchPath is PUT /v1/bridges/me/watch (BR-12).
const watchPath = "/v1/bridges/me/watch"

// SetWatchRequest is the CLI's half of BR-10/BR-12's watch list, matching
// the relay's request schema exactly (a strict schema: an unrecognized
// field is rejected, so this must never grow one the relay doesn't know). Both fields are optional so one call can name just the
// project(s) it is changing, or just the watch-new-projects flag.
type SetWatchRequest struct {
	WatchNewProjects *bool                   `json:"watch_new_projects,omitempty"`
	Projects         map[string]ProjectEntry `json:"projects,omitempty"`
}

// SetWatch performs BR-12's "agentpulse projects" sync: the relay applies
// req and returns the resulting watch list, which the caller should feed
// straight to (*watch.File).ApplyRelayList so the local copy adopts the
// relay's own version counter rather than guessing at one.
func (c *Client) SetWatch(ctx context.Context, req SetWatchRequest) (*WatchList, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding watch update: %w", err)
	}
	var out struct {
		WatchList *WatchList `json:"watch_list"`
	}
	if err := c.do(ctx, http.MethodPut, watchPath, body, true, &out); err != nil {
		return nil, err
	}
	return out.WatchList, nil
}

// do sends one small JSON request and decodes a small JSON response,
// classifying failures exactly as postChunk does so every caller in this
// package sees the same *StatusError kinds. A 2xx with an empty or
// unreadable body (the revoke's 204) decodes into nothing and is a
// success. out may be nil when no body is expected.
func (c *Client) do(ctx context.Context, method, path string, body []byte, auth bool, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.bridgeID+"."+c.bridgeSecret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &StatusError{Kind: KindTimeout}
		}
		return &StatusError{Kind: KindNetwork}
	}
	defer func() { _ = resp.Body.Close() }()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || readErr != nil || len(bytes.TrimSpace(data)) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("relay sent a response this bridge could not read")
		}
		return nil
	}

	switch resp.StatusCode {
	case http.StatusBadRequest:
		return &StatusError{Kind: KindBadRequest, StatusCode: resp.StatusCode, eventIndex: -1}
	case http.StatusUnauthorized:
		return &StatusError{Kind: KindUnauthorized, StatusCode: resp.StatusCode}
	case http.StatusTooManyRequests:
		d, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &StatusError{Kind: KindRateLimited, StatusCode: resp.StatusCode, RetryAfter: d, RetryAfterValid: ok}
	default:
		return &StatusError{Kind: KindServerError, StatusCode: resp.StatusCode}
	}
}
