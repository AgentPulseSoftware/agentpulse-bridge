//go:build darwin

package cred

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// securityBinary is a var, not a constant, so a test can point it at a
// stub script instead of the real /usr/bin/security (which would prompt
// or touch the real login Keychain).
var securityBinary = "/usr/bin/security"

// securityReadTimeout and securityWriteTimeout bound every shell-out to
// securityBinary. A locked login Keychain makes `security` either prompt
// for a password or otherwise block indefinitely; without a bound here,
// that hangs "agentpulse flush", "agentpulse pair", and this package's
// own tests forever instead of failing cleanly. Both are vars, like
// securityBinary, so a test can shrink them rather than actually waiting
// out real seconds.
//
// The read path (Get, Delete) keeps the original five seconds: "flush"
// and "hook" run unattended, so a Keychain that is going to prompt must
// fail fast rather than stall the developer's agent (BR-04).
//
// The write path (Set) gets thirty, because Set is only ever reached
// from "agentpulse pair", which is interactive by construction: the
// first write to a new Keychain item can raise an "allow access" or
// "unlock keychain" dialog, and a human needs more than five seconds to
// read it and type a login password. Thirty seconds is still a hard
// bound — nothing here becomes unbounded.
var (
	securityReadTimeout  = 5 * time.Second
	securityWriteTimeout = 30 * time.Second
)

// keychainAvailable reports whether the security(1) binary this backend
// shells out to exists (BR-06's macOS backend). This is a presence check,
// not a live probe of the Keychain itself: running security merely to
// check availability would touch the Keychain (and, for a locked
// keychain, could prompt), which is not something a passive "which
// backend is active" check should ever risk doing.
func keychainAvailable() bool {
	_, err := os.Stat(securityBinary)
	return err == nil
}

// keychainStore is BR-06's macOS backend: the login Keychain, service
// "agentpulse", account "bridge-secret", driven through security(1)
// — never a cgo Keychain binding (BR-01 requires a statically linked
// binary).
//
// The secret never appears as a process argument, where any local user
// could read it with `ps` (SEC-02). Reaching that is not as simple as it
// looks. add-generic-password's `-w` flag does NOT read the password
// from standard input when given no value; the security(1) man page says
// "Specify password to be added. Put at end of command to be prompted
// (recommended)", and "prompted" means read from the controlling
// terminal. Piping the secret to such an invocation hangs until its
// timeout, with "password data for new item:" printed to the terminal
// and nothing stored.
//
// So Set instead uses interactive mode: `security -i` with that exact
// two-element argv, one whole command line written to standard input and
// then EOF. The man page: "If the -i or -p options are provided,
// security will enter interactive mode and allow the user to enter
// multiple commands on stdin. When EOF is read from stdin security will
// exit." The secret rides on that line, so it is in no argument vector.
//
// Get and Delete stay plain argv calls: neither carries a secret in
// argv (find's `-w` prints the stored password to stdout rather than
// taking one), their exit codes already classify cleanly, and routing
// them through interactive mode would only interleave security's own
// diagnostics with the password on stdout for no benefit.
//
// The item is created with macOS's default access control: the creating
// application, /usr/bin/security, is trusted to read it back without a
// prompt, and nothing else is. That default is the protection, so -A
// ("allow any application to access this item without warning"), -T
// <app> and -T "" must never be added to any invocation here, in
// production or in a test (SEC-02). A Keychain dialog is something to
// diagnose, never something to switch off by widening this item's
// access control list.
type keychainStore struct{}

func newKeychainStore() *keychainStore { return &keychainStore{} }

func (k *keychainStore) Name() string { return NameKeychain }

func (k *keychainStore) Get() (string, error) {
	return k.get(securityReadTimeout)
}

// get is Get with an explicit bound. Get itself always uses the 5-second
// read bound, because its callers ("flush", "projects", "status") run
// unattended and must fail fast. Set's read-back verification calls it
// with the 30-second write bound instead: on a Mac that raises the
// Keychain access dialog, the write completes only once the user clicks
// Allow, and a 5-second read immediately afterwards can time out against
// a second dialog even though the secret is safely stored — which would
// make "pair" report failure on a pairing that actually worked (ERR-02).
func (k *keychainStore) get(timeout time.Duration) (string, error) {
	secret, err := k.rawGet(timeout)
	if err != nil {
		if isKeychainItemNotFound(err) {
			return "", ErrNotFound
		}
		return "", errors.New("keychain: reading secret failed")
	}
	if secret == "" {
		return "", ErrNotFound
	}
	return secret, nil
}

