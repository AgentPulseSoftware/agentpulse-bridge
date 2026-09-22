package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// This file implements byte-range splicing of the top-level "hooks" key
// into an existing settings.json document, instead of parsing the whole
// document and re-serializing it. That distinction matters: re-serializing
// the whole document (even preserving key order and raw values) still has
// to pick ONE formatting style and apply it everywhere, which silently
// reflows any value the operator wrote in a non-canonical way — for
// example a "permissions" value hand-written on one line inside an
// otherwise multi-line file. Splicing instead locates the exact byte range
// of the "hooks" member (or, if it doesn't exist yet, the exact insertion
// point) and replaces only that range, so every other byte in the file —
// including another key's own original formatting — is left untouched.

// member describes one member of a JSON object as found in the original
// source text: its key and the exact byte offsets of its value within that
// source.
type member struct {
	key        string
	keyStart   int // offset of the key's opening quote
	valueStart int // offset of the value's first byte
	valueEnd   int // offset one past the value's last byte
}

// positionedError is a settings-file problem this package's own scanning
// detects — not a json.SyntaxError the standard decoder itself raises —
// tagged with the byte offset describeJSONError needs to turn it into a
// line number (ERR-07: "abort with a message naming the file and line").
type positionedError struct {
	msg    string
	offset int
}

func (e *positionedError) Error() string { return e.msg }

// scanObject parses content (which must be a JSON object) and returns its
// top-level members in source order with byte offsets into content, plus
// the offsets of the object's own opening and closing braces. It refuses
// two things a plain json.Unmarshal would silently accept: the same
// top-level key listed twice (JSON syntax allows it, but which value is
// "real" is ambiguous — Go's own decoder just keeps the last one and says
// nothing), and anything after the object's closing '}' other than
// trailing whitespace.
func scanObject(content []byte) (members []member, braceOpen, braceClose int, err error) {
	dec := json.NewDecoder(bytes.NewReader(content))

	tok, err := dec.Token()
	if err != nil {
		return nil, 0, 0, err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return nil, 0, 0, fmt.Errorf("expected a JSON object, got %v", tok)
	}
	braceOpen = int(dec.InputOffset()) - 1

	seen := map[string]bool{}
	pos := int(dec.InputOffset())
	for dec.More() {
		keyStart := skipWhitespaceAndComma(content, pos)

		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, 0, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, 0, 0, fmt.Errorf("expected a string object key, got %v", keyTok)
		}
		if seen[key] {
			return nil, 0, 0, &positionedError{
				msg:    fmt.Sprintf("the settings file lists %q twice; fix it by hand", key),
				offset: keyStart,
			}
		}
		seen[key] = true
		valueStart := skipWhitespaceAndColon(content, int(dec.InputOffset()))

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, 0, fmt.Errorf("decoding value for key %q: %w", key, err)
		}
		valueEnd := int(dec.InputOffset())

		members = append(members, member{key: key, keyStart: keyStart, valueStart: valueStart, valueEnd: valueEnd})
		pos = valueEnd
	}

	closeTok, err := dec.Token()
	if err != nil {
		return nil, 0, 0, err
	}
	if d, ok := closeTok.(json.Delim); !ok || d != '}' {
		return nil, 0, 0, fmt.Errorf("expected a closing '}', got %v", closeTok)
	}
	braceClose = int(dec.InputOffset()) - 1

	// Nothing but whitespace may follow the object: the decoder must report
	// io.EOF for the next token, whether the leftover text is malformed
	// (a syntax error) or itself perfectly valid JSON (e.g. a second
	// top-level object concatenated after the first).
	if _, extraErr := dec.Token(); !errors.Is(extraErr, io.EOF) {
		return nil, 0, 0, &positionedError{
			msg:    "the settings file has content after its closing '}'; fix it by hand",
			offset: int(dec.InputOffset()),
		}
	}

	return members, braceOpen, braceClose, nil
}

