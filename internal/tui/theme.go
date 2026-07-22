package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme: one saturated accent, three warm grays. NFO austerity, not a
// rainbow dashboard. Accent defaults to phosphor amber.
type Theme struct {
	AccentHex string

	Accent    lipgloss.Style
	AccentDim lipgloss.Style // ember: the accent at ~55%, for quiet warmth
	Bright    lipgloss.Style
	Mid       lipgloss.Style
	Dim       lipgloss.Style
	Invert    lipgloss.Style    // one-frame flashes
	Ramp      [8]lipgloss.Style // ember -> accent -> pale gold, per block level

	// Surfaces: the two background planes that make the UI read as engineered
	// hardware instead of floating glyphs. Derived from the accent so any
	// configured hue keeps a matching temperature.
	FootFill  lipgloss.Style // footer strip filler
	FootKey   lipgloss.Style // key chip on the strip
	FootLabel lipgloss.Style // key description on the strip
	SelFill   lipgloss.Style // selection bar filler
	SelText   lipgloss.Style // selected row text
	SelMeta   lipgloss.Style // selected row secondary text

	// Receiver faceplate. Unlike the footer surface, this is a substantial
	// piece of the composition: a warm-black body, a slightly raised status
	// readout, and a quiet metal edge.
	PanelFill   lipgloss.Style
	PanelRaised lipgloss.Style
	PanelBorder lipgloss.Style
	Love        lipgloss.Style
	LoveFill    lipgloss.Style

	G Glyphs

	breatheSteps []lipgloss.Color
}

// Glyphs is the material palette. The ascii set is bad-SSH insurance: every
// fancy glyph has a 7-bit stand-in.
type Glyphs struct {
	Blocks   []rune // eighth blocks, quiet -> loud
	Rule     string // header rule
	Band     string // dial band line
	Marker   string // dial marker
	Heart    string
	Ellipsis string
	Decrypt  []rune // scramble set for the tuning effect
	Dot      string // separator dot
	Cursor   string // prompt cursor block
	Tick     string // preset mark on the band line
	Knob     string // volume slider thumb
	Pointer  string // active row in browsable lists
	FrameTL  string
	FrameTR  string
	FrameBL  string
	FrameBR  string
	FrameV   string
	FrameH   string
}

var unicodeGlyphs = Glyphs{
	Blocks:   []rune("▁▂▃▄▅▆▇█"),
	Rule:     "▔",
	Band:     "─",
	Marker:   "╂",
	Heart:    "♥",
	Ellipsis: "…",
	Decrypt:  []rune("▓▒░#%&@$*+=~"),
	Dot:      "·",
	Cursor:   "█",
	Tick:     "┴",
	Knob:     "●",
	Pointer:  "›",
	FrameTL:  "╭",
	FrameTR:  "╮",
	FrameBL:  "╰",
	FrameBR:  "╯",
	FrameV:   "│",
	FrameH:   "─",
}

var asciiGlyphs = Glyphs{
	Blocks:   []rune("_.-:=+*#"),
	Rule:     "-",
	Band:     "-",
	Marker:   "|",
	Heart:    "<3",
	Ellipsis: "...",
	Decrypt:  []rune("#%&@$*+=~"),
	Dot:      "*",
	Cursor:   "_",
	Tick:     "+",
	Knob:     "O",
	Pointer:  ">",
	FrameTL:  "+",
	FrameTR:  "+",
	FrameBL:  "+",
	FrameBR:  "+",
	FrameV:   "|",
	FrameH:   "-",
}

func NewTheme(accentHex string, ascii bool) Theme {
	if !validHex(accentHex) {
		accentHex = "#FFB000"
	}
	r, g, b := hexRGB(accentHex)
	t := Theme{
		AccentHex: accentHex,
		Accent:    lipgloss.NewStyle().Foreground(lipgloss.Color(accentHex)),
		// Grays derive from the accent hue, not a fixed olive: an amber
		// accent yields warm stone grays, a violet accent cool lavender
		// ones. Warmth comes from pulling each gray a fraction toward the
		// accent; the dim floor is raised so "quiet" never means illegible.
		Bright: lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.92, 0.06))),
		Mid:    lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.62, 0.10))),
		Dim:    lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.42, 0.14))),
		Invert: lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color(accentHex)),
		G:      unicodeGlyphs,
	}
	if ascii {
		t.G = asciiGlyphs
	}
	for i := 0; i < 24; i++ {
		f := 0.4 + 0.6*float64(i)/23.0
		t.breatheSteps = append(t.breatheSteps, lipgloss.Color(rgbHex(
			int(float64(r)*f), int(float64(g)*f), int(float64(b)*f))))
	}
	// Surface planes: near-black warmed by the accent hue.
	surface := lipgloss.Color(rgbHex(int(float64(r)*0.09)+16, int(float64(g)*0.09)+16, int(float64(b)*0.09)+16))
	panel := lipgloss.Color(rgbHex(int(float64(r)*0.045)+8, int(float64(g)*0.045)+8, int(float64(b)*0.045)+8))
	raised := lipgloss.Color(rgbHex(int(float64(r)*0.11)+14, int(float64(g)*0.11)+14, int(float64(b)*0.11)+14))
	selBg := lipgloss.Color(rgbHex(int(float64(r)*0.16)+22, int(float64(g)*0.16)+22, int(float64(b)*0.16)+22))
	t.FootFill = lipgloss.NewStyle().Background(surface)
	t.FootKey = lipgloss.NewStyle().Background(surface).Bold(true).Foreground(lipgloss.Color(rgbHex(
		int(float64(r)*0.80), int(float64(g)*0.80), int(float64(b)*0.80))))
	t.FootLabel = lipgloss.NewStyle().Background(surface).Foreground(lipgloss.Color("#8A8474"))
	t.SelFill = lipgloss.NewStyle().Background(selBg)
	t.SelText = lipgloss.NewStyle().Background(selBg).Bold(true).Foreground(lipgloss.Color("#F0ECDF"))
	t.SelMeta = lipgloss.NewStyle().Background(selBg).Foreground(lipgloss.Color("#9A9480"))
	t.PanelFill = lipgloss.NewStyle().Background(panel)
	t.PanelRaised = lipgloss.NewStyle().Background(raised)
	t.PanelBorder = lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(
		int(float64(r)*0.42), int(float64(g)*0.42), int(float64(b)*0.42))))
	// A second emotional material: coral is reserved for love and warnings.
	// The core receiver identity remains monochrome amber.
	t.Love = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF684D"))
	t.LoveFill = lipgloss.NewStyle().Background(lipgloss.Color("#35170F"))
	// Ember: the accent cooled to 55%. Preset ticks, settled hearts —
	// warmth without shouting.
	t.AccentDim = lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(
		int(float64(r)*0.55), int(float64(g)*0.55), int(float64(b)*0.55))))
	// The wave ramp: one hue, many temperatures. Low bars smolder at a
	// visible ember (~35%), full bars hit the accent, peaks push toward
	// pale gold. The base must be visible: below ~30% most terminals
	// render the color as black.
	for i := 0; i < 8; i++ {
		f := float64(i) / 7.0
		rr, gg, bb := t.rampColor(f)
		t.Ramp[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(rr, gg, bb)))
	}
	return t
}

