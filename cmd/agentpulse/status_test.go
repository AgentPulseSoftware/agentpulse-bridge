package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/state"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

func TestRunStatusPairedAndReachableExitsZero(t *testing.T) {
	setTestXDGDirs(t)
	cfg := config.Default()
	cfg.BridgeID = "brg_test"
	cfg.DeviceName = "Sam's iPhone"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, out := testCmd("")
	err := runStatus(context.Background(), statusDeps{out: out, relayFlag: srv.URL})
	if err != nil {
		t.Errorf("runStatus = %v, want nil (exit 0)", err)
	}
	if !strings.Contains(out.String(), "Sam's iPhone") {
		t.Errorf("output does not name the device:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "reachable") {
		t.Errorf("output does not say reachable:\n%s", out.String())
	}
}

func TestRunStatusUnreachableExitsOne(t *testing.T) {
	setTestXDGDirs(t)
	cfg := config.Default()
	cfg.BridgeID = "brg_test"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}

	_, out := testCmd("")
	err := runStatus(context.Background(), statusDeps{out: out, relayFlag: "http://127.0.0.1:1"})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runStatus = %v, want the already-reported sentinel (exit 1)", err)
	}
	if !strings.Contains(out.String(), "unreachable") {
		t.Errorf("output does not say unreachable:\n%s", out.String())
	}
}

func TestRunStatusRevokedPrintsExactMessageAndExitsOne(t *testing.T) {
	setTestXDGDirs(t)
	cfg := config.Default()
	cfg.BridgeID = "brg_test"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	st := state.State{UnpairedReason: "Unpaired by phone or revoked. Run `agentpulse pair`."}
	if err := state.Save(xdgpaths.StatePath(), st); err != nil {
		t.Fatal(err)
	}

	_, out := testCmd("")
	err := runStatus(context.Background(), statusDeps{out: out, relayFlag: "http://127.0.0.1:1"})
	if !errors.Is(err, errAlreadyReported) {
		t.Fatalf("runStatus = %v, want the already-reported sentinel (exit 1)", err)
	}
	if strings.TrimSpace(out.String()) != "Unpaired by phone or revoked. Run `agentpulse pair`." {
		t.Errorf("output = %q, want exactly the revoked message", out.String())
	}
}

// TestRunStatusReportsSpoolFailWhenUnwritable is ERR-02's "status" half:
// a spool directory a real "agentpulse hook" cannot write to (disk full,
// permissions) must show up as FAIL naming the path, not as a healthy
// "0 event(s) queued" — spool.Depth alone reads that way for a directory
// it was never able to create the spool file in.
func TestRunStatusReportsSpoolFailWhenUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	setTestXDGDirs(t)
	cfg := config.Default()
	cfg.BridgeID = "brg_test"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}

	spoolDir := filepath.Dir(xdgpaths.SpoolPath())
	if err := os.MkdirAll(spoolDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(spoolDir, 0o700) })

	_, out := testCmd("")
	_ = runStatus(context.Background(), statusDeps{out: out, relayFlag: "http://127.0.0.1:1"})

	printed := out.String()
	if !strings.Contains(printed, "Spool: FAIL") {
		t.Errorf("output does not report the spool as FAIL:\n%s", printed)
	}
	if !strings.Contains(printed, xdgpaths.SpoolPath()) {
		t.Errorf("output does not name the spool path (ERR-02):\n%s", printed)
	}
}
