package tui

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tinkthemaker/screech/internal/session"
)

// dialRows is the station-memory dial: a listening-density strip, the band
// with its needle and preset ticks, and the slot digits under the ticks
// they belong to.
//
// It used to be one row of band glyphs. That was defensible while the
// signal meter beside it was also one row, but the meter is six rows now
// and a hairline underneath it read as a placeholder. The digits are not
// decoration either: preset ticks were anonymous, so the dial could tell
// you a saved station lived at that position but never which one.
//
// The strip replaced a row of evenly spaced graduations. Graduations imply
// a quantity along the axis, and there isn't one — dial position is a hash
// of the station UUID, so a mark at 30% measured nothing while sitting
// directly above ticks that measured something real. Listening time is a
// genuine distribution over that same axis, so the row now carries it.
func (m Model) dialRows(width int, idle bool, th Theme) []string {
	if width < 1 {
		return []string{"", ""}
	}
	rows := make([]string, 0, 3)
	// No history yet means no distribution. Dropping the row is better than
	// drawing an empty one: the strip appears when it has something to say.
	if strip := m.dialDensityRow(width, th); strip != "" {
		rows = append(rows, strip)
	}
	return append(rows,
		m.bandRowTheme(width, idle, th),
		m.dialDigits(width, th),
	)
}

// buildDialDensity buckets cumulative listen time onto the dial axis and
// normalizes it to its own peak. The result is a shape, not a measurement:
// it answers "where do I actually live on this band" rather than "how many
// hours", which is what the readouts elsewhere are for.
func buildDialDensity(totals map[string]time.Duration, width int) []float64 {
	if width < 1 || len(totals) == 0 {
		return nil
	}
	raw := make([]float64, width)
	for uuid, d := range totals {
		if d <= 0 {
			continue
		}
		col := int(math.Round(stationDialPos(uuid) * float64(width-1)))
		if col >= 0 && col < width {
			raw[col] += d.Seconds()
		}
	}
	// A light blur either side. Without it a handful of stations render as
	// isolated single-cell spikes, which reads as noise rather than as a
	// distribution.
	out := make([]float64, width)
	peak := 0.0
	for i := range raw {
		out[i] = raw[i]
		if i > 0 {
			out[i] += 0.5 * raw[i-1]
		}
		if i < width-1 {
			out[i] += 0.5 * raw[i+1]
		}
		if out[i] > peak {
			peak = out[i]
		}
	}
	if peak <= 0 {
		return nil
	}
	for i := range out {
		out[i] /= peak
	}
	return out
}

// densityTopBlock caps the strip's tallest bar below a full cell. A run of
// full blocks is a solid slab, which is what made the strip read as a
// rendering fault rather than as a reading.
const densityTopBlock = 5

// dialDensityRow renders the strip as a one-row histogram: bar height
// carries the value, in a single quiet ember.
//
// Shade blocks were tried first, on the theory that ░▒▓ is literally a
// density scale. They failed for two reasons that only show up on a real
// screen. Fill density has no baseline, so adjacent heavy cells merge into
// one solid rectangle instead of reading as separate values; and the top of
// the scale measured 5.08 contrast against the panel, near the band line's
// own 5.28, so the busiest region of a background strip outweighed the
// instrument it sits under. Height against a common baseline reads as data
// at any density, and one colour keeps the strip subordinate.
func (m Model) dialDensityRow(width int, th Theme) string {
	d := m.density
	if len(d) != width {
		// Width changed since the last rebuild (a resize mid-frame). Skip
		// rather than render a strip that doesn't match the band below it.
		return ""
	}
	blocks := th.G.Blocks
	top := minI(densityTopBlock, len(blocks)-1)
	style := th.RampFor(1, 7)
	var b strings.Builder
	for _, v := range d {
		if v < 0.06 {
			b.WriteString(th.Dim.Render(" "))
			continue
		}
		lvl := int(math.Round(v * float64(top)))
		if lvl < 1 {
			lvl = 1
		}
		b.WriteString(style.Render(string(blocks[lvl])))
	}
	return b.String()
}

