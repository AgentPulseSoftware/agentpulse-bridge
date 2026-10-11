//go:build !dev && !testcred

package cred

import "testing"

// TestReleaseBuildIgnoresCredBackendOverrideEnv proves the test-only
// switch AGENTPULSE_TEST_CRED_BACKEND is not honoured in a release build:
// even set to "file", Default() runs the normal platform probe and returns
// the platform store (D93; see override_release.go). The variable name is
// written out here rather than taken from credBackendOverrideEnv, which a
// release build does not define.
func TestReleaseBuildIgnoresCredBackendOverrideEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("AGENTPULSE_TEST_CRED_BACKEND", "file")
	platform := &fakeStore{}
	withPlatformStore(t, func() Store { return platform })

	if got := Default(); got != platform {
		t.Errorf("Default() = %q, want the platform store: a release build must ignore the test override", got.Name())
	}
}
