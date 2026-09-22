package classify

import (
	"testing"
	"time"
)

// TestSchemaSmokeTest is a quick sanity check on the schema-validation
// plumbing itself (loadEventSchema/assertValidEvent), independent of the
// classifier: a hand-built valid event must pass, and a hand-built invalid
// one must fail. The real classifier behavior tables (classify_test.go)
// rely on this plumbing actually catching mismatches.
func TestSchemaSmokeTest(t *testing.T) {
	sch := loadEventSchema(t)

	valid := &Event{
		Schema:    1,
		EventID:   newULID(time.Now()),
		BridgeID:  "brg_abc123",
		SessionID: "sess_1",
		Project:   Project{KeyHash: hashPath("k"), Name: "notesapp"},
		TS:        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Type:      TypeStop,
		Counters:  Counters{},
		Payload:   EmptyPayload{},
	}
	assertValidEvent(t, sch, valid)
}

func TestSchemaSmokeTestCatchesInvalidEvent(t *testing.T) {
	sch := loadEventSchema(t)
	if sch == nil {
		t.Skipf("%s is not set: no event.v1 schema to validate against", eventSchemaEnv)
	}
	invalid := &Event{
		Schema:    2, // wrong: must be const 1
		EventID:   "not-a-ulid",
		BridgeID:  "brg_abc123",
		SessionID: "sess_1",
		Project:   Project{KeyHash: hashPath("k"), Name: "notesapp"},
		TS:        time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Type:      TypeStop,
		Counters:  Counters{},
		Payload:   EmptyPayload{},
	}
	if err := sch.Validate(mustDecodeForSchema(t, invalid)); err == nil {
		t.Error("expected schema validation to fail for an event with schema=2 and a bad event_id, but it passed")
	}
}
