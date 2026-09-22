//go:build !dev

package main

// maybeRecord is a no-op in the release build: AGENTPULSE_RECORD_DIR is
// silently ignored, and internal/recorder is not imported anywhere in this
// build, so its code isn't even linked into the binary. Fixture recording
// writes raw, unscrubbed session data to disk, so it must not be reachable
// — or present at all — outside a development build ("make build-dev").
// See hook_dev.go for the real implementation. A package-level variable
// (not a plain function) purely for symmetry with hook_dev.go, so a test
// can substitute a panicking stand-in.
var maybeRecord = func(dir string, raw []byte) error {
	return nil
}
