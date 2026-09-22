//go:build !darwin && !linux

package keepawake

import "testing"

// TestCommandUnsupportedPlatform never runs in this repo's CI (darwin and
// linux only, per BR-20), but exists so that building this package for
// any other GOOS still exercises and asserts the fallback behavior: no
// keep-awake implementation at all.
func TestCommandUnsupportedPlatform(t *testing.T) {
	if _, ok := Command(1234); ok {
		t.Error("Command() ok = true, want false on an unsupported platform")
	}
	if Supported() {
		t.Error("Supported() = true, want false on an unsupported platform")
	}
}
