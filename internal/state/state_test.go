package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileYieldsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := Load(path)
	if s != (State{}) {
		t.Errorf("Load() of a missing file = %+v, want the zero value", s)
	}
}

func TestLoadCorruptFileYieldsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Load(path)
	if s != (State{}) {
		t.Errorf("Load() of a corrupt file = %+v, want the zero value", s)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := State{
		Consecutive401:        2,
		UnpairedReason:        "",
		RequiredBridgeVersion: "1.4.0",
		LastEvent:             &LastEvent{TS: "2026-09-13T00:00:00Z", Type: "stop", SessionID: "sess_1"},
		LastFlush:             &LastFlush{TS: "2026-09-13T00:00:01Z", Outcome: OutcomeOK, HTTPStatus: 200},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	got := Load(path)
	if got.Consecutive401 != want.Consecutive401 {
		t.Errorf("Consecutive401 = %d, want %d", got.Consecutive401, want.Consecutive401)
	}
	if got.RequiredBridgeVersion != want.RequiredBridgeVersion {
		t.Errorf("RequiredBridgeVersion = %q, want %q", got.RequiredBridgeVersion, want.RequiredBridgeVersion)
	}
	if got.LastEvent == nil || *got.LastEvent != *want.LastEvent {
		t.Errorf("LastEvent = %+v, want %+v", got.LastEvent, want.LastEvent)
	}
	if got.LastFlush == nil || *got.LastFlush != *want.LastFlush {
		t.Errorf("LastFlush = %+v, want %+v", got.LastFlush, want.LastFlush)
	}
}

func TestSaveFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Save(path, State{Consecutive401: 1}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestSaveReadOnlyDirectoryReturnsWriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	path := filepath.Join(roDir, "sub", "state.json")
	err := Save(path, State{Consecutive401: 1})
	if err == nil {
		t.Fatal("Save() into a read-only directory returned no error")
	}
}
