package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testBinary is the fake installed-binary path these tests pretend to be
// running from, and testOwner is the ownership rule built from it (BR-09).
const testBinary = "/opt/agentpulse/bin/agentpulse"

var testOwner = NewOwner(testBinary)

// agentpulseEntries returns the eight BR-08 hook entries for a fake binary
// path, matching what "agentpulse record install <dir>" would build.
func agentpulseEntries(binary, recordDir string) []Entry {
	events := []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PermissionRequest", "Notification", "Stop", "SessionEnd",
	}
	cmd := "env AGENTPULSE_RECORD_DIR=" + recordDir + " " + binary + " hook"
	entries := make([]Entry, 0, len(events))
	for _, e := range events {
		entries = append(entries, Entry{Event: e, Matcher: "", Command: cmd, Timeout: 10})
	}
	return entries
}

// canonical reformats compact JSON the same way Plan does with a 2-space
// indent and a trailing newline, so tests can build fixtures that are
// already in the exact byte form a round trip must reproduce.
func canonical(t *testing.T, compact string) []byte {
	t.Helper()
	return canonicalIndent(t, compact, "  ")
}

// canonicalIndent is canonical with an explicit indent unit, for tests that
// exercise indent-detection (2-space, 4-space, tab).
func canonicalIndent(t *testing.T, compact string, indent string) []byte {
	t.Helper()
	out, err := prettyJSON([]byte(compact), indent)
	if err != nil {
		t.Fatalf("prettyJSON: %v", err)
	}
	return out
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

const unrelatedSettingsFixture = `{
  "model": "claude-sonnet-4",
  "permissions": {"allow": ["Bash(npm run test:*)"], "deny": []},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/my-linter hook", "timeout": 5}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "notify-send done", "timeout": 3}]}
    ]
  },
  "otherTopLevelKey": {"nested": [1, 2, 3], "flag": true}
}`

func TestInstallThenUninstallRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	original := canonical(t, unrelatedSettingsFixture)
	writeFile(t, settingsPath, original)

	installPlan, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if !installPlan.Changed {
		t.Fatalf("expected install to change the file")
	}
	if err := installPlan.Apply(); err != nil {
		t.Fatalf("Apply install: %v", err)
	}

	installed, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading installed settings: %v", err)
	}
	if !strings.Contains(string(installed), "agentpulse") {
		t.Fatalf("installed settings do not mention agentpulse:\n%s", installed)
	}
	if !strings.Contains(string(installed), "/usr/local/bin/my-linter hook") {
		t.Fatalf("install dropped the unrelated PreToolUse hook:\n%s", installed)
	}
	if !strings.Contains(string(installed), "notify-send done") {
		t.Fatalf("install dropped the unrelated Stop hook:\n%s", installed)
	}

	uninstallPlan, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if !uninstallPlan.Changed {
		t.Fatalf("expected uninstall to change the file")
	}
	if err := uninstallPlan.Apply(); err != nil {
		t.Fatalf("Apply uninstall: %v", err)
	}

	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading restored settings: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("round trip is not byte-for-byte identical.\n--- original ---\n%s\n--- restored ---\n%s", original, restored)
	}
}

func TestInstallPreservesUnrelatedContentAndKeyOrder(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, canonical(t, unrelatedSettingsFixture))

	p, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}

	var before, after map[string]any
	if err := json.Unmarshal(p.Before, &before); err != nil {
		t.Fatalf("unmarshal before: %v", err)
	}
	if err := json.Unmarshal(p.After, &after); err != nil {
		t.Fatalf("unmarshal after: %v", err)
	}
	if after["model"] != before["model"] {
		t.Errorf("model changed: %v -> %v", before["model"], after["model"])
	}
	if after["otherTopLevelKey"] == nil {
		t.Errorf("otherTopLevelKey was dropped")
	}

	// Top-level key order: everything already present keeps its relative
	// order, and "hooks" (already present in the fixture) is not moved to
	// the end.
	afterKeys := topLevelKeyOrder(t, p.After)
	wantOrder := []string{"model", "permissions", "hooks", "otherTopLevelKey"}
	if len(afterKeys) != len(wantOrder) {
		t.Fatalf("key order = %v, want %v", afterKeys, wantOrder)
	}
	for i, k := range wantOrder {
		if afterKeys[i] != k {
			t.Errorf("key order = %v, want %v", afterKeys, wantOrder)
			break
		}
	}
}

