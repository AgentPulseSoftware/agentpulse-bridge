//go:build !darwin && !linux

package keepawake

// platformCommand reports no keep-awake implementation on any OS other
// than macOS or Linux (BR-20 only ships darwin and linux builds).
func platformCommand(pid int) (argv []string, ok bool) {
	return nil, false
}
