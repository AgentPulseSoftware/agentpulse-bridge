package xdgpaths

import (
	"path/filepath"
	"testing"
)

func TestDirsRespectXDGEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgcfg")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdgstate")

	if got, want := ConfigDir(), filepath.Join("/tmp/xdgcfg", "agentpulse"); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
	if got, want := ConfigPath(), filepath.Join("/tmp/xdgcfg", "agentpulse", "config.json"); got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
	if got, want := StateDir(), filepath.Join("/tmp/xdgstate", "agentpulse"); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
	if got, want := SessionsDir(), filepath.Join("/tmp/xdgstate", "agentpulse", "sessions"); got != want {
		t.Errorf("SessionsDir() = %q, want %q", got, want)
	}
	if got, want := SpoolPath(), filepath.Join("/tmp/xdgstate", "agentpulse", "spool.ndjson"); got != want {
		t.Errorf("SpoolPath() = %q, want %q", got, want)
	}
	if got, want := StatePath(), filepath.Join("/tmp/xdgstate", "agentpulse", "state.json"); got != want {
		t.Errorf("StatePath() = %q, want %q", got, want)
	}
	if got, want := CredentialsPath(), filepath.Join("/tmp/xdgcfg", "agentpulse", "credentials"); got != want {
		t.Errorf("CredentialsPath() = %q, want %q", got, want)
	}
	if got, want := LogPath(), filepath.Join("/tmp/xdgstate", "agentpulse", "bridge.log"); got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
}

func TestDirsFallBackWhenXDGEnvUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if got := ConfigDir(); filepath.Base(got) != "agentpulse" || !filepath.IsAbs(got) {
		t.Errorf("ConfigDir() = %q, want an absolute path ending in agentpulse", got)
	}
	if got := StateDir(); filepath.Base(got) != "agentpulse" || !filepath.IsAbs(got) {
		t.Errorf("StateDir() = %q, want an absolute path ending in agentpulse", got)
	}
}
