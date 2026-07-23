package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"screech/internal/core"
)

// Theme: one saturated accent, three warm grays. NFO austerity, not a
// rainbow dashboard. Accent defaults to phosphor amber.
type Theme struct {
	Name      string
	AccentHex string
	// AccentHex2 is the optional second gradient stop. Empty keeps the
	// classic single-hue ramp (ember -> accent -> pale gold); set, the ramp
	// top blends accent -> AccentHex2 instead.
	AccentHex2 string

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

const (
	ThemeReceiver = "receiver"
	ThemeAustere  = "austere"
	ThemeVerdant  = "verdant"
	ThemeAzure    = "azure"
	ThemeViolet   = "violet"
	ThemeRose     = "rose"
	ThemeCharm    = "charm"
	ThemeLagoon   = "lagoon"
)

type ThemeChoice struct {
	ID          string
	Label       string
	Description string
}

var themeChoices = []ThemeChoice{
	{ID: ThemeReceiver, Label: "Receiver", Description: "Warm phosphor and ember"},
	{ID: ThemeAustere, Label: "Austere", Description: "Strict monochrome signal"},
	{ID: ThemeVerdant, Label: "Verdant", Description: "Green phosphor terminal glow"},
	{ID: ThemeAzure, Label: "Azure", Description: "Cool broadcast blue"},
	{ID: ThemeViolet, Label: "Violet", Description: "Deep lavender static"},
	{ID: ThemeRose, Label: "Rose", Description: "Soft neon bloom"},
	{ID: ThemeCharm, Label: "Charm", Description: "Signature pink-violet gradient"},
	{ID: ThemeLagoon, Label: "Lagoon", Description: "Mint to blue, the Charm reef"},
}

func ThemeChoices() []ThemeChoice {
	out := make([]ThemeChoice, len(themeChoices))
	copy(out, themeChoices)
	return out
}

func normalizeThemeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, choice := range themeChoices {
		if name == choice.ID {
			return name
		}
	}
	return ThemeReceiver
}

func NewNamedTheme(name, accentHex string, ascii bool) Theme {
	switch normalizeThemeName(name) {
	case ThemeAustere:
		return NewAustereTheme(ascii)
	case ThemeVerdant:
		return NewHueTheme(ThemeVerdant, "#45DC78", ascii)
	case ThemeAzure:
		return NewHueTheme(ThemeAzure, "#4FA8F5", ascii)
	case ThemeViolet:
		return NewHueTheme(ThemeViolet, "#A98BFF", ascii)
	case ThemeRose:
		return NewHueTheme(ThemeRose, "#FF7AB8", ascii)
	case ThemeCharm:
		return NewGradientTheme(ThemeCharm, "#FF7EB6", "#B48CFF", ascii)
	case ThemeLagoon:
		return NewGradientTheme(ThemeLagoon, "#3FE8B0", "#4FA8F5", ascii)
	default:
		return NewTheme(accentHex, ascii)
	}
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
		Name:      ThemeReceiver,
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

// NewAustereTheme removes hue entirely. It keeps the same material hierarchy
// as the receiver—canvas, body, raised readout, selection—but expresses it
// only through luminance. Love is white rather than a semantic accent.
func NewAustereTheme(ascii bool) Theme {
	return newAustereThemeScaled(ascii, 1.0)
}

// newAustereThemeScaled is NewAustereTheme with every hardcoded luminance
// multiplied by lum. Daypart temperature for a monochrome theme can only be
// a lightness shift; hue and warmth have no meaning without hue.
func newAustereThemeScaled(ascii bool, lum float64) Theme {
	g := func(hex string) lipgloss.Color {
		r, gg, b := hexRGB(hex)
		return lipgloss.Color(rgbHex(int(float64(r)*lum), int(float64(gg)*lum), int(float64(b)*lum)))
	}
	t := NewTheme("#D8D8D8", ascii)
	t.Name = ThemeAustere
	t.AccentHex = string(g("#D8D8D8"))
	t.Accent = lipgloss.NewStyle().Foreground(g("#D8D8D8"))
	t.AccentDim = lipgloss.NewStyle().Foreground(g("#858585"))
	t.Bright = lipgloss.NewStyle().Foreground(g("#EEEEEE"))
	t.Mid = lipgloss.NewStyle().Foreground(g("#A0A0A0"))
	t.Dim = lipgloss.NewStyle().Foreground(g("#626262"))
	t.Invert = lipgloss.NewStyle().Foreground(g("#080808")).Background(g("#E8E8E8"))

	panel := g("#0B0B0B")
	raised := g("#181818")
	footer := g("#121212")
	selected := g("#292929")
	t.PanelFill = lipgloss.NewStyle().Background(panel)
	t.PanelRaised = lipgloss.NewStyle().Background(raised)
	t.PanelBorder = lipgloss.NewStyle().Foreground(g("#686868"))
	t.FootFill = lipgloss.NewStyle().Background(footer)
	t.FootKey = lipgloss.NewStyle().Background(footer).Bold(true).Foreground(g("#E4E4E4"))
	t.FootLabel = lipgloss.NewStyle().Background(footer).Foreground(g("#808080"))
	t.SelFill = lipgloss.NewStyle().Background(selected)
	t.SelText = lipgloss.NewStyle().Background(selected).Bold(true).Foreground(g("#F2F2F2"))
	t.SelMeta = lipgloss.NewStyle().Background(selected).Foreground(g("#A0A0A0"))
	t.Love = lipgloss.NewStyle().Foreground(g("#F4F4F4"))
	t.LoveFill = lipgloss.NewStyle().Background(selected)

	t.breatheSteps = t.breatheSteps[:0]
	for i := 0; i < 24; i++ {
		v := int(float64(92+int(float64(i)/23.0*124)) * lum)
		t.breatheSteps = append(t.breatheSteps, lipgloss.Color(rgbHex(v, v, v)))
	}
	for i := 0; i < len(t.Ramp); i++ {
		v := int(float64(72+int(float64(i)/float64(len(t.Ramp)-1)*166)) * lum)
		t.Ramp[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(v, v, v)))
	}
	return t
}

