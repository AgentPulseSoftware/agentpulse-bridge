package classify

import (
	"crypto/rand"
	"time"
)

// crockfordAlphabet is Crockford's Base32 alphabet (excludes I, L, O, U to
// avoid confusion with 1, 1, 0, V), the alphabet ULID uses and the schema's
// event_id pattern (`^[0-9A-HJKMNP-TV-Z]{26}$`) requires.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID generates a ULID (Universally Unique Lexicographically Sortable
// Identifier) for t: a 48-bit millisecond timestamp followed by 80 bits of
// randomness, Crockford Base32 encoded to exactly 26 characters. Generated
// in-package, with no external dependency. A crypto/rand failure — not
// observed in practice on any platform this binary ships for — falls back
// to all-zero entropy rather than returning an error: event_id is a
// required field on every event, and BR-02 requires "agentpulse hook" to
// never fail, so a (still-valid, just less random) id beats none.
func newULID(t time.Time) string {
	ms := uint64(t.UnixMilli()) //nolint:gosec // deliberately truncated to 48 bits below, matching the ULID spec
	var entropy [10]byte
	_, _ = rand.Read(entropy[:])

	var id [16]byte
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	copy(id[6:], entropy[:])

	return encodeULID(id)
}

// encodeULID packs 128 bits into 26 Crockford Base32 characters (5 bits
// each; the first character carries only 3 meaningful bits, since
// 26*5 = 130 doesn't divide evenly into 128). This is the standard ULID
// bit-packing layout.
func encodeULID(id [16]byte) string {
	var out [26]byte
	out[0] = crockfordAlphabet[(id[0]&224)>>5]
	out[1] = crockfordAlphabet[id[0]&31]
	out[2] = crockfordAlphabet[(id[1]&248)>>3]
	out[3] = crockfordAlphabet[((id[1]&7)<<2)|((id[2]&192)>>6)]
	out[4] = crockfordAlphabet[(id[2]&62)>>1]
	out[5] = crockfordAlphabet[((id[2]&1)<<4)|((id[3]&240)>>4)]
	out[6] = crockfordAlphabet[((id[3]&15)<<1)|((id[4]&128)>>7)]
	out[7] = crockfordAlphabet[(id[4]&124)>>2]
	out[8] = crockfordAlphabet[((id[4]&3)<<3)|((id[5]&224)>>5)]
	out[9] = crockfordAlphabet[id[5]&31]
	out[10] = crockfordAlphabet[(id[6]&248)>>3]
	out[11] = crockfordAlphabet[((id[6]&7)<<2)|((id[7]&192)>>6)]
	out[12] = crockfordAlphabet[(id[7]&62)>>1]
	out[13] = crockfordAlphabet[((id[7]&1)<<4)|((id[8]&240)>>4)]
	out[14] = crockfordAlphabet[((id[8]&15)<<1)|((id[9]&128)>>7)]
	out[15] = crockfordAlphabet[(id[9]&124)>>2]
	out[16] = crockfordAlphabet[((id[9]&3)<<3)|((id[10]&224)>>5)]
	out[17] = crockfordAlphabet[id[10]&31]
	out[18] = crockfordAlphabet[(id[11]&248)>>3]
	out[19] = crockfordAlphabet[((id[11]&7)<<2)|((id[12]&192)>>6)]
	out[20] = crockfordAlphabet[(id[12]&62)>>1]
	out[21] = crockfordAlphabet[((id[12]&1)<<4)|((id[13]&240)>>4)]
	out[22] = crockfordAlphabet[((id[13]&15)<<1)|((id[14]&128)>>7)]
	out[23] = crockfordAlphabet[(id[14]&124)>>2]
	out[24] = crockfordAlphabet[((id[14]&3)<<3)|((id[15]&224)>>5)]
	out[25] = crockfordAlphabet[id[15]&31]
	return string(out[:])
}
