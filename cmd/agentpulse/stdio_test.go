package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
)

// testIsolationEnv is the baseline environment every test in this file
// that execs the real binary starts from: AGENTPULSE_RELAY points at a
// closed local port — 1 is reserved and nothing ever listens there, so a
// connection attempt is refused immediately instead of timing out — and
// AGENTPULSE_TEST_CRED_BACKEND=file (named so it reads as a test seam,
// not a supported setting) forces
// internal/cred's Default() straight to the file fallback (see
// internal/cred/select.go). Before this, these tests relied only on the
// bridge being unpaired (config.Load's Default() short-circuits
// runFlush before it ever resolves a relay URL or a credential store) to
// keep them off the network and the real Keychain/Secret Service,
// rather than enforcing isolation directly and independent of pairing
// state.
var testIsolationEnv = []string{
	"AGENTPULSE_RELAY=http://127.0.0.1:1",
	"AGENTPULSE_TEST_CRED_BACKEND=file",
}

// isolatedDir is t.TempDir(), except its cleanup tolerates a straggling
// detached "agentpulse flush" child still writing inside it for a moment
// after the test that spawned it has already returned.
//
// isolatedDir guards against this: hook.go's spawnFlushIfNeeded
// spawns "agentpulse flush" and — correctly, per BR-02, which forbids
// "hook" from waiting on anything — never waits for it. That detached
// child (BR-02/BR-03's whole point) can still be running, creating
// flush.lock or bridge.log under XDG_STATE_HOME, milliseconds after the
// exec'd "hook" process this test waited on (cmd.Run()) has already
// exited. Plain t.TempDir()'s cleanup does exactly one os.RemoveAll and
// fails the test hard if that straggler creates a file mid-removal
// ("directory not empty"). flushBudget (flush.go) bounds how long a
// straggler can possibly still be running, so retrying up to that long
// is a real bound, not a blind loop.
func isolatedDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "agentpulse-stdio-test-dir")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(flushBudget + 2*time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("removing %s: %v (a detached flush child may still be running)", dir, err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}

// buildAgentpulseOnce compiles the real release binary exactly once for
// this test file (the same pattern hook_bench_test.go uses for
// BenchmarkHook), so the BR-04 stdio tests below exec it directly rather
// than driving runHook/runFlush in-process — those functions' own
// deps.stderr injection already proves the message content is right
// (hook_test.go, flush_test.go); what this file proves is the OS-level
// guarantee itself: with AGENTPULSE_DEBUG unset, nothing written by
// internal/logging's real os.Stderr mirror (which is not test-injectable
// — see internal/logging's Get()) ever reaches the process's actual
// stdout or stderr file descriptors.
var (
	buildOnce         sync.Once
	agentpulseBinPath string
	buildErr          error
)

func buildAgentpulseOnce(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentpulse-stdio-test")
		if err != nil {
			buildErr = err
			return
		}
		agentpulseBinPath = filepath.Join(dir, "agentpulse-stdio-test")
		build := exec.Command("go", "build", "-o", agentpulseBinPath, ".")
		if out, err := build.CombinedOutput(); err != nil {
			buildErr = err
			t.Logf("build output: %s", out)
		}
	})
	if buildErr != nil {
		t.Fatalf("building agentpulse for stdio tests: %v", buildErr)
	}
	return agentpulseBinPath
}

// runAgentpulse execs the built binary with args, stdin as given, and the
// provided extra environment (on top of a minimal, XDG-scoped
// environment so it never touches the real developer machine's config or
// state), returning separately captured stdout and stderr.
func runAgentpulse(t *testing.T, bin string, args []string, stdin string, env ...string) (stdout, stderr []byte) {
	t.Helper()
	cmd := exec.Command(bin, args...) //nolint:gosec // bin is our own just-built binary, args are fixed by the test
	cmd.Stdin = bytes.NewBufferString(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.Env = append(append([]string{
		"XDG_CONFIG_HOME=" + isolatedDir(t),
		"XDG_STATE_HOME=" + isolatedDir(t),
		"PATH=" + os.Getenv("PATH"), // exec itself still needs to be resolvable
	}, testIsolationEnv...), env...)
	err := cmd.Run()
	if err != nil {
		t.Fatalf("running agentpulse %v: %v (want it to always exit 0 — BR-02/BR-03)", args, err)
	}
	return outBuf.Bytes(), errBuf.Bytes()
}

func TestHookProducesNoStdioWithoutDebug(t *testing.T) {
	bin := buildAgentpulseOnce(t)
	stdout, stderr := runAgentpulse(t, bin, []string{"hook"}, "not valid json at all")
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestHookMirrorsToStderrWithDebug(t *testing.T) {
	bin := buildAgentpulseOnce(t)
	stdout, stderr := runAgentpulse(t, bin, []string{"hook"}, "not valid json at all", "AGENTPULSE_DEBUG=1")
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty even with AGENTPULSE_DEBUG=1 (BR-02/BR-04 only ever mirror to stderr)", stdout)
	}
	if len(stderr) == 0 {
		t.Error("stderr is empty, want a debug line for the malformed hook JSON (ERR-01) with AGENTPULSE_DEBUG=1")
	}
}

// runAgentpulseFlushWithBrokenStateDir is TestFlushProducesNoStdioWithoutDebug
// and TestFlushMirrorsToStderrWithDebug's shared setup: XDG_STATE_HOME
// points at a regular file, not a directory, so flushLockPath's
// os.MkdirAll fails deterministically — a real local I/O error, without
// any network — and returns captured stdout/stderr.
func runAgentpulseFlushWithBrokenStateDir(t *testing.T, bin string, extraEnv ...string) (stdout, stderr []byte) {
	t.Helper()
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "flush") //nolint:gosec // bin is our own just-built binary
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.Env = append(append([]string{
		"XDG_CONFIG_HOME=" + filepath.Join(t.TempDir()),
		"XDG_STATE_HOME=" + notADir,
		"PATH=" + os.Getenv("PATH"),
	}, testIsolationEnv...), extraEnv...)
	if err := cmd.Run(); err != nil {
		t.Fatalf("running agentpulse flush: %v (want it to always exit 0 — BR-03)", err)
	}
	return outBuf.Bytes(), errBuf.Bytes()
}

