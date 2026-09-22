// Package pair implements the parts of SPEC 10.3's pairing protocol that
// are worth testing on their own: the QR payload and its terminal
// rendering, the machine name sent to the relay, and the poll loop that
// waits for the phone to redeem the code. The command wiring —
// credentials, configuration, and the Claude Code settings merge — lives
// in cmd/agentpulse/pair.go.
package pair

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// Payload is what the QR code encodes (SPEC 10.3 step 2). The phone's
// scanner hands this URL to the app, which reads the code out of it.
func Payload(code string) string {
	return "agentpulse://pair?c=" + code
}

// quietZone is the four-module light border the QR specification requires
// around a symbol for a scanner to find it.
const quietZone = 4

// The half-block glyphs BR-07 asks for. Each output character carries two
// vertically adjacent modules, so the printed code is half as tall as it is
// wide in character cells — which, with a terminal cell being about twice
// as tall as it is wide, comes out square on screen.
const (
	bothDark  = '█'
	upperDark = '▀'
	lowerDark = '▄'
	bothLight = ' '
)

// Render encodes payload as a QR code and draws it with half-block
// characters, one line per two module rows, including the quiet zone.
//
// Dark modules are drawn as glyphs and light modules as spaces, which is
// the right way round on a light terminal background. On the dark
// background most terminals use, the code comes out light-on-dark;
// iOS's scanner reads that inverted form, but "agentpulse pair --qr-invert"
// flips it for a scanner that refuses to.
func Render(payload string, invert bool) (string, error) {
	// qr.M is the medium error-correction level (about 15% of the symbol
	// can be obscured and still decode): enough to survive a fingerprint
	// on the screen or a slightly out-of-focus camera, without making the
	// symbol so large that it stops fitting an 80-column terminal.
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return "", fmt.Errorf("encoding the pairing QR code: %w", err)
	}

	side := code.Size + 2*quietZone
	dark := func(x, y int) bool {
		if x < quietZone || y < quietZone || x >= code.Size+quietZone || y >= code.Size+quietZone {
			return invert // the quiet zone is light, unless everything is inverted
		}
		return code.Black(x-quietZone, y-quietZone) != invert
	}

	var out strings.Builder
	for y := 0; y < side; y += 2 {
		for x := 0; x < side; x++ {
			upper := dark(x, y)
			lower := y+1 < side && dark(x, y+1)
			switch {
			case upper && lower:
				out.WriteRune(bothDark)
			case upper:
				out.WriteRune(upperDark)
			case lower:
				out.WriteRune(lowerDark)
			default:
				out.WriteRune(bothLight)
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}
