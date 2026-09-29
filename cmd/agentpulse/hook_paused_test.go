package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/classify"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// TestStopFailureNeverWritesErrorText is ADR-006 section 7's privacy
// test end to end: StopFailure documents and a usage-limit notification
// whose text fields hold a path and a sentence go through "agentpulse
// hook" with debug logging on. The spool gets paused events, and neither
// the text nor an unrecognised error value appears in the spool, the
// bridge log, stderr, or any other file the bridge writes.
func TestStopFailureNeverWritesErrorText(t *testing.T) {
	setTestXDGDirs(t)
	stubSelfExecutable(t)
	t.Setenv("AGENTPULSE_DEBUG", "1")

	const (
		path     = "/Users/someone/secret-project/main.go"
		sentence = "The build broke while editing the payment module."
		invented = "zz_invented_error_value"
	)
	text := `,"error_details":"429 Too Many Requests while reading ` + path + `","last_assistant_message":"API Error: ` + sentence + `"`
	doc := func(event, extra string) string {
		return `{"hook_event_name":"` + event + `","session_id":"sess_paused_privacy","cwd":"/nonexistent/proj"` + extra + `}`
	}
	docs := []string{
		doc("SessionStart", ""),
		doc("UserPromptSubmit", `,"prompt":"fix it"`),
		doc("StopFailure", `,"error":"`+invented+`"`+text),
		doc("StopFailure", `,"error":"rate_limit","agent_id":"a1b2c3","agent_type":"reviewer"`+text),
		doc("Notification", `,"notification_type":"quota_auto_resume_stale","message":"`+sentence+`","title":"`+path+`"`),
		// Malformed on purpose: the decode error reaches the debug log,
		// and must not quote the document.
		doc("StopFailure", `,"error":{"nested":"`+invented+`"}`+text),
	}
	var stderr bytes.Buffer
	for _, d := range docs {
		runHook(strings.NewReader(d), "", true, &stderr)
	}

	var causes []string
	for _, ev := range spooledEvents(t) {
		if ev.Type == classify.TypePaused {
			if ev.Subagent != nil {
				t.Errorf("paused carries subagent %+v", ev.Subagent)
			}
			p, _ := ev.Payload.(map[string]any)
			causes = append(causes, p["cause"].(string))
		}
	}
	if got := strings.Join(causes, ","); got != "api_error,limit_reset" {
		t.Errorf("paused causes = %s, want api_error,limit_reset", got)
	}

	secrets := []string{path, sentence, "secret-project", "Too Many Requests", "API Error", invented}
	check := func(where string, data []byte) {
		for _, s := range secrets {
			if bytes.Contains(data, []byte(s)) {
				t.Errorf("%s contains %q", where, s)
			}
		}
	}
	check("stderr", stderr.Bytes())
	logData, err := os.ReadFile(xdgpaths.LogPath())
	if err != nil || len(logData) == 0 {
		t.Errorf("bridge log is missing or empty (%v); the malformed document should have been logged", err)
	}
	for _, root := range []string{os.Getenv("HOME"), os.Getenv("XDG_STATE_HOME"), os.Getenv("XDG_CONFIG_HOME")} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, _ := os.ReadFile(p) //nolint:gosec // test-controlled temp path
			check(p, data)
			return nil
		})
	}
}