func skipWhitespaceAndComma(b []byte, pos int) int {
	for pos < len(b) {
		switch b[pos] {
		case ' ', '\t', '\n', '\r', ',':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func skipWhitespaceAndColon(b []byte, pos int) int {
	for pos < len(b) {
		switch b[pos] {
		case ' ', '\t', '\n', '\r', ':':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func findMember(members []member, key string) *member {
	for i := range members {
		if members[i].key == key {
			return &members[i]
		}
	}
	return nil
}

// leadingIndentOf returns the whitespace-only text between the start of
// pos's line and pos itself, for reusing an existing member's indentation
// when appending a new one after it. Returns fallback if that text isn't
// pure whitespace (e.g. a compact object with more than one member on a
// line) or is empty.
func leadingIndentOf(content []byte, pos int, fallback string) string {
	lineStart := pos
	for lineStart > 0 && content[lineStart-1] != '\n' {
		lineStart--
	}
	prefix := content[lineStart:pos]
	if len(prefix) == 0 {
		return fallback
	}
	for _, b := range prefix {
		if b != ' ' && b != '\t' {
			return fallback
		}
	}
	return string(prefix)
}

// prettyJSONValue reformats compact JSON with indent, for embedding as one
// member's value inside an already-indented document: unlike prettyJSON,
// there is no trailing newline, and every line after the first (which
// starts wherever the caller places it, typically right after "key": ) is
// prefixed with indent — one level, matching where a top-level member's
// value sits.
func prettyJSONValue(compact []byte, indent string) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, indent, indent); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// spliceTopLevelKey returns content with its top-level member named key
// replaced by newValue (if newValue is non-nil) or removed entirely (if
// newValue is nil, and the key is present at all), touching only the bytes
// that change. members, braceOpen, and braceClose must come from
// scanObject(content).
func spliceTopLevelKey(content []byte, members []member, braceOpen, braceClose int, key string, newValue []byte, indent string) []byte {
	idx := -1
	for i, m := range members {
		if m.key == key {
			idx = i
			break
		}
	}

	var out bytes.Buffer
	switch {
	case newValue == nil && idx < 0:
		return content // nothing to remove

	case newValue == nil: // remove members[idx]
		m := members[idx]
		var start, end int
		switch {
		case len(members) == 1:
			// The only member: collapse to an empty object.
			start, end = braceOpen+1, braceClose
		case idx == 0:
			// First of several: remove through the next member's key start,
			// keeping the whitespace between '{' and where this member used
			// to begin (so the next member inherits its indentation).
			start, end = m.keyStart, members[idx+1].keyStart
		default:
			// Not first: remove from the end of the previous member's value
			// (taking the separating comma with it) through the end of this
			// member's value.
			start, end = members[idx-1].valueEnd, m.valueEnd
		}
		out.Write(content[:start])
		out.Write(content[end:])
		return out.Bytes()

	case idx >= 0: // replace the existing member's value in place
		m := members[idx]
		out.Write(content[:m.valueStart])
		out.Write(newValue)
		out.Write(content[m.valueEnd:])
		return out.Bytes()

	default: // idx < 0, newValue != nil: insert a brand new member
		if len(members) == 0 {
			out.Write(content[:braceOpen+1])
			out.WriteByte('\n')
			out.WriteString(indent)
			out.WriteString(`"`)
			out.WriteString(key)
			out.WriteString(`": `)
			out.Write(newValue)
			out.WriteByte('\n')
			out.Write(content[braceClose:])
			return out.Bytes()
		}
		last := members[len(members)-1]
		lineIndent := leadingIndentOf(content, last.keyStart, indent)
		out.Write(content[:last.valueEnd])
		out.WriteString(",\n")
		out.WriteString(lineIndent)
		out.WriteString(`"`)
		out.WriteString(key)
		out.WriteString(`": `)
		out.Write(newValue)
		out.Write(content[last.valueEnd:])
		return out.Bytes()
	}
}

// buildFromScratch builds a brand new settings document (used when the
// settings file doesn't exist yet, or is empty) containing just the
// "hooks" key this install wants, pretty-printed with indent. There is
// nothing else in the document yet, so there is nothing else to preserve.
func buildFromScratch(owner Owner, byEvent map[string][]Entry, eventOrder []string, indent string) ([]byte, error) {
	hooksObj := newOrderedRawObject()
	for _, event := range eventOrder {
		if err := mergeEvent(hooksObj, owner, event, byEvent[event]); err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", event, err)
		}
	}
	if hooksObj.Len() == 0 {
		return []byte("{}\n"), nil
	}

	hooksBytes, err := marshalNoEscape(hooksObj)
	if err != nil {
		return nil, fmt.Errorf("marshaling hooks: %w", err)
	}
	top := newOrderedRawObject()
	top.Set("hooks", hooksBytes)
	topBytes, err := marshalNoEscape(top)
	if err != nil {
		return nil, fmt.Errorf("marshaling settings: %w", err)
	}
	return prettyJSON(topBytes, indent)
}

// spliceHooksInto rebuilds settings content by touching only its "hooks"
// member (mergeEvent's install/uninstall logic decides that member's new
// content, unchanged from before): every other top-level key's bytes are
// preserved byte-for-byte, whatever formatting they were written with.
func spliceHooksInto(content []byte, owner Owner, byEvent map[string][]Entry, eventOrder []string, install bool, indent string) ([]byte, error) {
	members, braceOpen, braceClose, err := scanObject(content)
	if err != nil {
		return nil, describeJSONError(content, err)
	}

	hooksObj := newOrderedRawObject()
	if m := findMember(members, "hooks"); m != nil {
		hooksRaw := content[m.valueStart:m.valueEnd]
		if err := json.Unmarshal(hooksRaw, hooksObj); err != nil {
			return nil, fmt.Errorf("hooks: %w", describeJSONError(hooksRaw, err))
		}
	}

	if install {
		for _, event := range eventOrder {
			if err := mergeEvent(hooksObj, owner, event, byEvent[event]); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", event, err)
			}
		}
	} else {
		for _, event := range append([]string{}, hooksObj.keys...) {
			if err := mergeEvent(hooksObj, owner, event, nil); err != nil {
				return nil, fmt.Errorf("hooks.%s: %w", event, err)
			}
		}
	}

	var newHooksValue []byte // nil means "delete the hooks member"
	if hooksObj.Len() > 0 {
		hooksCompact, err := marshalNoEscape(hooksObj)
		if err != nil {
			return nil, fmt.Errorf("marshaling hooks: %w", err)
		}
		newHooksValue, err = prettyJSONValue(hooksCompact, indent)
		if err != nil {
			return nil, fmt.Errorf("formatting hooks: %w", err)
		}
	}

	return spliceTopLevelKey(content, members, braceOpen, braceClose, "hooks", newHooksValue, indent), nil
}
