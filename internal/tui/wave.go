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
	rows         int       // stacked cells per channel; 0 means 1
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
	w := &Wave{levelAt: -999, rows: 1}
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

// maxWaveRows caps how tall one channel's bay can get. Three cells is
// twelve levels, which is enough to read loudness as a shape rather than a
// binary. Four was tried and rejected: a meter should keep headroom above
// typical program level, and at sixteen levels that headroom is a whole
// terminal row sitting permanently empty inside a drawn frame, which reads
// as a rendering fault rather than as restraint.
const maxWaveRows = 3

// Rows is the number of stacked cells per channel; Render always returns
// twice this many strings.
func (w *Wave) Rows() int {
	if w.rows < 1 {
		return 1
	}
	return w.rows
}

// SetRows sets the per-channel height of the bay, clamped to [1, maxWaveRows].
func (w *Wave) SetRows(n int) {
	if n < 1 {
		n = 1
	}
	if n > maxWaveRows {
		n = maxWaveRows
	}
	w.rows = n
}

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
		// True peak-hold: every bar's marker floats at least at the real
		// digital peak. Tilted by bassWeight so the ceiling follows the
		// bay's own shape — stamped flat it drew one continuous dotted rule
		// across the meter, which at this resolution reads as a divider
		// rather than as a reading.
		if live {
			ceiling := w.peakDisp * w.bassWeight(i)
			if w.peakL[i] < ceiling {
				w.peakL[i] = ceiling
			}
			if w.peakR[i] < ceiling {
				w.peakR[i] = ceiling
			}
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
		// Program level maps most of the bay: typical broadcast loudness
		// should sit around two-thirds up with the top reserved for peaks,
		// rather than the old 0.15+0.95x which left everything real
		// clustered in the bottom third.
		ampL = math.Min(0.20+1.0*w.lvlDispL, 1)
		ampR = math.Min(0.20+1.0*w.lvlDispR, 1)
	}
	// Texture varies the bar *around* the measured level rather than
	// scaling it down. The old (0.5 + 0.5*s) averaged 0.5, so every bar was
	// halved before bassWeight halved it again and the meter never left the
	// bottom of its range whatever the music did. This keeps the same shape
	// with a floor under it: mean ~0.72 of level, peaks at full.
	bw := w.bassWeight(i)
	tex := w.energy * (0.72 + 0.28*s) * bw
	return clampF(tex*ampL, 0, 1), clampF(tex*ampR, 0, 1)
}

