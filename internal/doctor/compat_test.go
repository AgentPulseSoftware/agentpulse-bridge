package doctor

import (
	"os"
	"regexp"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
)

// TestCompatJSONParses is the "parses cleanly" half of compat.go's chosen
// strategy for a malformed embedded file (panic on init). Running it
// here means a syntax mistake in compat.json fails a normal `go test`
// run with a clear message, rather than only showing up as a mysterious
// panic the first time some other test in this package happens to touch
// the compat variable.
func TestCompatJSONParses(t *testing.T) {
	c := mustParseCompat(compatJSON)
	if c.BaselineVersion == "" {
		t.Error("compat.json baseline_version is empty")
	}
	if len(c.Events) == 0 {
		t.Error("compat.json events is empty")
	}
}

// TestCompatJSONEventsMatchBR08Events exists for one reason: compat.json's
// event names must equal claudehooks.BR08Events
// exactly, same eight names in the same order. Changing either file
// alone breaks this.
func TestCompatJSONEventsMatchBR08Events(t *testing.T) {
	var got []string
	for _, e := range compat.Events {
		got = append(got, e.Name)
	}
	if !equalStrings(got, claudehooks.BR08Events) {
		t.Errorf("compat.json events = %v, want claudehooks.BR08Events = %v", got, claudehooks.BR08Events)
	}
}

// compatibilityMDEventPattern matches one row of COMPATIBILITY.md's "The
// eight registered hook events (BR-08)" table: a backtick-quoted event
// name in the first column.
var compatibilityMDEventPattern = regexp.MustCompile("(?m)^\\| `([A-Za-z]+)` \\|")

// compatibilityMDRecordedVersionPattern matches "Recorded on"'s
// "Claude Code version: `X.Y.Z`" line.
var compatibilityMDRecordedVersionPattern = regexp.MustCompile("Claude Code version: `([0-9]+\\.[0-9]+\\.[0-9]+)`")

// readCompatibilityMD reads COMPATIBILITY.md relative to this
// package's own directory (internal/doctor). The embed directive
// compat.go uses for compat.json cannot reach one directory up to this
// file, so this test is the only place that keeps compat.json and
// COMPATIBILITY.md talking about the same eight events and the same
// baseline version.
func readCompatibilityMD(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../COMPATIBILITY.md")
	if err != nil {
		t.Fatalf("reading COMPATIBILITY.md: %v", err)
	}
	return string(data)
}

func TestCompatibilityMDEventsMatchBR08Events(t *testing.T) {
	md := readCompatibilityMD(t)
	matches := compatibilityMDEventPattern.FindAllStringSubmatch(md, -1)
	var got []string
	for _, m := range matches {
		got = append(got, m[1])
	}
	if !equalStrings(got, claudehooks.BR08Events) {
		t.Errorf("COMPATIBILITY.md's event table lists %v, want exactly claudehooks.BR08Events = %v", got, claudehooks.BR08Events)
	}
}

func TestCompatibilityMDBaselineMatchesCompatJSON(t *testing.T) {
	md := readCompatibilityMD(t)
	m := compatibilityMDRecordedVersionPattern.FindStringSubmatch(md)
	if m == nil {
		t.Fatal("COMPATIBILITY.md's \"Recorded on\" section does not have a \"Claude Code version: `X.Y.Z`\" line")
	}
	if m[1] != compat.BaselineVersion {
		t.Errorf("COMPATIBILITY.md records %q, compat.json's baseline_version is %q", m[1], compat.BaselineVersion)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in     string
		wantOK bool
		want   [3]int
	}{
		{"2.1.261", true, [3]int{2, 1, 261}},
		{"2.1.261 (Claude Code)", true, [3]int{2, 1, 261}},
		{"Claude Code 2.1.261", true, [3]int{2, 1, 261}},
		{"2.1.261-beta.1", true, [3]int{2, 1, 261}},
		{"2.0.9", true, [3]int{2, 0, 9}},
		{"", false, [3]int{}},
		{"not a version", false, [3]int{}},
		{"v2", false, [3]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := parseVersion(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("parseVersion(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseVersion(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCompareVersionsIsNumericNotLexicographic(t *testing.T) {
	a := [3]int{2, 1, 9}
	b := [3]int{2, 1, 261}
	if compareVersions(a, b) >= 0 {
		t.Errorf("compareVersions(2.1.9, 2.1.261) = %d, want negative (2.1.9 < 2.1.261 numerically)", compareVersions(a, b))
	}
	if compareVersions(b, a) <= 0 {
		t.Errorf("compareVersions(2.1.261, 2.1.9) = %d, want positive", compareVersions(b, a))
	}
	if compareVersions(a, a) != 0 {
		t.Errorf("compareVersions(a, a) = %d, want 0", compareVersions(a, a))
	}
}

func TestUnsupportedEventsAtOrAboveBaselineIsEmpty(t *testing.T) {
	baseline, ok := parseVersion(compat.BaselineVersion)
	if !ok {
		t.Fatalf("compat.json's baseline_version %q does not parse", compat.BaselineVersion)
	}
	if got := unsupportedEvents(baseline); len(got) != 0 {
		t.Errorf("unsupportedEvents(baseline) = %v, want none", got)
	}
}

func TestUnsupportedEventsBelowBaselineListsAllEightInOrder(t *testing.T) {
	old := [3]int{0, 0, 1}
	got := unsupportedEvents(old)
	if !equalStrings(got, claudehooks.BR08Events) {
		t.Errorf("unsupportedEvents(0.0.1) = %v, want all 8 in claudehooks.BR08Events order = %v", got, claudehooks.BR08Events)
	}
}

func TestCompatJSONMinVersionsAllParse(t *testing.T) {
	for _, e := range compat.Events {
		if _, ok := parseVersion(e.MinVersion); !ok {
			t.Errorf("compat.json event %q has an unparseable min_version %q", e.Name, e.MinVersion)
		}
	}
}
