//go:build linux

package cred

// platformStore returns the Secret Service backend if secret-tool is on
// PATH, or nil (fall back to the file backend) otherwise.
func platformStore() Store {
	if secretServiceAvailable() {
		return newSecretServiceStore()
	}
	return nil
}