// bassWeight shapes the synthetic texture so the left (low-frequency) end
// of the wave responds harder to the loudness signal. One amplitude number
// can't be a spectrum, but it can lean on the perceptual shortcut that
// bass energy dominates loudness: bars at the left swing ~1.0, falling to
// ~0.74 at the right.
//
// The old 0.55 right-hand floor was too steep for the resolution available:
// combined with the texture it capped the rightmost bars at two of four
// levels, so the right third of the bay sat dead no matter how loud the
// music was. The tilt should be a lean, not a cliff.
func (w *Wave) bassWeight(i int) float64 {
	if w.bars <= 1 {
		return 1.0
	}
	x := float64(i) / float64(w.bars-1) // 0 left -> 1 right
	return 1.0 - 0.26*x
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

// brailleLevelsPerCell is the vertical resolution of one braille cell. The
// cell holds 8 dots but they are 4 rows of 2 columns, so a single row of
// braille can express exactly four heights, not eight — stacking cells is
// the only way to get more.
const brailleLevelsPerCell = 4

// brailleBar maps a 0..1 height onto one cell's 8 dots (without the U+2800
// base), filled bottom-up, both columns per level, the odd half-dot going
// to the left column. Returns the dot bits and how many vertical levels
// they occupy (0..4).
func brailleBar(h float64) (rune, int) {
	return brailleCell(clampF(h, 0, 1) * brailleLevelsPerCell)
}

// brailleCell fills one cell with `levels` of height (0..4, fractional).
// The fractional remainder lights the left column of the next level up,
// which reads as a half step.
func brailleCell(levels float64) (rune, int) {
	n := int(math.Round(clampF(levels, 0, brailleLevelsPerCell) * 2))
	full, half := n/2, n%2
	var bits rune
	for lvl := 1; lvl <= full; lvl++ {
		bits += brailleDots[lvl][0] + brailleDots[lvl][1]
	}
	if half == 1 && full+1 < len(brailleDots) {
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
// The total is always Rows()*2 — the left channel's block, then the right's.
func (w *Wave) Render(t Theme) []string {
	if w.bars < 1 {
		return make([]string, w.Rows()*2)
	}
	w.ensurePeakSteps(t)
	if t.G.Blocks[0] == '_' { // 7-bit palette: braille would be mojibake
		return append(
			w.renderBlockChannel(t, w.dispL, w.peakL),
			w.renderBlockChannel(t, w.dispR, w.peakR)...)
	}
	return append(
		w.renderBrailleChannel(t, w.dispL, w.peakL),
		w.renderBrailleChannel(t, w.dispR, w.peakR)...)
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

// renderBrailleChannel draws one channel across w.rows stacked cells,
// top-first. Total vertical resolution is rows*4 levels, so a two-row bay
// reads eight steps and a four-row bay sixteen — the difference between a
// meter that flickers between "flat" and "one dot" and one you can actually
// watch. Each cell is colored by the ramp position of the bar's overall
// height, so a tall bar is hot along its whole length rather than only at
// the top.
func (w *Wave) renderBrailleChannel(t Theme, disp, peak []float64) []string {
	rows := w.Rows()
	total := float64(rows * brailleLevelsPerCell)
	out := make([]string, rows)
	builders := make([]strings.Builder, rows)
	for i := 0; i < w.bars; i++ {
		h := clampF(disp[i], 0, 1)
		filled := h * total
		p := clampF(peak[i], 0, 1)
		peakLvl := p * total
		barStyle := t.Dim
		if filled > 0.01 {
			barStyle = t.RampFor(int(h*7.999), 7)
		}
		for row := 0; row < rows; row++ {
			// row 0 is the top cell; cellFloor is how many levels sit below it.
			cellFloor := float64((rows - 1 - row) * brailleLevelsPerCell)
			bits, levels := brailleCell(filled - cellFloor)
			style := barStyle
			if levels == 0 {
				style = t.Dim
				if row == rows-1 {
					bits = brailleBaselineBits // the flatline lives on the floor
					levels = 1
				}
			}
			// True peak-hold: overlay the marker above the bar rather than
			// erasing it, so the silhouette survives and the cell takes the
			// cooling-ramp color while the tick floats there.
			if p > 0.02 {
				if pl := int(math.Ceil(peakLvl - cellFloor)); pl >= 1 && pl <= brailleLevelsPerCell && pl > levels {
					bits += braillePeakDots(pl)
					style = w.peakSteps[peakStep(p-h)]
				}
			}
			builders[row].WriteString(style.Render(string(brailleBlank + bits)))
		}
	}
	for row := range builders {
		out[row] = builders[row].String()
	}
	return out
}

// renderBlockChannel is the 7-bit fallback: eighth-block cells, stacked the
// same way, with the peak marker taking a cell over while it floats above
// the bar.
func (w *Wave) renderBlockChannel(t Theme, disp, peak []float64) []string {
	blocks := t.G.Blocks
	maxLvl := len(blocks) - 1
	rows := w.Rows()
	total := float64(rows * maxLvl)
	out := make([]string, rows)
	builders := make([]strings.Builder, rows)
	for i := 0; i < w.bars; i++ {
		h := clampF(disp[i], 0, 1)
		filled := h * total
		peakFilled := clampF(peak[i], 0, 1) * total
		for row := 0; row < rows; row++ {
			cellFloor := float64((rows - 1 - row) * maxLvl)
			lvl := int(math.Round(clampF(filled-cellFloor, 0, float64(maxLvl))))
			pLvl := int(math.Round(clampF(peakFilled-cellFloor, 0, float64(maxLvl))))
			switch {
			case pLvl > lvl && pLvl > 0:
				builders[row].WriteString(w.peakSteps[peakStep(peak[i]-h)].Render(string(blocks[pLvl])))
			case lvl > 0:
				builders[row].WriteString(t.RampFor(int(h*float64(maxLvl)), maxLvl).Render(string(blocks[lvl])))
			default:
				builders[row].WriteString(t.Dim.Render(string(blocks[0])))
			}
		}
	}
	for row := range builders {
		out[row] = builders[row].String()
	}
	return out
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
