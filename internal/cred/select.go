package cred

import (
	"os"
	"sync"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/logging"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// credBackendOverrideEnv forces Default() straight to the file fallback,
// skipping the platform probe entirely, when set to "file". This exists
// only so a test that execs the real binary can guarantee it never
// shells out to the real macOS Keychain or
// Linux Secret Service, without relying on the incidental fact that an
// unpaired bridge never calls Default() at all. Nothing in this repo
// sets it outside tests; "pair", "doctor", and every other real command
// path always does the normal probe.
//
// Named AGENTPULSE_TEST_CRED_BACKEND, not AGENTPULSE_CRED_BACKEND, so the
// name itself makes clear this is a test seam, not a supported way for a
// real user to pick a backend. It is honoured
// in every build, including release binaries — that's fine, since it can
// only ever select the same file fallback path a missing platform store
// would already fall back to (xdgpaths.CredentialsPath()); it never lets
// the caller choose an arbitrary path.
const credBackendOverrideEnv = "AGENTPULSE_TEST_CRED_BACKEND"

// platformStore returns this OS's preferred backend if it is available,
// or nil if none is (an unsupported OS, or the platform's own tool isn't
// present) — see select_darwin.go, select_linux.go, select_other.go.
// Default falls back to the file backend whenever this returns nil.
var platformStoreFunc = platformStore

var (
	mu       sync.Mutex
	selected Store
	probed   bool
)

// Default selects the bridge's credential backend once per process and
// caches it (BR-06: "backend selection is probed once and cached per
// process"): the platform's own store (macOS Keychain, Linux Secret
// Service) when available, otherwise the file fallback — which logs,
// exactly once, that it is in use.
func Default() Store {
	mu.Lock()
	defer mu.Unlock()
	if probed {
		return selected
	}
	selected = probeLocked()
	probed = true
	return selected
}

// resetForTest clears the cached selection so a test can force a fresh
// probe under different conditions (an overridden securityBinary /
// secretToolBinary, or a different XDG_CONFIG_HOME). Only called from
// this package's own tests.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	selected = nil
	probed = false
}

func probeLocked() Store {
	if os.Getenv(credBackendOverrideEnv) == "file" {
		logging.Get().Info("agentpulse cred: file backend forced by test override, using file fallback",
			"path", xdgpaths.CredentialsPath())
		return newFileStore(xdgpaths.CredentialsPath())
	}
	if s := platformStoreFunc(); s != nil {
		return s
	}
	logging.Get().Info("agentpulse cred: no platform credential store available, using file fallback",
		"path", xdgpaths.CredentialsPath())
	return newFileStore(xdgpaths.CredentialsPath())
}
