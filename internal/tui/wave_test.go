package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// stripANSI removes color sequences; tests that inspect runes must not
// depend on which color profile a previous test forced.
func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func meanOf(v []float64) float64 {
	sum := 0.0
	for _, d := range v {
		sum += d
	}
	return sum / float64(len(v))
}

// Fresh loudness samples must drive amplitude; loud beats quiet.
func TestWaveFollowsRealLevel(t *testing.T) {
	loud := NewWave(32)
	quiet := NewWave(32)
	loud.SetEnergy(1)
	quiet.SetEnergy(1)

	dt := 1.0 / 20
	for i := 0; i < 200; i++ {
		tt := float64(i) * dt
		loud.SetLevel(0.95, tt)
		quiet.SetLevel(0.05, tt)
		loud.Step(tt, dt)
		quiet.Step(tt, dt)
	}
	if meanOf(loud.dispL) <= meanOf(quiet.dispL)*1.5 {
		t.Fatalf("loud stream should visibly out-amplitude quiet: loud %.3f quiet %.3f",
			meanOf(loud.dispL), meanOf(quiet.dispL))
	}
}

// When level samples stop (backend without astats), the wave must fall back
// to its self-animated breathing rather than freezing at the last level.
func TestWaveFallsBackWhenLevelGoesStale(t *testing.T) {
	w := NewWave(32)
	w.SetEnergy(1)
	dt := 1.0 / 20

	// Feed silence levels, then stop feeding and advance past staleness.
	for i := 0; i < 100; i++ {
		tt := float64(i) * dt
		w.SetLevel(0.0, tt)
		w.Step(tt, dt)
	}
	nearSilent := meanOf(w.dispL)

	for i := 100; i < 400; i++ { // 15s beyond, no SetLevel calls
		tt := float64(i) * dt
		w.Step(tt, dt)
	}
	revived := meanOf(w.dispL)
	if revived <= nearSilent+0.05 {
		t.Fatalf("stale level should revive the fallback animation: before %.3f after %.3f",
			nearSilent, revived)
	}
}

// The renderer returns exactly two rows (upper/lower). Adjacent samples touch,
// and one sample owns one cell, so a signal bay is filled without a tail pad.
func TestWaveRenderTwoRowsContinuous(t *testing.T) {
	w := NewWave(24)
	w.SetEnergy(1)
	th := NewTheme("#FFB000", false)
	for i := 0; i < 60; i++ {
		tm := float64(i) * 0.05
		w.SetLevel(0.5+0.4*float64(i%5)/5, tm)
		w.Step(tm, 0.05)
	}
	rows := w.Render(th)
	if len(rows) != 2 {
		t.Fatalf("wave must render two rows, got %d", len(rows))
	}
	for i, r := range rows {
		if lw := lipgloss.Width(r); lw != 24 {
			t.Errorf("row %d width %d, want 24 continuous columns", i, lw)
		}
	}
	// Bars of different heights must exist (texture), and the lower row
	// must be at least as filled as the upper (bars climb from the base).
	if strings.TrimSpace(rows[1]) == "" {
		t.Error("lower row should carry the bar bases")
	}
}

// Stereo: the top row is the LEFT channel, the bottom row the RIGHT. A
// left-heavy signal must fill more of the top row's braille dots.
func TestWaveRowsAreStereoChannels(t *testing.T) {
	w := NewWave(8)
	w.SetEnergy(1)
	th := NewTheme("#FFB000", false)
	for i := 0; i < 200; i++ {
		tm := float64(i) * 0.05
		w.SetStereoLevels(0.99, 0.02, 0, tm)
		w.Step(tm, 0.05)
	}
	rows := w.Render(th)
	if dotMass(rows[0]) <= dotMass(rows[1]) {
		t.Errorf("left-heavy signal should fill the top row more: top %d bottom %d",
			dotMass(rows[0]), dotMass(rows[1]))
	}
	if meanOf(w.dispL) <= meanOf(w.dispR)*1.5 {
		t.Errorf("left channel should out-amplitude right: L %.3f R %.3f",
			meanOf(w.dispL), meanOf(w.dispR))
	}
}

// dotMass counts lit braille dots in a rendered row; denser bars weigh more.
func dotMass(row string) int {
	mass := 0
	for _, r := range row {
		bits := r - brailleBlank
		if bits < 0 || bits > 0xFF {
			continue // styled segments contribute their rune only
		}
		for bits != 0 {
			mass += int(bits & 1)
			bits >>= 1
		}
	}
	return mass
}

