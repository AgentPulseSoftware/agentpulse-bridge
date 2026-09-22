//go:build linux

package cred

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// secretToolBinary is a var, not a constant, so a test can point it at a
// stub script instead of the real secret-tool (which would touch the
// real D-Bus Secret Service).
var secretToolBinary = "secret-tool"

// secretToolTimeout bounds every shell-out to secretToolBinary. A locked
// or unreachable Secret Service daemon (no D-Bus session, a keyring that
// needs unlocking with no one there to unlock it) makes secret-tool
// block indefinitely; without a bound here, that hangs "agentpulse
// flush", "agentpulse pair", and this package's own tests forever
// instead of failing cleanly. Five seconds matches keychain_darwin.go's
// securityTimeout. A var, like secretToolBinary, so a test can shrink it
// rather than actually waiting out five real seconds.
var secretToolTimeout = 5 * time.Second

// secretToolPath resolves secretToolBinary on PATH, or "" if it isn't
// there. BR-06: "Prefer secret-tool when it is on PATH (fixed argv, no
// shell)."
func secretToolPath() string {
	path, err := exec.LookPath(secretToolBinary)
	if err != nil {
		return ""
	}
	return path
}

// secretServiceAvailable reports whether secret-tool is on PATH. Like
// keychainAvailable on darwin, this is a presence check, not a live probe
// of the D-Bus session — "an unavailable Secret Service is a clean 'not
// available', not an error" (BR-06) is handled by Get/Set/Delete
// returning ordinary errors at call time if the daemon turns out not to
// be reachable when actually invoked, not by this selection check.
func secretServiceAvailable() bool {
	return secretToolPath() != ""
}

// secretServiceStore is BR-06's Linux backend: `secret-tool`, fixed
// argument vectors, no shell. store's password comes from stdin (that is
// how secret-tool itself defines "store": it reads the password from
// standard input), so the secret is never a process argument.
type secretServiceStore struct{}

func newSecretServiceStore() *secretServiceStore { return &secretServiceStore{} }

func (s *secretServiceStore) Name() string { return NameSecretService }

func (s *secretServiceStore) Get() (string, error) {
	out, err := runSecretTool(nil, "lookup", "service", service, "account", account)
	if err != nil {
		if isSecretToolNotFound(err) {
			return "", ErrNotFound
		}
		return "", errors.New("secret service: reading secret failed")
	}
	secret := strings.TrimRight(string(out), "\n")
	if secret == "" {
		return "", ErrNotFound
	}
	return secret, nil
}

func (s *secretServiceStore) Set(secret string) error {
	_, err := runSecretTool(strings.NewReader(secret), "store",
		"--label=AgentPulse bridge secret", "service", service, "account", account)
	if err != nil {
		return errors.New("secret service: storing secret failed")
	}
	return nil
}

func (s *secretServiceStore) Delete() error {
	_, err := runSecretTool(nil, "clear", "service", service, "account", account)
	if err != nil {
		// secret-tool clear exits 0 whether or not anything was found,
		// so a non-nil error here is a real failure (daemon unreachable,
		// binary missing), not "nothing to delete".
		return errors.New("secret service: deleting secret failed")
	}
	return nil
}

func runSecretTool(stdin *strings.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), secretToolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, secretToolBinary, args...) //nolint:gosec // fixed binary resolved via PATH, fixed argument vector, no shell
	// WaitDelay bounds how long Run() keeps waiting for secret-tool's
	// stdout/stderr pipes to close after the context above kills it —
	// see keychain_darwin.go's runSecurity for why this is needed in
	// addition to the context itself (Go's os/exec docs, "Cmd.WaitDelay").
	cmd.WaitDelay = secretToolTimeout
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &secretToolError{err: err, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

// secretToolError carries secret-tool's stderr for internal
// classification (isSecretToolNotFound) only; its Error() never includes
// stderr, so it stays safe to log.
type secretToolError struct {
	err    error
	stderr string
}

func (e *secretToolError) Error() string { return e.err.Error() }
func (e *secretToolError) Unwrap() error { return e.err }

// isSecretToolNotFound reports whether err is "lookup" exiting because no
// matching item exists: secret-tool exits non-zero with empty stdout and
// no stderr in that case, versus a real failure (daemon unreachable),
// which typically writes a message to stderr.
func isSecretToolNotFound(err error) bool {
	var ste *secretToolError
	if !errors.As(err, &ste) {
		return false
	}
	return strings.TrimSpace(ste.stderr) == ""
}
