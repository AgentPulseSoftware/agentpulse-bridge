package pair

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// TestSanitizeMachineName covers the relay's POST /v1/bridges schema:
// 1 to 64 UTF-16 code units, no path separator, never empty.
func TestSanitizeMachineName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"an ordinary hostname", "sam-macbook-air.local", "sam-macbook-air.local"},
		{"a name with spaces", "Sam's MacBook Air", "Sam's MacBook Air"},
		{"a forward slash is dropped", "sam/macbook", "sammacbook"},
		{"a backslash is dropped", `sam\macbook`, "sammacbook"},
		{"control characters are dropped", "sam\x00\tmacbook", "sammacbook"},
		{"surrounding whitespace is trimmed", "  sam-macbook  ", "sam-macbook"},
		{"an empty hostname falls back", "", fallbackMachineName},
		{"a hostname of only separators falls back", "///", fallbackMachineName},
		{"a long name is truncated", strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeMachineName(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeMachineName(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if got == "" {
				t.Error("the relay's schema requires a non-empty name")
			}
			if units := len(utf16.Encode([]rune(got))); units > maxMachineNameUnits {
				t.Errorf("name is %d UTF-16 units, the relay accepts at most %d", units, maxMachineNameUnits)
			}
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("name %q still contains a path separator", got)
			}
		})
	}
}

// TestSanitizeMachineNameCountsUTF16Units is the case a rune-based
// truncation would get wrong: emoji are two UTF-16 code units each, so 40
// of them are 80 by the relay's measure even though they are 40 runes.
func TestSanitizeMachineNameCountsUTF16Units(t *testing.T) {
	got := sanitizeMachineName(strings.Repeat("🙂", 40))
	if units := len(utf16.Encode([]rune(got))); units > maxMachineNameUnits {
		t.Errorf("name is %d UTF-16 units, the relay accepts at most %d", units, maxMachineNameUnits)
	}
	if !strings.HasPrefix(strings.Repeat("🙂", 40), got) {
		t.Errorf("truncation split a character: %q", got)
	}
}

func TestMachineNameIsUsable(t *testing.T) {
	got := MachineName()
	if got == "" {
		t.Fatal("MachineName returned an empty string")
	}
	if strings.ContainsAny(got, `/\`) {
		t.Errorf("MachineName = %q, which the relay's schema rejects", got)
	}
}