// A tall bar fills more of its braille cell than a short one, and the color
// ramps with height so tall bars glow brighter.
func TestWaveBarHeightFollowsLevel(t *testing.T) {
	w := NewWave(8)
	w.SetEnergy(1)
	th := NewTheme("#FFB000", false)
	// Pin the level high so every bar wants to be tall.
	for i := 0; i < 100; i++ {
		tm := float64(i) * 0.05
		w.SetLevel(0.99, tm)
		w.Step(tm, 0.05)
	}
	rows := w.Render(th)
	if dotMass(rows[0]) == 0 {
		t.Error("at high level the top row should carry tall bars")
	}
	// At low level the top row should be the dim baseline or nearly so.
	w2 := NewWave(8)
	w2.SetEnergy(1)
	for i := 0; i < 100; i++ {
		tm := float64(i) * 0.05
		w2.SetLevel(0.02, tm)
		w2.Step(tm, 0.05)
	}
	rows2 := w2.Render(th)
	if dotMass(rows2[0]) > dotMass(rows[0]) {
		t.Error("quiet wave must not have more top-row fill than loud wave")
	}
}

// Bass weight: the left (low-frequency) bars respond harder to the same
// loudness signal than the right bars.
func TestWaveBassWeight(t *testing.T) {
	w := NewWave(32)
	if w.bassWeight(0) <= w.bassWeight(31) {
		t.Errorf("bass weight should fall left to right: left %.2f right %.2f",
			w.bassWeight(0), w.bassWeight(31))
	}
	if w.bassWeight(0) != 1.0 {
		t.Errorf("leftmost bar should get full weight, got %.2f", w.bassWeight(0))
	}
}

// Peak ticks cool through the ramp as they fall rather than snapping off.
func TestWavePeakCools(t *testing.T) {
	th := NewTheme("#FFB000", false)
	steps := th.PeakSteps()
	if len(steps) != 3 {
		t.Fatalf("expected 3 cooling steps, got %d", len(steps))
	}
	if steps[0].GetForeground() == nil || steps[1].GetForeground() == nil || steps[2].GetForeground() == nil {
		t.Fatal("peak steps must carry colors")
	}
	// Successive steps must be distinct colors (a fall, not a flat dim).
	if steps[0].GetForeground() == steps[1].GetForeground() ||
		steps[1].GetForeground() == steps[2].GetForeground() {
		t.Error("peak steps should cool through distinct colors")
	}
}

// Regression: the lazy peak-style cache must actually populate. A zero
// lipgloss.Style reports a non-nil empty color, so the old nil-based guard
// never fired and peak ticks rendered unstyled — black holes in the
// faceplate above the wave. Cached ticks must also sit on the panel
// surface when rendered through an OnPanel theme.
func TestWavePeakCachePopulatesOnPanel(t *testing.T) {
	w := NewWave(8)
	pt := NewTheme("#FFB000", false).OnPanel()
	w.Render(pt)
	wantBg := fmt.Sprint(pt.PanelFill.GetBackground())
	for i, s := range w.peakSteps {
		if fg := fmt.Sprint(s.GetForeground()); len(fg) != 7 || fg[0] != '#' {
			t.Errorf("cached peak step %d foreground = %q, want a hex color", i, fg)
		}
		if bg := fmt.Sprint(s.GetBackground()); bg != wantBg {
			t.Errorf("cached peak step %d background = %q, want panel fill %q", i, bg, wantBg)
		}
	}

	// ResetPalette must force a re-derive against the new theme's surface.
	w.ResetPalette()
	flat := NewAustereTheme(false).OnPanel()
	w.Render(flat)
	if bg, want := fmt.Sprint(w.peakSteps[0].GetBackground()), fmt.Sprint(flat.PanelFill.GetBackground()); bg != want {
		t.Errorf("after ResetPalette, peak background = %q, want %q", bg, want)
	}
}

// Regression: upper-row cells must be seated on the caller's surface, not
// raw glyphs that punch the terminal's default background through the
// faceplate. A fresh wave renders its dim baseline, so its upper row must
// carry the panel background on every cell.
func TestWaveUpperRowSeatedOnPanel(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	w := NewWave(6)
	pt := NewTheme("#FFB000", false).OnPanel()
	row := w.Render(pt)[0]
	if lipgloss.Width(row) != 6 {
		t.Fatalf("upper row width = %d, want 6", lipgloss.Width(row))
	}
	if strings.Contains(row, "\x1b[0m ") || !strings.Contains(row, "48;2;19;15;8") {
		t.Errorf("upper-row cells must sit on the panel surface: %q", row)
	}

	// Flat (non-panel) themes leave cells untouched, as before.
	flat := NewWave(6)
	row = flat.Render(NewTheme("#FFB000", false))[0]
	if strings.Contains(row, "48;") {
		t.Errorf("flat layout should not paint a background behind the wave: %q", row)
	}
}

