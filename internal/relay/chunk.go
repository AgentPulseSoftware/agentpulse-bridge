package relay

import (
	"regexp"
	"strconv"
)

// MaxEventsPerChunk and MaxChunkBytes are RL-05's batch limits: at most 50
// events per request, at most 16 KB of encoded request body.
const (
	MaxEventsPerChunk = 50
	MaxChunkBytes     = 16 * 1024
)

// batchOverheadBytes is a conservative estimate of everything in the batch
// body besides the events themselves — `{"events":[],"watch_list_version":N}`
// plus commas between elements — used so nextChunk's running total stays a
// safe upper bound on the real encoded size without re-marshaling the
// whole envelope on every event.
const batchOverheadBytes = 48

// eventIndexPattern extracts the event index from a relay 400 body's
// "detail" field: the relay's request validation renders a per-event
// issue as "events.<N>.<field...>: message".
var eventIndexPattern = regexp.MustCompile(`^events\.(\d+)\.`)

// nextChunk builds one chunk from the front of events: as many events, in
// order, as fit within MaxEventsPerChunk and MaxChunkBytes, appending
// events until either limit would next be crossed (the byte bound binds
// first for large events). consumed is how many
// leading elements of events this call accounted for — always
// len(chunk)+len(oversized) — so the caller advances by exactly consumed
// regardless of whether chunk ended up empty.
//
// A chunk always gains at least one element when events is non-empty (an
// oversized leading event is consumed into oversized instead of chunk, a
// fittable one always starts a fresh chunk even if the running total
// estimate would otherwise call it too big for an *empty* chunk), so
// repeated calls always make progress and can never spin without
// advancing.
//
// oversized lists indices (into events) of any event that alone exceeds
// MaxChunkBytes and so can never be sent in any chunk; SPEC 10.1's closed,
// length-capped schema makes this impossible in practice, so it is handled
// defensively rather than expected to occur (the caller drops these
// permanently and logs their type only, never their content).
func nextChunk(events [][]byte) (chunk [][]byte, consumed int, oversized []int) {
	total := batchOverheadBytes
	for i, e := range events {
		if len(e) > MaxChunkBytes {
			if len(chunk) > 0 {
				// Stop the chunk here; this oversized event becomes the
				// very next call's leading element instead.
				break
			}
			oversized = append(oversized, i)
			consumed++
			continue
		}

		eventBytes := len(e) + 1 // +1 for the comma separating array elements
		if len(chunk) > 0 && (len(chunk) >= MaxEventsPerChunk || total+eventBytes > MaxChunkBytes) {
			break
		}
		chunk = append(chunk, e)
		total += eventBytes
		consumed++
	}
	return chunk, consumed, oversized
}

// parseEventIndex extracts the event index named by a 400's detail text,
// per eventIndexPattern. ok is false when detail doesn't name one.
func parseEventIndex(detail string) (index int, ok bool) {
	m := eventIndexPattern.FindStringSubmatch(detail)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
