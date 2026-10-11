package main

import "github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"

// defaultCredStore is the credential backend a command uses when its deps
// do not supply one: cred.Default(), the platform store probed once per
// process. It is a variable only so this package's TestMain can replace it
// with a store that refuses every call, which guarantees no in-process
// test ever reaches the real Keychain or Secret Service, whatever build
// tags the test run uses.
var defaultCredStore = cred.Default
