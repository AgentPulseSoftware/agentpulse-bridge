package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// InstalledCommands is a read-only inspection of settingsPath: for each
// Claude Code hook event name present in the file, it returns the
// commands in that event that owner.Owns reports as this bridge's own,
// in file order. Events with no owned command are absent from the
// returned map.
//
// This exists for "agentpulse doctor" (BR-15), which only
// reads. It never writes, creates, or repairs settingsPath, and never
// builds a Plan — it reuses this package's own parsing
// (scanObject/orderedRawObject/describeJSONError), the same code
// PlanInstall and PlanUninstall use to find the top-level "hooks" member,
// rather than adding a second JSON parser that could disagree with theirs.
//
// A missing file returns an error that satisfies errors.Is(err,
// fs.ErrNotExist) (equivalently, the check os.IsNotExist documents but,
// unlike os.IsNotExist itself, understands the %w-wrapped error this
// function returns). Malformed JSON returns the same file-and-line
// positioned error PlanInstall gives (ERR-07). Any other read failure is
// wrapped with %w as well.
func InstalledCommands(settingsPath string, owner Owner) (map[string][]string, error) {
	raw, err := os.ReadFile(settingsPath) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", settingsPath, err)
	}
	content, _ := bytes.CutPrefix(raw, utf8BOM)

	result := map[string][]string{}
	if len(bytes.TrimSpace(content)) == 0 {
		return result, nil
	}

	members, _, _, err := scanObject(content)
	if err != nil {
		return nil, describeJSONError(content, err)
	}
	m := findMember(members, "hooks")
	if m == nil {
		return result, nil
	}
	hooksRaw := content[m.valueStart:m.valueEnd]

	hooksObj := newOrderedRawObject()
	if err := json.Unmarshal(hooksRaw, hooksObj); err != nil {
		return nil, fmt.Errorf("hooks: %w", describeJSONError(hooksRaw, err))
	}

	for _, event := range hooksObj.keys {
		eventRaw, _ := hooksObj.Get(event)
		owned, err := ownedCommandsInEvent(eventRaw, owner)
		if err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", event, describeJSONError(eventRaw, err))
		}
		if len(owned) > 0 {
			result[event] = owned
		}
	}
	return result, nil
}

// ownedCommandsInEvent walks one hooks.<Event>[] array — a list of
// {"matcher","hooks"} groups — and returns the "command" of every hook
// entry owner.Owns reports as this bridge's, in array order. A group, or
// an entry within a group, that doesn't look like the shape this package
// itself writes is simply not owned (it never errors on account of
// another tool's differently-shaped entry — filterOwnedHooks in hooks.go
// takes the same stance for the same reason: an entry we don't recognize
// is never treated as ours).
func ownedCommandsInEvent(eventRaw json.RawMessage, owner Owner) ([]string, error) {
	var elements []json.RawMessage
	if err := json.Unmarshal(eventRaw, &elements); err != nil {
		return nil, err
	}

	var owned []string
	for _, elem := range elements {
		group := newOrderedRawObject()
		if err := json.Unmarshal(elem, group); err != nil {
			continue // not a {"matcher","hooks"} object; nothing of ours here
		}
		hooksRaw, ok := group.Get("hooks")
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(hooksRaw, &entries); err != nil {
			continue
		}
		for _, entry := range entries {
			var peek struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(entry, &peek); err == nil && owner.Owns(peek.Command) {
				owned = append(owned, peek.Command)
			}
		}
	}
	return owned, nil
}
