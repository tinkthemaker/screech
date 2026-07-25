package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"screech/internal/core"
)

// Screech is an ambient object: on screen for hours, read from across the
// room, in eight finishes whose accents span the whole hue circle. Every
// number in this file was measured against a build that shipped, so each
// threshold is a regression that actually happened rather than a guess.

const (
	// wcagBodyText is the AA contrast minimum for ordinary text. The panel
	// microlabels (TRACK, SIGNAL, BROADCAST, GENRE, the preset legend) all
	// render in Dim, so Dim has to clear it.
	wcagBodyText = 4.5
	// wcagGraphical is the AA minimum for a non-text element that carries
	// meaning. The wave's low ramp step is where the meter spends most of
	// its life, which makes it exactly that.
	wcagGraphical = 3.0
)

func rgbOf(c lipgloss.TerminalColor) (int, int, int) {
	if c == nil {
		return 0, 0, 0
	}
	return hexRGB(fmt.Sprint(c))
}

func contrastOf(fg, bg lipgloss.TerminalColor) float64 {
	fr, fg2, fb := rgbOf(fg)
	br, bg2, bb := rgbOf(bg)
	return contrastRatio(fr, fg2, fb, br, bg2, bb)
}

// Deriving grays by scaling RGB uniformly made "the same lightness" mean
// wildly different legibility per hue: green carries ~71% of perceived
// luminance and red ~21%, so the crimson finish's microlabels measured 3.4
// where amber's measured 4.1. Both were under AA; the point is that one
// number has to hold across every finish and every daypart.
func TestThemeContrastFloors(t *testing.T) {
	for _, ch := range ThemeChoices() {
		for _, dp := range []string{core.DaypartMorning, core.DaypartDay, core.DaypartEvening, core.DaypartNight} {
			th := NewDaypartTheme(ch.ID, "#FFB000", false, dp)
			panel := th.PanelFill.GetBackground()

			if got := contrastOf(th.Dim.GetForeground(), panel); got < wcagBodyText {
				t.Errorf("%s/%s: Dim vs panel = %.2f, want >= %.1f (microlabels render in Dim)",
					ch.Label, dp, got, wcagBodyText)
			}
			if got := contrastOf(th.Mid.GetForeground(), panel); got < wcagBodyText {
				t.Errorf("%s/%s: Mid vs panel = %.2f, want >= %.1f", ch.Label, dp, got, wcagBodyText)
			}
			if got := contrastOf(th.FootLabel.GetForeground(), th.FootFill.GetBackground()); got < wcagBodyText {
				t.Errorf("%s/%s: footer label vs strip = %.2f, want >= %.1f", ch.Label, dp, got, wcagBodyText)
			}
			if got := contrastOf(th.SelMeta.GetForeground(), th.SelFill.GetBackground()); got < wcagBodyText {
				t.Errorf("%s/%s: selection meta vs selection bar = %.2f, want >= %.1f", ch.Label, dp, got, wcagBodyText)
			}
		}
	}
}

// The ramp's bottom step used to be a fixed 35% of the accent, which on the
// crimson finish measured 1.3 against the panel — invisible. Since the bars
// sit near the bottom of the ramp most of the time, that step is the
// majority of what the meter actually shows.
func TestWaveRampIsVisibleAndMonotonic(t *testing.T) {
	for _, ch := range ThemeChoices() {
		th := NewDaypartTheme(ch.ID, "#FFB000", false, core.DaypartEvening)
		panel := th.PanelFill.GetBackground()

		if got := contrastOf(th.RampFor(0, 7).GetForeground(), panel); got < wcagGraphical {
			t.Errorf("%s: ramp floor vs panel = %.2f, want >= %.1f", ch.Label, got, wcagGraphical)
		}
		// A gradient that dips is worse than no gradient: it makes a quiet
		// bar look hotter than a louder one right beside it.
		prev := -1.0
		for i := 0; i < 8; i++ {
			r, g, b := rgbOf(th.RampFor(i, 7).GetForeground())
			lum := relLuminance(r, g, b)
			if lum < prev-1e-9 {
				t.Errorf("%s: ramp step %d is darker than step %d (%.4f < %.4f)", ch.Label, i, i-1, lum, prev)
			}
			prev = lum
		}
		lo := contrastOf(th.RampFor(0, 7).GetForeground(), panel)
		hi := contrastOf(th.RampFor(7, 7).GetForeground(), panel)
		if hi < lo*1.8 {
			t.Errorf("%s: ramp spans only %.2f→%.2f; the gradient has collapsed", ch.Label, lo, hi)
		}
	}
}

