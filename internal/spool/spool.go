// Package spool is the bridge's local, append-only, newline-delimited
// event queue (BR-02, BR-03; SPEC 11.2's "internal/spool"): a 500-event
// cap that evicts the oldest events first, and a Drain/commit pair
// "agentpulse flush" uses to
// read the queue, attempt delivery, and put back only what wasn't
// delivered — all without ever losing an event nobody has answered for
// (NFR-08) or blocking a concurrent Append for longer than one file
// operation (BR-02's 50ms hook budget).
package spool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Cap is BR-03's spool size limit: at most 500 events. Append evicts the
// oldest lines first once the file already holds this many.
const Cap = 500

// maxLineBytes bounds how large a single spool line Drain will still trust
// as one event before discarding it as malformed ("longer than a sane
// cap"). Every real event is well under 1 KB — the schema's largest
// free-text fields, project.name and task_label, cap at 64 and 80
// characters (SPEC 10.1) — so this is a generous multiple of that, not a
// limit anything legitimate should ever approach.
const maxLineBytes = 8 * 1024

// scanBufferCap bounds memory used while counting lines or scanning a
// possibly-larger-than-maxLineBytes line during Append's cheap common-case
// count: large enough that a merely-oversized (but not absurd) line still
// counts correctly, without ever loading the whole file.
const scanBufferCap = 1 << 20 // 1 MiB

// Append appends line as one line to the newline-delimited spool file at
// path, creating the file and its parent directory if needed. The parent
// directory is created with mode 0700 and the file with mode 0600 — spool
// contents are local-only, like everything else under
// $XDG_STATE_HOME/agentpulse (BR-05).
//
// The write is wrapped in an exclusive advisory lock (flock) held only for
// the duration of the write, so concurrent writers — another "agentpulse
// hook" invocation, and "agentpulse flush" draining or rewriting the same
// file (Drain/commit, below) — can never interleave and splice two
// events' bytes together on one line. line must not itself contain a
// newline; Append appends exactly one "\n" after it.
//
// If the file already holds Cap (500) lines, Append evicts the oldest
// lines first so the new one fits (BR-03). Counting existing lines is a
// cheap streaming scan that never holds the whole file in memory; only the
// rare eviction path (the spool is already full) reads it whole, since Cap
// bounds that read to at most 500 short lines.
func Append(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is derived from XDG state dir, not attacker input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	count, err := countLines(f)
	if err != nil {
		return err
	}

	if count < Cap {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return err
		}
		return writeLine(f, line)
	}

	// Eviction path: the file holds Cap lines already. Read it whole (at
	// most Cap short lines, not "the whole file" in the sense the common
	// case above avoids) and rewrite it with the oldest line(s) dropped so
	// there is room for the new one.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	lines := splitLines(data)
	drop := len(lines) - (Cap - 1)
	if drop < 0 {
		drop = 0
	}
	kept := lines[drop:]

	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for _, l := range kept {
		if err := writeLine(f, l); err != nil {
			return err
		}
	}
	return writeLine(f, line)
}

// Depth returns the number of events currently queued at path (BR-14's
// "spool depth"), without locking or otherwise disturbing the file — a
// missing spool (never paired, or freshly unpaired) is 0 events, not an
// error, matching every other Load in this codebase's tolerant contract.
func Depth(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from XDG state dir, not attacker input
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return countLines(f)
}

