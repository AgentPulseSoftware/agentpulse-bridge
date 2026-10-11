//go:build dev || testcred

package cred

import (
	"os"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/logging"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// credBackendOverrideEnv forces Default() straight to the file fallback,
// skipping the platform probe entirely, when set to "file". It exists only
// so a test that execs a built binary can guarantee that binary never
// shells out to the real macOS Keychain or Linux Secret Service.
//
// It is compiled in only with the "dev" build tag (make build-dev) or the
// "testcred" tag (the binaries the cmd/agentpulse tests build and exec).
// A release build never contains it (see override_release.go) — so no
// environment setting can move a real user's credential out of the
// platform store.
const credBackendOverrideEnv = "AGENTPULSE_TEST_CRED_BACKEND"

// forcedStore returns the file fallback when credBackendOverrideEnv is
// "file", logging that it did, and nil otherwise.
func forcedStore() Store {
	if os.Getenv(credBackendOverrideEnv) != "file" {
		return nil
	}
	logging.Get().Info("agentpulse cred: file backend forced by test override, using file fallback",
		"path", xdgpaths.CredentialsPath())
	return newFileStore(xdgpaths.CredentialsPath())
}
