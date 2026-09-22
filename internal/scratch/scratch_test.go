package scratch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
)

func TestLoadMissingYieldsFreshState(t *testing.T) {
	state := Load(t.TempDir(), "sess_abc")
	if state == nil {
		t.Fatal("Load() returned nil")
	}
	if !state.AwaitingTaskLabel {
		t.Error("fresh state should be AwaitingTaskLabel")
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	state := classify.NewSessionState()
	state.Commits = 3
	state.ReadHashes = map[string]bool{"deadbeef": true}

	if err := Save(dir, "sess_1", state); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}

	got := Load(dir, "sess_1")
	if got.Commits != 3 {
		t.Errorf("Commits = %d, want 3", got.Commits)
	}
	if !got.ReadHashes["deadbeef"] {
		t.Errorf("ReadHashes missing expected entry: %v", got.ReadHashes)
	}
}

func TestSaveFileMode(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "sess_1", classify.NewSessionState()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sessionPath(dir, "sess_1"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("scratch file mode = %o, want 0600", perm)
	}
}

func TestLoadCorruptFileYieldsFreshState(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "sess_bad.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := Load(dir, "sess_bad")
	if !state.AwaitingTaskLabel {
		t.Error("corrupt scratch should yield a fresh state")
	}
}

func TestSessionIDIsSanitizedForFilename(t *testing.T) {
	dir := t.TempDir()
	evil := "../../../etc/passwd"
	if err := Save(dir, evil, classify.NewSessionState()); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}
	// Must have stayed inside dir/sessions, never escaped it.
	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file under sessions/, got %d", len(entries))
	}
}

func TestDeleteRemovesFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "sess_1", classify.NewSessionState()); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "sess_1"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	if _, err := os.Stat(sessionPath(dir, "sess_1")); !os.IsNotExist(err) {
		t.Errorf("expected file to be gone, stat err = %v", err)
	}
}

func TestDeleteMissingFileIsNotError(t *testing.T) {
	if err := Delete(t.TempDir(), "sess_never_existed"); err != nil {
		t.Errorf("Delete() of a missing file returned error: %v", err)
	}
}

func TestSweepRemovesOnlyOldFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "sess_old", classify.NewSessionState()); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, "sess_new", classify.NewSessionState()); err != nil {
		t.Fatal(err)
	}
	oldPath := sessionPath(dir, "sess_old")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	if err := Sweep(dir, 24*time.Hour); err != nil {
		t.Fatalf("Sweep() returned error: %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expected old scratch file to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(sessionPath(dir, "sess_new")); err != nil {
		t.Errorf("expected new scratch file to survive: %v", err)
	}
}

func TestSweepMissingDirIsNotError(t *testing.T) {
	if err := Sweep(filepath.Join(t.TempDir(), "nope"), 24*time.Hour); err != nil {
		t.Errorf("Sweep() of a missing directory returned error: %v", err)
	}
}
