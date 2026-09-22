//go:build !darwin && !linux

package cred

// platformStore reports no platform backend on any OS other than macOS
// or Linux (BR-20 only ships darwin and linux builds): Default always
// falls back to the file backend there.
func platformStore() Store {
	return nil
}
