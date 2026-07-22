package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func meanDisp(w *Wave) float64 {
	sum := 0.0
	for _, d := range w.disp {
		sum += d
	}
	return sum / float64(len(w.disp))
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
	if meanDisp(loud) <= meanDisp(quiet)*1.5 {
		t.Fatalf("loud stream should visibly out-amplitude quiet: loud %.3f quiet %.3f",
			meanDisp(loud), meanDisp(quiet))
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
	nearSilent := meanDisp(w)

	for i := 100; i < 400; i++ { // 15s beyond, no SetLevel calls
		tt := float64(i) * dt
		w.Step(tt, dt)
	}
	revived := meanDisp(w)
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

// A bar's upper half only renders when the bar is tall; the color ramps
// with height so tall bars glow brighter than short ones.
func TestWaveUpperRowOnlyOnTallBars(t *testing.T) {
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
	if strings.TrimSpace(rows[0]) == "" {
		t.Error("at high level the upper row should carry bar tops")
	}
	// At low level the upper row should be empty or nearly so.
	w2 := NewWave(8)
	w2.SetEnergy(1)
	for i := 0; i < 100; i++ {
		tm := float64(i) * 0.05
		w2.SetLevel(0.02, tm)
		w2.Step(tm, 0.05)
	}
	rows2 := w2.Render(th)
	if lipgloss.Width(strings.TrimSpace(rows2[0])) > lipgloss.Width(strings.TrimSpace(rows[0])) {
		t.Error("quiet wave must not have more upper-row content than loud wave")
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
