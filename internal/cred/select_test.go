package cred

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// withPlatformStore overrides platformStoreFunc for the duration of one
// test, restoring the real one (and clearing Default's cache both before
// and after) on cleanup.
func withPlatformStore(t *testing.T, f func() Store) {
	t.Helper()
	orig := platformStoreFunc
	platformStoreFunc = f
	resetForTest()
	t.Cleanup(func() {
		platformStoreFunc = orig
		resetForTest()
	})
}

func TestDefaultUsesPlatformStoreWhenAvailable(t *testing.T) {
	fake := &fakeStore{}
	withPlatformStore(t, func() Store { return fake })

	got := Default()
	if got != fake {
		t.Errorf("Default() = %v, want the platform store", got)
	}
}

func TestDefaultFallsBackToFileWhenNoPlatformStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withPlatformStore(t, func() Store { return nil })

	got := Default()
	if got.Name() != "file fallback" {
		t.Errorf("Default().Name() = %q, want %q", got.Name(), "file fallback")
	}
}

func TestDefaultLogsWhenFallingBackToFile(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("AGENTPULSE_DEBUG", "")
	withPlatformStore(t, func() Store { return nil })

	_ = Default()

	data, err := os.ReadFile(filepath.Join(stateDir, "agentpulse", "bridge.log"))
	if err != nil {
		t.Fatalf("reading bridge.log: %v", err)
	}
	if !strings.Contains(string(data), "file fallback") {
		t.Errorf("bridge.log = %q, want a line about the file fallback being in use", data)
	}
}

func TestDefaultIsCachedPerProcess(t *testing.T) {
	calls := 0
	withPlatformStore(t, func() Store {
		calls++
		return &fakeStore{}
	})

	first := Default()
	second := Default()
	if calls != 1 {
		t.Errorf("platform store probed %d times, want exactly 1 (cached per process)", calls)
	}
	if first != second {
		t.Error("Default() returned different stores across calls, want the cached one reused")
	}
}

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

// TestDefaultFileFallbackPathIsUnderConfigDir is a smoke check that
// Default's fallback actually uses xdgpaths.CredentialsPath(), not some
// other location.
func TestDefaultFileFallbackPathIsUnderConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withPlatformStore(t, func() Store { return nil })

	store := Default()
	if err := store.Set("s3cr3t"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if _, err := os.Stat(xdgpaths.CredentialsPath()); err != nil {
		t.Errorf("credentials file not found at CredentialsPath(): %v", err)
	}
}
