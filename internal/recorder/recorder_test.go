package recorder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesDirAndFirstFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings") // does not exist yet
	raw := []byte(`{"hook_event_name":"SessionStart","session_id":"abc"}`)

	path, err := Write(dir, raw)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "001-SessionStart.json" {
		t.Errorf("path = %q, want basename 001-SessionStart.json", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("written bytes = %q, want exactly %q (verbatim, no reformatting)", got, raw)
	}
}

func TestWriteCounterFollowsHighestExistingFile(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed with a gap and an out-of-order file to make sure we take the
	// max, not count+1 or the last-modified file.
	for _, name := range []string{"001-SessionStart.json", "005-PreToolUse.json", "003-Stop.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}

	path, err := Write(dir, []byte(`{"hook_event_name":"SessionEnd"}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "006-SessionEnd.json" {
		t.Errorf("path = %q, want basename 006-SessionEnd.json", path)
	}
}

func TestWriteMultipleIncrementsSequentially(t *testing.T) {
	dir := t.TempDir()
	names := []string{}
	for i := 0; i < 3; i++ {
		path, err := Write(dir, []byte(`{"hook_event_name":"Stop"}`))
		if err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		names = append(names, filepath.Base(path))
	}
	want := []string{"001-Stop.json", "002-Stop.json", "003-Stop.json"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("file %d = %q, want %q", i, names[i], w)
		}
	}
}

func TestWriteMalformedJSONUsesUnknownEventName(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, []byte(`not json at all`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "001-unknown.json" {
		t.Errorf("path = %q, want basename 001-unknown.json", path)
	}
}

func TestWriteMissingEventNameUsesUnknown(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, []byte(`{"session_id":"abc"}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(path) != "001-unknown.json" {
		t.Errorf("path = %q, want basename 001-unknown.json", path)
	}
}

func TestWriteSanitizesUnsafeEventNameForFilename(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, []byte(`{"hook_event_name":"../../etc/passwd"}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	base := filepath.Base(path)
	if base != "001-______etc_passwd.json" {
		t.Errorf("path = %q, want a sanitized basename", base)
	}
	// The written file must stay inside dir.
	if filepath.Dir(path) != dir {
		t.Errorf("path escaped the recording dir: %q", path)
	}
}
