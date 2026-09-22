package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateBridgeSendsTheSchemaAndReadsTheResponse(t *testing.T) {
	var gotBody CreateBridgeRequest
	var gotAuth, gotMethod, gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"bridge_id":"brg_abc","bridge_secret":"s3cr3t","pairing_code":"7KMPQ2VZ","expires_at":"2026-09-14T12:00:00Z"}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.CreateBridge(context.Background(), CreateBridgeRequest{
		Name: "sam-macbook", OS: "darwin", Arch: "arm64", Version: "1.2.3",
	})
	if err != nil {
		t.Fatalf("CreateBridge: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/bridges" {
		t.Errorf("request was %s %s, want POST /v1/bridges", gotMethod, gotPath)
	}
	// SPEC 10.3 step 1 is unauthenticated: sending a half-built credential
	// here would be a bug.
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want no header on the unauthenticated create", gotAuth)
	}
	if gotBody.Name != "sam-macbook" || gotBody.OS != "darwin" || gotBody.Arch != "arm64" || gotBody.Version != "1.2.3" {
		t.Errorf("request body = %+v, want the four schema fields", gotBody)
	}
	want := CreateBridgeResponse{BridgeID: "brg_abc", Secret: "s3cr3t", Code: "7KMPQ2VZ", ExpiresAt: "2026-09-14T12:00:00Z"}
	if got != want {
		t.Errorf("CreateBridge = %+v, want %+v", got, want)
	}
}

func TestCreateBridgeErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		retryAfter  string
		wantKind    Kind
		wantRetry   time.Duration
		wantMessage string
	}{
		{name: "rate limited", status: 429, body: `{"error":"rate_limited"}`, retryAfter: "60", wantKind: KindRateLimited, wantRetry: 60 * time.Second},
		{name: "bad request", status: 400, body: `{"error":"bad_request"}`, wantKind: KindBadRequest},
		{name: "server error", status: 500, body: `{"error":"internal"}`, wantKind: KindServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client, err := NewClient(Config{BaseURL: srv.URL, Version: "test"})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			_, err = client.CreateBridge(context.Background(), CreateBridgeRequest{Name: "m", OS: "darwin", Arch: "arm64", Version: "test"})

			var status *StatusError
			if !errors.As(err, &status) {
				t.Fatalf("CreateBridge error = %v, want a *StatusError", err)
			}
			if status.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", status.Kind, tc.wantKind)
			}
			if tc.wantRetry != 0 && (!status.RetryAfterValid || status.RetryAfter != tc.wantRetry) {
				t.Errorf("RetryAfter = %v (valid %v), want %v", status.RetryAfter, status.RetryAfterValid, tc.wantRetry)
			}
		})
	}
}

func TestCreateBridgeRejectsAnIncompleteResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"bridge_id":"brg_abc"}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, Version: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.CreateBridge(context.Background(), CreateBridgeRequest{Name: "m", OS: "darwin", Arch: "arm64", Version: "test"}); err == nil {
		t.Error("CreateBridge accepted a response with no secret, want an error")
	}
}

func TestPairingStatus(t *testing.T) {
	tests := []struct {
		name string
		body string
		want PairingStatus
	}{
		{"waiting", `{"paired":false,"expires_at":"2026-09-14T12:00:00Z"}`, PairingStatus{ExpiresAt: "2026-09-14T12:00:00Z"}},
		{"paired", `{"paired":true,"device_name":"Sam's iPhone"}`, PairingStatus{Paired: true, DeviceName: "Sam's iPhone"}},
		// BR-18: an unknown field is ignored, not an error.
		{"unknown fields ignored", `{"paired":true,"device_name":"iPhone","surprise":{"x":1}}`, PairingStatus{Paired: true, DeviceName: "iPhone"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/bridges/me/pairing" {
					t.Errorf("request was %s %s, want GET /v1/bridges/me/pairing", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer brg_abc.s3cr3t" {
					t.Errorf("Authorization = %q, want the bridge credential", got)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client, err := NewClient(Config{BaseURL: srv.URL, BridgeID: "brg_abc", BridgeSecret: "s3cr3t", Version: "test"})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			got, err := client.PairingStatus(context.Background())
			if err != nil {
				t.Fatalf("PairingStatus: %v", err)
			}
			if got != tc.want {
				t.Errorf("PairingStatus = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRevokeSelf(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"204 from a current relay", http.StatusNoContent, false},
		{"404 from a relay without the endpoint yet", http.StatusNotFound, true},
		{"405 from a relay without the endpoint yet", http.StatusMethodNotAllowed, true},
		{"401 after the phone already removed this bridge", http.StatusUnauthorized, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/v1/bridges/me" {
					t.Errorf("request was %s %s, want DELETE /v1/bridges/me", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			client, err := NewClient(Config{BaseURL: srv.URL, BridgeID: "brg_abc", BridgeSecret: "s3cr3t", Version: "test"})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			err = client.RevokeSelf(context.Background())
			if tc.wantErr != (err != nil) {
				t.Errorf("RevokeSelf error = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}

func TestSetWatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/bridges/me/watch" {
			t.Errorf("request was %s %s, want PUT /v1/bridges/me/watch", r.Method, r.URL.Path)
		}
		var body SetWatchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Projects["hash1"].Watched != true {
			t.Errorf("body = %+v, want hash1 watched", body)
		}
		_, _ = w.Write([]byte(`{"watch_list":{"version":9,"watch_new_projects":true,"projects":{"hash1":{"watched":true}}}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, BridgeID: "brg_abc", BridgeSecret: "s3cr3t", Version: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	watched := true
	got, err := client.SetWatch(context.Background(), SetWatchRequest{Projects: map[string]ProjectEntry{"hash1": {Watched: watched}}})
	if err != nil {
		t.Fatalf("SetWatch: %v", err)
	}
	if got == nil || got.Version != 9 || !got.Projects["hash1"].Watched {
		t.Errorf("SetWatch result = %+v, want version 9 with hash1 watched", got)
	}
}
