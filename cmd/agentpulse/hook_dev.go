//go:build dev

package main

import "github.com/agentpulsesoftware/agentpulse-bridge/internal/recorder"

// maybeRecord writes raw hook JSON to dir (SPEC 9.4, 18's fixture
// recording). Only linked into development builds ("make build-dev",
// built with -tags dev): see hook_release.go for why the release binary
// must never even contain this code path, let alone run it. It is a
// package-level variable (not a plain function) so tests can substitute a
// panicking stand-in to exercise newHookCmd's recover() without needing a
// real way to make recording itself panic.
var maybeRecord = func(dir string, raw []byte) error {
	_, err := recorder.Write(dir, raw)
	return err
}