// One braille row is four levels, not eight — the cell's 8 dots are 4 rows
// of 2 columns. At four levels the meter was effectively binary across the
// entire useful loudness range, which is what made it read as static.
func TestWaveHasUsableVerticalResolution(t *testing.T) {
	if maxWaveRows*brailleLevelsPerCell < 8 {
		t.Fatalf("a full-height bay resolves %d levels, too few to read as a shape",
			maxWaveRows*brailleLevelsPerCell)
	}
	w := NewWave(4)
	w.SetRows(maxWaveRows)
	if got := len(w.Render(NewTheme("#FFB000", false))); got != maxWaveRows*2 {
		t.Fatalf("Render returned %d rows, want %d (both channels)", got, maxWaveRows*2)
	}
	// Every row must be exactly one cell per bar, or the bay overflows.
	for i, row := range w.Render(NewTheme("#FFB000", false)) {
		if got := lipgloss.Width(row); got != 4 {
			t.Errorf("row %d width = %d, want 4", i, got)
		}
	}
}

// settle: drive the meter to a steady state at a given 0..1 level.
func settle(level float64, bars int) *Wave {
	w := NewWave(bars)
	w.SetRows(maxWaveRows)
	w.SetEnergy(1)
	tt := 0.0
	for f := 0; f < 400; f++ {
		tt += 0.05
		w.SetStereoLevels(level, level, level, tt)
		w.Step(tt, 0.05)
	}
	return w
}

func meanHeight(w *Wave) float64 {
	sum := 0.0
	for _, v := range w.dispL {
		sum += v
	}
	return sum / float64(len(w.dispL))
}

// The meter's job is showing the difference between a quiet stream and a
// loud one. It used to compress everything real into the bottom of its
// range: at -14 dBFS, typical streaming loudness, only 2 of 48 cells reached
// the top half of a four-level bay.
func TestMeterSeparatesQuietFromLoud(t *testing.T) {
	// The same curve the mpv backend applies, over real broadcast levels.
	unit := func(db float64) float64 {
		lv := (db + 36) / 33
		return clampF(lv, 0, 1)
	}
	quiet := meanHeight(settle(unit(-30), 48))
	typical := meanHeight(settle(unit(-14), 48))
	loud := meanHeight(settle(unit(-6), 48))

	t.Logf("mean bar height: quiet(-30dB)=%.2f typical(-14dB)=%.2f loud(-6dB)=%.2f", quiet, typical, loud)
	if typical-quiet < 0.15 {
		t.Errorf("quiet and typical differ by %.2f of the bay; the meter barely moves", typical-quiet)
	}
	if loud-typical < 0.05 {
		t.Errorf("typical and loud differ by %.2f of the bay", loud-typical)
	}
	// Typical program should use the bay's middle, leaving headroom above.
	if typical < 0.40 || typical > 0.80 {
		t.Errorf("typical loudness sits at %.2f of the bay; want it in the middle with room above", typical)
	}
	// No bar may pin: a meter that saturates has stopped reporting.
	if loud > 0.95 {
		t.Errorf("loud program pins the meter at %.2f", loud)
	}
}

// The right-hand bars were capped at 0.55 by bassWeight, which at four
// levels left the right third of the bay structurally dead however loud the
// music got.
func TestBassWeightLeavesTheRightSideUsable(t *testing.T) {
	w := NewWave(48)
	w.SetRows(maxWaveRows)
	total := float64(maxWaveRows * brailleLevelsPerCell)
	rightmost := w.bassWeight(w.bars - 1)
	if levels := rightmost * total; levels < total*0.6 {
		t.Errorf("rightmost bar tops out at %.1f of %.0f levels; the tilt is a cliff, not a lean", levels, total)
	}
}