func topLevelKeyOrder(t *testing.T, data []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if _, err := dec.Token(); err != nil { // '{'
		t.Fatalf("token: %v", err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		keys = append(keys, tok.(string))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return keys
}

func TestInstallCreatesMissingSettingsFile(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	p, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if p.Before != nil {
		t.Fatalf("expected no Before content for a missing file, got %q", p.Before)
	}
	if err := p.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings file was not created: %v", err)
	}
}

func TestReinstallReplacesExistingEntryInPlace(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, canonical(t, `{"hooks": {}}`))

	first, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec-one"))
	if err != nil {
		t.Fatalf("first PlanInstall: %v", err)
	}
	if err := first.Apply(); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	second, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec-two"))
	if err != nil {
		t.Fatalf("second PlanInstall: %v", err)
	}
	if err := second.Apply(); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	final, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading final settings: %v", err)
	}
	if strings.Contains(string(final), "rec-one") {
		t.Errorf("stale entry from the first install survived:\n%s", final)
	}
	if !strings.Contains(string(final), "rec-two") {
		t.Errorf("expected the second install's directory to be present:\n%s", final)
	}
	if strings.Count(string(final), `"PreToolUse"`) != 1 {
		t.Errorf("reinstall duplicated the PreToolUse group:\n%s", final)
	}
}

func TestInstallIsNoopWhenAlreadyInstalled(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, canonical(t, `{}`))

	entries := agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec")
	first, err := PlanInstall(settingsPath, testOwner, entries)
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if err := first.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	second, err := PlanInstall(settingsPath, testOwner, entries)
	if err != nil {
		t.Fatalf("second PlanInstall: %v", err)
	}
	if second.Changed {
		t.Errorf("expected no-op reinstall to report Changed=false\ndiff:\n%s", second.Diff)
	}
}

