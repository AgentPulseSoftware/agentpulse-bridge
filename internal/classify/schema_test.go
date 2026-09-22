package classify

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// eventSchemaEnv names the environment variable that points these tests
// at a copy of the AgentPulse `event.v1` JSON Schema — the closed schema
// SPEC 10.1 defines and the relay enforces on every request. The schema
// is published with the AgentPulse relay contract and is deliberately not
// vendored into this repository: one schema, one source of truth.
//
// When the variable is unset the schema-validation assertions are skipped
// and every other assertion in the surrounding test still runs; the
// relay's own validation remains the enforcing gate either way. Set it to
// a local path to run them:
//
//	AGENTPULSE_EVENT_SCHEMA=/path/to/event.v1.json go test ./internal/classify
//
// The go.mod/go.sum entries for github.com/santhosh-tekuri/jsonschema/v5
// are a test-only dependency: nothing outside _test.go files imports it.
const eventSchemaEnv = "AGENTPULSE_EVENT_SCHEMA"

// eventSchemaPath returns the configured schema path, or "" when none is
// configured.
func eventSchemaPath() string {
	return os.Getenv(eventSchemaEnv)
}

// loadEventSchema compiles the configured event.v1 schema, or returns nil
// when no schema is configured. A path that is set but unusable is a
// failure, not a skip: an operator who asked for validation must not get
// silence instead.
func loadEventSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := eventSchemaPath()
	if path == "" {
		return nil
	}
	sch, err := jsonschema.Compile(path)
	if err != nil {
		t.Fatalf("compiling %s (from %s): %v", path, eventSchemaEnv, err)
	}
	return sch
}

// TestEventSchemaValidationIsConfigured reports, as a visible skip rather
// than as silence, whether this run validated events against event.v1.
func TestEventSchemaValidationIsConfigured(t *testing.T) {
	if eventSchemaPath() == "" {
		t.Skipf("%s is not set: event.v1 schema validation did not run in this suite", eventSchemaEnv)
	}
}

// mustDecodeForSchema marshals ev and re-decodes it with json.UseNumber(),
// which the library's own Validate doc comment recommends: the schema's
// `schema` field uses `"const": 1`, and decoding numbers as json.Number
// rather than float64 avoids any precision ambiguity.
func mustDecodeForSchema(t *testing.T, ev *Event) any {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshaling event: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decoding marshaled event for schema validation: %v", err)
	}
	return v
}

// assertValidEvent marshals ev and validates it against the event.v1
// schema, failing the test with the schema's own validation error on any
// mismatch. With no schema configured (sch == nil) the validation is
// skipped and the caller's other assertions still run.
func assertValidEvent(t *testing.T, sch *jsonschema.Schema, ev *Event) {
	t.Helper()
	if ev == nil {
		t.Fatal("assertValidEvent called with a nil event")
	}
	if sch == nil {
		return
	}
	v := mustDecodeForSchema(t, ev)
	if err := sch.Validate(v); err != nil {
		data, _ := json.Marshal(ev)
		t.Errorf("event failed schema validation: %v\nevent JSON: %s", err, data)
	}
}
