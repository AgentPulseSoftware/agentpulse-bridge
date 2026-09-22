package atomicfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type sample struct {
	Name string `json:"name"`
}

func TestWriteJSONRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "file.json")
	if err := WriteJSON(path, sample{Name: "sam"}, 0o600); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	var got sample
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling written file: %v", err)
	}
	if got.Name != "sam" {
		t.Errorf("Name = %q, want sam", got.Name)
	}
}

func TestWriteJSONFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.json")
	if err := WriteJSON(path, sample{Name: "a"}, 0o600); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestWriteJSONParentDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentpulse")
	path := filepath.Join(dir, "file.json")
	if err := WriteJSON(path, sample{Name: "a"}, 0o600); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir mode = %o, want 0700", perm)
	}
}

// TestWriteJSONLeavesNoTempFilesBehind asserts the only file present
// after a successful write is the target itself — the temp file created
// in the same directory (what makes the rename atomic) does not linger.
func TestWriteJSONLeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	if err := WriteJSON(path, sample{Name: "a"}, 0o600); err != nil {
		t.Fatalf("WriteJSON() error: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "file.json" {
		t.Errorf("directory entries = %v, want exactly [file.json]", entries)
	}
}

// TestWriteJSONMarshalErrorLeavesExistingFileIntact simulates the "crash
// between temp and rename" case for the earliest possible failure (data
// that cannot be marshaled at all, so no temp file is even created): the
// pre-existing file at path must be untouched, since the only mutation
// that ever reaches the real path is the final os.Rename, and that never
// runs here.
func TestWriteJSONMarshalErrorLeavesExistingFileIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.json")
	if err := os.WriteFile(path, []byte(`{"name":"original"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// channels cannot be marshaled to JSON: this fails before any temp
	// file is written.
	err := WriteJSON(path, map[string]any{"bad": make(chan int)}, 0o600)
	if err == nil {
		t.Fatal("WriteJSON() with an unmarshalable value returned no error")
	}
	var werr *WriteError
	if !errors.As(err, &werr) {
		t.Fatalf("error = %v, want a *WriteError", err)
	}
	if werr.Path != path {
		t.Errorf("WriteError.Path = %q, want %q", werr.Path, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"original"}` {
		t.Errorf("existing file = %q, want it untouched", data)
	}
}

// TestWriteJSONReadOnlyDirectory exercises ERR-02's error path: a
// directory the process cannot write into. Skipped when running as root,
// which ignores Unix permission bits.
func TestWriteJSONReadOnlyDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits only")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) }) // let TempDir cleanup remove it

	path := filepath.Join(roDir, "sub", "file.json")
	err := WriteJSON(path, sample{Name: "a"}, 0o600)
	if err == nil {
		t.Fatal("WriteJSON() into a read-only directory returned no error")
	}
	var werr *WriteError
	if !errors.As(err, &werr) {
		t.Fatalf("error = %v, want a *WriteError", err)
	}
	if werr.Path != path {
		t.Errorf("WriteError.Path = %q, want %q", werr.Path, path)
	}
}
