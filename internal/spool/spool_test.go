package spool

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestAppendCreatesFileAndDirWithSafeModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "agentpulse")
	path := filepath.Join(dir, "spool.ndjson")

	if err := Append(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("Append() returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat spool file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("spool file mode = %o, want 0600", perm)
	}

	data, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\"a\":1}\n"; got != want {
		t.Errorf("spool contents = %q, want %q", got, want)
	}
}

func TestAppendAppendsMultipleLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")

	if err := Append(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(lines), lines)
	}
	if lines[0] != `{"a":1}` || lines[1] != `{"a":2}` {
		t.Errorf("unexpected lines: %v", lines)
	}
}

// TestAppendConcurrentWritesNeverInterleave exercises the flock: many
// goroutines append distinct, individually-parseable lines concurrently,
// and every single line in the resulting file must come out exactly as one
// of the goroutines wrote it — never two lines' bytes spliced together.
func TestAppendConcurrentWritesNeverInterleave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	const n = 50

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := strings.Repeat("x", 40) + "-" + strconv.Itoa(i)
			if err := Append(path, []byte(line)); err != nil {
				t.Errorf("Append() returned error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	lines := readLines(t, path)
	if len(lines) != n {
		t.Fatalf("got %d lines, want %d (a shorter or longer count means writes interleaved)", len(lines), n)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		if len(l) < 40 || !strings.HasPrefix(l, strings.Repeat("x", 40)+"-") {
			t.Fatalf("corrupted/interleaved line: %q", l)
		}
		if seen[l] {
			t.Fatalf("duplicate line (bytes written twice): %q", l)
		}
		seen[l] = true
	}
}

func TestAppendEvictsOldestFirstAtCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")

	for i := 0; i < Cap+10; i++ {
		if err := Append(path, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatalf("Append(%d) returned error: %v", i, err)
		}
	}

	lines := readLines(t, path)
	if len(lines) != Cap {
		t.Fatalf("spool holds %d lines, want exactly Cap=%d", len(lines), Cap)
	}
	// The oldest 10 (i=0..9) must have been evicted first; the newest
	// Cap events (i=10..Cap+9) must all be present, in order.
	for idx, l := range lines {
		want := fmt.Sprintf(`{"i":%d}`, idx+10)
		if l != want {
			t.Fatalf("line %d = %q, want %q (oldest-first eviction not preserved)", idx, l, want)
		}
	}
}

func TestDrainReturnsEventsInOrderAndEmptiesUntouchedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	for i := 0; i < 5; i++ {
		if err := Append(path, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	batch, discarded, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("Drain() returned error: %v", err)
	}
	if discarded != 0 {
		t.Errorf("discarded = %d, want 0", discarded)
	}
	if len(batch) != 5 {
		t.Fatalf("got %d events, want 5", len(batch))
	}
	for i, l := range batch {
		want := fmt.Sprintf(`{"i":%d}`, i)
		if string(l) != want {
			t.Errorf("batch[%d] = %q, want %q", i, l, want)
		}
	}

	// Drain itself must not have touched the file yet (NFR-08: nothing is
	// lost if the caller never calls commit).
	lines := readLines(t, path)
	if len(lines) != 5 {
		t.Fatalf("file has %d lines after Drain but before commit, want 5 (Drain must not remove anything)", len(lines))
	}

	if err := commit(nil); err != nil {
		t.Fatalf("commit() returned error: %v", err)
	}
	lines = readLines(t, path)
	if len(lines) != 0 {
		t.Fatalf("file has %d lines after commit(nil), want 0", len(lines))
	}
}

func TestDrainOnMissingFileReturnsEmptyBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "spool.ndjson")
	batch, discarded, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("Drain() returned error: %v", err)
	}
	if len(batch) != 0 || discarded != 0 {
		t.Fatalf("batch=%v discarded=%d, want empty", batch, discarded)
	}
	if err := commit(nil); err != nil {
		t.Fatalf("commit() returned error: %v", err)
	}
}