// Drain reads every event currently in the spool file at path under an
// exclusive lock and returns them as batch, in original order, together
// with discarded (a count of malformed lines dropped along the way — not
// valid JSON, or over maxLineBytes — logged at the caller's discretion)
// and a commit closure.
//
// Drain does not remove anything from the file itself; only commit does.
// A caller that crashes, is killed, or simply never calls commit leaves
// the spool exactly as Drain found it, so an event nobody has answered for
// is never lost (NFR-08) — the cost is that the file may briefly hold
// events a caller is also holding in memory to attempt delivery, which is
// fine: nothing else removes them but commit.
//
// commit must be called exactly once, with keep set to the subsequence of
// batch (preserving relative order) that should remain spooled for a
// future attempt — typically batch with the delivered prefix and any
// permanently-dropped events removed. commit re-locks the file, and
// preserves any lines a concurrent Append added after Drain ran (Append
// only ever appends at the end, so anything beyond the position Drain read
// up to is new and must survive) rewriting the file as keep followed by
// that new tail, clamped to Cap.
func Drain(path string) (batch [][]byte, discarded int, commit func(keep [][]byte) error, err error) {
	noop := func([][]byte) error { return nil }

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, 0, noop, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is derived from XDG state dir, not attacker input
	if err != nil {
		return nil, 0, noop, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, 0, noop, err
	}
	data, readErr := io.ReadAll(f)
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
	if readErr != nil {
		return nil, 0, noop, readErr
	}

	rawLines := splitLines(data)
	rawCount := len(rawLines)
	batch = make([][]byte, 0, len(rawLines))
	for _, l := range rawLines {
		if len(l) > maxLineBytes || !json.Valid(l) {
			discarded++
			continue
		}
		batch = append(batch, l)
	}

	commit = func(keep [][]byte) error {
		return rewrite(path, rawCount, keep)
	}
	return batch, discarded, commit, nil
}

// rewrite replaces the spool file's content with keep followed by any
// lines a concurrent Append added after the position the matching Drain
// read up to (rawCount raw, pre-discard lines), clamped to Cap (oldest
// dropped first) so a rewrite can never leave the file over the limit.
//
// This assumes the file's first rawCount lines at rewrite time are still
// exactly what Drain read: true as long as only one flush runs at a time
// (the flush.lock in cmd/agentpulse) and Append never removes or reorders
// existing lines, only appends — with one narrow exception, noted below.
func rewrite(path string, rawCount int, keep [][]byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is derived from XDG state dir, not attacker input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	current := splitLines(data)

	var appended [][]byte
	if len(current) >= rawCount {
		appended = current[rawCount:]
	} else {
		// Append's own Cap eviction rewrote the front of the file while
		// this drain was in flight (only possible if the spool was
		// already at Cap when this Drain ran, and enough events arrived
		// during delivery to trigger eviction again) — position rawCount
		// no longer means what it did. We can no longer tell "already
		// accounted for" from "new" by position, so preserve everything
		// currently in the file rather than risk silently dropping an
		// event nobody has answered for (NFR-08): a duplicate delivered
		// twice is harmless (the relay dedupes by event_id, SPEC 10.1),
		// a lost one is not.
		appended = current
	}

	final := make([][]byte, 0, len(keep)+len(appended))
	final = append(final, keep...)
	final = append(final, appended...)
	if drop := len(final) - Cap; drop > 0 {
		final = final[drop:]
	}

	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for _, l := range final {
		if err := writeLine(f, l); err != nil {
			return err
		}
	}
	return nil
}

func writeLine(f *os.File, line []byte) error {
	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	_, err := f.Write(buf)
	return err
}

// countLines counts non-empty newline-terminated lines in f from its
// current position, without holding the whole file in memory: the common
// case (a spool well under Cap) only ever streams through it once. f's
// position is left at EOF. A line longer than scanBufferCap fails the
// scan; callers on this path (Append, over a spool that is by definition
// full of individually-small events until proven otherwise) treat that
// the same as any other read error.
func countLines(f *os.File) (int, error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), scanBufferCap)
	n := 0
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			n++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// splitLines splits data on "\n" and drops empty lines (a trailing
// newline, or an entirely empty file, must not become a phantom line),
// returning fresh copies so callers can hold them past the buffer data
// came from.
func splitLines(data []byte) [][]byte {
	parts := bytes.Split(data, []byte("\n"))
	lines := make([][]byte, 0, len(parts))
	for _, l := range parts {
		if len(l) == 0 {
			continue
		}
		cp := make([]byte, len(l))
		copy(cp, l)
		lines = append(lines, cp)
	}
	return lines
}
