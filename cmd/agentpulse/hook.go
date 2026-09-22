package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/lockfile"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/logging"
)

// newHookCmd builds the "agentpulse hook" command (BR-02). It must exit 0 on
// every path and must never write to stdout or stderr unless
// AGENTPULSE_DEBUG=1 is set, because it runs synchronously inside every
// Claude Code tool call (CLAUDE.md). A deferred recover() backstops
// that guarantee against any panic anywhere in the call chain (including
// inside maybeRecord), per SPEC 18's fuzzing requirement.
//
// Recording (AGENTPULSE_RECORD_DIR) only ever runs in a development build
// (see maybeRecord, hook_dev.go, hook_release.go); classification and
// spooling (hook_classify.go) run in every build.
func newHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:                   "hook",
		Short:                 "Receive one Claude Code hook event on stdin",
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			debug := os.Getenv("AGENTPULSE_DEBUG") == "1"
			defer func() {
				if r := recover(); r != nil {
					debugf(cmd.ErrOrStderr(), debug, "agentpulse hook: recovered from panic: %v", r)
				}
				err = nil // always exit 0 (BR-02), even after a recovered panic
			}()
			runHook(cmd.InOrStdin(), os.Getenv("AGENTPULSE_RECORD_DIR"), debug, cmd.ErrOrStderr())
			return nil
		},
	}
}

// runHook implements the body of "agentpulse hook" as a plain function so it
// can be unit tested without exec'ing a binary. It never returns an error:
// every failure is swallowed (and, only if debug is true, logged to
// stderr), exactly as BR-02 and BR-04 require.
func runHook(stdin io.Reader, recordDir string, debug bool, stderr io.Writer) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		debugf(stderr, debug, "agentpulse hook: reading stdin: %v", err)
		return
	}
	if len(raw) == 0 {
		return
	}

	if recordDir != "" {
		if err := maybeRecord(recordDir, raw); err != nil {
			debugf(stderr, debug, "agentpulse hook: recording: %v", err)
		}
	}

	if err := classifyAndSpool(raw); err != nil {
		debugf(stderr, debug, "agentpulse hook: %v", err)
	}

	spawnFlushIfNeeded(debug, stderr)
}

// spawnFlushIfNeeded is BR-02's second half: spawn a detached "agentpulse
// flush" so the event classifyAndSpool just appended (if any) actually
// reaches the relay, without "hook" waiting on any of that itself.
//
// It first peeks flush.lock with a non-blocking try-then-immediately-
// release: if another flush is already running, the lock is held and this
// skips the spawn entirely, so the common path under a burst of hook
// calls costs one flock syscall rather than a process spawn. This is a
// heuristic, not a guarantee — flush.go's own TryLock is what actually
// enforces "only one flusher per user" (BR-03); a spawn that loses a tiny
// race here just becomes a flush that exits immediately once it finds the
// lock held.
// selfExecutable resolves the path this process should exec as "flush"
// (os.Executable() in production). It is a var, not a direct call, so
// BenchmarkHook can point it at a real compiled agentpulse binary instead
// of the ephemeral `go test` binary — argv[0] "flush" against a test
// binary would re-enter the test runner's own flag parsing, not run the
// flush command.
var selfExecutable = os.Executable

func spawnFlushIfNeeded(debug bool, stderr io.Writer) {
	unlock, ok, err := lockfile.TryLock(flushLockPath())
	if err != nil {
		debugf(stderr, debug, "agentpulse hook: checking flush lock: %v", err)
		return
	}
	if !ok {
		return
	}
	unlock()

	self, err := selfExecutable()
	if err != nil {
		debugf(stderr, debug, "agentpulse hook: resolving own path: %v", err)
		return
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		debugf(stderr, debug, "agentpulse hook: opening %s: %v", os.DevNull, err)
		return
	}
	defer func() { _ = devNull.Close() }()

	cmd := exec.Command(self, "flush") //nolint:gosec // self is our own resolved binary path, "flush" is a fixed argument
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		debugf(stderr, debug, "agentpulse hook: spawning flush: %v", err)
		return
	}
	_ = cmd.Process.Release()
}

// debugf is every "hook" and "flush" call site's one diagnostic helper
// (BR-04, ERR-01): it always writes to the bridge's own rotating log
// file via internal/logging — at debug level, so it only actually lands
// there once AGENTPULSE_DEBUG=1 raises the log's threshold, matching
// ERR-01's "log at debug level" exactly — and, only when enabled (the
// caller's own AGENTPULSE_DEBUG=1 check), additionally mirrors the same
// line to w (the command's own stderr), which is what lets tests assert
// on exactly what a real invocation would print without touching the
// real os.Stderr internal/logging's own debug mirror writes to.
func debugf(w io.Writer, enabled bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logging.Get().Debug(msg)
	if !enabled {
		return
	}
	_, _ = fmt.Fprintf(w, "%s\n", msg)
}