// OnPanel returns the ordinary foreground palette seated on the receiver's
// warm-black surface. Keeping this as a derived theme lets the wave and dial
// retain all of their existing color logic without punching black holes
// through the faceplate.
func (t Theme) OnPanel() Theme {
	bg := t.PanelFill.GetBackground()
	t.Accent = t.Accent.Background(bg)
	t.AccentDim = t.AccentDim.Background(bg)
	t.Bright = t.Bright.Background(bg)
	t.Mid = t.Mid.Background(bg)
	t.Dim = t.Dim.Background(bg)
	for i := range t.Ramp {
		t.Ramp[i] = t.Ramp[i].Background(bg)
	}
	return t
}

// BreatheStyle returns the accent style modulated by a slow sine — the
// sleeping-LED effect for idle mode. period ~6s.
func (t Theme) BreatheStyle(seconds float64) lipgloss.Style {
	phase := (math.Sin(2*math.Pi*seconds/6.0) + 1) / 2 // 0..1
	i := int(phase * float64(len(t.breatheSteps)-1))
	return lipgloss.NewStyle().Foreground(t.breatheSteps[i])
}

func validHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 16, 32)
	return err == nil
}

func hexRGB(s string) (int, int, int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return int(v >> 16 & 0xFF), int(v >> 8 & 0xFF), int(v & 0xFF)
}

func rgbHex(r, g, b int) string {
	cl := func(x int) int {
		if x < 0 {
			return 0
		}
		if x > 255 {
			return 255
		}
		return x
	}
	return fmt.Sprintf("#%02X%02X%02X", cl(r), cl(g), cl(b))
}

// grayHex builds a neutral gray of the given lightness (0..1 over 0-255),
// warmed by pulling a fraction of each channel toward the accent. That
// fraction is what stops the grays reading as olive dirt: the gray family
// shares the accent's temperature instead of fighting it.
func grayHex(ar, ag, ab int, lightness, warmth float64) string {
	v := lightness * 255
	return rgbHex(
		int(v*(1-warmth)+float64(ar)*warmth),
		int(v*(1-warmth)+float64(ag)*warmth),
		int(v*(1-warmth)+float64(ab)*warmth),
	)
}

// rampColor is the accent at position f (0..1) along the ember→accent→pale
// ramp. 0 is a visible ember (never near-black), 1 the accent, >0.6 pushes
// toward pale gold. Used by the wave's per-level gradient and the dial.
func (t Theme) rampColor(f float64) (int, int, int) {
	r, g, b := hexRGB(t.AccentHex)
	if f <= 0.6 {
		k := 0.35 + (f/0.6)*0.65
		return int(float64(r) * k), int(float64(g) * k), int(float64(b) * k)
	}
	k := (f - 0.6) / 0.4 * 0.45
	return int(float64(r) + (255-float64(r))*k),
		int(float64(g) + (255-float64(g))*k),
		int(float64(b) + (255-float64(b))*k)
}

// RampFor returns the ramp style for a given height level, with dark-mode
// readability: at small heights the base of the bar uses a dimmer step so
// short bars don't outshine tall ones. level and maxLevel are 0-indexed.
func (t Theme) RampFor(level, maxLevel int) lipgloss.Style {
	if maxLevel < 1 {
		maxLevel = 1
	}
	f := float64(level) / float64(maxLevel)
	idx := int(f * float64(len(t.Ramp)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(t.Ramp) {
		idx = len(t.Ramp) - 1
	}
	return t.Ramp[idx]
}

// PeakSteps is the cooling ramp for wave peak ticks: a fresh peak starts at
// the accent and falls through ember to invisible over the steps.
func (t Theme) PeakSteps() [3]lipgloss.Style {
	r1, g1, b1 := t.rampColor(0.6)
	r2, g2, b2 := t.rampColor(0.35)
	r3, g3, b3 := t.rampColor(0.18)
	return [3]lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r1, g1, b1))),
		lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r2, g2, b2))),
		lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r3, g3, b3))),
	}
}