// playingAt builds a model in the ordinary listening state at a given size.
func playingAt(t *testing.T, w, h int) Model {
	t.Helper()
	m := testModel(t)
	m.now = time.Now()
	m.start = m.now.Add(-time.Minute)
	m.lastKey, m.lastAct = m.now, m.now
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = mm.(Model)
	m.ph, m.syncing = phPlay, false
	m.haveSt, m.haveTrack, m.stereo = true, true, true
	m.st = core.Station{UUID: "u1", Name: "BADROCK HARD & HEAVY", Codec: "MP3",
		Bitrate: 320, Tags: "heavy metal, hard rock, thrash"}
	m.playStart = m.now.Add(-56 * time.Second)
	m.track = "Cradle Of Filth · Right Wing of the Garden Triptych"
	m.reason = "preset 4"
	m.tw = NewTypewriter(m.reason, m.now.Add(-5*time.Second))
	return m
}

// The faceplate was a fixed ten rows at every height, so a 130x53 terminal
// showed a business card floating in 75% empty screen while the library
// view beside it filled the same terminal properly.
func TestFaceplateUsesTheHeightItIsGiven(t *testing.T) {
	for _, s := range []struct {
		w, h    int
		minFill float64
	}{
		{92, 24, 0.75},
		{100, 30, 0.60},
		{120, 40, 0.45},
		{130, 53, 0.35},
		{80, 24, 0.50}, // too narrow for the two-bay panel; stacked layout
	} {
		m := playingAt(t, s.w, s.h)
		lines := strings.Split(m.View(), "\n")
		content := 0
		for _, ln := range lines {
			if lipgloss.Width(strings.TrimSpace(ln)) > 0 {
				content++
			}
		}
		fill := float64(content) / float64(len(lines))
		if fill < s.minFill {
			t.Errorf("%dx%d: %d of %d rows carry content (%.0f%%), want at least %.0f%%",
				s.w, s.h, content, len(lines), fill*100, s.minFill*100)
		}
	}
}

// Growing the panel must not break the resize contract: never wider than
// the terminal, never taller, never wrapped.
func TestFaceplateStillFitsAtEverySize(t *testing.T) {
	for _, s := range []struct{ w, h int }{
		{130, 53}, {120, 40}, {100, 30}, {92, 24}, {84, 24}, {80, 24},
		{70, 20}, {64, 16}, {50, 12}, {38, 10}, {24, 8}, {20, 6}, {200, 80},
	} {
		m := playingAt(t, s.w, s.h)
		assertFits(t, m.View(), s.w, s.h)
	}
}

// The signal bay splits top-half left channel, bottom-half right, but the
// only labels beside it read LOW and HIGH — which describe the horizontal
// axis. Nothing said the vertical split meant anything.
func TestSignalBayNamesItsChannelAxis(t *testing.T) {
	m := playingAt(t, 120, 40)
	m.stereo = true
	if !strings.Contains(stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n")), "L/R") {
		t.Error("a stereo stream should label the bay L/R")
	}
	m.stereo = false
	if !strings.Contains(stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n")), "MONO") {
		t.Error("a stream with only one measured channel should say MONO, not imply a stereo image")
	}
}

// Tags drive tag affinity, one of the three signals choosing the next
// station, and had no representation on screen at all.
func TestFaceplateShowsStationTags(t *testing.T) {
	m := playingAt(t, 120, 40)
	view := stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n"))
	if !strings.Contains(view, "GENRE") || !strings.Contains(view, "heavy metal") {
		t.Errorf("the faceplate should carry the station's tags:\n%s", view)
	}
	// A station with no tags drops the block rather than showing an orphan label.
	m.st.Tags = ""
	view = stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n"))
	if strings.Contains(view, "GENRE") {
		t.Error("an untagged station should not render an empty GENRE label")
	}
}

// The tuning ceremony shares the bay with the meter, so it has to produce
// exactly as many rows or the panel reflows mid-retune.
func TestStaticCollapseMatchesTheWaveRowCount(t *testing.T) {
	for _, rows := range []int{1, 2, 3} {
		m := playingAt(t, 120, 40)
		m.wave.SetRows(rows)
		m.ph, m.tuneAt = phTune, m.now
		got := m.staticRows(20, m.th)
		if len(got) != rows*2 {
			t.Fatalf("rows=%d: static collapse produced %d rows, want %d", rows, len(got), rows*2)
		}
		for i, r := range got {
			if lipgloss.Width(r) != 20 {
				t.Errorf("rows=%d: static row %d width %d, want 20", rows, i, lipgloss.Width(r))
			}
		}
	}
}