func TestFlushProducesNoStdioWithoutDebug(t *testing.T) {
	bin := buildAgentpulseOnce(t)
	stdout, stderr := runAgentpulseFlushWithBrokenStateDir(t, bin)
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestFlushMirrorsToStderrWithDebug(t *testing.T) {
	bin := buildAgentpulseOnce(t)
	stdout, stderr := runAgentpulseFlushWithBrokenStateDir(t, bin, "AGENTPULSE_DEBUG=1")
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want empty even with AGENTPULSE_DEBUG=1", stdout)
	}
	if len(stderr) == 0 {
		t.Error("stderr is empty, want a debug line for the lock-acquisition failure with AGENTPULSE_DEBUG=1")
	}
}

// TestHookReturnsQuicklyEvenWhenFlushIsSlow is a regression test: "agentpulse
// hook" spawns
// a detached "agentpulse flush" (hook.go's spawnFlushIfNeeded) and must
// never wait on it, no matter how long that child ends up taking.
//
// It exercises the real, paired path end to end: a real config.json with
// a bridge_id, a real credential in the file-backend fallback (forced via
// AGENTPULSE_TEST_CRED_BACKEND=file so this never touches a real platform
// store), and AGENTPULSE_RELAY pointed at an HTTP server that never
// answers — so the spawned flush really would run for BR-03's whole
// 5-second budget (flushBudget in flush.go) if "hook" ever waited on it.
// hook.go's own detachment (Setpgid, all three of the child's stdio
// redirected to os.DevNull — never inherited from "hook"'s own pipes)
// must make the parent "agentpulse hook" process return in well under a
// second regardless.
func TestHookReturnsQuicklyEvenWhenFlushIsSlow(t *testing.T) {
	bin := buildAgentpulseOnce(t)

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Mirrors flush_test.go's TestRunFlushHungServerCutOffByBudget: the
	// handler must be unblocked before Close() (which waits for in-flight
	// handlers) is called, so close(block) has to run first — a plain
	// "defer srv.Close()" after "defer close(block)" would deadlock (LIFO
	// order runs Close() first).
	defer func() {
		close(block)
		srv.Close()
	}()

	// isolatedDir, not t.TempDir(): this test's own spawned flush child
	// (unlike the others in this file) is guaranteed to do real work —
	// paired config, a real file-backend credential — against a slow
	// relay, so it is the likeliest of all of them to still be writing
	// under XDG_STATE_HOME after "hook" itself has already returned.
	configHome := isolatedDir(t)
	stateHome := isolatedDir(t)
	agentpulseConfigDir := filepath.Join(configHome, "agentpulse")
	if err := os.MkdirAll(agentpulseConfigDir, 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	cfgData, err := json.Marshal(config.Config{BridgeID: "brg_test_slow_flush"})
	if err != nil {
		t.Fatalf("marshaling test config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentpulseConfigDir, "config.json"), cfgData, 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentpulseConfigDir, "credentials"), []byte("s3cr3t"), 0o600); err != nil {
		t.Fatalf("writing test credential: %v", err)
	}

	cmd := exec.Command(bin, "hook") //nolint:gosec // bin is our own just-built binary
	cmd.Stdin = bytes.NewBufferString(`{"hook_event_name":"SessionStart","session_id":"sess_slow_flush"}`)
	// Stdout/Stderr are ordinary (non-*os.File) io.Writers, exactly like
	// runAgentpulse's elsewhere in this file: os/exec backs those with a
	// real pipe and a goroutine that copies until EOF, and Wait() (which
	// Run() calls) blocks on that goroutine finishing — the precise
	// mechanism that could let a child that inherited these pipes hang the
	// parent, if hook.go didn't redirect to os.DevNull instead. Capturing output
	// this way, rather than leaving it nil (silently discarded), means
	// this test would actually catch a regression that broke that
	// redirection, not just fail to notice one.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = []string{
		"XDG_CONFIG_HOME=" + configHome,
		"XDG_STATE_HOME=" + stateHome,
		"PATH=" + os.Getenv("PATH"),
		"AGENTPULSE_TEST_CRED_BACKEND=file",
		"AGENTPULSE_RELAY=" + srv.URL,
	}

	// Run in a goroutine with a bounded wait, not a direct cmd.Run(): if
	// hook.go's detachment ever regresses, cmd.Run() itself would hang
	// (waiting on the pipe-copy goroutine above), and this test should
	// fail fast with a clear message instead of hanging until go test's
	// own -timeout kills the whole binary.
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("running agentpulse hook: %v (want it to always exit 0 — BR-02)\nstdout: %s\nstderr: %s", err, stdout.Bytes(), stderr.Bytes())
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("agentpulse hook took %v to return, want well under a second: it must not wait on the detached flush child (BR-02/BR-03)", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("agentpulse hook did not return within 4s: it appears to be waiting on the detached flush child (BR-02/BR-03)")
	}
}
