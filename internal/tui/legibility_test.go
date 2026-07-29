package tui

import (
	"fmt"
	"math"
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

// A finish must not change which colour it is depending on the clock. The
// daypart temper applies warmth as a channel push (red up, blue down),
// which rotates the hue as a side effect. For most accents that's under two
// degrees; for one already at the red end of the wheel there is nowhere to
// rotate but into magenta, and midday moved the crimson finish to hue 339.
func TestDaypartTemperKeepsEachFinishItsOwnColour(t *testing.T) {
	for _, ch := range ThemeChoices() {
		if ch.ID == ThemeAustere {
			continue // no hue to preserve
		}
		base := NewNamedTheme(ch.ID, "#FFB000", false)
		bh, bs, _ := rgbToHSL(hexRGB(base.AccentHex))
		if bs < 0.12 {
			continue
		}
		for _, dp := range []string{core.DaypartMorning, core.DaypartDay,
			core.DaypartEvening, core.DaypartNight} {
			th := NewDaypartTheme(ch.ID, "#FFB000", false, dp)
			h, _, _ := rgbToHSL(hexRGB(th.AccentHex))
			drift := math.Abs(math.Mod(h-bh+540, 360) - 180)
			if drift > maxDaypartHueDrift+0.5 {
				t.Errorf("%s at %s drifted %.1f° from hue %.1f to %.1f",
					ch.Label, dp, drift, bh, h)
			}
		}
	}
}

// Slayer is "blood red on black iron". It shipped on #DC143C, which is CSS
// crimson at hue 348 — twelve degrees into the magenta side of red — so
// every gray, surface and ramp step derived from it came out rose.
func TestSlayerIsBloodRedNotRose(t *testing.T) {
	redward := func(h float64) bool { return h >= 350 || h <= 20 }
	for _, dp := range []string{core.DaypartMorning, core.DaypartDay,
		core.DaypartEvening, core.DaypartNight} {
		th := NewDaypartTheme(ThemeSlayer, "#FFB000", false, dp)
		h, _, _ := rgbToHSL(hexRGB(th.AccentHex))
		if !redward(h) {
			t.Errorf("%s: accent hue %.1f is off the red axis", dp, h)
		}
		// The ramp bottom is blood; the top is heated metal. Neither may
		// wander into the magenta arc, which is what "pale red" becomes.
		for i := 0; i < 8; i++ {
			r, g, b := hexRGB(fmt.Sprint(th.RampFor(i, 7).GetForeground()))
			hh, ss, _ := rgbToHSL(r, g, b)
			if ss > 0.15 && hh > 60 && hh < 350 {
				t.Errorf("%s: ramp[%d] hue %.1f is neither red nor ember", dp, i, hh)
			}
		}
	}
	// The top of the ramp should be visibly hotter in hue than the bottom:
	// blood into ember, the way worked iron goes.
	th := NewDaypartTheme(ThemeSlayer, "#FFB000", false, core.DaypartEvening)
	lo, _, _ := rgbToHSL(hexRGB(fmt.Sprint(th.RampFor(0, 7).GetForeground())))
	hi, _, _ := rgbToHSL(hexRGB(fmt.Sprint(th.RampFor(7, 7).GetForeground())))
	if hi-lo < 8 {
		t.Errorf("ramp runs %.1f° to %.1f°; it should heat toward ember", lo, hi)
	}
}

// The density strip is context sitting under an instrument. It must not
// outweigh the band line it explains: shade blocks put its busiest cells at
// 5.08 contrast against a band line of 5.28, and being nearly solid fill,
// they read as a slab of corruption rather than as a reading.
func TestDensityStripStaysSubordinate(t *testing.T) {
	for _, ch := range ThemeChoices() {
		th := NewDaypartTheme(ch.ID, "#FFB000", false, core.DaypartEvening)
		pt := th.OnPanel()
		panel := pt.PanelFill.GetBackground()
		strip := contrastOf(pt.RampFor(1, 7).GetForeground(), panel)
		band := contrastOf(pt.Dim.GetForeground(), panel)
		if strip >= band {
			t.Errorf("%s: strip contrast %.2f is not below the band's %.2f", ch.Label, strip, band)
		}
		// Still has to clear the graphical floor, or it isn't a reading.
		if strip < wcagGraphical {
			t.Errorf("%s: strip contrast %.2f is under the %.1f floor", ch.Label, strip, wcagGraphical)
		}
	}
}

// Bars are capped below a full cell so a busy run never becomes a solid
// block, and the row always matches the band width beneath it.
func TestDensityStripDrawsBarsNotSlabs(t *testing.T) {
	const width = 40
	m := playingAt(t, 120, 40)
	totals := map[string]time.Duration{}
	for i := 0; i < 30; i++ {
		totals[fmt.Sprintf("s-%d", i)] = time.Duration(i+1) * time.Hour
	}
	m.listenTotals = totals
	m.density = buildDialDensity(totals, width)
	row := stripANSI(m.dialDensityRow(width, m.th.OnPanel()))
	if lipgloss.Width(row) != width {
		t.Fatalf("strip is %d wide, want %d", lipgloss.Width(row), width)
	}
	blocks := m.th.G.Blocks
	full := blocks[len(blocks)-1]
	for _, r := range row {
		if r == full {
			t.Errorf("strip used a full block %q; a run of those is a slab, not a histogram", string(r))
			break
		}
	}
}

// The two-hue finishes bake their ramp in NewTheme and get their second
// stop assigned afterwards, so for a long time the wave — which reads the
// baked array, not the live function — never saw the gradient at all.
func TestGradientFinishesReachTheirSecondHueInTheBakedRamp(t *testing.T) {
	for _, c := range []struct{ id, stop2 string }{
		{ThemeCharm, "#B48CFF"}, {ThemeLagoon, "#4FA8F5"}, {ThemeSlayer, "#FF6A18"},
	} {
		th := NewNamedTheme(c.id, "#FFB000", false)
		baked, _, _ := rgbToHSL(hexRGB(fmt.Sprint(th.Ramp[len(th.Ramp)-1].GetForeground())))
		want, _, _ := rgbToHSL(hexRGB(c.stop2))
		if d := math.Abs(math.Mod(baked-want+540, 360) - 180); d > 4 {
			t.Errorf("%s: baked ramp top hue %.1f, second stop is %.1f", c.id, baked, want)
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

// The dial is the meter's counterpart, in the same bay, and was a single
// row of band glyphs while the meter grew to six. It carries the density
// strip, the band, and the slot digits now.
func TestDialIsAMultiRowInstrument(t *testing.T) {
	m := playingAt(t, 120, 40)
	m.presets = map[int]string{1: "aa", 4: "u1", 7: "cc"}

	// With no listening history there is no distribution, so the strip is
	// absent rather than blank.
	if got := len(m.dialRows(48, false, m.th.OnPanel())); got != 2 {
		t.Errorf("with no history the dial should be 2 rows, got %d", got)
	}

	m.listenTotals = map[string]time.Duration{
		"aa": 3 * time.Hour, "u1": time.Hour, "cc": 20 * time.Minute,
	}
	m.density = buildDialDensity(m.listenTotals, 48)
	rows := m.dialRows(48, false, m.th.OnPanel())
	if len(rows) != 3 {
		t.Fatalf("dial produced %d rows, want 3", len(rows))
	}
	for i, r := range rows {
		if got := lipgloss.Width(r); got != 48 {
			t.Errorf("dial row %d width %d, want 48", i, got)
		}
	}
	digits := stripANSI(rows[2])
	for _, slot := range []string{"1", "4", "7"} {
		if !strings.Contains(digits, slot) {
			t.Errorf("preset %s has no digit on the dial; ticks alone are anonymous", slot)
		}
	}
}

// The strip replaced evenly spaced graduations, which implied a quantity
// along an axis that is a hash and therefore has none. What replaces them
// has to actually track listening.
func TestDialDensityTracksListening(t *testing.T) {
	const width = 60
	heavy, light := "busy-station", "barely-heard-station"
	d := buildDialDensity(map[string]time.Duration{
		heavy: 10 * time.Hour,
		light: 2 * time.Minute,
	}, width)
	if d == nil {
		t.Fatal("no distribution built from two stations with history")
	}
	if len(d) != width {
		t.Fatalf("distribution is %d wide, want %d", len(d), width)
	}
	hot := int(math.Round(stationDialPos(heavy) * float64(width-1)))
	cold := int(math.Round(stationDialPos(light) * float64(width-1)))
	if d[hot] <= d[cold] {
		t.Errorf("the station with 10h (%.2f) should read hotter than the one with 2m (%.2f)",
			d[hot], d[cold])
	}
	if d[hot] != 1.0 {
		t.Errorf("the peak should normalize to 1.0, got %.3f", d[hot])
	}
	// Empty history yields nothing to draw, not a row of zeros.
	if buildDialDensity(nil, width) != nil {
		t.Error("no history should produce no strip")
	}
	if buildDialDensity(map[string]time.Duration{"x": 0}, width) != nil {
		t.Error("zero listen time should produce no strip")
	}
}

// Dial positions are hashed, so two presets can land on adjacent columns.
// Their digits must not run together: a 1 beside a 7 reads as seventeen.
func TestDialDigitsNeverAbut(t *testing.T) {
	m := playingAt(t, 120, 40)
	// Nine presets on a narrow scale is the worst case for collisions.
	m.presets = map[int]string{}
	for i := 1; i <= 9; i++ {
		m.presets[i] = fmt.Sprintf("station-%d", i)
	}
	for _, width := range []int{24, 32, 48, 64} {
		row := stripANSI(m.dialDigits(width, m.th))
		if lipgloss.Width(row) != width {
			t.Fatalf("width %d: digits row is %d wide", width, lipgloss.Width(row))
		}
		runes := []rune(row)
		for i := 0; i+1 < len(runes); i++ {
			if runes[i] != ' ' && runes[i+1] != ' ' {
				t.Errorf("width %d: digits %q and %q abut and read as one number:\n%q",
					width, string(runes[i]), string(runes[i+1]), row)
			}
		}
	}
}

// The bays were separated by three plain spaces, which read as two adjacent
// text columns rather than one chassis with two compartments.
func TestBaysAreSeparatedByASeam(t *testing.T) {
	m := playingAt(t, 120, 40)
	rows := m.receiverRows(m.innerWidth(), m.contentHeight())
	seams := 0
	for _, r := range rows {
		// Count rows carrying an interior vertical beyond the two frame edges.
		if strings.Count(stripANSI(r), m.th.G.Seam) > 2 {
			seams++
		}
	}
	if seams < len(rows)/2 {
		t.Errorf("only %d of %d panel rows carry a bay seam", seams, len(rows))
	}
}

// The panel had two Bright+Bold elements competing to be the hero. On a
// receiver the track is what's playing and the station is where it's from.
func TestOnlyTheTrackTitleIsTheHero(t *testing.T) {
	m := playingAt(t, 120, 40)
	m.st.Name = "BADROCK"
	view := stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n"))
	if !strings.Contains(view, "B A D R O C K") {
		t.Errorf("a short station name should read as a letterspaced source plate:\n%s", view)
	}
	// Past the gate it stays plain uppercase: letterspacing a long directory
	// name is legible in principle and unreadable in practice.
	m.st.Name = "BADROCK HARD & HEAVY"
	view = stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n"))
	if !strings.Contains(view, "BADROCK HARD & HEAVY") {
		t.Errorf("a long station name should fall back to plain caps:\n%s", view)
	}
	// And a name too long for the bay truncates rather than overflowing.
	m.st.Name = "A Very Long Station Name That Cannot Possibly Fit In The Left Bay"
	assertFits(t, m.View(), 120, 40)
}

// Every figure screech knew lived in the header rail; the panel itself
// carried no numbers at all.
func TestPanelCarriesItsFigures(t *testing.T) {
	m := playingAt(t, 120, 40)
	m.stTotal = 4*time.Hour + 12*time.Minute
	m.trackPlays = 3
	m.presets = map[int]string{1: "aa", 4: "u1"}
	view := stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n"))
	for _, want := range []string{"4h 12m", "heard 3", "2 SAVED", "2 CH"} {
		if !strings.Contains(view, want) {
			t.Errorf("panel is missing the %q readout:\n%s", want, view)
		}
	}
	// A first hearing states nothing: "heard 1x" is noise, not a fact.
	m.trackPlays = 1
	if strings.Contains(stripANSI(strings.Join(m.receiverRows(m.innerWidth(), m.contentHeight()), "\n")), "heard 1") {
		t.Error("a first play should not be announced")
	}
}

func TestTotalTimeFormatting(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, ""},
		{30 * time.Second, "<1m"},
		{45 * time.Minute, "45m"},
		{4*time.Hour + 12*time.Minute, "4h 12m"},
		{50 * time.Hour, "2d 02h"},
	} {
		if got := fmtTotalTime(c.d); got != c.want {
			t.Errorf("fmtTotalTime(%v) = %q, want %q", c.d, got, c.want)
		}
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
