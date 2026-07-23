package tui

import (
	"math"
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Wave is the stereo signal meter: two rows of touching braille cells, the
// top row the LEFT channel and the bottom row the RIGHT, with true peak-hold
// markers that jump to the real digital peak and cool slowly. Metering is
// real (mpv astats per-channel RMS + overall peak); texture is synthetic —
// the same bass-weighted sines as before, now modulated per channel. The
// renderer can also be fed an arbitrary bar slice through SetBars, so when
// Path 2 brings real FFT data the visuals don't change — only the data
// source does.
//
// Braille cells give each terminal column 2x4 sub-cells, so one channel's
// silhouette is drawn with 8-dot vertical resolution inside a single row.
// Adjacent columns still touch, producing one continuous signal silhouette.
// Bars are bass-weighted — the left end responds harder to the loudness
// signal — so per-channel levels still read as a spectrum instead of a
// uniform bounce.
type Wave struct {
	bars         int
	p1, p2, p3   []float64 // per-bar phase offsets
	dispL, dispR []float64 // displayed per-channel bar heights 0..1
	peakL, peakR []float64 // per-channel peak-hold markers
	energy       float64   // 0 = flatline (tuning), 1 = playing
	targetEnergy float64

	// Real metering from the player (mpv astats), when available.
	levelL, levelR float64 // latest per-channel RMS loudness 0..1
	levelPeak      float64 // latest true digital peak 0..1
	lvlDispL       float64 // smoothed
	lvlDispR       float64
	peakDisp       float64
	levelAt        float64 // Step-clock seconds of last sample; <0 = never

	// ext is an external bar slice (Path 2 FFT drop-in) fed via SetBars;
	// nil means internal synthesis drives the display.
	ext []float64

	peakSteps [3]lipgloss.Style // cooling ramp for falling peaks
	peakReady bool              // peakSteps holds styles derived from the current theme
}

func NewWave(bars int) *Wave {
	w := &Wave{levelAt: -999}
	w.Resize(bars)
	return w
}

// SetLevel feeds a mono loudness sample (0..1) stamped with the same clock
// Step uses. Kept for mono backends: it mirrors the value into both
// channels. Fresh samples drive the wave's amplitude; if they stop coming
// (backend without astats), Step falls back to the self-animated breathing.
func (w *Wave) SetLevel(v, t float64) {
	w.SetStereoLevels(v, v, 0, t)
}

// SetStereoLevels feeds one metering window: per-channel loudness and the
// true digital peak, all 0..1, stamped with the same clock Step uses.
func (w *Wave) SetStereoLevels(l, r, peak, t float64) {
	w.levelL = clampF(l, 0, 1)
	w.levelR = clampF(r, 0, 1)
	w.levelPeak = clampF(peak, 0, 1)
	w.levelAt = t
}

// SetBars feeds an external bar slice (0..1 per bar) straight into the
// display — the Path 2 FFT drop-in. The slice is resampled to the current
// bar count and eased/peak-held exactly like synthesized bars; pass nil to
// return to internal synthesis.
func (w *Wave) SetBars(b []float64) {
	if b == nil {
		w.ext = nil
		return
	}
	w.ext = make([]float64, len(b))
	for i, v := range b {
		w.ext[i] = clampF(v, 0, 1)
	}
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
	w.dispL = make([]float64, bars)
	w.dispR = make([]float64, bars)
	w.peakL = make([]float64, bars)
	w.peakR = make([]float64, bars)
	for i := 0; i < bars; i++ {
		w.p1[i] = rng.Float64() * 2 * math.Pi
		w.p2[i] = rng.Float64() * 2 * math.Pi
		w.p3[i] = rng.Float64() * 2 * math.Pi
	}
}

func (w *Wave) SetEnergy(e float64) { w.targetEnergy = clampF(e, 0, 1) }

// ResetPalette forces lazily cached peak colors to follow a newly selected
// theme on the next render.
func (w *Wave) ResetPalette() { w.peakReady = false }

// Step advances the animation. t is absolute seconds, dt frame seconds.
func (w *Wave) Step(t, dt float64) {
	w.energy += (w.targetEnergy - w.energy) * clampF(dt*2.2, 0, 1)

	// Amplitude: real metering when fresh, self-animated breathing otherwise.
	live := t-w.levelAt < 3.0
	if live {
		w.lvlDispL += (w.levelL - w.lvlDispL) * clampF(dt*10, 0, 1)
		w.lvlDispR += (w.levelR - w.lvlDispR) * clampF(dt*10, 0, 1)
		w.peakDisp += (w.levelPeak - w.peakDisp) * clampF(dt*10, 0, 1)
	}
	for i := 0; i < w.bars; i++ {
		tgtL, tgtR := w.targets(i, t, live)
		w.dispL[i] += (tgtL - w.dispL[i]) * clampF(dt*9, 0, 1)
		w.dispR[i] += (tgtR - w.dispR[i]) * clampF(dt*9, 0, 1)
		w.peakL[i] = peakHold(w.peakL[i], w.dispL[i], dt)
		w.peakR[i] = peakHold(w.peakR[i], w.dispR[i], dt)
		if live && w.peakL[i] < w.peakDisp {
			w.peakL[i] = w.peakDisp // true peak-hold: jump to the real peak
		}
		if live && w.peakR[i] < w.peakDisp {
			w.peakR[i] = w.peakDisp
		}
	}
}

// peakHold decays a marker at the classic rate and pins it to its bar.
func peakHold(p, disp, dt float64) float64 {
	p -= dt * 0.22
	if disp > p {
		p = disp
	}
	if p < 0 {
		p = 0
	}
	return p
}

// targets computes this frame's desired bar heights for both channels:
// external bars verbatim, else the bass-weighted synthetic texture modulated
// by the per-channel level (or by breathing when metering is stale).
func (w *Wave) targets(i int, t float64, live bool) (float64, float64) {
	if w.ext != nil {
		v := w.ext[i*len(w.ext)/w.bars]
		return v, v
	}
	x := float64(i)
	s := 0.55*math.Sin(1.7*t+w.p1[i]+x*0.35) +
		0.30*math.Sin(3.1*t+w.p2[i]-x*0.21) +
		0.15*math.Sin(5.3*t+w.p3[i]+x*0.53)
	ampL := 0.55 + 0.45*math.Sin(0.23*t+x*0.11) // fallback breathing
	ampR := ampL
	if live {
		ampL = math.Min(0.15+0.95*w.lvlDispL, 1)
		ampR = math.Min(0.15+0.95*w.lvlDispR, 1)
	}
	bw := w.bassWeight(i)
	tex := w.energy * (0.5 + 0.5*s) * bw
	return clampF(tex*ampL, 0, 1), clampF(tex*ampR, 0, 1)
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

// Braille: each cell is 2 columns x 4 rows of dots (U+2800 base). Bit
// values per vertical level, bottom-up, left/right column.
var brailleDots = [5][2]rune{
	{0, 0},
	{0x40, 0x80}, // level 1 (bottom): dots 7, 8
	{0x04, 0x20}, // level 2: dots 3, 6
	{0x02, 0x10}, // level 3: dots 2, 5
	{0x01, 0x08}, // level 4 (top): dots 1, 4
}

const brailleBlank = 0x2800

// brailleBaselineBits is the flatline: only the bottom dot row lit.
const brailleBaselineBits = 0x40 + 0x80

// brailleBar maps a 0..1 height onto the cell's 8 dots (without the U+2800
// base), filled bottom-up, both columns per level, the odd half-dot going
// to the left column. Returns the dot bits and how many vertical levels
// they occupy (0..4).
func brailleBar(h float64) (rune, int) {
	n := int(math.Round(clampF(h, 0, 1) * 8))
	full, half := n/2, n%2
	var bits rune
	for lvl := 1; lvl <= full; lvl++ {
		bits += brailleDots[lvl][0] + brailleDots[lvl][1]
	}
	if half == 1 {
		bits += brailleDots[full+1][0]
	}
	return bits, full + half
}

// braillePeakDots is the dot pair of a single level: the peak-hold marker
// floats as a bright segment at the peak line, above the bar's own dots.
func braillePeakDots(lvl int) rune {
	if lvl < 1 {
		lvl = 1
	}
	if lvl > 4 {
		lvl = 4
	}
	return brailleDots[lvl][0] + brailleDots[lvl][1]
}

// Render draws the wave as exactly two terminal rows of touching cells:
// top row LEFT channel, bottom row RIGHT. Unicode terminals get braille
// silhouettes with 8-dot vertical resolution per row; the ASCII palette
// falls back to eighth-block cells. Each bar's color climbs the
// ember→accent→pale ramp with height, and a peak-hold marker cooling
// through the same ramp floats at the peak line. One bar maps to one
// terminal cell, so the renderer always fills its assigned signal bay
// exactly.
//
// Rows are returned top-first; the caller prints them on consecutive lines.
func (w *Wave) Render(t Theme) []string {
	if w.bars < 1 {
		return []string{"", ""}
	}
	w.ensurePeakSteps(t)
	if t.G.Blocks[0] == '_' { // 7-bit palette: braille would be mojibake
		return []string{
			w.renderBlockRow(t, w.dispL, w.peakL),
			w.renderBlockRow(t, w.dispR, w.peakR),
		}
	}
	return []string{
		w.renderBrailleRow(t, w.dispL, w.peakL),
		w.renderBrailleRow(t, w.dispR, w.peakR),
	}
}

func (w *Wave) ensurePeakSteps(t Theme) {
	if w.peakReady {
		return
	}
	// The cache needs an explicit flag: peakSteps is an array (len is
	// always 3) and a zero lipgloss.Style reports a non-nil empty
	// color, so a nil check on the zero value never fires.
	// Seat the ticks on the caller's surface: an OnPanel theme carries
	// the faceplate background on its grays; flat layouts leave it
	// unset and the terminal background shows through, as before.
	bg := t.Dim.GetBackground()
	steps := t.PeakSteps()
	for i := range steps {
		steps[i] = steps[i].Background(bg)
	}
	w.peakSteps = steps
	w.peakReady = true
}

// peakStep picks the cooling-ramp step for a marker this far above its bar.
func peakStep(frac float64) int {
	switch {
	case frac < 0.08:
		return 2
	case frac < 0.2:
		return 1
	default:
		return 0
	}
}

func (w *Wave) renderBrailleRow(t Theme, disp, peak []float64) string {
	var b strings.Builder
	for i := 0; i < w.bars; i++ {
		n := int(math.Round(clampF(disp[i], 0, 1) * 8))
		var bits rune
		var style lipgloss.Style
		levels := 1 // the baseline occupies the bottom level
		if n == 0 {
			bits, style = brailleBaselineBits, t.Dim
		} else {
			bits, levels = brailleBar(disp[i])
			style = t.RampFor(n-1, 7)
		}
		if p := clampF(peak[i], 0, 1); p > 0.02 {
			if pLvl := int(math.Ceil(p * 4)); pLvl > levels {
				// True peak-hold: overlay the marker's dot pair above the
				// bar instead of erasing it — the silhouette stays and the
				// cell takes the cooling-ramp color while it floats there.
				bits += braillePeakDots(pLvl)
				style = w.peakSteps[peakStep(p-disp[i])]
			}
		}
		b.WriteString(style.Render(string(brailleBlank + bits)))
	}
	return b.String()
}

// renderBlockRow is the 7-bit fallback: one eighth-block cell per bar with
// the peak marker taking the cell over while it floats above the bar.
func (w *Wave) renderBlockRow(t Theme, disp, peak []float64) string {
	blocks := t.G.Blocks
	maxLvl := len(blocks) - 1
	var b strings.Builder
	for i := 0; i < w.bars; i++ {
		lvl := int(math.Round(clampF(disp[i], 0, 1) * float64(maxLvl)))
		pLvl := int(math.Round(clampF(peak[i], 0, 1) * float64(maxLvl)))
		switch {
		case pLvl > lvl && pLvl > 0:
			b.WriteString(w.peakSteps[peakStep(peak[i]-disp[i])].Render(string(blocks[pLvl])))
		case lvl > 0:
			b.WriteString(t.RampFor(lvl, maxLvl).Render(string(blocks[lvl])))
		default:
			b.WriteString(t.Dim.Render(string(blocks[0])))
		}
	}
	return b.String()
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
