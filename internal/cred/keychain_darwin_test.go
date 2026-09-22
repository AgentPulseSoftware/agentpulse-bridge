//go:build darwin

package cred

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubSecurityScript writes a small shell script standing in for
// /usr/bin/security, so keychainStore's own logic (argument
// construction, the interactive-mode command line, exit-code/stderr
// classification) is exercised against a real exec'd process without
// ever touching the real login Keychain.
//
// It is deliberately modelled on how the real binary behaves, not on how
// the code wishes it behaved — that mismatch is what would let a bug
// slip through. In particular the stub REJECTS the old mechanism: invoked as
// `add-generic-password … -w` with no value, the real security(1) prompts
// on the controlling terminal and stores nothing, so the stub prints
// "password data for new item:" on stderr and exits non-zero. Any future
// change that goes back to passing the secret through a piped
// `add-generic-password` invocation fails these tests immediately.
//
// The supported write path is `-i` (interactive mode): one command line
// on stdin, whose first word must be add-generic-password and whose -w
// value is the secret to persist. Reads and deletes stay plain argv and
// keep security(1)'s exit codes: 44 with a "could not be found" stderr
// message when there is nothing to act on.
func stubSecurityScript(t *testing.T) string {
	t.Helper()
	return writeStubSecurity(t, t.TempDir(), "")
}

// writeStubSecurity writes the stub into dir and returns its path.
// storeInstead, when non-empty, is persisted in place of the secret the
// command line actually carried, which is how a "the Keychain kept
// something else" read-back mismatch is simulated.
func writeStubSecurity(t *testing.T, dir, storeInstead string) string {
	t.Helper()
	script := filepath.Join(dir, "security")
	state := filepath.Join(dir, "state")
	notFound := "echo 'security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.' >&2; exit 44"
	persist := `printf '%s' "$secret" > ` + shq(state)
	if storeInstead != "" {
		persist = `printf '%s' ` + shq(storeInstead) + ` > ` + shq(state)
	}
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		// The old, broken mechanism: the real binary would prompt on the
		// tty here and never read the pipe.
		"  add-generic-password)\n" +
		"    printf 'password data for new item:' >&2; exit 1 ;;\n" +
		"  -i)\n" +
		"    IFS= read -r line || { echo 'security: no command' >&2; exit 1; }\n" +
		"    set -f\n" +
		"    set -- $line\n" +
		"    [ \"$1\" = 'add-generic-password' ] || { echo \"security: unknown command\" >&2; exit 1; }\n" +
		"    secret=''; prev=''\n" +
		"    for a in \"$@\"; do\n" +
		"      if [ \"$prev\" = '-w' ]; then secret=$a; break; fi\n" +
		"      prev=$a\n" +
		"    done\n" +
		"    [ -n \"$secret\" ] || { printf 'password data for new item:' >&2; exit 1; }\n" +
		"    " + persist + "\n" +
		"    exit 0 ;;\n" +
		"  find-generic-password)\n" +
		"    if [ -f " + shq(state) + " ]; then cat " + shq(state) + "; echo; exit 0; " +
		"else " + notFound + "; fi ;;\n" +
		"  delete-generic-password)\n" +
		"    if [ -f " + shq(state) + " ]; then rm -f " + shq(state) + "; exit 0; " +
		"else " + notFound + "; fi ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub security script: %v", err)
	}
	return script
}

// shq single-quotes s for embedding in the generated shell script; the
// paths involved are all under t.TempDir(), never attacker input.
func shq(s string) string {
	return "'" + s + "'"
}

// useStub points securityBinary at script for the duration of the test.
func useStub(t *testing.T, script string) {
	t.Helper()
	orig := securityBinary
	securityBinary = script
	t.Cleanup(func() { securityBinary = orig })
}

func TestKeychainStoreSatisfiesContract(t *testing.T) {
	useStub(t, stubSecurityScript(t))

	assertStoreContract(t, newKeychainStore())
}

