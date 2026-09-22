//go:build darwin

package keepawake

import "strconv"

// platformCommand is BR-16's macOS behavior: "caffeinate -i -w <pid>",
// exactly as the requirement states it. caffeinate ships with macOS, so
// there is no PATH check here (unlike the Linux helpers below) — pid is
// the only variable part, rendered as plain decimal.
func platformCommand(pid int) (argv []string, ok bool) {
	return []string{"caffeinate", "-i", "-w", strconv.Itoa(pid)}, true
}
