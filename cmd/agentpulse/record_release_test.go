//go:build !dev

package main

import "testing"

// TestReleaseBuildHasNoRecordCommand proves the core safety
// requirement: a binary built without the "dev" tag ("make build", what
// ships to users) has no "record" command, since recording writes raw,
// unscrubbed session data to disk. This file itself carries the "!dev" tag
// so the assertion only runs — and can only be true — against the release
// build; see TestDevBuildHasRecordCommand in record_dev_test.go for the
// other half (that "make build-dev" does have it).
func TestReleaseBuildHasNoRecordCommand(t *testing.T) {
	root := newRootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "record" {
			t.Fatalf("release build must not have a %q command", cmd.Name())
		}
	}
}