// TestKeychainStoreRejectsThePromptingWriteMechanism is a regression test:
// if Set ever goes back to `add-generic-password … -w` with
// the secret on a pipe, the stub behaves like the real binary (prompt on
// stderr, non-zero exit) and Set must fail. It passes today only because
// Set uses `security -i`.
func TestKeychainStoreRejectsThePromptingWriteMechanism(t *testing.T) {
	dir := t.TempDir()
	useStub(t, writeStubSecurity(t, dir, ""))

	if err := newKeychainStore().Set("the-secret-value"); err != nil {
		t.Fatalf("Set() error: %v (the write path must not use the prompting add-generic-password form)", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("reading what the stub stored: %v", err)
	}
	if string(got) != "the-secret-value" {
		t.Errorf("stub stored %q, want the secret Set was given", string(got))
	}
}

func TestKeychainStoreNeverPutsSecretInArgv(t *testing.T) {
	// A stub receives argv exactly like the real binary would, so if the
	// secret were ever passed as an argument (rather than on the
	// interactive command line) it would show up in the script's own
	// "$@" here. The stub records argv, one element per line, and stdin
	// verbatim, and the assertions below check both.
	dir := t.TempDir()
	script := filepath.Join(dir, "security")
	argvFile := filepath.Join(dir, "argv")
	stdinFile := filepath.Join(dir, "stdin")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + shq(argvFile) + "; done\n" +
		"cat >> " + shq(stdinFile) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub security script: %v", err)
	}
	useStub(t, script)

	const secret = "the-secret-value"
	// Set's read-back will fail against this recording stub (it stores
	// nothing), which is fine: the recordings are what this test asserts
	// on, and they are written before the read-back runs.
	_ = newKeychainStore().Set(secret)

	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("reading the recorded argv: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "-i" {
		t.Fatalf("first recorded argv = %q, want the write to be exactly [\"-i\"]", lines)
	}
	for _, a := range lines {
		if strings.Contains(a, secret) {
			t.Fatal("the secret appeared as a process argument (argv), want it only ever on stdin")
		}
	}

	stdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading the recorded stdin: %v", err)
	}
	if !strings.Contains(string(stdin), secret) {
		t.Errorf("the secret did not reach security(1) on stdin; got %d bytes", len(stdin))
	}
	if want := "add-generic-password -a " + account + " -s " + service + " -U -w " + secret + "\n"; string(stdin) != want {
		t.Errorf("stdin = %q, want exactly one add-generic-password command line", string(stdin))
	}
}

// TestKeychainStoreRefusesUnsupportedSecrets covers the allowlist guard:
// anything outside the relay's base64url alphabet is refused before any
// child process starts, so nothing is written and nothing is mangled.
func TestKeychainStoreRefusesUnsupportedSecrets(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"space", "two words"},
		{"double quote", `a"b`},
		{"single quote", "a'b"},
		{"backslash", `a\b`},
		{"newline", "a\nb"},
		{"tab", "a\tb"},
		{"dollar", "a$b"},
		{"semicolon", "a;b"},
		{"backtick", "a`b"},
		{"control byte", "a\x00b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			useStub(t, writeStubSecurity(t, dir, ""))

			err := newKeychainStore().Set(tt.secret)
			if !errors.Is(err, ErrUnsupportedSecret) {
				t.Fatalf("Set(%q) error = %v, want ErrUnsupportedSecret", tt.secret, err)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("the error text repeats the rejected secret: %q", err.Error())
			}
			if _, statErr := os.Stat(filepath.Join(dir, "state")); statErr == nil {
				t.Error("a rejected secret still wrote something to the store")
			}
		})
	}
}

