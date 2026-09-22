//go:build dev

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevBuildHasRecordCommand(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"record", "install"})
	if err != nil {
		t.Fatalf("Find(record install) returned error: %v", err)
	}
	if cmd.Name() != "install" {
		t.Errorf("expected the install subcommand, got %q", cmd.Name())
	}
	if _, _, err := root.Find([]string{"record", "uninstall"}); err != nil {
		t.Errorf("Find(record uninstall) returned error: %v", err)
	}
}

// TestRecordInstallThenUninstallRoundTripsSettings drives the CLI exactly
// as the manual test does: a temporary $HOME with a settings
// file that already has an unrelated hook, "record install --yes" against
// a recording directory, then "record uninstall --yes", checked against
// os.ReadFile so the assertion matches what a person opening the file would
// see.
func TestRecordInstallThenUninstallRoundTripsSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", claudeDir, err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	// hooks.Plan always reformats the whole file through json.Indent (2
	// spaces, trailing newline) — see internal/hooks/hooks.go's prettyJSON
	// doc comment — so the fixture must already be in that exact canonical
	// form for the round trip below to come out byte-for-byte identical.
	compact := `{"model":"claude-sonnet-4","hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"notify-send done","timeout":3}]}]}}`
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(compact), "", "  "); err != nil {
		t.Fatalf("indenting fixture: %v", err)
	}
	buf.WriteByte('\n')
	original := buf.Bytes()
	if err := os.WriteFile(settingsPath, original, 0o600); err != nil {
		t.Fatalf("writing settings fixture: %v", err)
	}

	recordDir := t.TempDir()

	install := newRootCmd()
	var installOut bytes.Buffer
	install.SetOut(&installOut)
	install.SetArgs([]string{"record", "install", recordDir, "--yes"})
	if err := install.Execute(); err != nil {
		t.Fatalf("record install: %v", err)
	}

	installed, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading installed settings: %v", err)
	}
	if !strings.Contains(string(installed), "AGENTPULSE_RECORD_DIR='"+recordDir+"'") {
		t.Errorf("installed settings do not reference the (shell-quoted) record dir:\n%s", installed)
	}
	if !strings.Contains(string(installed), "notify-send done") {
		t.Errorf("install dropped the unrelated Stop hook:\n%s", installed)
	}

	// Simulate Claude Code actually running the installed hook command: set
	// AGENTPULSE_RECORD_DIR the way settings.json now does, and confirm a
	// file lands in the recording directory.
	t.Setenv("AGENTPULSE_RECORD_DIR", recordDir)
	stubSelfExecutable(t) // see hook_test.go: avoids self-exec'ing the test binary
	hookCmd := newRootCmd()
	hookCmd.SetIn(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"sess_1"}`))
	var hookOut bytes.Buffer
	hookCmd.SetOut(&hookOut)
	hookCmd.SetArgs([]string{"hook"})
	if err := hookCmd.Execute(); err != nil {
		t.Fatalf("hook: %v", err)
	}

	entries, err := os.ReadDir(recordDir)
	if err != nil {
		t.Fatalf("reading record dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one recorded file, got %v", entries)
	}

	uninstall := newRootCmd()
	var uninstallOut bytes.Buffer
	uninstall.SetOut(&uninstallOut)
	uninstall.SetArgs([]string{"record", "uninstall", "--yes"})
	if err := uninstall.Execute(); err != nil {
		t.Fatalf("record uninstall: %v", err)
	}

	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading restored settings: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("settings were not restored byte-for-byte.\n--- original ---\n%s\n--- restored ---\n%s", original, restored)
	}
}

func TestShellQuoteSingle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/tmp/rec", "'/tmp/rec'"},
		{"/tmp/my recordings", "'/tmp/my recordings'"},
		{"it's/quoted", `'it'\''s/quoted'`},
	}
	for _, tc := range cases {
		if got := shellQuoteSingle(tc.in); got != tc.want {
			t.Errorf("shellQuoteSingle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRecordInstallShellQuotesPathsWithSpaces covers a recording
// directory containing a space, which must survive
// as one shell argument in the installed hook command, not split into two.
func TestRecordInstallShellQuotesPathsWithSpaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	recordDir := filepath.Join(t.TempDir(), "my recordings")
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", recordDir, err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"record", "install", recordDir, "--yes"})
	if err := root.Execute(); err != nil {
		t.Fatalf("record install: %v", err)
	}

	installed, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading installed settings: %v", err)
	}
	if !strings.Contains(string(installed), "AGENTPULSE_RECORD_DIR='"+recordDir+"'") {
		t.Errorf("recording dir with a space was not shell-quoted:\n%s", installed)
	}
}

func TestRecordInstallAsksForConfirmationWithoutYes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	recordDir := t.TempDir()
	root := newRootCmd()
	root.SetIn(strings.NewReader("n\n"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"record", "install", recordDir})
	if err := root.Execute(); err != nil {
		t.Fatalf("record install: %v", err)
	}
	if !strings.Contains(out.String(), "Proceed?") {
		t.Errorf("expected a confirmation prompt, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected the declined install to abort, got:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("declining the prompt should not have written settings.json")
	}
}
