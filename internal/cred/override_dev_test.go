//go:build dev || testcred

package cred

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultHonorsCredBackendOverrideEnv covers the seam:
// AGENTPULSE_TEST_CRED_BACKEND=file (named so it reads as a test seam,
// not a supported user setting) must force the file fallback even when
// a platform store is available, so a test that execs the real binary
// can guarantee it never touches the real Keychain/Secret Service
// without depending on the bridge being unpaired. This must also log,
// same as the natural no-platform-store fallback does (BR-06), so this
// asserts on both the selected store and the log
// line, using the same bridge.log read TestDefaultLogsWhenFallingBackToFile
// uses.
func TestDefaultHonorsCredBackendOverrideEnv(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("AGENTPULSE_DEBUG", "")
	t.Setenv(credBackendOverrideEnv, "file")
	withPlatformStore(t, func() Store {
		t.Fatal("platform store probed despite " + credBackendOverrideEnv + "=file")
		return nil
	})

	got := Default()
	if got.Name() != "file fallback" {
		t.Errorf("Default().Name() = %q, want %q", got.Name(), "file fallback")
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "agentpulse", "bridge.log"))
	if err != nil {
		t.Fatalf("reading bridge.log: %v", err)
	}
	if !strings.Contains(string(data), "file backend forced by test override") {
		t.Errorf("bridge.log = %q, want a line about the test override forcing the file backend", data)
	}
}