// TestKeychainStoreAcceptsARelayShapedSecret is the other half of the
// guard: a real 43-character base64url secret, including the leading-dash
// case, must go through untouched.
func TestKeychainStoreAcceptsARelayShapedSecret(t *testing.T) {
	for _, secret := range []string{
		"aB3-_xY9aB3-_xY9aB3-_xY9aB3-_xY9aB3-_xY9aB3",
		"-leading-dash-is-a-valid-base64url-secret--",
	} {
		t.Run(secret[:6], func(t *testing.T) {
			useStub(t, stubSecurityScript(t))

			k := newKeychainStore()
			if err := k.Set(secret); err != nil {
				t.Fatalf("Set() error: %v", err)
			}
			got, err := k.Get()
			if err != nil {
				t.Fatalf("Get() error: %v", err)
			}
			if got != secret {
				t.Errorf("Get() = %q, want the stored secret", got)
			}
		})
	}
}

// TestKeychainStoreVerifiesItsOwnWrite covers the read-back check: a
// security(1) that exits 0 but stored something else (or nothing) must
// still make Set fail, since interactive mode's exit status is an
// observed rather than documented property.
func TestKeychainStoreVerifiesItsOwnWrite(t *testing.T) {
	t.Run("stored value differs", func(t *testing.T) {
		useStub(t, writeStubSecurity(t, t.TempDir(), "something-else"))

		if err := newKeychainStore().Set("the-secret-value"); err == nil {
			t.Fatal("Set() = nil against a store that kept a different value, want an error")
		}
	})

	t.Run("nothing stored", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "security")
		// Consumes the command line and exits 0, but stores nothing, so
		// the read-back finds no item at all.
		body := "#!/bin/sh\n" +
			"case \"$1\" in\n" +
			"  -i) cat > /dev/null; exit 0 ;;\n" +
			"  *) echo 'security: The specified item could not be found in the keychain.' >&2; exit 44 ;;\n" +
			"esac\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			t.Fatalf("writing stub security script: %v", err)
		}
		useStub(t, script)

		if err := newKeychainStore().Set("the-secret-value"); err == nil {
			t.Fatal("Set() = nil against a store that kept nothing, want an error")
		}
	})
}

// TestKeychainStoreSetFailsOnNonZeroExit: a security(1) that reads the
// whole command line and then fails must make Set fail, and its stderr
// must not reach the returned error (it is captured for classification
// only).
func TestKeychainStoreSetFailsOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "security")
	body := "#!/bin/sh\ncat > /dev/null\necho 'add-generic-password: returned 45' >&2\nexit 45\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub security script: %v", err)
	}
	useStub(t, script)

	err := newKeychainStore().Set("the-secret-value")
	if err == nil {
		t.Fatal("Set() = nil against a failing security(1), want an error")
	}
	if got := err.Error(); got != "keychain: storing secret failed" {
		t.Errorf("Set() error = %q, want the fixed wording with no stderr and no secret in it", got)
	}
}

// TestKeychainStoreReadBackUsesTheWriteTimeout is a regression test:
// Set's read-back verification must be bounded by the write timeout,
// not the read one. On a Mac that raises the Keychain dialog the
// write only finishes once the user clicks Allow, and a 5-second read
// straight afterwards can time out on a pairing that actually succeeded.
// The stub below makes the read slower than the read bound but well
// inside the write bound, so Set can only pass if it uses the write bound.
func TestKeychainStoreReadBackUsesTheWriteTimeout(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	script := filepath.Join(dir, "security")
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  -i)\n" +
		"    IFS= read -r line\n" +
		"    set -f; set -- $line\n" +
		"    secret=''; prev=''\n" +
		"    for a in \"$@\"; do\n" +
		"      if [ \"$prev\" = '-w' ]; then secret=$a; break; fi\n" +
		"      prev=$a\n" +
		"    done\n" +
		"    printf '%s' \"$secret\" > " + shq(state) + "\n" +
		"    exit 0 ;;\n" +
		"  find-generic-password)\n" +
		// Slower than securityReadTimeout below, faster than
		// securityWriteTimeout.
		"    sleep 1\n" +
		"    cat " + shq(state) + "; echo; exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub security script: %v", err)
	}
	useStub(t, script)

	origRead, origWrite := securityReadTimeout, securityWriteTimeout
	securityReadTimeout = 200 * time.Millisecond
	securityWriteTimeout = 10 * time.Second
	t.Cleanup(func() {
		securityReadTimeout = origRead
		securityWriteTimeout = origWrite
	})

	k := newKeychainStore()
	if err := k.Set("the-secret-value"); err != nil {
		t.Fatalf("Set() error: %v (the read-back must use securityWriteTimeout, not securityReadTimeout)", err)
	}
	// Get itself stays on the short read bound, for the unattended
	// callers (flush, projects, status) that must fail fast.
	if _, err := k.Get(); err == nil {
		t.Error("Get() = nil against a read slower than securityReadTimeout, want Get to keep the short bound")
	}
}

