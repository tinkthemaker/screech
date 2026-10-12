package tui

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// --- decrypt: the tuning set piece ---
// The old station name dissolves into noise glyphs that resolve left-to-right
// into the new name. Classic demoscene text-decrypt.
//
// The struct holds only the clock: Render takes the target text so the
// ceremony always resolves into exactly what the settled frame draws. The
// hero used to decrypt toward a fixed-width render while the receiver
// letterspaced the final name, so every tune ended with a visible snap.

type Decrypt struct {
	start time.Time
	dur   time.Duration
}

func NewDecrypt(now time.Time) Decrypt {
	return Decrypt{start: now, dur: 800 * time.Millisecond}
}

// Active reports whether the scramble is still running. A zero Decrypt is
// never active, so reduced motion can leave the field unset and the settled
// text renders directly.
func (d Decrypt) Active(now time.Time) bool {
	return !d.start.IsZero() && now.Sub(d.start) < d.dur
}

func (d Decrypt) Render(now time.Time, t Theme, target string, maxCols int) string {
	if maxCols < 1 {
		return ""
	}
	elapsed := now.Sub(d.start)
	frac := float64(elapsed) / float64(d.dur)
	if frac > 1 {
		frac = 1
	}
	runes := []rune(target)
	resolved := int(frac * float64(len(runes)+1))
	frame := int(elapsed / (50 * time.Millisecond))
	set := t.G.Decrypt
	var b strings.Builder
	cols := 0
	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if cols+rw > maxCols {
			break
		}
		cols += rw
		switch {
		case i < resolved:
			b.WriteString(t.Bright.Render(string(r)))
		case r == ' ':
			b.WriteRune(' ')
		default:
			// A two-column rune gets two one-column scramble glyphs: the
			// row must measure the same mid-effect as settled, or the
			// plate jitters as each CJK rune resolves.
			g := set[(i*31+frame*17)%len(set)]
			for c := 0; c < rw; c++ {
				if c > 0 {
					g = set[(i*31+frame*17+c)%len(set)]
				}
				b.WriteString(t.Dim.Render(string(g)))
			}
		}
	}
	return b.String()
}

// --- typewriter: the reason line types itself in ---

type Typewriter struct {
	text  []rune
	start time.Time
}

func NewTypewriter(text string, now time.Time) Typewriter {
	return Typewriter{text: []rune(text), start: now}
}

func (tw Typewriter) Render(now time.Time) string {
	if len(tw.text) == 0 {
		return ""
	}
	n := int(now.Sub(tw.start) / (20 * time.Millisecond))
	if n < 0 {
		n = 0
	}
	if n > len(tw.text) {
		n = len(tw.text)
	}
	return string(tw.text[:n])
}

// RenderOver types an arbitrary text on the typewriter's clock. The model
// stores the reason token in tw, but the views render its human detail —
// RenderOver lets any string share the same start time. Before the clock
// starts (the deliberate post-pick delay) nothing shows; once finished, the
// full text holds.
func (tw Typewriter) RenderOver(now time.Time, text string) string {
	dt := now.Sub(tw.start)
	if dt <= 0 {
		return ""
	}
	n := int(dt / (20 * time.Millisecond))
	runes := []rune(text)
	if n > len(runes) {
		n = len(runes)
	}
	return string(runes[:n])
}

// --- marquee: long-title etiquette ---
// Pause 2s, scroll once across, pause 2s, then rest on the truncated head.
// No endless loops: a title that keeps crawling reads as broken, and the
// ellipsis is the honest answer once the single pass has run.

func marquee(text string, width int, now, anchor time.Time, ellipsis string) string {
	tw := runewidth.StringWidth(text)
	if tw <= width || width < 4 {
		return runewidth.Truncate(text, width, ellipsis)
	}
	overflow := tw - width
	const pause = 2 * time.Second
	perCol := 140 * time.Millisecond
	elapsed := now.Sub(anchor)
	if elapsed < 0 {
		elapsed = 0
	}
	// Past one full cycle the scroll is done: the title rests on its
	// ellipsis-truncated head rather than snapping back or looping.
	if elapsed >= pause+time.Duration(overflow)*perCol+pause {
		return runewidth.Truncate(text, width, ellipsis)
	}
	var offset int
	switch {
	case elapsed < pause:
		offset = 0
	default:
		// The end pause holds the tail: without the clamp, elapsed time
		// keeps marching the offset past overflow and the title shrinks
		// off the left edge for the closing two seconds.
		offset = minI(int((elapsed-pause)/perCol), overflow)
	}
	return sliceCols(text, offset, width)
}

// sliceCols returns the substring covering [startCol, startCol+width) in
// display columns, padding around wide runes.
func sliceCols(s string, startCol, width int) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if col+rw <= startCol {
			col += rw
			continue
		}
		if col < startCol { // wide rune straddles the start boundary
			b.WriteRune(' ')
			col += rw
			continue
		}
		if col+rw > startCol+width {
			break
		}
		b.WriteRune(r)
		col += rw
	}
	return b.String()
}

// --- static: the tuning ceremony's noise ---
// staticHash is a deterministic integer hash for the static collapse: same
// cell + frame, same glyph, with no math/rand state to seed or race.

func staticHash(x, row, frame int) uint32 {
	h := uint32(x)*374761393 + uint32(row)*668265263 + uint32(frame)*2246822519
	h = (h ^ (h >> 13)) * 1274126177
	return h ^ (h >> 16)
}

// --- spring: dial physics ---
// Small critically-underdamped spring; overshoot and settle. Hand-rolled to
// keep the dependency list at four.

type Spring struct {
	Pos, Vel float64
	freq     float64 // angular frequency
	damp     float64 // damping ratio <1 = overshoot
}

func NewSpring(pos float64) *Spring {
	return &Spring{Pos: pos, freq: 9.0, damp: 0.55}
}

func (s *Spring) Step(target, dt float64) {
	// Semi-implicit Euler on a damped harmonic oscillator.
	f := s.freq
	accel := -2*s.damp*f*s.Vel - f*f*(s.Pos-target)
	s.Vel += accel * dt
	s.Pos += s.Vel * dt
}

func (s *Spring) Settled(target float64) bool {
	return absF(s.Pos-target) < 0.001 && absF(s.Vel) < 0.005
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
