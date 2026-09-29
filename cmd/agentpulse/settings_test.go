package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// realTempDir is t.TempDir() with any symlink in it resolved away — on
// macOS, $TMPDIR itself is a symlink (/var -> /private/var), which would
// otherwise make every "unchanged path" assertion below fail for a
// reason that has nothing to do with the Homebrew logic under test.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeExecutable creates path as a plain, executable regular file (not a
// symlink), creating its parent directories as needed.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test-controlled temp path
		t.Fatal(err)
	}
}

// TestIsHomebrewCellarPath is a pure table test of the pattern match
// itself, independent of any real symlink.
func TestIsHomebrewCellarPath(t *testing.T) {
	tests := []struct {
		name     string
		resolved string
		binName  string
		want     bool
	}{
		{"exact Homebrew shape", "/opt/homebrew/Cellar/agentpulse/1.2.0/bin/agentpulse", "agentpulse", true},
		{"Linuxbrew prefix", "/home/linuxbrew/.linuxbrew/Cellar/agentpulse/1.2.0/bin/agentpulse", "agentpulse", true},
		{"go install path", "/Users/sam/go/bin/agentpulse", "agentpulse", false},
		{"missing bin/ level", "/opt/homebrew/Cellar/agentpulse/1.2.0/agentpulse", "agentpulse", false},
		{"formula name does not match binary name", "/opt/homebrew/Cellar/other-tool/1.2.0/bin/agentpulse", "agentpulse", false},
		{"final component is not the binary name", "/opt/homebrew/Cellar/agentpulse/1.2.0/bin/wrapper", "agentpulse", false},
		{"Cellar directory missing entirely", "/opt/homebrew/lib/agentpulse/1.2.0/bin/agentpulse", "agentpulse", false},
		{"empty binary name", "/opt/homebrew/Cellar/agentpulse/1.2.0/bin/agentpulse", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHomebrewCellarPath(tt.resolved, tt.binName); got != tt.want {
				t.Errorf("isHomebrewCellarPath(%q, %q) = %v, want %v", tt.resolved, tt.binName, got, tt.want)
			}
		})
	}
}

// TestStableBinaryPathHomebrewBinSymlinkSurvivesUpgrade simulates a
// Homebrew Cellar/opt layout and a `brew upgrade` in a scratch directory:
// $(brew --prefix)/bin/agentpulse is a symlink straight into the
// versioned Cellar path. stableBinaryPath must keep the symlink path
// stable across the upgrade, which is exactly what P5-27 requires — a
// hook written with this path keeps working after `brew upgrade` deletes
// the old version and relinks the symlink to the new one.
func TestStableBinaryPathHomebrewBinSymlinkSurvivesUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows")
	}
	prefix := realTempDir(t)
	cellarV1 := filepath.Join(prefix, "Cellar", "agentpulse", "1.0.0", "bin", "agentpulse")
	writeExecutable(t, cellarV1)

	binLink := filepath.Join(prefix, "bin", "agentpulse")
	if err := os.MkdirAll(filepath.Dir(binLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarV1, binLink); err != nil {
		t.Fatal(err)
	}

	got, err := stableBinaryPath(binLink)
	if err != nil {
		t.Fatalf("stableBinaryPath before upgrade: %v", err)
	}
	if got != binLink {
		t.Errorf("stableBinaryPath(%q) = %q, want the unresolved symlink path itself", binLink, got)
	}

	// Simulate `brew upgrade`: delete the old versioned Cellar directory,
	// install the new version elsewhere, and relink bin/agentpulse to it
	// — exactly what Homebrew does, and the reason a resolved path used
	// to go stale.
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(cellarV1))); err != nil {
		t.Fatal(err)
	}
	cellarV2 := filepath.Join(prefix, "Cellar", "agentpulse", "1.1.0", "bin", "agentpulse")
	writeExecutable(t, cellarV2)
	if err := os.Remove(binLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarV2, binLink); err != nil {
		t.Fatal(err)
	}

	got, err = stableBinaryPath(binLink)
	if err != nil {
		t.Fatalf("stableBinaryPath after upgrade: %v", err)
	}
	if got != binLink {
		t.Errorf("stableBinaryPath(%q) after upgrade = %q, want the same unresolved symlink path", binLink, got)
	}
	resolved, err := filepath.EvalSymlinks(binLink)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != cellarV2 {
		t.Errorf("the symlink resolves to %q after upgrade, want the new version %q", resolved, cellarV2)
	}
}

// TestStableBinaryPathOptFormulaSymlink covers the second Homebrew shape
// the card names: $(brew --prefix)/opt/agentpulse/bin/agentpulse, where
// opt/agentpulse itself is the part Homebrew relinks (to the versioned
// Cellar directory, not to the binary file).
func TestStableBinaryPathOptFormulaSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows")
	}
	prefix := realTempDir(t)
	cellarVersionDir := filepath.Join(prefix, "Cellar", "agentpulse", "1.0.0")
	writeExecutable(t, filepath.Join(cellarVersionDir, "bin", "agentpulse"))

	optFormula := filepath.Join(prefix, "opt", "agentpulse")
	if err := os.MkdirAll(filepath.Dir(optFormula), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellarVersionDir, optFormula); err != nil {
		t.Fatal(err)
	}
	optBin := filepath.Join(optFormula, "bin", "agentpulse")

	got, err := stableBinaryPath(optBin)
	if err != nil {
		t.Fatalf("stableBinaryPath: %v", err)
	}
	if got != optBin {
		t.Errorf("stableBinaryPath(%q) = %q, want the unresolved opt/ path itself", optBin, got)
	}
}

// TestStableBinaryPathGoInstallIsUnaffected is the "falling back to the
// resolved path for go install/manual installs" half of P5-27: a plain,
// non-symlinked binary is returned unchanged.
func TestStableBinaryPathGoInstallIsUnaffected(t *testing.T) {
	goBin := filepath.Join(realTempDir(t), "go", "bin", "agentpulse")
	writeExecutable(t, goBin)

	got, err := stableBinaryPath(goBin)
	if err != nil {
		t.Fatalf("stableBinaryPath: %v", err)
	}
	if got != goBin {
		t.Errorf("stableBinaryPath(%q) = %q, want the same path unchanged", goBin, got)
	}
}

// TestStableBinaryPathManualSymlinkResolvesNormally is a manual symlink
// to somewhere that is not a Homebrew Cellar path: P5-27's rule is
// specific to Homebrew's own layout, so this still resolves to the real
// file, matching the pre-P5-27 behaviour.
func TestStableBinaryPathManualSymlinkResolvesNormally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows")
	}
	real := filepath.Join(realTempDir(t), "somewhere", "agentpulse")
	writeExecutable(t, real)

	link := filepath.Join(realTempDir(t), "usr-local-bin", "agentpulse")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got, err := stableBinaryPath(link)
	if err != nil {
		t.Fatalf("stableBinaryPath: %v", err)
	}
	if got != real {
		t.Errorf("stableBinaryPath(%q) = %q, want the resolved real path %q", link, got, real)
	}
}

// TestCurrentBinaryPathMatchesOSExecutable is a smoke test that
// currentBinaryPath still returns a real, existing path when run as an
// ordinary `go test` binary (not a Homebrew install) — it is not itself
// a Homebrew symlink, so this exercises the untouched fallback branch of
// stableBinaryPath end to end, through os.Executable().
func TestCurrentBinaryPathMatchesOSExecutable(t *testing.T) {
	got, err := currentBinaryPath()
	if err != nil {
		t.Fatalf("currentBinaryPath: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("currentBinaryPath() = %q does not exist: %v", got, err)
	}
}
