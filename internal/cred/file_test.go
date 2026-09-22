package cred

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileStoreSatisfiesContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	assertStoreContract(t, newFileStore(path))
}

func TestFileStoreFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	store := newFileStore(path)
	if err := store.Set("s3cr3t"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestFileStoreParentDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentpulse")
	path := filepath.Join(dir, "credentials")
	store := newFileStore(path)
	if err := store.Set("s3cr3t"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir mode = %o, want 0700", perm)
	}
}

// TestFileStoreCorrectsWrongPermissions covers the case explicitly:
// a pre-existing credentials file with looser-than-0600 permissions is
// corrected on read, not refused (see file.go's Get doc comment for why
// this backend chose "corrected" over "refused").
func TestFileStoreCorrectsWrongPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits only")
	}
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("s3cr3t"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newFileStore(path)
	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("Get() = %q, want %q", got, "s3cr3t")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode after Get() = %o, want corrected to 0600", perm)
	}
}

func TestFileStoreEmptyFileIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newFileStore(path)
	if _, err := store.Get(); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() of an empty file = %v, want ErrNotFound", err)
	}
}
