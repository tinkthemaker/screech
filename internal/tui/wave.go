package tui

import (
	"math"
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Wave is the synthetic spectrum: layered slow sines per bar, eased toward
// target each frame, with peak-hold ticks that fall slowly. Amplitude is
// real (mpv astats RMS); texture is synthetic. It renders from a plain
// []float64, so when Path 2 brings real FFT data the visuals don't change —
// only the data source does.
//
// Since the C pass the wave renders two rows of half-blocks instead of one
// row of eighth-blocks: each bar is a vertical gradient from a dim ember
// base through the accent to a pale peak, and peak ticks cool through the
// ramp as they fall. Adjacent columns touch, producing one continuous signal
// silhouette rather than a dotted sequence. Bars are also bass-weighted — the left end responds
// harder to the loudness signal — so the single amplitude number still reads
// as a spectrum instead of a uniform bounce.
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

	peakSteps [3]lipgloss.Style // cooling ramp for falling peaks
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
	w.level = clampF(v, 0, 1)
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

func (w *Wave) SetEnergy(e float64) { w.targetEnergy = clampF(e, 0, 1) }

// Step advances the animation. t is absolute seconds, dt frame seconds.
func (w *Wave) Step(t, dt float64) {
	w.energy += (w.targetEnergy - w.energy) * clampF(dt*2.2, 0, 1)

	// Amplitude: real loudness when fresh, self-animated breathing otherwise.
	live := t-w.levelAt < 3.0
	if live {
		w.levelDisp += (w.level - w.levelDisp) * clampF(dt*10, 0, 1)
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
		target := w.energy * (0.5 + 0.5*s) * amp * w.bassWeight(i)
		target = clampF(target, 0, 1)
		w.disp[i] += (target - w.disp[i]) * clampF(dt*9, 0, 1)
		w.peak[i] -= dt * 0.22
		if w.disp[i] > w.peak[i] {
			w.peak[i] = w.disp[i]
		}
		if w.peak[i] < 0 {
			w.peak[i] = 0
		}
	}
}

// bassWeight shapes the synthetic texture so the left (low-frequency) end
// of the wave responds harder to the loudness signal. One amplitude number
// can't be a spectrum, but it can lean on the perceptual shortcut that
// bass energy dominates loudness: bars at the left swing ~1.0, falling to
// ~0.55 at the right.
func (w *Wave) bassWeight(i int) float64 {
	if w.bars <= 1 {
		return 1.0
	}
	x := float64(i) / float64(w.bars-1) // 0 left -> 1 right
	return 1.0 - 0.45*x
}

// Render draws the wave as two rows of touching half-blocks: an upper row
// (each bar's top half) and a lower row (its base). Each bar's color climbs
// the ember→accent→pale ramp with height, and a peak tick cooling through
// the same ramp renders above its bar while it falls. One sample maps to one
// terminal cell, so the renderer always fills its assigned signal bay exactly.
//
// Rows are returned top-first; the caller prints them on consecutive lines.
func (w *Wave) Render(t Theme) []string {
	if w.bars < 1 {
		return []string{"", ""}
	}
	if len(w.peakSteps) == 0 || w.peakSteps[0].GetForeground() == nil {
		w.peakSteps = t.PeakSteps()
	}
	blocks := t.G.Blocks // eighth blocks, quiet -> loud
	maxLvl := len(blocks) - 1

	var upper, lower strings.Builder
	for i := 0; i < w.bars; i++ {
		// Bar height in eighth-block levels across the two rows combined.
		total := int(math.Round(w.disp[i] * float64(2*maxLvl)))
		lowLvl := total
		if lowLvl > maxLvl {
			lowLvl = maxLvl
		}
		upLvl := total - maxLvl
		if upLvl < 0 {
			upLvl = 0
		}
		if upLvl > maxLvl {
			upLvl = maxLvl
		}

		// Peak: the highest level this bar reached recently, rendered as a
		// cooling tick in the upper row.
		peakTotal := int(math.Round(w.peak[i] * float64(2*maxLvl)))
		peakUp := peakTotal - maxLvl
		if peakUp < 0 {
			peakUp = 0
		}

		// Lower row: the bar base always renders at its own level color;
		// when the bar is taller than one row the base is full-height.
		if lowLvl > 0 {
			lower.WriteString(t.RampFor(lowLvl, maxLvl).Render(string(blocks[lowLvl])))
		} else {
			lower.WriteString(t.Dim.Render(string(blocks[0])))
		}

		// Upper row: bar top if tall enough, else a cooling peak tick, else
		// a space so the two rows stay aligned.
		switch {
		case upLvl > 0:
			upper.WriteString(t.RampFor(maxLvl+upLvl, 2*maxLvl).Render(string(blocks[upLvl])))
		case peakUp > 1:
			// Cooling peak: which step of the fall it's on.
			frac := w.peak[i] - w.disp[i]
			step := 0
			if frac < 0.08 {
				step = 2
			} else if frac < 0.2 {
				step = 1
			}
			upper.WriteString(w.peakSteps[step].Render(string(blocks[peakUp])))
		default:
			upper.WriteByte(' ')
		}
	}
	return []string{upper.String(), lower.String()}
}

func clampF(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