// TestKeychainStoreSetReadBackTimeoutAfterASuccessfulWrite is a
// regression test: a write that lands cleanly,
// followed by a read-back that is killed by securityWriteTimeout, must be
// reported as "may have been stored but could not be read back" rather
// than the flat "storing secret failed" a real refusal gets. Without this
// handling, get() collapses every non-not-found read failure (a timeout
// included) into one generic error, so isSecurityTimeout could never be
// true by the time Set saw it.
func TestKeychainStoreSetReadBackTimeoutAfterASuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	script := filepath.Join(dir, "security")
	const stderrMarker = "stub-stderr-must-not-be-reported"
	body := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  -i)\n" +
		"    IFS= read -r line\n" +
		"    set -f; set -- $line\n" +
		"    secret=''; prev=''\n" +
		"    for a in \"$@\"; do\n" +
		"      if [ \"$prev\" = '-w' ]; then secret=$a; break; fi\n" +
		"      prev=$a\n" +
		"    done\n" +
		"    printf '%s' \"$secret\" > " + shq(state) + "\n" +
		"    exit 0 ;;\n" +
		"  find-generic-password)\n" +
		// The write above already landed; this read-back never returns,
		// so the read-back itself must be the thing that times out.
		"    echo '" + stderrMarker + "' >&2\n" +
		"    sleep 30\n" +
		"    exit 0 ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing stub security script: %v", err)
	}
	useStub(t, script)

	// securityWriteTimeout bounds both the write above and the read-back
	// below (Set has one write bound, not two), so it must be generous
	// enough that the real write — a shell fork/exec plus one local file
	// write — reliably finishes under a loaded `go test ./...` well
	// inside it, while the read-back's own 30s sleep still safely exceeds
	// it. 2s comfortably clears both.
	origWrite := securityWriteTimeout
	securityWriteTimeout = 2 * time.Second
	t.Cleanup(func() { securityWriteTimeout = origWrite })

	err := newKeychainStore().Set("the-secret-value")
	if err == nil {
		t.Fatal("Set() = nil, want an error (the read-back never returned)")
	}
	got := err.Error()
	for _, want := range []string{"may have been stored but could not be read back", "2s", "agentpulse status"} {
		if !strings.Contains(got, want) {
			t.Errorf("Set() error = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "the-secret-value") {
		t.Error("Set() error contains the secret")
	}
	if strings.Contains(got, stderrMarker) {
		t.Error("Set() error contains security(1)'s stderr")
	}
	// Confirm the write really did land — the scenario this message
	// exists for is "stored, but unconfirmed", not "never stored".
	stored, readErr := os.ReadFile(state)
	if readErr != nil || string(stored) != "the-secret-value" {
		t.Fatalf("stub state = %q, %v; want the write to have actually landed", stored, readErr)
	}
}

// TestKeychainStoreSetReadBackMismatchAfterASuccessfulWrite is the
// "plain-failure" counterpart pinned alongside the timeout case above: a
// read-back that returns promptly but disagrees with what Set wrote (the
// Keychain kept something else) is not a timeout, and must keep the flat
// "storing secret failed" wording.
func TestKeychainStoreSetReadBackMismatchAfterASuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	useStub(t, writeStubSecurity(t, dir, "something-else-entirely"))

	err := newKeychainStore().Set("the-secret-value")
	if err == nil {
		t.Fatal("Set() = nil, want an error (the read-back disagreed with what was written)")
	}
	if got, want := err.Error(), "keychain: storing secret failed"; got != want {
		t.Errorf("Set() error = %q, want exactly %q", got, want)
	}
}

