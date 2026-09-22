//go:build darwin

package cred

// platformStore returns the macOS Keychain backend if /usr/bin/security
// is present, or nil (fall back to the file backend) otherwise.
func platformStore() Store {
	if keychainAvailable() {
		return newKeychainStore()
	}
	return nil
}
