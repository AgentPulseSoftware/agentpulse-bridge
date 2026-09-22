package doctor

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strconv"
)

// compatJSON is compat.json, embedded so "agentpulse doctor" carries the
// compatibility table inside the binary rather than reading it from
// disk (go:embed cannot reach COMPATIBILITY.md, one directory up
// and outside this package — see that file's "The machine-readable
// copy" section for how the two stay in agreement).
//
//go:embed compat.json
var compatJSON []byte

// compatEvent is one row of compat.json's "events" array: a Claude Code
// hook event name and the earliest Claude Code version known to accept
// it.
type compatEvent struct {
	Name       string `json:"name"`
	MinVersion string `json:"min_version"`
}

// compatData is compat.json's shape.
type compatData struct {
	BaselineVersion string        `json:"baseline_version"`
	Events          []compatEvent `json:"events"`
}

// compat is compat.json, parsed once at package init. A malformed
// embedded file is a programming error — it ships inside the binary,
// nothing external can corrupt it — so a bad compat.json fails loudly at
// startup (panic) rather than making every "doctor" check-3 verdict
// silently wrong; compat_test.go additionally parses it directly so CI
// catches a syntax mistake before it ever reaches this package-init path.
var compat = mustParseCompat(compatJSON)

func mustParseCompat(data []byte) compatData {
	var c compatData
	if err := json.Unmarshal(data, &c); err != nil {
		panic("internal/doctor: compat.json is malformed: " + err.Error())
	}
	return c
}

// versionPattern finds the first X.Y.Z run of digits in a string,
// ignoring anything around it — a leading "Claude Code " label, a
// trailing "-beta.1" suffix or " (Claude Code)" parenthetical, or (for
// compat.json's own min_version strings) nothing at all.
var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// parseVersion extracts the leading X.Y.Z from s as three comparable
// integers. ok is false if no such pattern is found. This is the one
// small unexported version-comparison helper, used instead of a semver
// dependency: three integers, compared numerically
// (2.1.9 < 2.1.261, where a lexicographic string compare would get that
// backwards), with any suffix ignored.
func parseVersion(s string) (v [3]int, ok bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// compareVersions returns -1, 0, or 1 as a is numerically below, equal
// to, or above b.
func compareVersions(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// unsupportedEvents returns the compat.json event names whose
// min_version is above version, in table order (BR08Events order, since
// compat_test.go pins compat.json to that order). version must already
// be three parsed integers, e.g. from parseVersion. An event whose own
// min_version fails to parse is treated as always supported rather than
// always unsupported — compat.json is this package's own embedded data,
// not operator input, so that can only happen if a future edit to it
// breaks the "X.Y.Z" shape, which compat_test.go also guards against.
func unsupportedEvents(version [3]int) []string {
	var names []string
	for _, e := range compat.Events {
		min, ok := parseVersion(e.MinVersion)
		if !ok {
			continue
		}
		if compareVersions(version, min) < 0 {
			names = append(names, e.Name)
		}
	}
	return names
}
