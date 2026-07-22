package tui

import (
	"math"
	"math/rand"
	"strings"

	"screech/internal/mathx"
)

// Wave is the fake spectrum: layered slow sines per bar, eased toward target
// each frame, with peak-hold ticks that fall slowly (the VU-meter detail that
// sells the illusion). It renders from a plain []float64, so when Path 2
// brings real FFT data the visuals don't change — only the data source does.
type Wave struct {
	bars         int
	p1, p2, p3   []float64 // per-bar phase offsets
	disp, peak   []float64
	energy       float64 // 0 = flatline (tuning), 1 = playing
	targetEnergy float64

	// Real loudness from the player (mpv astats RMS), when available.
	level     float64 // 0..1
	levelDisp float64 // smoothed
	levelAt   float64 // Step-clock seconds of last sample; <0 = never
}

func NewWave(bars int) *Wave {
	w := &Wave{levelAt: -999}
	w.Resize(bars)
	return w
}

// SetLevel feeds a real loudness sample (0..1) stamped with the same clock
// Step uses. Fresh samples drive the wave's amplitude; if they stop coming
// (backend without astats), Step falls back to the self-animated breathing.
func (w *Wave) SetLevel(v, t float64) {
	w.level = mathx.Clamp(v, 0, 1)
	w.levelAt = t
}

func (w *Wave) Resize(bars int) {
	if bars < 1 {
		bars = 1
	}
	if bars == w.bars {
		return
	}
	rng := rand.New(rand.NewSource(0x5c12eec4)) // stable phases across resizes
	w.bars = bars
	w.p1 = make([]float64, bars)
	w.p2 = make([]float64, bars)
	w.p3 = make([]float64, bars)
	w.disp = make([]float64, bars)
	w.peak = make([]float64, bars)
	for i := 0; i < bars; i++ {
		w.p1[i] = rng.Float64() * 2 * math.Pi
		w.p2[i] = rng.Float64() * 2 * math.Pi
		w.p3[i] = rng.Float64() * 2 * math.Pi
	}
}

func (w *Wave) SetEnergy(e float64) { w.targetEnergy = mathx.Clamp(e, 0, 1) }

// Step advances the animation. t is absolute seconds, dt frame seconds.
func (w *Wave) Step(t, dt float64) {
	w.energy += (w.targetEnergy - w.energy) * mathx.Clamp(dt*2.2, 0, 1)

	// Amplitude: real loudness when fresh, self-animated breathing otherwise.
	live := t-w.levelAt < 3.0
	if live {
		w.levelDisp += (w.level - w.levelDisp) * mathx.Clamp(dt*10, 0, 1)
	}
	for i := 0; i < w.bars; i++ {
		x := float64(i)
		s := 0.55*math.Sin(1.7*t+w.p1[i]+x*0.35) +
			0.30*math.Sin(3.1*t+w.p2[i]-x*0.21) +
			0.15*math.Sin(5.3*t+w.p3[i]+x*0.53)
		amp := 0.55 + 0.45*math.Sin(0.23*t+x*0.11) // fallback breathing
		if live {
			amp = 0.15 + 0.95*w.levelDisp
			if amp > 1 {
				amp = 1
			}
		}
		target := w.energy * (0.5 + 0.5*s) * amp
		target = mathx.Clamp(target, 0, 1)
		w.disp[i] += (target - w.disp[i]) * mathx.Clamp(dt*9, 0, 1)
		w.peak[i] -= dt * 0.22
		if w.disp[i] > w.peak[i] {
			w.peak[i] = w.disp[i]
		}
		if w.peak[i] < 0 {
			w.peak[i] = 0
		}
	}
}

// Render draws one line. Bars show in mid tone; where the falling peak sits
// notably above the live level, the peak ghost renders instead, in dim.
func (w *Wave) Render(t Theme) string {
	blocks := t.G.Blocks
	n := len(blocks) - 1
	var b strings.Builder
	for i := 0; i < w.bars; i++ {
		li := int(math.Round(w.disp[i] * float64(n)))
		pi := int(math.Round(w.peak[i] * float64(n)))
		if pi > li+1 {
			b.WriteString(t.Dim.Render(string(blocks[pi])))
		} else {
			b.WriteString(t.Mid.Render(string(blocks[li])))
		}
	}
	return b.String()
}
