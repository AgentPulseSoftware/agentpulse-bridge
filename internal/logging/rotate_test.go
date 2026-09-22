package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingWriterRotatesAtBoundaryKeepingTwoBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.log")
	// A small boundary so the test writes bytes, not megabytes: five
	// "generations" of a 40-byte line each, boundary at 100 bytes (a
	// generation is 2-3 lines), maxBackups 2 — exactly BR-04's policy,
	// scaled down.
	w := newRotatingWriter(path, 100, 2)

	line := func(gen int) []byte {
		return []byte(strings.Repeat(gensym(gen), 1) + "\n")
	}
	// Each generation writes enough bytes on its own to exceed the
	// 100-byte boundary by itself, guaranteeing one rotation per
	// generation and a clean, predictable generation-per-file mapping.
	for gen := 1; gen <= 5; gen++ {
		if _, err := w.Write(line(gen)); err != nil {
			t.Fatalf("Write() gen %d: %v", gen, err)
		}
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading current log: %v", err)
	}
	backup1, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("reading .1 backup: %v", err)
	}
	backup2, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("reading .2 backup: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf(".3 backup exists (or Stat errored unexpectedly: %v), want exactly two backups retained", err)
	}

	if !bytes.Contains(current, []byte("gen5")) {
		t.Errorf("current log = %q, want the newest generation", current)
	}
	if !bytes.Contains(backup1, []byte("gen4")) {
		t.Errorf(".1 backup = %q, want the second-newest generation", backup1)
	}
	if !bytes.Contains(backup2, []byte("gen3")) {
		t.Errorf(".2 backup = %q, want the third-newest generation", backup2)
	}
}

func gensym(gen int) string {
	// 90+ bytes on its own, comfortably over the 100-byte test boundary
	// together with the leading generation marker, so every generation
	// forces exactly one rotation.
	return "gen" + string(rune('0'+gen)) + ":" + strings.Repeat("x", 90)
}

func TestRotatingWriterCreatesDirAndFileMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "agentpulse")
	path := filepath.Join(dir, "bridge.log")
	w := newRotatingWriter(path, maxLogBytes, maxLogBackups)

	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("bridge.log was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

// TestRotatingWriterNeverReturnsAnError is the "logging must never be the
// reason a command fails" guarantee: even pointed at a path that can
// never be opened for writing, Write reports success.
func TestRotatingWriterNeverReturnsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	w := newRotatingWriter(filepath.Join(roDir, "sub", "bridge.log"), maxLogBytes, maxLogBackups)
	n, err := w.Write([]byte("hello\n"))
	if err != nil {
		t.Errorf("Write() returned an error, want it swallowed: %v", err)
	}
	if n != len("hello\n") {
		t.Errorf("Write() n = %d, want %d (reported as fully written)", n, len("hello\n"))
	}
}