// dialDigits labels each preset tick with its slot number. The station
// currently playing takes the accent; the rest sit at ember, the same
// relationship the ticks themselves have on the band.
func (m Model) dialDigits(width int, th Theme) string {
	cells := make([]string, width)
	blank := th.Dim.Render(" ")
	for i := range cells {
		cells[i] = blank
	}
	current := m.s.CurrentSlot()
	slots := make([]int, 0, len(m.s.Presets))
	for slot := range m.s.Presets {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	// Dial positions are hashed, so two presets can land on neighbouring
	// columns and their digits then read as one number — a 1 beside a 7 is
	// "17", not two presets. Each digit therefore claims a cell with at
	// least one blank either side, searching outward for room. Low slots go
	// first so the one nearest its true position is the one you'd press.
	taken := make([]bool, width)
	free := func(c int) bool {
		if c < 0 || c >= width {
			return false
		}
		for d := -1; d <= 1; d++ {
			if n := c + d; n >= 0 && n < width && taken[n] {
				return false
			}
		}
		return true
	}
	for _, slot := range slots {
		want := int(math.Round(stationDialPos(m.s.Presets[slot]) * float64(width-1)))
		col := -1
		for off := 0; off < width && col < 0; off++ {
			switch {
			case free(want - off):
				col = want - off
			case free(want + off):
				col = want + off
			}
		}
		if col < 0 {
			continue // no room left on the scale; the tick still shows
		}
		taken[col] = true
		style := th.AccentDim
		if slot == current {
			style = th.Accent.Bold(true)
		}
		cells[col] = style.Render(strconv.Itoa(slot))
	}
	return strings.Join(cells, "")
}

func (m Model) bandRowTheme(iw int, idle bool, th Theme) string {
	pos := clampF(m.dial.Pos, 0, 1)
	col := int(math.Round(pos * float64(iw-1)))
	markerStyle := th.Accent
	if idle && m.motion() {
		markerStyle = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
		if bg := th.Accent.GetBackground(); hasColor(bg) {
			markerStyle = markerStyle.Background(bg)
		}
	}
	// Love lands on the dial: one coral pulse of the needle, seated on the
	// same surface as its neighbors.
	if m.s.LovedTrack && m.now.Sub(m.loveAt) < 600*time.Millisecond {
		markerStyle = th.Love
		if bg := th.Accent.GetBackground(); hasColor(bg) {
			markerStyle = markerStyle.Background(bg)
		}
	}
	cells := make([]string, iw)
	isTick := make([]bool, iw)
	dimBand := th.Dim.Render(th.G.Band)
	for i := range cells {
		cells[i] = dimBand
	}
	for _, uuid := range m.s.Presets {
		t := int(math.Round(stationDialPos(uuid) * float64(iw-1)))
		if t >= 0 && t < iw {
			cells[t] = th.AccentDim.Render(th.G.Tick)
			isTick[t] = true
		}
	}
	// Warm bleed: a 5-cell ember gradient around the marker, fading through
	// the ramp with distance. Preset ticks win over the bleed. While the
	// needle is still traveling — or the stream hasn't locked — the bleed
	// becomes a detune smear: a wider trail of ramp-low dither glyphs that
	// clears back to the warm bleed once the spring settles on a lock.
	bleed := []struct {
		off  int
		frac float64
	}{{-2, 0.18}, {-1, 0.42}, {1, 0.42}, {2, 0.18}}
	detuning := !m.dial.Settled(m.dialTgt) || m.s.Phase == session.Tuning
	if detuning {
		bleed = []struct {
			off  int
			frac float64
		}{{-3, 0.18}, {-2, 0.30}, {-1, 0.42}, {1, 0.42}, {2, 0.30}, {3, 0.18}}
	}
	dither := "░"
	if th.G.Blocks[0] == '_' {
		dither = "#"
	}
	for _, bl := range bleed {
		j := col + bl.off
		if j < 0 || j >= iw || isTick[j] {
			continue
		}
		r, g, b := th.rampColor(bl.frac)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r, g, b)))
		if bg := th.Accent.GetBackground(); hasColor(bg) {
			style = style.Background(bg)
		}
		glyph := th.G.Band
		if detuning {
			off := bl.off
			if off < 0 {
				off = -off
			}
			glyph = th.G.Dot
			if off >= 2 {
				glyph = dither
			}
		}
		cells[j] = style.Render(glyph)
	}
	if col >= 0 && col < iw {
		cells[col] = markerStyle.Render(th.G.Marker)
	}
	return strings.Join(cells, "")
}
