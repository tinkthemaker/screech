package core

import "strings"

// CleanText makes a string safe to render on a fixed-height terminal.
// Everything screech displays that comes off the network — radio-browser
// station metadata anyone can submit, ICY titles from arbitrary stream
// servers — passes through here: terminal escapes (OSC 52 clipboard writes,
// title sets, CSI) are consumed wholesale rather than left as stray
// payload text, and layout breakers (newlines, bidi overrides) are
// flattened, so hostile or sloppy input can't scribble on the screen or
// reorder its neighbors. Everything else — emoji, ZWJ sequences, CJK —
// is kept.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b || r == 0x9b || r == 0x9d:
			i = skipEscape(rs, i)
		case r == '\t' || r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029:
			b.WriteRune(' ')
		case r < ' ' || r == 0x7f:
			// C0 controls and DEL: dropped entirely.
		case r >= 0x80 && r <= 0x9f:
			// Other C1 controls.
		case r == 0x200e || r == 0x200f ||
			(r >= 0x202a && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069):
			// Bidi controls: a station name may not reorder the frame.
		default:
			b.WriteRune(r)
		}
	}
	// Fields also trims: whitespace runs collapse to single spaces.
	return strings.Join(strings.Fields(b.String()), " ")
}

// skipEscape consumes a control sequence beginning at rune index i — ESC in
// its C0 form or the single-byte C1 CSI (0x9b) / OSC (0x9d) — and returns
// the index of its last byte. A sequence that never terminates simply runs
// to the end of input.
func skipEscape(rs []rune, i int) int {
	if rs[i] == 0x1b {
		if i+1 >= len(rs) {
			return i
		}
		switch rs[i+1] {
		case '[':
			i++ // CSI handled below with rs[i] == '['
		case ']', 'P', 'X', '^', '_':
			return skipToStringEnd(rs, i+1)
		default:
			return i + 1 // two-byte sequences: ESC ( B and friends
		}
	}
	if rs[i] == 0x9d {
		return skipToStringEnd(rs, i)
	}
	// CSI, as ESC [ ... or a lone 0x9b: parameter/intermediate bytes then
	// a final byte in 0x40–0x7e.
	for i+1 < len(rs) {
		i++
		if rs[i] >= 0x40 && rs[i] <= 0x7e {
			return i
		}
	}
	return i
}

// skipToStringEnd consumes an OSC-family string sequence (OSC, DCS, SOS,
// PM, APC) starting at its introducer: it ends at BEL, the C1 ST (0x9c), or
// the two-byte ST (ESC \), whichever comes first.
func skipToStringEnd(rs []rune, i int) int {
	for i+1 < len(rs) {
		i++
		if rs[i] == 0x07 || rs[i] == 0x9c {
			return i
		}
		if rs[i] == 0x1b && i+1 < len(rs) && rs[i+1] == '\\' {
			return i + 1
		}
	}
	return i
}