// TestKeychainStoreNeverBlocksForever is a regression test: a `security`
// invocation that never returns — modeling a locked login Keychain
// prompting on a non-interactive session — must not hang the caller past
// its timeout. Both the read and the write bound are covered, and both
// are shrunk so the test itself stays fast.
func TestKeychainStoreNeverBlocksForever(t *testing.T) {
	tests := []struct {
		name string
		call func(*keychainStore) error
	}{
		{"Get", func(k *keychainStore) error { _, err := k.Get(); return err }},
		{"Delete", func(k *keychainStore) error { return k.Delete() }},
		{"Set", func(k *keychainStore) error { return k.Set("the-secret-value") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "security")
			body := "#!/bin/sh\nsleep 60\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatalf("writing stub security script: %v", err)
			}
			useStub(t, script)

			origRead, origWrite := securityReadTimeout, securityWriteTimeout
			securityReadTimeout = 200 * time.Millisecond
			securityWriteTimeout = 200 * time.Millisecond
			t.Cleanup(func() {
				securityReadTimeout = origRead
				securityWriteTimeout = origWrite
			})

			done := make(chan error, 1)
			go func() { done <- tt.call(newKeychainStore()) }()

			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("%s() returned nil error against a hanging security(1), want a timeout error", tt.name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s() did not return within 5s of a 200ms timeout: the context timeout did not bound the shell-out", tt.name)
			}
		})
	}
}

// TestKeychainStoreSetDistinguishesATimeoutFromAFailure is a regression
// test: a write killed by securityWriteTimeout may still
// land in the Keychain afterwards, so Set must say so, while a write
// security(1) refused promptly keeps the flat "failed" wording. Neither
// message may carry the secret or security(1)'s stderr.
func TestKeychainStoreSetDistinguishesATimeoutFromAFailure(t *testing.T) {
	const secret = "the-secret-value"
	const stderrMarker = "stub-stderr-must-not-be-reported"
	tests := []struct {
		name        string
		body        string
		wantExact   string
		wantContain []string
	}{
		{
			name: "killed by the write timeout",
			body: "#!/bin/sh\necho '" + stderrMarker + "' >&2\nsleep 30\n",
			wantContain: []string{
				"timed out after 200ms",
				"may still have been stored",
				"agentpulse unpair",
			},
		},
		{
			name:      "refused promptly",
			body:      "#!/bin/sh\ncat > /dev/null\necho '" + stderrMarker + "' >&2\nexit 45\n",
			wantExact: "keychain: storing secret failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "security")
			if err := os.WriteFile(script, []byte(tt.body), 0o700); err != nil {
				t.Fatalf("writing stub security script: %v", err)
			}
			useStub(t, script)

			origWrite := securityWriteTimeout
			securityWriteTimeout = 200 * time.Millisecond
			t.Cleanup(func() { securityWriteTimeout = origWrite })

			err := newKeychainStore().Set(secret)
			if err == nil {
				t.Fatal("Set() = nil, want an error")
			}
			got := err.Error()
			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("Set() error = %q, want exactly %q", got, tt.wantExact)
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("Set() error = %q, want it to contain %q", got, want)
				}
			}
			if strings.Contains(got, secret) {
				t.Error("Set() error contains the secret")
			}
			if strings.Contains(got, stderrMarker) {
				t.Error("Set() error contains security(1)'s stderr")
			}
		})
	}
}