// TestCommitKeepsOnlyUndeliveredSuffixAndPreservesConcurrentAppend
// simulates a partial delivery: the first 3 of 5 drained events were
// delivered, the caller commits the remaining 2 as "keep", and a fresh
// event appended *during* the (simulated) delivery window must survive
// commit's rewrite untouched and in the right place (after the kept
// events, since it arrived later) — BR-03/NFR-08's ordering guarantee.
func TestCommitKeepsOnlyUndeliveredSuffixAndPreservesConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	for i := 0; i < 5; i++ {
		if err := Append(path, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	batch, _, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("Drain() returned error: %v", err)
	}
	if len(batch) != 5 {
		t.Fatalf("got %d events, want 5", len(batch))
	}

	// A concurrent "hook" append lands while "flush" is still trying to
	// deliver the drained batch.
	if err := Append(path, []byte(`{"i":"new"}`)); err != nil {
		t.Fatal(err)
	}

	// Events 0,1,2 delivered; 3,4 were not.
	keep := batch[3:]
	if err := commit(keep); err != nil {
		t.Fatalf("commit() returned error: %v", err)
	}

	lines := readLines(t, path)
	want := []string{`{"i":3}`, `{"i":4}`, `{"i":"new"}`}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %v, want %v", len(lines), lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestDrainDiscardsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	if err := Append(path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	// A line longer than maxLineBytes.
	if err := Append(path, []byte(`{"a":"`+strings.Repeat("x", maxLineBytes)+`"}`)); err != nil {
		t.Fatal(err)
	}

	batch, discarded, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("Drain() returned error: %v", err)
	}
	if discarded != 2 {
		t.Fatalf("discarded = %d, want 2", discarded)
	}
	if len(batch) != 2 {
		t.Fatalf("got %d events, want 2 (malformed lines must be excluded from batch)", len(batch))
	}

	// Malformed lines must never come back, even if the caller keeps
	// everything: they are gone the moment Drain classified them.
	if err := commit(batch); err != nil {
		t.Fatalf("commit() returned error: %v", err)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("file has %d lines after commit, want 2 (malformed lines must not reappear)", len(lines))
	}
}

func TestCommitClampsToCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	batch, _, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("Drain() returned error: %v", err)
	}
	if len(batch) != 0 {
		t.Fatalf("got %d events on an empty spool, want 0", len(batch))
	}

	keep := make([][]byte, Cap+5)
	for i := range keep {
		keep[i] = []byte(fmt.Sprintf(`{"i":%d}`, i))
	}
	if err := commit(keep); err != nil {
		t.Fatalf("commit() returned error: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != Cap {
		t.Fatalf("got %d lines after commit, want Cap=%d (commit must clamp too)", len(lines), Cap)
	}
	// Oldest-first: the surviving lines must be the last Cap of keep.
	wantFirst := fmt.Sprintf(`{"i":%d}`, 5)
	if lines[0] != wantFirst {
		t.Errorf("first surviving line = %q, want %q", lines[0], wantFirst)
	}
}

// TestConcurrentAppendAndDrainNeverSpliceOrLoseALine runs many concurrent
// Appends alongside repeated Drain/commit(nil) cycles (the flush loop
// draining everything it sees each time) and checks, at the end, that
// every line ever appended is accounted for exactly once across whatever
// remains on disk plus what every commit(nil) call actually drained —
// nothing spliced, nothing duplicated, nothing lost. Run with -race.
func TestConcurrentAppendAndDrainNeverSpliceOrLoseALine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf(`{"n":%d,"pad":"%s"}`, i, strings.Repeat("y", 20))
			if err := Append(path, []byte(line)); err != nil {
				t.Errorf("Append() returned error: %v", err)
			}
		}(i)
	}

	seen := map[string]bool{}
	var mu sync.Mutex
	stop := make(chan struct{})
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			batch, _, commit, err := Drain(path)
			if err != nil {
				t.Errorf("Drain() returned error: %v", err)
				return
			}
			mu.Lock()
			for _, l := range batch {
				if seen[string(l)] {
					t.Errorf("line seen twice across drains: %q", l)
				}
				seen[string(l)] = true
			}
			mu.Unlock()
			if err := commit(nil); err != nil {
				t.Errorf("commit() returned error: %v", err)
				return
			}
		}
	}()

	wg.Wait()
	close(stop)
	drainWG.Wait()

	// One final drain sweeps up anything appended after the last
	// in-loop drain observed it.
	batch, _, commit, err := Drain(path)
	if err != nil {
		t.Fatalf("final Drain() returned error: %v", err)
	}
	for _, l := range batch {
		if seen[string(l)] {
			t.Errorf("line seen twice across drains: %q", l)
		}
		seen[string(l)] = true
	}
	if err := commit(nil); err != nil {
		t.Fatal(err)
	}

	if len(seen) != n {
		t.Fatalf("accounted for %d distinct lines, want %d (splice, loss, or duplication)", len(seen), n)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestDepth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.ndjson")

	if got, err := Depth(path); err != nil || got != 0 {
		t.Errorf("Depth of a missing file = (%d, %v), want (0, nil)", got, err)
	}

	for i := 0; i < 3; i++ {
		if err := Append(path, []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := Depth(path); err != nil || got != 3 {
		t.Errorf("Depth = (%d, %v), want (3, nil)", got, err)
	}
}
