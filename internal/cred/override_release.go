//go:build !dev && !testcred

package cred

// forcedStore is always nil in a release build: the test-only switch that
// forces the file fallback (override_dev.go) is not compiled in,
// so Default() always runs the normal platform probe.
func forcedStore() Store { return nil }
