// Package hooks merges AgentPulse's Claude Code hook entries into
// ~/.claude/settings.json (BR-07, BR-08, BR-09) without disturbing anything
// else the operator has configured there, and removes them again cleanly.
//
// It is used by the dev-only "agentpulse record install/uninstall"
// commands, and is written so "agentpulse pair"/"unpair" can call the
// same Install/Uninstall functions.
package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// utf8BOM is the three-byte UTF-8 byte-order mark some editors and Windows
// tools prepend to JSON files. It is not itself valid JSON, so it must be
// stripped before parsing; a file that had one keeps one.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Entry is one hook command AgentPulse wants registered for a Claude Code
// event.
type Entry struct {
	// Event is the Claude Code hook event name, e.g. "PreToolUse".
	Event string
	// Matcher is the hook matcher. BR-08 always uses "" (empty matcher).
	Matcher string
	// Command is the full shell command Claude Code will run.
	Command string
	// Timeout is the hook timeout in seconds. BR-08 uses 10.
	Timeout int
}

// hookSpec mirrors one element of a hooks.<Event>[].hooks array in Claude
// Code's settings.json, for building an entry AgentPulse itself owns.
// Timeout is omitempty so a zero value doesn't fabricate a "timeout: 0"
// that was never there; BR-08 always sets it to 10 in practice.
type hookSpec struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// hookGroup mirrors one element of a hooks.<Event> array: a matcher plus the
// commands that run for it. Used only to build a brand new group AgentPulse
// is adding; an existing group is never decoded into this struct (see
// filterOwnedHooks), so another tool's entry in the same group keeps every
// field it was written with, known or not.
type hookGroup struct {
	Matcher string     `json:"matcher"`
	Hooks   []hookSpec `json:"hooks"`
}

// Plan is a proposed change to a settings file: the bytes before and after,
// and a human-readable diff between them. Nothing is written until Apply is
// called, so the caller can show Diff to the operator and ask for
// confirmation first (BR-07).
type Plan struct {
	Path    string
	Before  []byte // nil if the file did not exist yet
	After   []byte
	Diff    string
	Changed bool
}

