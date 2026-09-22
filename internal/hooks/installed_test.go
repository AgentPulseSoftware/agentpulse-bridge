package hooks

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertUnchanged fails the test if path's bytes differ from want,
// enforcing InstalledCommands' "never writes" contract the direct way:
// checking the file itself, not just that no *Plan was built.
func assertUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s after InstalledCommands: %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s changed after a read-only call.\n--- before ---\n%s\n--- after ---\n%s", path, want, got)
	}
}

func TestInstalledCommandsAllEightOwned(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	entries := agentpulseEntries(testBinary, "/tmp/rec")
	plan, err := PlanInstall(settingsPath, testOwner, entries)
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if err := plan.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	wantEvents := []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PermissionRequest", "Notification", "Stop", "SessionEnd",
	}
	if len(got) != len(wantEvents) {
		t.Fatalf("InstalledCommands returned %d events, want %d: %v", len(got), len(wantEvents), got)
	}
	for _, event := range wantEvents {
		commands, ok := got[event]
		if !ok {
			t.Errorf("missing event %s in %v", event, got)
			continue
		}
		if len(commands) != 1 || !strings.HasSuffix(commands[0], testBinary+" hook") {
			t.Errorf("%s: commands = %v, want exactly one command ending in %q", event, commands, testBinary+" hook")
		}
	}

	assertUnchanged(t, settingsPath, before)
}

func TestInstalledCommandsAnotherToolOnly(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	fixture := `{
  "hooks": {
    "SessionStart": [
      {"matcher": "", "hooks": [{"type": "command", "command": "/usr/local/bin/other-tool hook", "timeout": 5}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/my-linter run", "timeout": 5}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "notify-send done", "timeout": 3}]}
    ]
  }
}`
	before := canonical(t, fixture)
	writeFile(t, settingsPath, before)

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("InstalledCommands = %v, want no owned events (every command belongs to another tool)", got)
	}

	assertUnchanged(t, settingsPath, before)
}

func TestInstalledCommandsSharedGroup(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	fixture := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [
        {"type": "command", "command": "/usr/local/bin/other-tool hook", "timeout": 5},
        {"type": "command", "command": "` + testBinary + ` hook", "timeout": 10}
      ]}
    ]
  }
}`
	before := canonical(t, fixture)
	writeFile(t, settingsPath, before)

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	commands, ok := got["PreToolUse"]
	if !ok || len(commands) != 1 || commands[0] != testBinary+" hook" {
		t.Errorf("PreToolUse commands = %v, want exactly [%q]", got["PreToolUse"], testBinary+" hook")
	}
	if len(got) != 1 {
		t.Errorf("InstalledCommands = %v, want only PreToolUse", got)
	}

	assertUnchanged(t, settingsPath, before)
}

func TestInstalledCommandsMissingFile(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	_, err := InstalledCommands(settingsPath, testOwner)
	if err == nil {
		t.Fatal("expected an error for a missing settings file")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want one satisfying errors.Is(err, fs.ErrNotExist)", err)
	}
}

func TestInstalledCommandsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	// Line 3 has a trailing comma, which is invalid JSON.
	before := []byte("{\n  \"model\": \"x\",\n  \"hooks\": [1, 2,],\n}\n")
	writeFile(t, settingsPath, before)

	_, err := InstalledCommands(settingsPath, testOwner)
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error = %v, want it to name line 3 (ERR-07)", err)
	}

	assertUnchanged(t, settingsPath, before)
}

func TestInstalledCommandsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	fixture := `{
  "hooks": {
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "` + testBinary + ` hook", "timeout": 10}]}
    ]
  }
}`
	before := append(append([]byte{}, utf8BOM...), canonical(t, fixture)...)
	writeFile(t, settingsPath, before)

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	if commands := got["Stop"]; len(commands) != 1 || commands[0] != testBinary+" hook" {
		t.Errorf("Stop commands = %v, want exactly [%q]", commands, testBinary+" hook")
	}

	assertUnchanged(t, settingsPath, before)
}

func TestInstalledCommandsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, []byte(""))

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("InstalledCommands = %v, want empty map for an empty file", got)
	}
}

func TestInstalledCommandsNoHooksKey(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	before := canonical(t, `{"model": "claude-sonnet-4"}`)
	writeFile(t, settingsPath, before)

	got, err := InstalledCommands(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("InstalledCommands: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("InstalledCommands = %v, want empty map when there is no \"hooks\" key", got)
	}

	assertUnchanged(t, settingsPath, before)
}
