package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestReleaseBinaryHasNoTestCredBackendSwitch builds the binary exactly as
// a release does, with no build tags, and proves the test-only switch that
// forces the file credential backend is not in it at all: not merely
// ignored, but absent, so no environment setting can move a real user's
// credential out of the platform store (D93). Built with -tags testcred,
// as the stdio tests do, the name is present, which shows the check can
// fail.
func TestReleaseBinaryHasNoTestCredBackendSwitch(t *testing.T) {
	const name = "AGENTPULSE_TEST_CRED_BACKEND"
	tests := []struct {
		name    string
		tags    []string
		present bool
	}{
		{name: "release build", present: false},
		{name: "testcred build", tags: []string{"-tags", "testcred"}, present: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "agentpulse")
			args := append([]string{"build", "-trimpath"}, tt.tags...)
			args = append(args, "-o", bin, ".")
			if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
				t.Fatalf("go %v: %v\n%s", args, err, out)
			}
			data, err := os.ReadFile(bin)
			if err != nil {
				t.Fatalf("reading built binary: %v", err)
			}
			if got := bytes.Contains(data, []byte(name)); got != tt.present {
				t.Errorf("binary contains %s = %v, want %v", name, got, tt.present)
			}
		})
	}
}