// Apply atomically writes Plan.After to Plan.Path, creating parent
// directories as needed. It does nothing (and returns nil) if the plan made
// no change. The write goes through a temp file in the same directory,
// fsynced before the rename so the new content is durable even across a
// crash right after Apply returns, then the directory itself is fsynced so
// the rename is durable too (best-effort: some platforms/filesystems don't
// support directory fsync, so that step's error is not fatal).
func (p *Plan) Apply() error {
	if !p.Changed {
		return nil
	}
	dir := filepath.Dir(p.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".agentpulse-settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename below succeeds

	if _, err := tmp.Write(p.After); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if info, statErr := os.Stat(p.Path); statErr == nil {
		_ = os.Chmod(tmpPath, info.Mode())
	}
	if err := os.Rename(tmpPath, p.Path); err != nil {
		return fmt.Errorf("replacing %s: %w", p.Path, err)
	}

	if dirFile, err := os.Open(dir); err == nil { //nolint:gosec // operator-controlled path
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

// PlanInstall reads settingsPath (creating an empty settings document in
// memory if it does not exist yet, and treating a present-but-empty file
// the same as "{}"), merges entries into hooks.<event> by event name, and
// returns a Plan. Every entry replaces AgentPulse's existing entry for that
// event (if any) so re-running install with a new directory updates it in
// place; entries for other tools, and every other top-level key, are left
// untouched (BR-07). Invalid JSON aborts with the file and line (ERR-07). A
// leading UTF-8 byte-order mark is tolerated and preserved.
func PlanInstall(settingsPath string, owner Owner, entries []Entry) (*Plan, error) {
	return plan(settingsPath, owner, entries, true)
}

// PlanUninstall reads settingsPath and removes every hook entry owner owns
// (see Owner.Owns), from every event — not just the eight BR-08 registers —
// leaving all other hooks and settings untouched (BR-09).
func PlanUninstall(settingsPath string, owner Owner) (*Plan, error) {
	return plan(settingsPath, owner, nil, false)
}

func plan(settingsPath string, owner Owner, entries []Entry, install bool) (*Plan, error) {
	before, err := os.ReadFile(settingsPath) //nolint:gosec // operator-controlled path
	notExist := os.IsNotExist(err)
	if err != nil && !notExist {
		return nil, fmt.Errorf("reading %s: %w", settingsPath, err)
	}
	if notExist {
		before = nil
	}

	content, hadBOM := bytes.CutPrefix(before, utf8BOM)

	if !install && len(content) == 0 {
		// Nothing to uninstall from a file that doesn't exist, or is
		// empty: don't create one (or write "{}" into it) just to say so.
		return &Plan{Path: settingsPath, Before: before, After: before, Diff: "", Changed: false}, nil
	}

	byEvent := map[string][]Entry{}
	var eventOrder []string
	for _, e := range entries {
		if _, seen := byEvent[e.Event]; !seen {
			eventOrder = append(eventOrder, e.Event)
		}
		byEvent[e.Event] = append(byEvent[e.Event], e)
	}

	indent := detectIndent(content)

	var newContent []byte
	if len(content) == 0 {
		newContent, err = buildFromScratch(owner, byEvent, eventOrder, indent)
	} else {
		newContent, err = spliceHooksInto(content, owner, byEvent, eventOrder, install, indent)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", settingsPath, err)
	}

	after := newContent
	if hadBOM {
		after = append(append([]byte{}, utf8BOM...), newContent...)
	}

	// The diff is computed against the operator's own original bytes, not
	// a re-pretty-printed version of them, so it shows exactly what will
	// change — nothing more, and (thanks to splicing) usually much less
	// than the whole file.
	return &Plan{
		Path:    settingsPath,
		Before:  before,
		After:   after,
		Diff:    Diff(string(before), string(after)),
		Changed: !bytes.Equal(before, after),
	}, nil
}

// mergeEvent updates hooksObj's array for event in place: it drops every
// hook entry owner owns (see filterOwnedHooks),
// then (if wanted is non-empty) appends one fresh group per entry in
// wanted. Groups belonging entirely to other tools, and every hook entry
// within a shared group that isn't AgentPulse's, are carried through as
// their original raw bytes — unknown fields and all — never decoded into a
// Go struct and re-serialized.
func mergeEvent(hooksObj *orderedRawObject, owner Owner, event string, wanted []Entry) error {
	var elements []json.RawMessage
	if raw, ok := hooksObj.Get(event); ok {
		if err := json.Unmarshal(raw, &elements); err != nil {
			return describeJSONError(raw, err)
		}
	}

	kept := make([]json.RawMessage, 0, len(elements)+len(wanted))
	for _, elem := range elements {
		filtered, droppedWholeGroup, err := filterOwnedHooks(elem, owner)
		if err != nil {
			// Not a recognizable {"matcher","hooks"} group; leave it exactly
			// as written rather than risk corrupting something we don't
			// understand.
			kept = append(kept, elem)
			continue
		}
		if droppedWholeGroup {
			continue // every hook in this group was ours; drop the group
		}
		kept = append(kept, filtered) // == elem verbatim if nothing was ours
	}

	for _, e := range wanted {
		g := hookGroup{
			Matcher: e.Matcher,
			Hooks: []hookSpec{{
				Type:    "command",
				Command: e.Command,
				Timeout: e.Timeout,
			}},
		}
		b, err := marshalNoEscape(g)
		if err != nil {
			return fmt.Errorf("marshaling new hook group: %w", err)
		}
		kept = append(kept, b)
	}

	if len(kept) == 0 {
		hooksObj.Delete(event)
		return nil
	}
	arr, err := marshalNoEscape(kept)
	if err != nil {
		return fmt.Errorf("marshaling hook array: %w", err)
	}
	hooksObj.Set(event, arr)
	return nil
}

// filterOwnedHooks looks at one hooks.<Event>[] element (a {"matcher",
// "hooks"} group) and removes any hook entry whose "command" owner owns
// (BR-09). Only entries being removed are ever decoded (into a
// throwaway struct with just a Command field, to test it); every entry
// that is kept — including another tool's, with whatever fields it has or
// doesn't (BR-09's shared-group case) — passes through as the exact raw
// bytes it arrived as. If nothing in the group is AgentPulse's, filtered is
// elem itself, byte-for-byte, and droppedWholeGroup is false. If every hook
// in the group was AgentPulse's, droppedWholeGroup is true and filtered is
// unused. If elem doesn't look like a {"matcher","hooks"} group at all, err
// is non-nil and the caller keeps elem untouched.
func filterOwnedHooks(elem json.RawMessage, owner Owner) (filtered json.RawMessage, droppedWholeGroup bool, err error) {
	group := newOrderedRawObject()
	if err := json.Unmarshal(elem, group); err != nil {
		return nil, false, err
	}
	hooksRaw, ok := group.Get("hooks")
	if !ok {
		return elem, false, nil // no "hooks" key: nothing of ours to remove
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(hooksRaw, &entries); err != nil {
		return nil, false, err
	}

	kept := make([]json.RawMessage, 0, len(entries))
	droppedAny := false
	for _, entry := range entries {
		var peek struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(entry, &peek); err == nil && owner.Owns(peek.Command) {
			droppedAny = true
			continue
		}
		kept = append(kept, entry) // raw bytes verbatim: unknown fields and all
	}
	if !droppedAny {
		return elem, false, nil
	}
	if len(kept) == 0 {
		return nil, true, nil
	}

	keptArr, err := marshalNoEscape(kept)
	if err != nil {
		return nil, false, err
	}
	group.Set("hooks", keptArr)
	newElem, err := marshalNoEscape(group)
	if err != nil {
		return nil, false, err
	}
	return newElem, false, nil
}

// marshalNoEscape is json.Marshal without HTML-escaping "<", ">", and "&".
// The standard json.Marshal always HTML-escapes those three bytes — even
// inside an already-encoded json.RawMessage it is embedding verbatim,
// because it runs a compact() pass with escaping on over the *entire*
// output of a nested value's MarshalJSON, not just content it encodes
// itself. That would corrupt another tool's hook command containing, say,
// "make && echo done > out.txt". json.Encoder.SetEscapeHTML(false) is the
// documented way to turn that off; this settings file is never embedded in
// HTML, so there is no reason to want the escaping.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// detectIndent looks at settings JSON as originally written and returns the
// indent unit to reuse when rewriting it: a tab if the first indented line
// starts with one, otherwise that line's leading spaces (so a 2-space file
// stays 2-space and a 4-space file stays 4-space). Falls back to two spaces
// for a compact, brand new, or otherwise unindented file.
func detectIndent(content []byte) string {
	lines := bytes.Split(content, []byte("\n"))
	for i, line := range lines {
		if i == 0 || len(line) == 0 {
			continue // line 0 is just "{" in every indented style
		}
		switch line[0] {
		case '\t':
			return "\t"
		case ' ':
			n := 0
			for n < len(line) && line[n] == ' ' {
				n++
			}
			return strings.Repeat(" ", n)
		default:
			return "  " // first non-blank line isn't indented at all
		}
	}
	return "  "
}

// prettyJSON reformats compact JSON with the given indent and a trailing
// newline.
func prettyJSON(compact []byte, indent string) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", indent); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// describeJSONError turns a json.SyntaxError (or similar) into an error that
// names the 1-based line it occurred on, per ERR-07 ("abort with a message
// naming the file and line"). It unwraps with errors.As rather than a plain
// type assertion, since scanObject and mergeEvent both wrap the decoder's
// own error with additional context (e.g. "decoding value for key %q: %w")
// before it reaches here.
func describeJSONError(data []byte, err error) error {
	var offset int64 = -1
	var syn *json.SyntaxError
	var ute *json.UnmarshalTypeError
	var pos *positionedError
	switch {
	case errors.As(err, &syn):
		offset = syn.Offset
	case errors.As(err, &ute):
		offset = ute.Offset
	case errors.As(err, &pos):
		offset = int64(pos.offset)
	}
	if offset < 0 {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	line := 1
	for i := 0; i < int(offset) && i < len(data); i++ {
		if data[i] == '\n' {
			line++
		}
	}
	if pos != nil {
		// pos.msg already says what's wrong in plain language (a duplicate
		// key, trailing content); "invalid JSON" would be misleading here
		// since neither is actually a JSON syntax error.
		return fmt.Errorf("line %d: %s", line, pos.msg)
	}
	return fmt.Errorf("invalid JSON at line %d: %w", line, err)
}
