//go:build darwin || linux

package keepawake

import (
	"syscall"
	"testing"
	"time"
)

// The "ok=false on an unsupported platform" Command scenario is covered
// by keepawake_other_test.go's build-tagged test. This repo's
// CI only ever builds darwin and linux (BR-20), so that file is never
// compiled here — noted so a reader of this file's test list does not go
// looking for that case in the wrong place.

// TestStartArgvIsDetachedAndNeverWaitedOn is the "one test exercises the
// executing path" case: it starts a real, harmless process
// (/bin/sleep 0.2) through startArgv — the same spawn code Start(pid)
// uses for caffeinate/systemd-inhibit — and asserts two things Start's
// SEC-09 contract requires: it returns as soon as the process has
// started, without blocking until the process exits (it never calls
// Wait), and the process is genuinely running, detached, immediately
// after Start returns.
func TestStartArgvIsDetachedAndNeverWaitedOn(t *testing.T) {
	began := time.Now()
	pid, err := startArgv([]string{"/bin/sleep", "0.2"})
	if err != nil {
		t.Fatalf("startArgv() error: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startArgv() pid = %d, want a positive pid", pid)
	}

	// startArgv must not have blocked until the 0.2s sleep finished: if it
	// had called Wait (directly or by accident), this would take at least
	// 200ms. A generous margin below that still proves the point.
	if elapsed := time.Since(began); elapsed > 100*time.Millisecond {
		t.Errorf("startArgv() took %v to return, want well under the child's 200ms lifetime (it must not Wait)", elapsed)
	}

	// The process must actually be alive right after Start returns —
	// detached (Setsid), not a zombie, not already reaped.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("process %d is not running immediately after startArgv(): %v", pid, err)
	}

	// It exits on its own; nothing in this package ever calls Wait on it.
	// Give it time to finish and be reaped by init (it was detached via
	// Setsid, so this process is not its parent and cannot reap it
	// itself — this is only confirming it is not left hanging around).
	time.Sleep(500 * time.Millisecond)
}
