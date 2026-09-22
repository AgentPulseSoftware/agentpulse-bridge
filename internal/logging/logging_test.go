package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryLevelLandsInFile is a table test: a logger built at debug
// level must produce one line per call, for every level, in its output.
func TestEveryLevelLandsInFile(t *testing.T) {
	cases := []struct {
		level slog.Level
		log   func(l *slog.Logger, msg string)
		want  string
	}{
		{slog.LevelDebug, func(l *slog.Logger, msg string) { l.Debug(msg) }, "DEBUG"},
		{slog.LevelInfo, func(l *slog.Logger, msg string) { l.Info(msg) }, "INFO"},
		{slog.LevelWarn, func(l *slog.Logger, msg string) { l.Warn(msg) }, "WARN"},
		{slog.LevelError, func(l *slog.Logger, msg string) { l.Error(msg) }, "ERROR"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New(&buf, slog.LevelDebug)
			tc.log(logger, "message-"+tc.want)

			out := buf.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("output = %q, want it to contain level %q", out, tc.want)
			}
			if !strings.Contains(out, "message-"+tc.want) {
				t.Errorf("output = %q, want it to contain the message", out)
			}
		})
	}
}

// TestDefaultLevelFiltersOutDebug matches BR-04's "default info": at the
// info threshold, a debug-level call must not appear in the output at
// all, since ERR-01 relies on that ("log at debug level" only becomes
// visible once AGENTPULSE_DEBUG=1 raises the threshold).
func TestDefaultLevelFiltersOutDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Debug("should not appear")
	logger.Info("should appear")

	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("output = %q, want the debug-level line filtered out", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("output = %q, want the info-level line present", out)
	}
}

func TestGetWritesToBridgeLogUnderStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("AGENTPULSE_DEBUG", "")

	logger := Get()
	logger.Info("hello from TestGetWritesToBridgeLogUnderStateDir")

	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "agentpulse", "bridge.log"))
	if err != nil {
		t.Fatalf("reading bridge.log: %v", err)
	}
	if !strings.Contains(string(data), "hello from TestGetWritesToBridgeLogUnderStateDir") {
		t.Errorf("bridge.log = %q, want the logged message", data)
	}
}

func TestGetIsCachedPerStateDirAndDebugFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("AGENTPULSE_DEBUG", "")

	first := Get()
	second := Get()
	if first != second {
		t.Error("Get() returned different loggers for the same state dir and debug flag, want the cached one reused")
	}

	t.Setenv("AGENTPULSE_DEBUG", "1")
	third := Get()
	if first == third {
		t.Error("Get() returned the cached (non-debug) logger after AGENTPULSE_DEBUG changed, want a fresh one")
	}
}
