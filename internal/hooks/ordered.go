package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// orderedRawObject is a JSON object that remembers the order its keys were
// read in and keeps every value as the exact raw bytes it was written with.
//
// encoding/json's normal map[string]T does not preserve key order (Go sorts
// map keys when marshaling), and unmarshaling into a struct or map discards
// the original formatting of every value. Since BR-07 requires that unrelated
// settings ("without disturbing any other content or other hooks") survive a
// merge unchanged, this type is the ordered-map workaround: keys keep
// their original order, and any value we do not
// explicitly rewrite is carried through as the exact bytes it started as
// (json.RawMessage preserves the original substring, whitespace included).
// Formatting is only "as far as encoding/json allows": once a value is
// rewritten (because we changed it), it is re-serialized in our own
// canonical style rather than the operator's original spacing.
type orderedRawObject struct {
	keys []string
	vals map[string]json.RawMessage
}

func newOrderedRawObject() *orderedRawObject {
	return &orderedRawObject{vals: map[string]json.RawMessage{}}
}

// Get returns the raw value for key and whether it was present.
func (o *orderedRawObject) Get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set inserts or replaces key's value. New keys are appended to the end,
// preserving the order of everything already present.
func (o *orderedRawObject) Set(key string, value json.RawMessage) {
	if _, exists := o.vals[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// Delete removes key if present.
func (o *orderedRawObject) Delete(key string) {
	if _, exists := o.vals[key]; !exists {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Len reports how many keys are present.
func (o *orderedRawObject) Len() int {
	return len(o.keys)
}

// UnmarshalJSON implements json.Unmarshaler, decoding token by token so the
// original key order is preserved and every value is captured as raw bytes.
func (o *orderedRawObject) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("expected a JSON object, got %v", tok)
	}

	o.keys = nil
	o.vals = map[string]json.RawMessage{}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected a string object key, got %v", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return fmt.Errorf("decoding value for key %q: %w", key, err)
		}
		o.Set(key, raw)
	}

	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// MarshalJSON implements json.Marshaler, writing keys back in their
// recorded order.
func (o *orderedRawObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		v := o.vals[k]
		if len(v) == 0 {
			buf.WriteString("null")
		} else {
			buf.Write(v)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