func TestUninstallRemovesOnlyAgentpulseEntriesFromASharedGroup(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	// A PreToolUse group that already mixes AgentPulse's hook with an
	// unrelated tool's hook under the same empty matcher.
	writeFile(t, settingsPath, canonical(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "", "hooks": [
					{"type": "command", "command": "/opt/agentpulse/bin/agentpulse hook", "timeout": 10},
					{"type": "command", "command": "my-other-tool hook", "timeout": 5}
				]}
			]
		}
	}`))

	p, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if err := p.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	final, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading final settings: %v", err)
	}
	if strings.Contains(string(final), "agentpulse") {
		t.Errorf("agentpulse entry survived uninstall:\n%s", final)
	}
	if !strings.Contains(string(final), "my-other-tool hook") {
		t.Errorf("uninstall removed an unrelated tool's hook:\n%s", final)
	}
}

func TestUninstallOnMissingFileIsNoop(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	p, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if p.Changed {
		t.Errorf("expected uninstalling from a missing file to be a no-op")
	}
	if err := p.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("uninstall on a missing file should not create one")
	}
}

func TestInvalidJSONAbortsWithFileAndLine(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	// Line 3 has a trailing comma, which is invalid JSON.
	writeFile(t, settingsPath, []byte("{\n  \"model\": \"x\",\n  \"broken\": [1, 2,],\n}\n"))

	_, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
	if !strings.Contains(err.Error(), settingsPath) {
		t.Errorf("error does not name the file: %v", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error does not name the line: %v", err)
	}
}

func TestDiffShowsOnlyTheAgentpulseChange(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, canonical(t, `{"model": "claude-sonnet-4"}`))

	p, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if !strings.Contains(p.Diff, "+") {
		t.Errorf("diff has no additions:\n%s", p.Diff)
	}
	// The "model" key/value is untouched content: its text must still
	// appear somewhere in the diff (a trailing comma may be added since
	// "hooks" now follows it, which is why we don't require the exact same
	// line to be unchanged).
	if !strings.Contains(p.Diff, `"claude-sonnet-4"`) {
		t.Errorf("diff lost the unrelated model value entirely:\n%s", p.Diff)
	}
}

// TestUninstallPreservesAnotherToolsEntryExactly covers BR-09: a shared
// hook group where the other tool's
// entry has no "timeout" and carries a field AgentPulse doesn't know
// about. Only entries actually being removed are ever decoded (see
// filterOwnedHooks); everything else must survive as the exact bytes it
// was written with.
func TestUninstallPreservesAnotherToolsEntryExactly(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	original := canonical(t, `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "", "hooks": [
					{"type": "command", "command": "my-other-tool hook", "id": "lint-1"},
					{"type": "command", "command": "/opt/agentpulse/bin/agentpulse hook", "timeout": 10}
				]}
			]
		}
	}`)
	writeFile(t, settingsPath, original)

	p, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if err := p.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	final, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading final settings: %v", err)
	}
	if strings.Contains(string(final), "agentpulse") {
		t.Errorf("agentpulse entry survived uninstall:\n%s", final)
	}
	// The other tool's entry — no "timeout", an unknown "id" field — must
	// survive with its own fields intact, not reconstructed through a
	// struct that would silently drop "id" or add a fabricated "timeout".
	// (The file as a whole is still re-indented by prettyJSON each time a
	// Plan is built, so this checks content, not exact original spacing —
	// see the indent-detection tests for whitespace fidelity.)
	var got map[string]any
	if err := json.Unmarshal(final, &got); err != nil {
		t.Fatalf("final settings is not valid JSON: %v", err)
	}
	entries := got["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one surviving hook entry, got %d: %v", len(entries), entries)
	}
	entry := entries[0].(map[string]any)
	if entry["command"] != "my-other-tool hook" {
		t.Errorf("command = %v, want \"my-other-tool hook\"", entry["command"])
	}
	if entry["id"] != "lint-1" {
		t.Errorf("unknown field \"id\" was not preserved: %v", entry["id"])
	}
	if _, hasTimeout := entry["timeout"]; hasTimeout {
		t.Errorf("a \"timeout\" field was fabricated where the original had none: %v", entry)
	}
}

func TestIndentDetectionRoundTrips(t *testing.T) {
	cases := []struct {
		name   string
		indent string
	}{
		{"two spaces", "  "},
		{"four spaces", "    "},
		{"tab", "\t"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			settingsPath := filepath.Join(dir, "settings.json")
			// An unrelated value containing "&&" and ">" must survive
			// unescaped: confirms marshalNoEscape is doing its job
			// for every indent style, not just the default. No "hooks" key
			// to start with: this is the common real-world case, and lets
			// the round trip below prove install-then-uninstall restores
			// the file exactly, not just to an empty "hooks": {}.
			original := canonicalIndent(t, `{"buildCommand": "make lint && echo done > out.txt"}`, tc.indent)
			writeFile(t, settingsPath, original)

			install, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
			if err != nil {
				t.Fatalf("PlanInstall: %v", err)
			}
			if err := install.Apply(); err != nil {
				t.Fatalf("Apply install: %v", err)
			}

			installed, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading installed settings: %v", err)
			}
			if !strings.Contains(string(installed), `make lint && echo done > out.txt`) {
				t.Errorf("&& or > was HTML-escaped in the installed file:\n%s", installed)
			}
			// Check for the literal 6-character escape sequence text, not
			// the character it would decode to (a naive `"&"` in Go
			// source is just "&" and would defeat this check).
			if strings.Contains(string(installed), `\`+"u0026") || strings.Contains(string(installed), `\`+"u003e") {
				t.Errorf("installed file contains HTML-escaped characters:\n%s", installed)
			}
			// Confirm the indent unit itself was detected and reused for
			// the "hooks" key this install appended: it must start its own
			// line with exactly tc.indent, not some other width.
			if !strings.Contains(string(installed), "\n"+tc.indent+`"hooks":`) {
				t.Errorf("indent not preserved for the appended hooks key (want prefix %q):\n%s", tc.indent, installed)
			}

			uninstall, err := PlanUninstall(settingsPath, testOwner)
			if err != nil {
				t.Fatalf("PlanUninstall: %v", err)
			}
			if err := uninstall.Apply(); err != nil {
				t.Fatalf("Apply uninstall: %v", err)
			}

			restored, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading restored settings: %v", err)
			}
			if string(restored) != string(original) {
				t.Fatalf("round trip with %s indent is not byte-for-byte identical.\n--- original ---\n%s\n--- restored ---\n%s",
					tc.name, original, restored)
			}
		})
	}
}

func TestUTF8BOMIsStrippedAndReemitted(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	body := canonical(t, `{"model": "claude-sonnet-4"}`)
	original := append(append([]byte{}, utf8BOM...), body...)
	writeFile(t, settingsPath, original)

	install, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall: %v", err)
	}
	if !bytes.HasPrefix(install.After, utf8BOM) {
		t.Errorf("installed content lost the UTF-8 BOM")
	}
	if err := install.Apply(); err != nil {
		t.Fatalf("Apply install: %v", err)
	}

	uninstall, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if err := uninstall.Apply(); err != nil {
		t.Fatalf("Apply uninstall: %v", err)
	}

	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading restored settings: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("BOM round trip is not byte-for-byte identical.\n--- original ---\n%q\n--- restored ---\n%q", original, restored)
	}
}

func TestEmptySettingsFileIsTreatedAsEmptyObject(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, []byte{}) // present, but 0 bytes

	p, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall on an empty file returned an error: %v", err)
	}
	if !p.Changed {
		t.Errorf("expected installing into an empty file to change it")
	}
	if err := p.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	uninstall, err := PlanUninstall(settingsPath, testOwner)
	if err != nil {
		t.Fatalf("PlanUninstall: %v", err)
	}
	if err := uninstall.Apply(); err != nil {
		t.Fatalf("Apply uninstall: %v", err)
	}

	// Known limitation: a literal 0-byte (or nonexistent)
	// starting file normalizes to "{}" rather than back to 0 bytes. There
	// is no way to tell those two apart later without persisting state
	// across calls (which the settings-merge design deliberately avoids),
	// so once install has had to create *something*, uninstall's only
	// stateless option is the same "delete hooks, collapse to {}" rule
	// used for every other file. See TestByteIdentityAfterInstallThenUninstall's
	// "empty JSON object" case for the byte-identical round trip this
	// package does guarantee.
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading restored settings: %v", err)
	}
	if string(restored) != "{}\n" {
		t.Errorf("restored content = %q, want the minimal empty object", restored)
	}
}

// TestByteIdentityAfterInstallThenUninstall covers a 4-space file, a tab
// file, a UTF-8-BOM file, a minimal empty-object
// file, and a realistic hand-written file with a one-line "permissions"
// value — each must round-trip byte-for-byte through install then
// uninstall. This is what proves the splice approach (splice.go): every
// byte outside the "hooks" member, however it was originally formatted,
// survives untouched.
func TestByteIdentityAfterInstallThenUninstall(t *testing.T) {
	cases := []struct {
		name     string
		original []byte
	}{
		{
			name:     "4-space indent",
			original: canonicalIndent(t, `{"model": "claude-sonnet-4"}`, "    "),
		},
		{
			name:     "tab indent",
			original: canonicalIndent(t, `{"model": "claude-sonnet-4"}`, "\t"),
		},
		{
			name:     "UTF-8 BOM",
			original: append(append([]byte{}, utf8BOM...), canonical(t, `{"model": "claude-sonnet-4"}`)...),
		},
		{
			name:     "empty JSON object",
			original: []byte("{}\n"),
		},
		{
			name: "realistic hand-written file with a one-line permissions value",
			original: []byte(
				"{\n" +
					"  \"model\": \"claude-sonnet-4\",\n" +
					"  \"permissions\": {\"allow\": [\"Bash(npm run test:*)\", \"Bash(npm run lint:*)\"], \"deny\": []}\n" +
					"}\n"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			settingsPath := filepath.Join(dir, "settings.json")
			writeFile(t, settingsPath, tc.original)

			install, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
			if err != nil {
				t.Fatalf("PlanInstall: %v", err)
			}
			if err := install.Apply(); err != nil {
				t.Fatalf("Apply install: %v", err)
			}

			installed, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading installed settings: %v", err)
			}
			if string(installed) == string(tc.original) {
				t.Fatalf("install did not change the file at all")
			}

			uninstall, err := PlanUninstall(settingsPath, testOwner)
			if err != nil {
				t.Fatalf("PlanUninstall: %v", err)
			}
			if err := uninstall.Apply(); err != nil {
				t.Fatalf("Apply uninstall: %v", err)
			}

			restored, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading restored settings: %v", err)
			}
			if string(restored) != string(tc.original) {
				t.Fatalf("round trip is not byte-for-byte identical.\n--- original ---\n%q\n--- restored ---\n%q",
					tc.original, restored)
			}
		})
	}
}

// TestPlanAbortsOnDuplicateTopLevelKey covers a settings file listing
// the same top-level key twice, which is ambiguous (which
// value is "real"?), so it must abort rather than silently pick one, the
// way json.Unmarshal into a map would.
func TestPlanAbortsOnDuplicateTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	original := []byte(`{ "hooks": {"Stop": []}, "model": "x", "hooks": {"Notification": []} }`)
	writeFile(t, settingsPath, original)

	_, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err == nil {
		t.Fatal("expected PlanInstall to abort on a duplicate top-level key")
	}
	if !strings.Contains(err.Error(), `"hooks"`) || !strings.Contains(err.Error(), "twice") {
		t.Errorf("error does not describe the duplicate key: %v", err)
	}

	after, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatalf("reading settings after aborted install: %v", readErr)
	}
	if string(after) != string(original) {
		t.Errorf("settings file was written despite the abort:\n%s", after)
	}
}

// TestPlanAbortsOnContentAfterClosingBrace covers anything after the
// top-level object's closing '}' — malformed junk, or
// even a second, perfectly valid JSON value — must abort rather than be
// silently ignored.
func TestPlanAbortsOnContentAfterClosingBrace(t *testing.T) {
	cases := []struct {
		name     string
		original string
	}{
		{"malformed trailing junk", "{\"model\":\"x\"}\njunk\n"},
		{"a second valid JSON value", `{"a":1}{"b":2}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			settingsPath := filepath.Join(dir, "settings.json")
			writeFile(t, settingsPath, []byte(tc.original))

			_, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
			if err == nil {
				t.Fatal("expected PlanInstall to abort on trailing content")
			}

			after, readErr := os.ReadFile(settingsPath)
			if readErr != nil {
				t.Fatalf("reading settings after aborted install: %v", readErr)
			}
			if string(after) != tc.original {
				t.Errorf("settings file was written despite the abort:\n%s", after)
			}
		})
	}
}

// TestPlanAllowsTrailingWhitespaceAfterClosingBrace confirms the
// trailing-content check doesn't reject the completely ordinary case of
// a settings file
// ending in a trailing newline (or several), which is how nearly every
// real settings.json is written.
func TestPlanAllowsTrailingWhitespaceAfterClosingBrace(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	writeFile(t, settingsPath, []byte("{\"model\":\"x\"}\n\n  \n"))

	_, err := PlanInstall(settingsPath, testOwner, agentpulseEntries("/opt/agentpulse/bin/agentpulse", "/tmp/rec"))
	if err != nil {
		t.Fatalf("PlanInstall rejected ordinary trailing whitespace: %v", err)
	}
}
