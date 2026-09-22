//go:build linux

package keepawake

import (
	"os/exec"
	"strconv"
)

// lookPath is exec.LookPath, overridable by tests so they can simulate
// systemd-inhibit or tail being present or absent without depending on
// what is actually installed on the machine running the test.
var lookPath = exec.LookPath

// platformCommand is BR-16's Linux behavior. BR-16's literal wording is
// "systemd-inhibit ... sleep infinity, bound to the same PID by a
// watcher". The bridge's own hook process exits in milliseconds, so
// it cannot be that watcher, and a detached "sleep infinity" with nothing
// watching it would inhibit idle forever. "tail --pid=<pid> -f /dev/null"
// is the watcher instead: it is systemd-inhibit's own child process (not
// a separate watcher of a separate sleep), it exits on its own the moment
// the Claude Code PID exits, ending the inhibitor with it, and the
// argument vector stays exactly as fixed as caffeinate's — every word
// but the one decimal pid is a constant.
//
// ok is false if either systemd-inhibit or tail is not on PATH.
func platformCommand(pid int) (argv []string, ok bool) {
	if _, err := lookPath("systemd-inhibit"); err != nil {
		return nil, false
	}
	if _, err := lookPath("tail"); err != nil {
		return nil, false
	}
	return []string{
		"systemd-inhibit",
		"--what=idle",
		"--who=agentpulse",
		"--why=Claude Code session",
		"tail",
		"--pid=" + strconv.Itoa(pid),
		"-f",
		"/dev/null",
	}, true
}
