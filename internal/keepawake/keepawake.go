// Package keepawake spawns the platform's own idle-inhibiting tool, bound
// to a Claude Code session's process ID, so a long-running agent session
// does not get interrupted by the machine sleeping (BR-16, opt-in via
// "agentpulse projects --keep-awake on").
//
// Every exported entry point here is deliberately narrow, because this is
// the one place in the bridge that spawns a process outside its own
// control (SEC-09's prompt-injection posture: "the bridge never executes,
// evaluates, or shells out based on hook contents except the fixed
// caffeinate or systemd-inhibit invocation with a numeric PID"):
//
//   - Command returns a fixed argument vector for pid and nothing else;
//     it never runs anything, so it is safe to call from a test or from
//     "doctor" just to check platform support.
//   - Start is the only function that actually spawns a process, and it
//     spawns exactly Command's argv with no shell (never "sh -c") and no
//     value beyond the platform's fixed words plus a single decimal pid —
//     never anything derived from Claude Code hook input.
//   - Start never calls Wait on the process it starts. The whole point of
//     keep-awake is that it outlives "agentpulse hook" (which exits in
//     milliseconds); waiting on it would defeat that, and is exactly the
//     kind of thing SEC-09 rules out this package ever needing to do.
package keepawake

import (
	"errors"
	"os/exec"
	"syscall"
)

// ErrNotSupported is returned by Start (and reported by Command's ok
// return) when keep-awake has no implementation on this platform, or a
// helper binary it needs (systemd-inhibit or tail, on Linux) is not on
// PATH. Callers treat this as "log once and do nothing", never as an
// error worth surfacing to the person running Claude Code.
var ErrNotSupported = errors.New("keepawake: not supported on this platform")

// Command returns the exact, fixed argument vector this platform uses to
// keep the machine awake for the lifetime of the process identified by
// pid. It is pure: it never executes anything, only decides what Start
// would run, which is what makes it safe for a table test to assert
// against directly and for "doctor" to call just to check platform
// support (with a throwaway pid).
//
// ok is false when this platform has no keep-awake implementation, or
// (Linux) a required helper binary is not on PATH.
func Command(pid int) (argv []string, ok bool) {
	return platformCommand(pid)
}

// Supported reports whether keep-awake can run at all on this machine,
// without needing a real PID — "agentpulse doctor"'s informational
// keep-awake row uses this to tell "on and supported" apart from "on,
// but not supported here".
func Supported() bool {
	_, ok := platformCommand(0)
	return ok
}

// Start spawns Command's argument vector for pid, detached from this
// process (SEC-09, BR-16):
//
//   - the argv is exactly what Command returns: fixed platform words plus
//     one decimal pid, never a shell, never anything from hook input.
//   - Stdin/Stdout/Stderr are left nil (no pipe to leak or block on).
//   - SysProcAttr.Setsid starts the child in its own session, so it
//     survives "agentpulse hook" exiting instead of being torn down with
//     it.
//   - Start returns as soon as the process has started; it never calls
//     Wait, so it can never block on, or be tied to the lifetime of,
//     anything the keep-awake process itself waits on.
//
// Start returns an error rather than logging one itself; the caller
// (classifyAndSpool) decides how to fold that into its own error
// reporting, exactly like every other local failure on that path.
func Start(pid int) (spawnedPID int, err error) {
	argv, ok := Command(pid)
	if !ok {
		return 0, ErrNotSupported
	}
	return startArgv(argv)
}

// startArgv is Start's actual spawn, factored out so the package's own
// test can exercise the real detached-spawn mechanics (Setsid, no Wait)
// against a harmless, always-available command (/bin/sleep) instead of
// caffeinate or systemd-inhibit, without duplicating any of the SEC-09
// rules Start itself must follow — this is the one and only place this
// package calls exec.Command.
func startArgv(argv []string) (spawnedPID int, err error) {
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // fixed platform argv plus one numeric pid, never a shell, never hook-derived (SEC-09)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