// daypartTemper maps a core daypart to a brightness multiplier and a warmth
// bias (-1 cool .. +1 warm). Midday runs brighter and cooler; night dims and
// warms toward ember. Morning carries a gentle warm lift, evening is the
// neutral reference the themes were authored against.
func daypartTemper(dp string) (bright, warm float64) {
	switch dp {
	case core.DaypartMorning:
		return 1.03, 0.5
	case core.DaypartDay:
		return 1.09, -0.6
	case core.DaypartNight:
		return 0.82, 1.0
	default: // evening and anything unforeseen
		return 1.0, 0.0
	}
}

// temperHex applies a daypart to one hex color: bright scales every channel,
// warm pushes red up and blue down (green barely moves, like a physical
// dimmer on a warm filament).
func temperHex(hex string, bright, warm float64) string {
	r, g, b := hexRGB(hex)
	return rgbHex(
		int(float64(r)*bright+warm*36),
		int(float64(g)*bright+warm*8),
		int(float64(b)*bright-warm*36),
	)
}

// NewDaypartTheme resolves a named theme exactly as NewNamedTheme does, then
// tempers its hue signature for the time of day: brighter and cooler midday,
// dimmer and warmer at night. Austere, having no hue, takes the lightness
// shift only. The persisted theme choice is untouched — this is a viewing
// condition, not a setting.
func NewDaypartTheme(name, accentHex string, ascii bool, dp string) Theme {
	bright, warm := daypartTemper(dp)
	if normalizeThemeName(name) == ThemeAustere {
		return newAustereThemeScaled(ascii, bright)
	}
	t := NewNamedTheme(name, accentHex, ascii)
	if bright == 1.0 && warm == 0.0 {
		return t
	}
	accent := temperHex(t.AccentHex, bright, warm)
	if t.AccentHex2 != "" {
		return NewGradientTheme(t.Name, accent, temperHex(t.AccentHex2, bright, warm), ascii)
	}
	return NewHueTheme(t.Name, accent, ascii)
}

// NewHueTheme is a receiver finish in a fixed hue. Every gray, surface, and
// ramp derives from the accent exactly as the configurable receiver theme
// derives them — only the hue (and the persisted name) differ, so the
// faceplate keeps its engineered-hardware read in any color.
func NewHueTheme(name, accentHex string, ascii bool) Theme {
	t := NewTheme(accentHex, ascii)
	t.Name = name
	return t
}

// NewGradientTheme is a receiver finish with a two-hue signature: the ramp
// top blends from the accent into a second hue instead of pale gold. Grays
// and surfaces still derive from the first accent, so the faceplate keeps a
// single home temperature while the signal instruments carry the gradient.
func NewGradientTheme(name, accentHex, accentHex2 string, ascii bool) Theme {
	t := NewTheme(accentHex, ascii)
	t.Name = name
	if validHex(accentHex2) {
		t.AccentHex2 = accentHex2
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

// rampColor is the accent at position f (0..1) along the ramp. 0 is a
// visible ember (never near-black), 0.6 the accent. Above 0.6 the top pushes
// toward pale gold, or — when AccentHex2 is set — blends all the way into
// the second hue. Used by the wave's per-level gradient and the dial.
func (t Theme) rampColor(f float64) (int, int, int) {
	r, g, b := hexRGB(t.AccentHex)
	if f <= 0.6 {
		k := 0.35 + (f/0.6)*0.65
		return int(float64(r) * k), int(float64(g) * k), int(float64(b) * k)
	}
	if t.AccentHex2 != "" {
		r2, g2, b2 := hexRGB(t.AccentHex2)
		k := (f - 0.6) / 0.4
		return int(float64(r) + (float64(r2)-float64(r))*k),
			int(float64(g) + (float64(g2)-float64(g))*k),
			int(float64(b) + (float64(b2)-float64(b))*k)
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