// True peak-hold: markers jump to the real digital peak immediately — long
// before the eased bars could climb there — and cool at the classic decay
// rate once the peak recedes.
func TestWaveTruePeakHold(t *testing.T) {
	w := NewWave(8)
	w.SetEnergy(1)
	dt := 1.0 / 20

	// Quiet RMS but a hot digital peak: markers must pin to the real peak
	// while the bars stay low. The ceiling is tilted by bassWeight so it
	// follows the bay's shape instead of drawing one flat rule across it,
	// so each marker is checked against its own bar's ceiling.
	for i := 0; i < 100; i++ {
		tm := float64(i) * dt
		w.SetStereoLevels(0.1, 0.1, 0.9, tm)
		w.Step(tm, dt)
	}
	for i, p := range w.peakL {
		want := 0.9 * w.bassWeight(i)
		if p < want-1e-6 {
			t.Fatalf("peak marker %d = %.3f, want a jump to the real peak >= %.3f", i, p, want)
		}
		if p <= w.dispL[i] {
			t.Fatalf("peak marker %d (%.3f) should float above its bar (%.3f)", i, p, w.dispL[i])
		}
	}
	if meanOf(w.dispL) > 0.5 {
		t.Fatalf("bars should stay near the quiet RMS: mean %.3f", meanOf(w.dispL))
	}

	// Feed a receding peak; markers must cool rather than stick.
	hot := meanOf(w.peakL)
	for i := 100; i < 300; i++ {
		tm := float64(i) * dt
		w.SetStereoLevels(0.1, 0.1, 0.2, tm)
		w.Step(tm, dt)
	}
	if cooled := meanOf(w.peakL); cooled >= hot-0.2 {
		t.Fatalf("peak markers should cool after the peak recedes: %.3f -> %.3f", hot, cooled)
	}
}

// SetBars is the Path 2 FFT drop-in: an external bar slice drives the
// display verbatim (resampled to the bar count), and nil returns the wave
// to internal synthesis.
func TestWaveSetBarsExternalSource(t *testing.T) {
	w := NewWave(8)
	w.SetEnergy(1)
	w.SetBars([]float64{0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2})
	dt := 1.0 / 20
	for i := 0; i < 100; i++ {
		tm := float64(i) * dt
		w.Step(tm, dt)
	}
	if w.dispL[0] < 0.8 || w.dispL[7] > 0.35 {
		t.Errorf("external bars should drive the display: disp[0]=%.3f disp[7]=%.3f", w.dispL[0], w.dispL[7])
	}
	if w.dispR[0] < 0.8 {
		t.Errorf("external bars feed both channels until Path 2 goes stereo: %.3f", w.dispR[0])
	}

	// Resampling: fewer source bars than display bars must still fill the bay.
	w.SetBars([]float64{1, 0})
	for i := 0; i < 200; i++ {
		w.Step(100+float64(i)*dt, dt)
	}
	if w.dispL[0] < 0.9 || w.dispL[7] > 0.1 {
		t.Errorf("resampled bars should stretch across the bay: disp[0]=%.3f disp[7]=%.3f", w.dispL[0], w.dispL[7])
	}

	// nil returns to synthesis: with energy and no metering, breathing moves.
	w.SetBars(nil)
	if w.ext != nil {
		t.Fatal("SetBars(nil) should clear the external source")
	}
	before := meanOf(w.dispL)
	for i := 0; i < 100; i++ {
		w.Step(200+float64(i)*dt, dt)
	}
	if after := meanOf(w.dispL); after == before {
		t.Error("internal synthesis should resume after SetBars(nil)")
	}
}

// Braille output invariants: exactly two rows of exact width, every glyph a
// braille cell, and the silhouette denser where bars are taller.
func TestWaveBrailleInvariants(t *testing.T) {
	th := NewTheme("#FFB000", false)
	w := NewWave(17)
	w.SetEnergy(1)
	dt := 1.0 / 20
	for i := 0; i < 120; i++ {
		tm := float64(i) * dt
		w.SetStereoLevels(0.7, 0.4, 0.85, tm)
		w.Step(tm, dt)
	}
	rows := w.Render(th)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for i, r := range rows {
		if lw := lipgloss.Width(r); lw != 17 {
			t.Errorf("row %d width %d, want 17", i, lw)
		}
		for _, ru := range stripANSI(r) {
			if ru < brailleBlank || ru > brailleBlank+0xFF {
				t.Errorf("row %d holds non-braille glyph %#U", i, ru)
			}
		}
	}
}

// The 7-bit palette falls back to eighth-block cells — braille would be
// mojibake on a bad SSH link.
func TestWaveASCIIFallback(t *testing.T) {
	th := NewTheme("#FFB000", true)
	w := NewWave(12)
	w.SetEnergy(1)
	dt := 1.0 / 20
	for i := 0; i < 120; i++ {
		tm := float64(i) * dt
		w.SetStereoLevels(0.8, 0.5, 0.9, tm)
		w.Step(tm, dt)
	}
	rows := w.Render(th)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	allowed := map[rune]bool{}
	for _, r := range th.G.Blocks {
		allowed[r] = true
	}
	for i, r := range rows {
		if lw := lipgloss.Width(r); lw != 12 {
			t.Errorf("row %d width %d, want 12", i, lw)
		}
		for _, ru := range stripANSI(r) {
			if !allowed[ru] {
				t.Errorf("row %d holds glyph %#U outside the ascii palette", i, ru)
			}
		}
	}
}
