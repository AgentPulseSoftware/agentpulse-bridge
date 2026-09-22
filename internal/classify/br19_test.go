package classify

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// forbiddenFieldNames are exactly the values BR-19 and CLAUDE.md
// name: "the bridge never sends tool_input, tool_response, prompt, cwd, or
// transcript_path values", enforced (per BR-19) "by the event type
// definitions (section 10.1), which have no fields that could carry them".
//
// TestEventStructHasNoForbiddenFields below reflects over the payload
// types eventAndPayloadTypes() names and checks each field's name and json
// tag against this list — a "grep your own event struct in a test" proof,
// run against today's known types. Read narrowly, that is
// what it proves: it catches one of these names on a type this file
// already lists, not on a payload type someone adds later and forgets to
// list here (eventAndPayloadTypes is a plain Go slice literal, not
// something Classify or event.go itself iterates, so there is nothing that
// would force the list to stay in sync).
//
// The actual comprehensive guarantee against a forbidden field reaching
// the wire doesn't depend on this list staying complete: every event this
// package can build is validated in classify_test.go (assertValidEvent)
// against the AgentPulse `event.v1` schema, and that schema sets
// additionalProperties: false on every variant — a stray field on any
// event actually emitted and tested fails that validation regardless of
// whether its Go type is named in eventAndPayloadTypes below.
var forbiddenFieldNames = []string{
	"cwd", "prompt", "tool_input", "tool_response", "transcript_path",
}

// eventAndPayloadTypes lists Event and every payload type defined in
// event.go, as of this writing. It is a plain literal, not derived from
// anything the classifier itself registers or iterates — see the
// commentary on forbiddenFieldNames above for what that does and doesn't
// prove. Kept as an explicit list so it at least fails loudly on the types
// it does know about, rather than silently iterating zero of them.
func eventAndPayloadTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeOf(Event{}),
		reflect.TypeOf(Project{}),
		reflect.TypeOf(Counters{}),
		reflect.TypeOf(SessionStartPayload{}),
		reflect.TypeOf(PromptSubmittedPayload{}),
		reflect.TypeOf(ActivityPayload{}),
		reflect.TypeOf(VerificationStartedPayload{}),
		reflect.TypeOf(VerificationFinishedPayload{}),
		reflect.TypeOf(NeedsInputPayload{}),
		reflect.TypeOf(EmptyPayload{}),
		reflect.TypeOf(PRCreatedPayload{}),
		reflect.TypeOf(SessionEndPayload{}),
	}
}

// TestEventStructHasNoForbiddenFields greps the event struct in a test to
// prove no forbidden field exists. It checks both the Go field name and
// the JSON tag, on Event and every payload type eventAndPayloadTypes()
// names, against BR-19's forbidden list. See that function's comment for
// the scope of what this specific test does and doesn't guarantee; the schema
// validation every emitted event goes through in classify_test.go is the
// guarantee that doesn't depend on this list staying in sync.
func TestEventStructHasNoForbiddenFields(t *testing.T) {
	for _, typ := range eventAndPayloadTypes() {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			lowerName := strings.ToLower(f.Name)
			tag := f.Tag.Get("json")
			tagName, _, _ := strings.Cut(tag, ",")
			for _, forbidden := range forbiddenFieldNames {
				if lowerName == strings.ReplaceAll(forbidden, "_", "") || tagName == forbidden {
					t.Errorf("%s.%s: forbidden field name %q (BR-19: never send cwd, prompt, tool_input, tool_response, or transcript_path)",
						typ.Name(), f.Name, forbidden)
				}
			}
		}
	}
}

// TestEventSchemaFileItselfForbidsTheseFields is a companion check: it
// greps the schema's raw text (not just this package's Go structs) for
// the same forbidden names, so a schema edit that reintroduces one of
// them (outside this package entirely) is caught too. It runs only when a
// copy of event.v1 is configured; see schema_test.go.
func TestEventSchemaFileItselfForbidsTheseFields(t *testing.T) {
	path := eventSchemaPath()
	if path == "" {
		t.Skipf("%s is not set: no event.v1 schema to grep", eventSchemaEnv)
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path to the published schema
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	text := string(data)
	for _, forbidden := range forbiddenFieldNames {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Errorf("%s contains a %q property; BR-19 forbids it from the event schema", path, forbidden)
		}
	}
}
