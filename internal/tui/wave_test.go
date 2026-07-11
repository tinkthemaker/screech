package tui

import "testing"

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
