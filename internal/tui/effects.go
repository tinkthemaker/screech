package tui

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// --- decrypt: the tuning set piece ---
// The old station name dissolves into noise glyphs that resolve left-to-right
// into the new name. Classic demoscene text-decrypt.

type Decrypt struct {
	target []rune
	start  time.Time
	dur    time.Duration
}

func NewDecrypt(target string, now time.Time) Decrypt {
	return Decrypt{target: []rune(target), start: now, dur: 800 * time.Millisecond}
}

func (d Decrypt) Active(now time.Time) bool {
	return len(d.target) > 0 && now.Sub(d.start) < d.dur
}

func (d Decrypt) Render(now time.Time, t Theme, maxCols int) string {
	if len(d.target) == 0 || maxCols < 1 {
		return ""
	}
	elapsed := now.Sub(d.start)
	frac := float64(elapsed) / float64(d.dur)
	if frac > 1 {
		frac = 1
	}
	resolved := int(frac * float64(len(d.target)+1))
	frame := int(elapsed / (50 * time.Millisecond))
	set := t.G.Decrypt
	var b strings.Builder
	cols := 0
	for i, r := range d.target {
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
			g := set[(i*31+frame*17)%len(set)]
			b.WriteString(t.Dim.Render(string(g)))
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

// --- marquee: long-title etiquette ---
// Pause 2s, scroll once across, pause 2s, snap back. No endless loops.

func marquee(text string, width int, now, anchor time.Time, ellipsis string) string {
	tw := runewidth.StringWidth(text)
	if tw <= width || width < 4 {
		return runewidth.Truncate(text, width, ellipsis)
	}
	overflow := tw - width
	const pause = 2 * time.Second
	perCol := 140 * time.Millisecond
	cycle := pause + time.Duration(overflow)*perCol + pause
	pos := now.Sub(anchor) % cycle
	if pos < 0 {
		pos = 0
	}
	var offset int
	switch {
	case pos < pause:
		offset = 0
	case pos < pause+time.Duration(overflow)*perCol:
		offset = int((pos - pause) / perCol)
	default:
		offset = overflow
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
