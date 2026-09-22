package main

import (
	"bytes"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
)

// BenchmarkClassifyAndSpool measures BR-02's budget directly: "classify
// plus spool append must be under 50 ms p95". It repeatedly classifies a
// realistic PreToolUse Bash verification call (the heaviest single hook
// type: it decodes tool_input, runs DetectRunner's regex table, and
// appends to the spool) for one long-running session, so scratch load/save
// and the spool's flock are exercised exactly as they would be in
// production, not skipped. Run it with:
//
//	go test ./cmd/agentpulse/ -bench BenchmarkClassifyAndSpool -benchtime=2000x
//
// and report the resulting ns/op (p95 across -count=N repeats, or the
// reported average as a proxy).
func BenchmarkClassifyAndSpool(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_STATE_HOME", b.TempDir())

	raw := []byte(`{"hook_event_name":"PreToolUse","session_id":"sess_bench","cwd":"/nonexistent/agentpulse-bench-project","tool_name":"Bash","tool_input":{"command":"pytest -q tests/"}}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := classifyAndSpool(raw); err != nil {
			b.Fatalf("classifyAndSpool() returned error: %v", err)
		}
	}
}

// BenchmarkHook measures the actual BR-02 budget: the full
// "agentpulse hook" body (runHook), including spawnFlushIfNeeded's
// non-blocking flock peek and — whenever the peek finds the lock free,
// which is most iterations, since the spawned "flush" exits almost
// immediately while unpaired — the real process spawn. Run it with:
//
//	go test ./cmd/agentpulse/ -bench BenchmarkHook -benchtime 200x
//
// and report the p95 (ns/op, or -count=N with benchstat for a real
// percentile); NFR-02/BR-02 require it under 50ms.
//
// selfExecutable is pointed at a real compiled agentpulse binary (built
// once in b.TempDir(), outside the timed loop) rather than os.Executable()
// (the ephemeral go test binary), which would not behave as "agentpulse
// flush" when exec'd — see hook.go's selfExecutable doc comment. The
// spawned binary finds no config.json (XDG_CONFIG_HOME is a fresh
// b.TempDir()), so it is unpaired and every spawned flush exits in
// microseconds without ever contacting a relay.
func BenchmarkHook(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_STATE_HOME", b.TempDir())

	binPath := filepath.Join(b.TempDir(), "agentpulse-bench")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		b.Fatalf("building agentpulse for BenchmarkHook: %v\n%s", err, out)
	}

	origSelf := selfExecutable
	selfExecutable = func() (string, error) { return binPath, nil }
	b.Cleanup(func() { selfExecutable = origSelf })

	raw := []byte(`{"hook_event_name":"PreToolUse","session_id":"sess_bench_hook","cwd":"/nonexistent/agentpulse-bench-project","tool_name":"Bash","tool_input":{"command":"pytest -q tests/"}}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runHook(bytes.NewReader(raw), "", false, io.Discard)
	}
}
