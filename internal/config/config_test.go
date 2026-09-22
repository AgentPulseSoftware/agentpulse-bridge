package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileYieldsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error for a missing file: %v", err)
	}
	if cfg.BridgeID != UnpairedBridgeID {
		t.Errorf("BridgeID = %q, want %q", cfg.BridgeID, UnpairedBridgeID)
	}
	if cfg.TaskLabel {
		t.Errorf("TaskLabel = true, want false by default")
	}
}

func TestLoadMalformedFileYieldsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err == nil {
		t.Error("Load() returned no error for malformed JSON, want a non-nil error for debug logging")
	}
	if cfg.BridgeID != UnpairedBridgeID {
		t.Errorf("BridgeID = %q, want %q", cfg.BridgeID, UnpairedBridgeID)
	}
}

func TestLoadValidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"bridge_id":"brg_abc123","task_label":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.BridgeID != "brg_abc123" {
		t.Errorf("BridgeID = %q, want brg_abc123", cfg.BridgeID)
	}
	if !cfg.TaskLabel {
		t.Errorf("TaskLabel = false, want true")
	}
}

func TestLoadEmptyBridgeIDGetsPlaceholder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"bridge_id":"","task_label":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.BridgeID != UnpairedBridgeID {
		t.Errorf("BridgeID = %q, want %q", cfg.BridgeID, UnpairedBridgeID)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := Config{
		BridgeID:   "brg_xyz",
		PairedAt:   "2026-09-13T00:00:00Z",
		DeviceName: "Sam's iPhone",
		Relay:      "",
		TaskLabel:  true,
		KeepAwake:  true,
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got.BridgeID != want.BridgeID ||
		got.PairedAt != want.PairedAt ||
		got.DeviceName != want.DeviceName ||
		got.Relay != want.Relay ||
		got.TaskLabel != want.TaskLabel ||
		got.KeepAwake != want.KeepAwake {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestLoadLegacyWatchNewProjectsKeyLoadsAndIsStrippedOnSave covers the case
// where an old config.json that still
// carries "watch_new_projects" (a key this package must tolerate and
// drop; BR-10's real source of truth is internal/watch.List) must still
// load without error, and a subsequent Save must drop that key while
// preserving a genuinely unknown one.
func TestLoadLegacyWatchNewProjectsKeyLoadsAndIsStrippedOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"bridge_id":"brg_abc","watch_new_projects":false,"some_future_field":"kept-me"}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error for a config with the legacy key: %v", err)
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("re-parsing saved config: %v", err)
	}
	if _, present := raw["watch_new_projects"]; present {
		t.Error("watch_new_projects was written back by Save(), want it dropped")
	}
	future, ok := raw["some_future_field"]
	if !ok {
		t.Fatal("some_future_field was dropped by Save(), want it preserved")
	}
	if string(future) != `"kept-me"` {
		t.Errorf("some_future_field = %s, want %q", future, "kept-me")
	}
}

func TestSaveFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
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

// TestSavePreservesUnknownFields is a forward-compatibility case worth
// pinning: a newer bridge's config.json, read and re-saved by
// this binary, must not lose a field this binary doesn't know about.
func TestSavePreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"bridge_id":"brg_abc","task_label":true,"future_field":"kept-me"}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	cfg.DeviceName = "changed" // simulate this binary updating a known field
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("re-parsing saved config: %v", err)
	}
	future, ok := raw["future_field"]
	if !ok {
		t.Fatal("future_field was dropped by Save()")
	}
	if string(future) != `"kept-me"` {
		t.Errorf("future_field = %s, want %q", future, "kept-me")
	}
	var deviceName struct {
		DeviceName string `json:"device_name"`
	}
	if err := json.Unmarshal(data, &deviceName); err != nil {
		t.Fatal(err)
	}
	if deviceName.DeviceName != "changed" {
		t.Errorf("device_name = %q, want %q", deviceName.DeviceName, "changed")
	}
}

func TestSaveReadOnlyDirectoryReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	path := filepath.Join(roDir, "sub", "config.json")
	if err := Save(path, Default()); err == nil {
		t.Fatal("Save() into a read-only directory returned no error")
	}
}
