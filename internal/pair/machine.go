package pair

import (
	"os"
	"strings"
	"unicode"
	"unicode/utf16"
)

// fallbackMachineName is sent when the machine has no usable hostname at
// all; the relay's schema requires a non-empty name, and a person with two
// unnamed machines can still tell them apart by when they paired.
const fallbackMachineName = "unnamed machine"

// maxMachineNameUnits is the relay's limit of 64 UTF-16 code units
// (JavaScript string length), not Unicode code points — so a name full of
// emoji can be under 64 runes and still be rejected. This truncates by
// the relay's own measure.
const maxMachineNameUnits = 64

// MachineName returns this machine's hostname, shaped to satisfy the
// relay's POST /v1/bridges schema: no path separator, no control
// character, 1 to 64 UTF-16 code units, never empty (SPEC 10.3 step 1).
// It is the only machine-identifying string pairing sends, and it is
// chosen by the operator's own computer name, not derived from any file
// path (BR-13's spirit).
func MachineName() string {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return sanitizeMachineName(host)
}

func sanitizeMachineName(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return -1 // the relay's schema rejects a path separator outright
		case r == unicode.ReplacementChar || !unicode.IsPrint(r):
			return -1
		default:
			return r
		}
	}, raw)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return fallbackMachineName
	}
	return truncateUTF16(cleaned, maxMachineNameUnits)
}

// truncateUTF16 cuts s to at most limit UTF-16 code units without ever
// splitting a rune.
func truncateUTF16(s string, limit int) string {
	if len(utf16.Encode([]rune(s))) <= limit {
		return s
	}
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			return s[:i]
		}
		units += width
	}
	return s
}
