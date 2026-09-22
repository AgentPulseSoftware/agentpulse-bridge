//go:build linux

package cred

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubSecretToolScript writes a small shell script standing in for
// secret-tool, exercising secretServiceStore's own argument construction
// and stdin piping against a real exec'd process without ever touching
// the real D-Bus Secret Service. See keychain_darwin_test.go's
// stubSecurityScript for the same idea on macOS.
func stubSecretToolScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "secret-tool")
	state := filepath.Join(dir, "state")
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  store) cat > '" + state + "'; exit 0 ;;\n" +
		"  lookup) if [ -f '" + state + "' ]; then cat '" + state + "'; exit 0; else exit 1; fi ;;\n" +
		"  clear) rm -f '" + state + "'; exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub secret-tool script: %v", err)
	}
	return script
}

func TestSecretServiceStoreSatisfiesContract(t *testing.T) {
	orig := secretToolBinary
	secretToolBinary = stubSecretToolScript(t)
	t.Cleanup(func() { secretToolBinary = orig })

	assertStoreContract(t, newSecretServiceStore())
}

func TestSecretServiceStoreNeverPutsSecretInArgv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret-tool")
	marker := filepath.Join(dir, "argv-leak")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    *the-secret-value*) touch '" + marker + "' ;;\n" +
		"  esac\n" +
		"done\n" +
		"cat > /dev/null\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub secret-tool script: %v", err)
	}

	orig := secretToolBinary
	secretToolBinary = script
	t.Cleanup(func() { secretToolBinary = orig })

	if err := newSecretServiceStore().Set("the-secret-value"); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the secret appeared as a process argument (argv), want it only ever sent on stdin")
	}
}

// TestSecretServiceStoreNeverBlocksForever is a regression test: a
// secret-tool invocation that never returns — modeling an unreachable or
// locked Secret Service daemon — must not hang the caller past
// secretToolTimeout. Reduces the timeout to well under the script's own
// sleep so the test itself stays fast.
func TestSecretServiceStoreNeverBlocksForever(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "secret-tool")
	body := "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub secret-tool script: %v", err)
	}

	origBin, origTimeout := secretToolBinary, secretToolTimeout
	secretToolBinary = script
	secretToolTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		secretToolBinary = origBin
		secretToolTimeout = origTimeout
	})

	done := make(chan error, 1)
	go func() {
		_, err := newSecretServiceStore().Get()
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Get() returned nil error against a hanging secret-tool, want a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Get() did not return within 5s of a 200ms secretToolTimeout: the context timeout did not bound the shell-out")
	}
}
