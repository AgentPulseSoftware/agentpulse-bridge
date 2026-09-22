package main

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// TestRunHookDoesNothingWhenRecordDirIsUnset and the rest of this file
// exercise behavior that must hold in every build, dev or release: they
// carry no build tag. Tests specific to recording actually happening (or,
// in the release build, deliberately not happening) live in
// hook_dev_test.go and hook_release_test.go.

// setTestXDGDirs points every XDG path this package resolves (config,
// state, scratch, spool) at fresh temporary directories, so no test in
// this file — however it drives runHook or the "hook" command — can ever
// read or write the real developer machine's ~/.config or ~/.local/state.
// It also points $HOME at a fresh temporary directory, so
// defaultSettingsPath() (used by "doctor"'s checks 1 and 2)
// never resolves to a real ~/.claude/settings.json either.
func setTestXDGDirs(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// stubSelfExecutable makes spawnFlushIfNeeded's exec attempt fail
// immediately and harmlessly (a nonexistent path), restoring the real
// selfExecutable on cleanup.
//
// Any test that reaches runHook — whether by calling it directly or by
// driving the real "hook" command through Cobra (root.Execute() with args
// {"hook"}) — must call this first. runHook always calls
// spawnFlushIfNeeded, with no way to opt out from the caller's side: left
// at its default, selfExecutable is os.Executable(), which under `go
// test` resolves to the compiled *test binary* for this package —
// spawnFlushIfNeeded would then exec that test binary with argv "flush",
// which, having no "-test.run" filter, runs this package's entire test
// suite again, including this same hook-spawning test, recursively, as
// many times as it takes to exhaust the machine. stubSelfExecutable
// guards against exactly this; every test that reaches runHook calls it
// first (see the note above).
// (hook_bench_test.go's BenchmarkHook already
// works around the same hazard, by pointing selfExecutable at a real,
// separately built binary instead.)
func stubSelfExecutable(t *testing.T) {
	t.Helper()
	original := selfExecutable
	selfExecutable = func() (string, error) {
		return "", errors.New("self-exec disabled in tests")
	}
	t.Cleanup(func() { selfExecutable = original })
}

func TestRunHookDoesNothingWhenRecordDirIsUnset(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	var stderr bytes.Buffer
	// Must not panic, print, or otherwise fail when there is nowhere to
	// record to; this is the normal (non-recording) path (BR-02).
	runHook(strings.NewReader(`{"hook_event_name":"Stop"}`), "", false, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output: %q", stderr.String())
	}
}

func TestRunHookHandlesEmptyStdin(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	var stderr bytes.Buffer
	runHook(strings.NewReader(""), "", false, &stderr)
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr output: %q", stderr.String())
	}
}

// TestHookCommandExitsCleanlyWhenSpoolDirUnwritable is ERR-02's "hook"
// half driven through the real "hook" command (not just runHook), the
// same way TestHookCommandAlwaysExitsCleanly is: a spool directory this
// process cannot write to (a scratch state dir chmodded 0500, never a
// real one) must still leave "hook" exiting 0, silently, exactly like
// every other classifyAndSpool failure (BR-02, BR-04).
func TestHookCommandExitsCleanlyWhenSpoolDirUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits")
	}
	setTestXDGDirs(t)
	stubSelfExecutable(t)

	stateDir := xdgpaths.StateDir()
	if err := os.MkdirAll(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	root := newRootCmd()
	root.SetIn(strings.NewReader(`{"hook_event_name":"Stop","session_id":"sess_unwritable"}`))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"hook"})

	if err := root.Execute(); err != nil {
		t.Fatalf("hook command returned an error, must always exit 0: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("unexpected stdout output: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr output without AGENTPULSE_DEBUG: %q", errOut.String())
	}
}

func TestHookCommandAlwaysExitsCleanly(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	root := newRootCmd()
	root.SetIn(strings.NewReader(`{"hook_event_name":"Stop"}`))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"hook"})

	if err := root.Execute(); err != nil {
		t.Fatalf("hook command returned an error, must always exit 0: %v", err)
	}
}

// TestHookCommandRecoversFromPanic substitutes a panicking maybeRecord to
// prove newHookCmd's recover() actually backstops BR-02's "exits 0 on
// every path" guarantee against a panic anywhere in the call chain, not
// just a returned error.
func TestHookCommandRecoversFromPanic(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	original := maybeRecord
	maybeRecord = func(dir string, raw []byte) error {
		panic("boom")
	}
	defer func() { maybeRecord = original }()

	t.Setenv("AGENTPULSE_RECORD_DIR", t.TempDir())
	t.Setenv("AGENTPULSE_DEBUG", "1")

	root := newRootCmd()
	root.SetIn(strings.NewReader(`{"hook_event_name":"Stop"}`))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"hook"})

	if err := root.Execute(); err != nil {
		t.Fatalf("hook command returned an error after a recovered panic, must always exit 0: %v", err)
	}
	if !strings.Contains(errOut.String(), "recovered from panic") {
		t.Errorf("expected a debug message about the recovered panic, got %q", errOut.String())
	}
}

// FuzzHook is the SPEC 18 fuzz test: "feeds random and malformed JSON to
// hook and asserts exit 0". It drives the real "hook" command end to end
// (not just runHook) so a panic anywhere in the chain, not only a returned
// error, is exercised through newHookCmd's own recover().
func FuzzHook(f *testing.F) {
	f.Add([]byte(`{"hook_event_name":"Stop"}`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"hook_event_name":`))
	f.Add([]byte(`{"hook_event_name": null}`))
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte("\x00\x01\xff\xfe"))
	f.Add([]byte(`{"hook_event_name":"../../../etc/passwd"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		setTestXDGDirs(t)
		stubSelfExecutable(t)
		t.Setenv("AGENTPULSE_RECORD_DIR", t.TempDir())

		root := newRootCmd()
		root.SetIn(bytes.NewReader(data))
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs([]string{"hook"})

		if err := root.Execute(); err != nil {
			t.Fatalf("hook returned an error for input %q, must always exit 0: %v", data, err)
		}
		if out.Len() != 0 {
			t.Fatalf("hook wrote to stdout for input %q: %q", data, out.String())
		}
		if errOut.Len() != 0 {
			t.Fatalf("hook wrote to stderr without AGENTPULSE_DEBUG for input %q: %q", data, errOut.String())
		}
	})
}
