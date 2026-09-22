//go:build linux

package keepawake

import (
	"errors"
	"testing"
)

// stubLookPath replaces lookPath for the duration of the test with a
// function that succeeds only for names in present, and restores the
// real exec.LookPath afterward.
func stubLookPath(t *testing.T, present ...string) {
	t.Helper()
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	set := make(map[string]bool, len(present))
	for _, name := range present {
		set[name] = true
	}
	lookPath = func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestCommandLinux(t *testing.T) {
	stubLookPath(t, "systemd-inhibit", "tail")

	argv, ok := Command(9001)
	if !ok {
		t.Fatal("Command() ok = false, want true when both helpers are on PATH")
	}
	want := []string{
		"systemd-inhibit",
		"--what=idle",
		"--who=agentpulse",
		"--why=Claude Code session",
		"tail",
		"--pid=9001",
		"-f",
		"/dev/null",
	}
	if len(argv) != len(want) {
		t.Fatalf("Command() = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("Command()[%d] = %q, want %q (full: %v)", i, argv[i], want[i], argv)
		}
	}
}

func TestCommandLinuxSystemdInhibitAbsent(t *testing.T) {
	stubLookPath(t, "tail") // systemd-inhibit missing

	if _, ok := Command(9001); ok {
		t.Error("Command() ok = true, want false when systemd-inhibit is not on PATH")
	}
}

func TestCommandLinuxTailAbsent(t *testing.T) {
	stubLookPath(t, "systemd-inhibit") // tail missing

	if _, ok := Command(9001); ok {
		t.Error("Command() ok = true, want false when tail is not on PATH")
	}
}

func TestCommandLinuxBothAbsent(t *testing.T) {
	stubLookPath(t) // neither present

	if _, ok := Command(9001); ok {
		t.Error("Command() ok = true, want false when neither helper is on PATH")
	}
}

func TestStartLinuxReturnsErrNotSupportedWhenHelpersAbsent(t *testing.T) {
	stubLookPath(t) // neither present

	if _, err := Start(9001); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Start() error = %v, want ErrNotSupported", err)
	}
}
