package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/claudehooks"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/cred"
)

// codePattern finds the pairing code in pair's own output. It is the
// Crockford base32 alphabet SPEC 10.3 step 1 specifies, minus the
// ambiguous letters.
var codePattern = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{8}\b`)

// watchingWriter tees everything written to it into a buffer and calls
// onCode the first time a pairing code appears, which is how the scripted
// "phone" below learns the code while pair is still polling.
type watchingWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	seen   bool
	onCode func(code string)
}

func (w *watchingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	fire := ""
	if !w.seen {
		if m := codePattern.FindString(w.buf.String()); m != "" {
			w.seen = true
			fire = m
		}
	}
	w.mu.Unlock()
	if fire != "" {
		go w.onCode(fire)
	}
	return n, err
}

func (w *watchingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestPairAgainstALocalRelay is SPEC section 18's integration test: start
// a relay listening on http://localhost:8787, then
//
//	AGENTPULSE_E2E_RELAY=http://localhost:8787 go test ./cmd/agentpulse -run TestPairAgainstALocalRelay
//
// It pairs with a temporary HOME, redeems the printed code the way the
// phone does (POST /v1/devices then POST /v1/pairings), and checks the
// poll completes and the hooks were written. It skips when the variable is
// unset or nothing is listening, so a machine without a local relay stays
// green.
//
// By default the credential store is a fake, so no platform store is
// touched. Setting AGENTPULSE_CRED_E2E=1 as well swaps in cred.Default()
// so the whole pairing path, store included, is exercised:
//
//	AGENTPULSE_E2E_RELAY=http://localhost:8787 AGENTPULSE_CRED_E2E=1 \
//	  go test ./cmd/agentpulse -run TestPairAgainstALocalRelay -v
//
// A store path that could never work against the real binary must not
// pass unnoticed behind the fake. It is opt-in,
// so CI is unaffected, and it never skips its way out: once
// AGENTPULSE_CRED_E2E=1 asks for the platform store, anything short of
// it fails the test (see e2eCredStore).
func TestPairAgainstALocalRelay(t *testing.T) {
	baseURL := strings.TrimSuffix(os.Getenv("AGENTPULSE_E2E_RELAY"), "/")
	if baseURL == "" {
		t.Skip("set AGENTPULSE_E2E_RELAY=http://localhost:8787 (with a relay listening there) to run this")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Get(baseURL + "/v1/health"); err != nil { //nolint:noctx // a liveness probe in a test
		t.Skipf("no relay listening at %s: %v", baseURL, err)
	} else {
		_ = resp.Body.Close()
	}

	// Captured before pairingHome replaces HOME: the credential store's
	// security(1) calls need the real one (see realHomeStore), and
	// os.UserHomeDir reads $HOME on macOS, so this is the only moment it
	// is still the truth.
	realHome, err := os.UserHomeDir()
	if err != nil || realHome == "" {
		t.Fatalf("resolving the real home directory before the harness replaces HOME: %v", err)
	}

	home := pairingHome(t)
	if realHome == home {
		t.Fatalf("the harness home and the real home are the same directory: the credential store "+
			"would be reached with a fabricated HOME (got %d bytes of path)", len(home))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// redeemAsPhone runs in its own goroutine (watchingWriter.onCode fires
	// with "go"), so it cannot call t.Errorf itself: the testing package
	// panics if a test logs after the test function has returned. It
	// reports its result on this channel instead, read below in the test
	// goroutine.
	redeemErr := make(chan error, 1)
	out := &watchingWriter{onCode: func(code string) {
		redeemErr <- redeemAsPhone(client, baseURL, code)
	}}
	cmd, _ := testCmd("")
	cmd.SetOut(out)

	if err := runPair(ctx, pairDeps{
		out: out, cmd: cmd, relayFlag: baseURL, yes: true, credStore: e2eCredStore(t, realHome),
	}); err != nil {
		t.Fatalf("runPair against %s: %v\n%s", baseURL, err, out.String())
	}

	select {
	case err := <-redeemErr:
		if err != nil {
			t.Fatalf("redeeming as the phone: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the phone's redeem never reported a result")
	}

	hooksByEvent := settingsHooks(t, filepath.Join(home, ".claude", "settings.json"))
	if len(hooksByEvent) != len(claudehooks.BR08Events) {
		t.Errorf("settings has %d hook events, want %d", len(hooksByEvent), len(claudehooks.BR08Events))
	}
}

// redeemAsPhone does what the iPhone app does in SPEC 10.3 step 3: create
// a device, then redeem the code with device auth. It returns the error
// rather than reporting it directly, since it runs in its own goroutine
// (see redeemErr above).
func redeemAsPhone(client *http.Client, baseURL, code string) error {
	resp, err := client.Post(baseURL+"/v1/devices", "application/json", strings.NewReader(`{}`)) //nolint:noctx // bounded by client.Timeout
	if err != nil {
		return fmt.Errorf("creating the test device: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var device struct {
		ID     string `json:"device_id"`
		Secret string `json:"device_secret"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&device); err != nil {
		return fmt.Errorf("decoding the test device: %w", err)
	}

	body := strings.NewReader(`{"code":"` + code + `"}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/pairings", body) //nolint:noctx // bounded by client.Timeout
	if err != nil {
		return fmt.Errorf("building the redeem request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+device.ID+"."+device.Secret)
	req.Header.Set("Content-Type", "application/json")
	redeem, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("redeeming the pairing code: %w", err)
	}
	defer func() { _ = redeem.Body.Close() }()
	if redeem.StatusCode != http.StatusOK {
		return fmt.Errorf("redeeming the pairing code returned %d, want 200", redeem.StatusCode)
	}
	return nil
}

// realHomeStore is a test-only cred.Store decorator that gives the
// credential store's security(1) calls the user's real HOME while
// everything else in the pairing harness keeps the temporary one.
//
// security(1) resolves the default keychain, and the Security framework
// resolves its preferences, underneath $HOME — and pairingHome fabricates
// it — so the store must see the real HOME to reach the actual login
// Keychain without raising an access dialog. The fabricated HOME stays
// for the rest of the test (settings.json, XDG_CONFIG_HOME,
// XDG_STATE_HOME: SEC-14) and only the store sees the real one. Nothing
// about the item is weakened: no -A, no -T, macOS's default access
// control (SEC-02).
//
// Mutating process-wide state is safe here only because runPair reaches
// the store from a single goroutine, and every method restores the
// previous value on every path, including a panic.
type realHomeStore struct {
	store    cred.Store
	realHome string
}

// useRealHome sets HOME to the real home and returns the restore func the
// caller must defer.
func (s *realHomeStore) useRealHome() func() {
	prev, had := os.LookupEnv("HOME")
	_ = os.Setenv("HOME", s.realHome)
	return func() {
		if had {
			_ = os.Setenv("HOME", prev)
			return
		}
		_ = os.Unsetenv("HOME")
	}
}

func (s *realHomeStore) Name() string { return s.store.Name() }

func (s *realHomeStore) Get() (string, error) {
	restore := s.useRealHome()
	defer restore()
	return s.store.Get()
}

func (s *realHomeStore) Set(secret string) error {
	restore := s.useRealHome()
	defer restore()
	return s.store.Set(secret)
}

func (s *realHomeStore) Delete() error {
	restore := s.useRealHome()
	defer restore()
	return s.store.Delete()
}

// e2eCredStore picks the credential store TestPairAgainstALocalRelay
// pairs into: the in-memory fake by default (no platform store is
// touched, so CI and a plain `go test ./...` are unaffected), or
// cred.Default() wrapped in realHomeStore when AGENTPULSE_CRED_E2E=1 is
// also set. It logs the store's name — a fixed label, never a path, a
// status detail or the secret (SEC-02, BR-04).
//
// In the AGENTPULSE_CRED_E2E=1 mode this refuses to pretend: the only
// skip is a non-darwin machine, and every other way of not reaching the
// macOS Keychain is a failure with the reason named. In particular the
// file fallback here means the AGENTPULSE_TEST_CRED_BACKEND override
// leaked, or cred.Default() was already probed and cached by an earlier
// test in this binary (BR-06 caches once per process).
//
// This mode overwrites and then deletes the real "agentpulse" Keychain
// item, so a Mac that is genuinely paired must run `agentpulse pair`
// again afterwards. It must always be run with
// `-run TestPairAgainstALocalRelay`, because cred.Default() caches the
// selected store for the life of the process; a wider run would hand
// later tests in the same binary the real Keychain under this test's
// fabricated HOME instead of their own fake.
func e2eCredStore(t *testing.T, realHome string) cred.Store {
	t.Helper()
	if os.Getenv("AGENTPULSE_CRED_E2E") != "1" {
		t.Log("credential store: in-memory fake (AGENTPULSE_CRED_E2E is not 1)")
		return &fakeCredStore{}
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("AGENTPULSE_CRED_E2E=1 pairs into the macOS Keychain; this is %s", runtime.GOOS)
	}

	// Registered before anything else in this branch so t.Cleanup's LIFO
	// order runs it last, after every cleanup registered later. It
	// therefore runs before pairingHome's own t.Setenv restores HOME,
	// which is exactly why it goes through the wrapper.
	var store cred.Store
	t.Cleanup(func() {
		if store == nil {
			return
		}
		cleanUpCredential(t, store)
	})

	// The empty string is not "file", so probeLocked runs the normal
	// platform probe; t.Setenv's own cleanup puts the harness override
	// back for every other test in this binary. Never os.Unsetenv, which
	// would not be restored.
	t.Setenv("AGENTPULSE_TEST_CRED_BACKEND", "")
	store = &realHomeStore{store: cred.Default(), realHome: realHome}
	if name := store.Name(); name != cred.NameKeychain {
		t.Fatalf("AGENTPULSE_CRED_E2E=1 asked for the %s and got the %s: the test override leaked, "+
			"or cred.Default() was already probed and cached by an earlier test in this binary",
			cred.NameKeychain, name)
	}
	t.Logf("credential store: %s (AGENTPULSE_CRED_E2E=1)", store.Name())
	return store
}

// cleanUpCredential deletes the item this test stored and then proves it
// is gone, because a Set killed by its timeout can still land a moment
// after the caller has given up and after the test has finished:
// security(1) is killed, pair reports failure, and the item
// appears afterwards. So this deletes, then polls for up to 15 seconds
// and only returns on two consecutive not-found readings at least a
// second apart. A late arrival is deleted again and fails the test — the
// difference between "the Keychain is clean" and "we think it is". Only
// the elapsed time is reported, never the value.
func cleanUpCredential(t *testing.T, store cred.Store) {
	t.Helper()
	if err := store.Delete(); err != nil {
		t.Errorf("cleaning up the credential store: %v", err)
	}
	start := time.Now()
	deadline := start.Add(15 * time.Second)
	var firstClean time.Time
	for {
		_, err := store.Get()
		switch {
		case errors.Is(err, cred.ErrNotFound):
			if firstClean.IsZero() {
				firstClean = time.Now()
			} else if time.Since(firstClean) >= time.Second {
				return
			}
		case err != nil:
			firstClean = time.Time{}
		default:
			firstClean = time.Time{}
			t.Errorf("a credential landed in the %s %s after the caller had given up; deleting it again",
				store.Name(), time.Since(start).Round(time.Millisecond))
			if err := store.Delete(); err != nil {
				t.Errorf("deleting the late credential: %v", err)
			}
		}
		if time.Now().After(deadline) {
			t.Errorf("the %s could not be confirmed empty within %s of the cleanup delete",
				store.Name(), time.Since(start).Round(time.Millisecond))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}
