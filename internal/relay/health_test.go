package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", r.Method)
		}
		if r.URL.Path != "/v1/health" {
			t.Errorf("path = %s, want /v1/health", r.URL.Path)
		}
		w.Header().Set("Date", "Mon, 14 Sep 2026 12:00:00 GMT")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	got := c.Health(context.Background())
	if !got.Reachable || got.StatusCode != http.StatusNoContent {
		t.Errorf("Health = %+v, want Reachable true, StatusCode 204", got)
	}
	want := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if !got.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", got.Date, want)
	}
}

func TestHealthUnreachable(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1:1", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got := c.Health(ctx)
	if got.Reachable {
		t.Errorf("Health = %+v, want Reachable false", got)
	}
}
