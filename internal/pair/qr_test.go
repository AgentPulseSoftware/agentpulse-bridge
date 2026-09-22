package pair

import (
	"strings"
	"testing"

	"rsc.io/qr"
)

func TestPayload(t *testing.T) {
	if got, want := Payload("A1B2C3D4"), "agentpulse://pair?c=A1B2C3D4"; got != want {
		t.Errorf("Payload = %q, want %q", got, want)
	}
}

// TestRenderDecodesToPayload checks that the printed QR decodes to
// agentpulse://pair?c=<code>. The QR dependency ships no decoder, so the rendered half-block art is read back into a
// module grid and compared against the grid the same encoder produces for
// the expected payload: if a single module were wrong, misplaced, or
// dropped by the renderer, the grids would differ.
func TestRenderDecodesToPayload(t *testing.T) {
	const code = "7KMPQ2VZ"

	for _, invert := range []bool{false, true} {
		art, err := Render(Payload(code), invert)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got := parseHalfBlocks(t, art, invert)

		want, err := qr.Encode(Payload(code), qr.M)
		if err != nil {
			t.Fatalf("qr.Encode: %v", err)
		}
		if len(got) < want.Size+2*quietZone {
			t.Fatalf("rendered %d rows, want at least %d", len(got), want.Size+2*quietZone)
		}
		for y := 0; y < want.Size; y++ {
			for x := 0; x < want.Size; x++ {
				if got[y+quietZone][x+quietZone] != want.Black(x, y) {
					t.Fatalf("module (%d,%d) = %v, want %v (invert=%v)", x, y, got[y+quietZone][x+quietZone], want.Black(x, y), invert)
				}
			}
		}
		// The quiet zone has to be light, or a scanner will not find the
		// symbol at all.
		for x := range got[0] {
			if got[0][x] {
				t.Fatalf("the top quiet-zone row has a dark module at x=%d (invert=%v)", x, invert)
			}
		}
	}
}

func TestRenderUsesOnlyTheAllowedGlyphs(t *testing.T) {
	art, err := Render(Payload("7KMPQ2VZ"), false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, r := range art {
		switch r {
		case bothDark, upperDark, lowerDark, bothLight, '\n':
		default:
			t.Fatalf("rendered QR contains %q, want only the half-block glyphs, a space, or a newline", r)
		}
	}
	if lines := strings.Count(art, "\n"); lines == 0 {
		t.Fatal("rendered QR has no lines")
	}
}

// parseHalfBlocks turns the rendered art back into a module grid, undoing
// the inversion so the result is always "true means a dark module of the
// symbol itself".
func parseHalfBlocks(t *testing.T, art string, invert bool) [][]bool {
	t.Helper()
	var grid [][]bool
	for _, line := range strings.Split(strings.TrimRight(art, "\n"), "\n") {
		var upper, lower []bool
		for _, r := range line {
			switch r {
			case bothDark:
				upper, lower = append(upper, true), append(lower, true)
			case upperDark:
				upper, lower = append(upper, true), append(lower, false)
			case lowerDark:
				upper, lower = append(upper, false), append(lower, true)
			case bothLight:
				upper, lower = append(upper, false), append(lower, false)
			default:
				t.Fatalf("unexpected glyph %q in rendered QR", r)
			}
		}
		if invert {
			flip(upper)
			flip(lower)
		}
		grid = append(grid, upper, lower)
	}
	return grid
}

func flip(row []bool) {
	for i := range row {
		row[i] = !row[i]
	}
}