// rawGet is get without the generic-error collapse: it returns
// security(1)'s own error (a *securityError, unwrapped by isSecurityTimeout
// and isKeychainItemNotFound) rather than the flat "reading secret failed"
// text get's public-facing callers get.
//
// Set's read-back verification calls this instead of get, because get's
// wrapping is exactly the bug this function exists to avoid repeating: get
// folds every non-not-found failure into one generic error, so a caller
// downstream of it can never tell a read that timed out apart from one
// security(1) refused outright (isSecurityTimeout(err) can never be true
// on get's return value). Set needs that distinction — a read-back killed
// by the timeout leaves the write's own success unknown, while a read-back
// security(1) plainly refused does not — so it reads the security(1)
// error directly rather than through get's lossy wrapping: this is what
// lets Set report "the read-back timeout message after a successful
// Keychain write" instead of a flat failure.
func (k *keychainStore) rawGet(timeout time.Duration) (string, error) {
	out, err := runSecurity(timeout, nil, "find-generic-password", "-a", account, "-s", service, "-w")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (k *keychainStore) Set(secret string) error {
	if err := validateKeychainSecret(secret); err != nil {
		return err
	}

	// One command line, then EOF. This is NOT shell input: it is read by
	// security(1)'s own command reader, so the "fixed binary path, no
	// shell" rule (SEC-09) still holds and nothing here may ever grow a
	// "&&", a ";" or a "$(...)" — those would be literal argument text to
	// security, not operators, and the guard above rejects them anyway.
	//
	// -U ("update item if it already exists") is required: pair calls Set
	// on every run, and re-pairing must overwrite the existing item
	// rather than fail. -w is last, with the secret as its value, which
	// keeps the secret out of this process's argv (see the type doc);
	// -X (hex password) would not, since it is still an argv element.
	//
	// Exactly one command per invocation is mandatory: interactive mode
	// exits with the status of the last command it ran, so a second line
	// would mask this one's failure.
	line := "add-generic-password -a " + account + " -s " + service + " -U -w " + secret + "\n"
	if _, err := runSecurity(securityWriteTimeout, strings.NewReader(line), "-i"); err != nil {
		// A write killed by its own timeout is not the same answer as a
		// write security(1) refused: the child is killed, this call
		// reports failure, and the item can still appear in the Keychain
		// a moment later (for instance if an access dialog is answered
		// after we gave up). Saying "failed" there would leave a real
		// credential behind a "pairing failed" message, so this path
		// names the uncertainty and tells the user how to resolve it.
		// The duration is formatted from the bound itself, so it reads
		// "30s" in production and stays honest if a test shrinks it.
		// No secret and no security(1) stderr in either message.
		if isSecurityTimeout(err) {
			return fmt.Errorf("keychain: storing the secret timed out after %s; if a Keychain dialog "+
				"was answered late the item may still have been stored — check \"agentpulse doctor\" "+
				"and run \"agentpulse unpair\" if the bridge is no longer paired", securityWriteTimeout)
		}
		return errors.New("keychain: storing secret failed")
	}

	// Read back and compare. Interactive mode's exit status is an
	// observed property of the binary rather than a documented one, so
	// Set confirms its own work instead of trusting a zero exit: a
	// mismatch, or nothing stored at all, is a failed Set. Both values
	// are already in this process's memory, so a plain comparison is
	// enough (no timing signal an attacker could use), and neither value
	// is logged on either branch. The read uses the write bound, not the
	// read bound: see get's doc comment.
	//
	// This calls rawGet, not get: the write above already succeeded, so a
	// read-back that times out is a different situation from a read-back
	// security(1) refuses outright — the secret may genuinely be sitting
	// in the Keychain, just unconfirmed, and the message below says so.
	// get's own wrapping collapses that distinction (isSecurityTimeout can
	// never be true on its return value), which is why Set reads
	// security(1)'s error directly here instead.
	stored, err := k.rawGet(securityWriteTimeout)
	if err != nil {
		if isSecurityTimeout(err) {
			return fmt.Errorf("keychain: the secret may have been stored but could not be read back within %s; "+
				"run \"agentpulse status\"", securityWriteTimeout)
		}
		return errors.New("keychain: storing secret failed")
	}
	if stored != secret {
		return errors.New("keychain: storing secret failed")
	}
	return nil
}

// keychainSecretAllowed reports whether b is a byte this backend will
// put on security(1)'s command line unescaped: the base64url alphabet
// the relay's secrets actually use (SEC-02), plus a few
// characters a future encoding change might introduce. Every quote,
// backslash, control character, space and newline is excluded, which is
// exactly the set security's command reader would treat as grouping or
// escaping rather than as data.
func keychainSecretAllowed(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("._=~+/-", b) >= 0
}

// validateKeychainSecret refuses anything Set cannot carry unambiguously,
// before any child process is started, so a rejected secret stores
// nothing. Deliberately conservative: refusing beats a silently mangled
// credential, and this can never fire on a secret the relay issues. The
// returned error never contains the secret or any part of it.
func validateKeychainSecret(secret string) error {
	if secret == "" {
		return ErrUnsupportedSecret
	}
	for i := 0; i < len(secret); i++ {
		if !keychainSecretAllowed(secret[i]) {
			return ErrUnsupportedSecret
		}
	}
	return nil
}

func (k *keychainStore) Delete() error {
	_, err := runSecurity(securityReadTimeout, nil, "delete-generic-password", "-a", account, "-s", service)
	if err != nil {
		if isKeychainItemNotFound(err) {
			return nil // already gone
		}
		return errors.New("keychain: deleting secret failed")
	}
	return nil
}

// runSecurity execs securityBinary with a fixed argument vector (never a
// shell) and, when stdin is non-nil, feeds it as the command's standard
// input (and closes it at EOF, which is how interactive mode is told the
// command stream is over). timeout bounds the whole call: see
// securityReadTimeout and securityWriteTimeout for which path uses
// which. It returns stdout; stderr is captured only to classify the
// error (isKeychainItemNotFound) and is deliberately never included in
// any returned error string, since `security`'s own error text is not
// something this package can guarantee never echoes back
// caller-supplied content.
//
// stdin is a concrete *strings.Reader rather than an io.Reader so that
// the nil the read paths pass stays a nil the "stdin != nil" test below
// can see: a nil stored in an io.Reader interface is a non-nil interface
// value, which would set cmd.Stdin to something that panics on read.
func runSecurity(timeout time.Duration, stdin *strings.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, securityBinary, args...) //nolint:gosec // fixed binary path, fixed/known argument vector, no shell
	// WaitDelay bounds how long Run() keeps waiting for security(1)'s
	// stdout/stderr pipes to close after the context above kills it.
	// Without this, killing the direct child is not enough if it ever
	// spawned a grandchild that inherited those pipes and is still
	// holding them open (Go's os/exec docs, "Cmd.WaitDelay"): Wait()
	// otherwise blocks on the pipe copy until that grandchild exits on
	// its own, which is exactly the kind of unbounded hang this timeout
	// exists to prevent.
	cmd.WaitDelay = timeout
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A context kill surfaces as a plain *exec.ExitError (killed),
		// indistinguishable from security(1) refusing, so the deadline is
		// recorded here where it is still visible.
		return nil, &securityError{
			err:      err,
			stderr:   stderr.String(),
			timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
		}
	}
	return stdout.Bytes(), nil
}

// securityError carries security(1)'s stderr for internal classification
// (isKeychainItemNotFound) only; its Error() never includes stderr, so it
// stays safe to log.
type securityError struct {
	err      error
	stderr   string
	timedOut bool
}

func (e *securityError) Error() string { return e.err.Error() }
func (e *securityError) Unwrap() error { return e.err }

// isSecurityTimeout reports whether err is security(1) being killed by
// this package's own timeout rather than exiting on its own. Callers use
// it to say "we do not know whether the write landed" instead of "the
// write failed".
func isSecurityTimeout(err error) bool {
	var se *securityError
	return errors.As(err, &se) && se.timedOut
}

// isKeychainItemNotFound reports whether err is security(1) exiting
// because the item does not exist (exit status 44,
// errSecItemNotFound) rather than some other failure.
func isKeychainItemNotFound(err error) bool {
	var se *securityError
	if !errors.As(err, &se) {
		return false
	}
	var exitErr *exec.ExitError
	if errors.As(se.err, &exitErr) && exitErr.ExitCode() == 44 {
		return true
	}
	return strings.Contains(se.stderr, "could not be found")
}
