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

	// rampFloor is the ramp's bottom color, precomputed. The lift that
	// produces it is a binary search, and rampColor runs per-cell per-frame
	// on the header rule and the dial, so it must not be done inline.
	// cacheRampFloor has to be re-run by any constructor that reassigns
	// AccentHex after NewTheme.
	rampFloor [3]int
}

const (
	ThemeReceiver = "receiver"
	ThemeAustere  = "austere"
	ThemeVerdant  = "verdant"
	ThemeAzure    = "azure"
	ThemeViolet   = "violet"
	ThemeSlayer   = "slayer"
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
	{ID: ThemeSlayer, Label: "Slayer", Description: "Blood red on black iron"},
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
	case ThemeSlayer:
		// Blood red into heated iron. The accent was #DC143C, which is CSS
		// "crimson" and sits at hue 348 — twelve degrees into the magenta
		// side of red, so every gray, surface and ramp step derived from it
		// came out rose. A second stop is unusual for a single-hue finish,
		// but red is the one hue where the normal ramp top fails: pushing
		// any red toward pale desaturates it through salmon, and the wave
		// is six rows of that. Running it to ember instead reads as worked
		// metal, which is the point of the finish.
		return NewGradientTheme(ThemeSlayer, "#C21A16", "#FF6A18", ascii)
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
	Seam     string // gutter between the two faceplate bays
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
	Seam:     "│",
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
	Seam:     ":",
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
		// Lightness steps are chosen against the panel surface these sit on:
		// dim carries every microlabel (TRACK, SIGNAL, BROADCAST, the preset
		// legend), so it has to clear WCAG AA rather than merely look quiet.
		// 0.42 measured 3.2–4.2 across the finishes, under the 4.5 body
		// threshold in all of them. See TestThemeContrastFloors.
		Bright: lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.93, 0.06))),
		Mid:    lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.70, 0.10))),
		Dim:    lipgloss.NewStyle().Foreground(lipgloss.Color(grayHex(r, g, b, 0.52, 0.14))),
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
	// Footer and selection text derive from the accent like everything else.
	// These were fixed warm grays, which meant the blue and green finishes
	// wore tan labels — the one place the palette didn't follow its hue.
	// The lightness steps sit above the panel equivalents because both sit
	// on lighter surfaces than the faceplate body.
	t.FootLabel = lipgloss.NewStyle().Background(surface).Foreground(lipgloss.Color(grayHex(r, g, b, 0.60, 0.10)))
	t.SelFill = lipgloss.NewStyle().Background(selBg)
	t.SelText = lipgloss.NewStyle().Background(selBg).Bold(true).Foreground(lipgloss.Color(grayHex(r, g, b, 0.95, 0.05)))
	t.SelMeta = lipgloss.NewStyle().Background(selBg).Foreground(lipgloss.Color(grayHex(r, g, b, 0.66, 0.10)))
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
	t.buildRamp()
	return t
}

// buildRamp bakes the eight ramp styles the wave, bevel and density strip
// render with. One hue, many temperatures: low bars smolder at a visible
// ember, full bars hit the accent, peaks push toward pale — or, when a
// second stop is set, all the way into the second hue.
//
// Any constructor that changes AccentHex or AccentHex2 must call this
// again. Not doing so is how the two-hue finishes ended up with a
// single-hue meter: NewTheme baked the array, and the second stop was
// assigned afterwards, so only the callers that recompute rampColor live
// (the header rule, the dial bleed) ever saw the gradient.
func (t *Theme) buildRamp() {
	// The floor is a luminance target, not a fraction of the accent — most
	// of the meter lives down there, so it has to be legible in every hue.
	t.cacheRampFloor()
	for i := 0; i < len(t.Ramp); i++ {
		f := float64(i) / float64(len(t.Ramp)-1)
		rr, gg, bb := t.rampColor(f)
		t.Ramp[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(rr, gg, bb)))
	}
}

// NewAustereTheme removes hue entirely. It keeps the same material hierarchy
// as the receiver—canvas, body, raised readout, selection—but expresses it
// only through luminance. Love is white rather than a semantic accent.
func NewAustereTheme(ascii bool) Theme {
	return newAustereThemeScaled(ascii, 1.0)
}

// newAustereThemeScaled is NewAustereTheme with its luminance multiplied by
// lum. Daypart temperature for a monochrome theme can only be a lightness
// shift; hue and warmth have no meaning without hue.
//
// Surfaces and text scale at different rates on purpose. Multiplying both by
// the same factor looks like it should preserve contrast and doesn't: these
// backgrounds are dark enough to sit in sRGB's linear toe while the text
// sits up in the gamma curve, so an even 0.82 at night cost the microlabels
// a fifth of their contrast and dropped them under AA. The hue finishes
// dodge this because they re-derive their grays from a fixed lightness; the
// monochrome one has to be told. Text easing down more slowly than the
// surface is also just how a real backlit panel behaves.
func newAustereThemeScaled(ascii bool, lum float64) Theme {
	scale := func(hex string, k float64) lipgloss.Color {
		r, gg, b := hexRGB(hex)
		return lipgloss.Color(rgbHex(int(float64(r)*k), int(float64(gg)*k), int(float64(b)*k)))
	}
	g := func(hex string) lipgloss.Color { return scale(hex, lum) }
	textLum := 0.5 + 0.5*lum
	gt := func(hex string) lipgloss.Color { return scale(hex, textLum) }

	t := NewTheme("#D8D8D8", ascii)
	t.Name = ThemeAustere
	t.AccentHex = string(gt("#D8D8D8"))
	t.Accent = lipgloss.NewStyle().Foreground(gt("#D8D8D8"))
	t.AccentDim = lipgloss.NewStyle().Foreground(gt("#858585"))
	t.Bright = lipgloss.NewStyle().Foreground(gt("#EFEFEF"))
	t.Mid = lipgloss.NewStyle().Foreground(gt("#B2B2B2"))
	// #626262 measured 3.2 against the austere panel, the worst of any
	// finish. Monochrome has no hue to hide behind, so the microlabels have
	// to earn their legibility from luminance alone.
	t.Dim = lipgloss.NewStyle().Foreground(gt("#8C8C8C"))
	t.Invert = lipgloss.NewStyle().Foreground(g("#080808")).Background(gt("#E8E8E8"))

	panel := g("#0B0B0B")
	raised := g("#181818")
	footer := g("#121212")
	selected := g("#292929")
	t.PanelFill = lipgloss.NewStyle().Background(panel)
	t.PanelRaised = lipgloss.NewStyle().Background(raised)
	t.PanelBorder = lipgloss.NewStyle().Foreground(gt("#686868"))
	t.FootFill = lipgloss.NewStyle().Background(footer)
	t.FootKey = lipgloss.NewStyle().Background(footer).Bold(true).Foreground(gt("#E4E4E4"))
	t.FootLabel = lipgloss.NewStyle().Background(footer).Foreground(gt("#909090"))
	t.SelFill = lipgloss.NewStyle().Background(selected)
	t.SelText = lipgloss.NewStyle().Background(selected).Bold(true).Foreground(gt("#F2F2F2"))
	t.SelMeta = lipgloss.NewStyle().Background(selected).Foreground(gt("#AEAEAE"))
	t.Love = lipgloss.NewStyle().Foreground(gt("#F4F4F4"))
	t.LoveFill = lipgloss.NewStyle().Background(selected)

	t.cacheRampFloor() // AccentHex was reassigned above; the floor must follow
	t.breatheSteps = t.breatheSteps[:0]
	for i := 0; i < 24; i++ {
		v := int(float64(92+int(float64(i)/23.0*124)) * lum)
		t.breatheSteps = append(t.breatheSteps, lipgloss.Color(rgbHex(v, v, v)))
	}
	// The monochrome ramp floor was 72, which measured 2.2 against the
	// austere panel — under the 3:1 that any meaningful graphical element
	// needs, and the meter spends most of its time on that step. 98 is the
	// gray that clears it.
	for i := 0; i < len(t.Ramp); i++ {
		v := int(float64(98+int(float64(i)/float64(len(t.Ramp)-1)*140)) * textLum)
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

// maxDaypartHueDrift caps how far the daypart temper may rotate a hue.
//
// Warmth is applied as a channel push — red up, blue down — which rotates
// the hue as a side effect. For most accents that side effect is under two
// degrees and reads as warmth. For an accent already at the red end of the
// wheel there is nowhere to rotate but into magenta: the midday "cool" push
// moved the crimson finish to hue 339, which is pink. A finish should not
// change which colour it is depending on the time of day.
const maxDaypartHueDrift = 6.0

// temperHex applies a daypart to one hex color: bright scales every channel,
// warm pushes red up and blue down (green barely moves, like a physical
// dimmer on a warm filament), and the resulting hue is held within
// maxDaypartHueDrift of where it started.
func temperHex(hex string, bright, warm float64) string {
	r, g, b := hexRGB(hex)
	tr := int(float64(r)*bright + warm*36)
	tg := int(float64(g)*bright + warm*8)
	tb := int(float64(b)*bright - warm*36)
	tr, tg, tb = clampByte(tr), clampByte(tg), clampByte(tb)

	h0, s0, _ := rgbToHSL(r, g, b)
	h1, s1, l1 := rgbToHSL(tr, tg, tb)
	if s0 < 0.02 || s1 < 0.02 {
		return rgbHex(tr, tg, tb) // no hue worth preserving
	}
	drift := math.Mod(h1-h0+540, 360) - 180 // signed shortest path
	if math.Abs(drift) > maxDaypartHueDrift {
		h1 = math.Mod(h0+math.Copysign(maxDaypartHueDrift, drift)+360, 360)
		tr, tg, tb = hslToRGB(h1, s1, l1)
	}
	return rgbHex(tr, tg, tb)
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// rgbToHSL and hslToRGB exist so the daypart temper can reason about hue
// directly. Channel arithmetic alone cannot tell warmth from rotation.
func rgbToHSL(r, g, b int) (h, s, l float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	l = (mx + mn) / 2
	d := mx - mn
	if d == 0 {
		return 0, 0, l
	}
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return
}

func hslToRGB(h, s, l float64) (int, int, int) {
	if s <= 0 {
		v := int(math.Round(l * 255))
		return v, v, v
	}
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var rf, gf, bf float64
	switch {
	case h < 60:
		rf, gf, bf = c, x, 0
	case h < 120:
		rf, gf, bf = x, c, 0
	case h < 180:
		rf, gf, bf = 0, c, x
	case h < 240:
		rf, gf, bf = 0, x, c
	case h < 300:
		rf, gf, bf = x, 0, c
	default:
		rf, gf, bf = c, 0, x
	}
	return int(math.Round((rf + m) * 255)),
		int(math.Round((gf + m) * 255)),
		int(math.Round((bf + m) * 255))
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
		t.buildRamp() // the baked ramp predates the second stop
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

// --- perceptual luminance ---
//
// Screen colors are not equally bright at equal channel values: green
// carries ~71% of perceived luminance, red ~21%, blue ~7%. Scaling RGB
// uniformly therefore produces wildly different legibility depending on the
// accent's hue, which is why a crimson finish used to read three shades
// darker than an amber one at the same nominal step. Everything below works
// in relative luminance so one rule holds across every finish.

// srgbToLinear undoes the sRGB transfer function for one channel.
func srgbToLinear(v int) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// relLuminance is the WCAG relative luminance of an RGB triple, 0..1.
func relLuminance(r, g, b int) float64 {
	return 0.2126*srgbToLinear(r) + 0.7152*srgbToLinear(g) + 0.0722*srgbToLinear(b)
}

// contrastRatio is the WCAG contrast between two RGB triples, 1..21.
func contrastRatio(r1, g1, b1, r2, g2, b2 int) float64 {
	a, b := relLuminance(r1, g1, b1), relLuminance(r2, g2, b2)
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// liftToLuminance brightens a color along its own hue until it reaches the
// target relative luminance. Scaling is tried first so the hue survives
// intact; only when a fully saturated version still falls short (deep reds
// and blues simply cannot carry much luminance) does it blend toward white.
// A color already at or above the target is returned untouched — this only
// ever raises a floor, never dims anything.
func liftToLuminance(r, g, b int, target float64) (int, int, int) {
	if target <= 0 || relLuminance(r, g, b) >= target {
		return r, g, b
	}
	// Phase one: scale up to the point where the first channel saturates.
	peak := maxI(maxI(r, g), b)
	if peak > 0 {
		maxScale := 255 / float64(peak)
		lo, hi := 1.0, maxScale
		for i := 0; i < 24; i++ {
			mid := (lo + hi) / 2
			if relLuminance(int(float64(r)*mid), int(float64(g)*mid), int(float64(b)*mid)) < target {
				lo = mid
			} else {
				hi = mid
			}
		}
		sr, sg, sb := int(float64(r)*hi), int(float64(g)*hi), int(float64(b)*hi)
		if relLuminance(sr, sg, sb) >= target {
			return sr, sg, sb
		}
		r, g, b = sr, sg, sb
	}
	// Phase two: the hue is maxed out and still too dark. Wash toward white.
	lo, hi := 0.0, 1.0
	mix := func(k float64) (int, int, int) {
		return int(float64(r) + (255-float64(r))*k),
			int(float64(g) + (255-float64(g))*k),
			int(float64(b) + (255-float64(b))*k)
	}
	for i := 0; i < 24; i++ {
		mid := (lo + hi) / 2
		if relLuminance(mix(mid)) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return mix(hi)
}

// grayHex builds a gray of the given lightness (0..1 over 0-255), warmed by
// pulling a fraction of each channel toward the accent. That fraction is
// what stops the grays reading as olive dirt: the gray family shares the
// accent's temperature instead of fighting it.
//
// The warmed result is then renormalized back to the neutral gray's
// luminance. Without that step, warming toward a dark accent quietly *dims*
// the gray — pulling 14% toward crimson drops the green channel hardest,
// and green is most of what the eye reads as brightness. Renormalizing is
// what makes one lightness number mean the same legibility in all eight
// finishes.
func grayHex(ar, ag, ab int, lightness, warmth float64) string {
	v := lightness * 255
	r := int(v*(1-warmth) + float64(ar)*warmth)
	g := int(v*(1-warmth) + float64(ag)*warmth)
	b := int(v*(1-warmth) + float64(ab)*warmth)
	nv := int(v)
	return rgbHex(liftToLuminance(r, g, b, relLuminance(nv, nv, nv)))
}

// rampFloorLuminance is the relative luminance the bottom of the wave ramp
// must reach. WCAG asks 3:1 for graphical objects that carry meaning, and
// against these near-black panels (luminance ~0.005) that lands here.
//
// It matters more than it sounds: most of the meter sits near the bottom of
// the ramp most of the time, so the low step is not a garnish, it's the
// majority of what the eye actually sees. At 35% of accent it measured 1.3
// on the crimson finish — invisible.
const rampFloorLuminance = 0.12

// rampLuminanceGain is how much brighter the ramp top sits than the accent,
// and rampTopLuminance caps it short of white. These are relative
// luminance, not HSL lightness, because lightness is not what the eye
// reads: rotating a hue from mint toward blue loses perceived brightness
// even as lightness rises, since blue carries 7% of luminance and green
// carries 71%.
const (
	rampLuminanceGain = 0.34
	rampTopLuminance  = 0.62
	// The rise is guaranteed even for an accent already at the cap, and
	// bounded short of white so a peak still reads as a color.
	rampMinLuminanceRise = 0.10
	rampCeilLuminance    = 0.88
)

// setLuminance rescales a color to a target relative luminance, preserving
// its hue and saturation. Used to hold the two-hue ramp monotonic while its
// hue travels.
func setLuminance(r, g, b int, target float64) (int, int, int) {
	h, s, _ := rgbToHSL(r, g, b)
	lo, hi := 0.0, 1.0
	for i := 0; i < 24; i++ {
		mid := (lo + hi) / 2
		rr, gg, bb := hslToRGB(h, s, mid)
		if relLuminance(rr, gg, bb) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hslToRGB(h, s, hi)
}

// cacheRampFloor computes the ramp's bottom color: the accent cooled to an
// ember, then lifted along its own hue until it clears rampFloorLuminance.
// Interpolating up from a precomputed floor (rather than lifting each step
// independently) is what keeps the ramp monotonic — per-step lifting made
// the crimson finish brighten, dip, then brighten again.
func (t *Theme) cacheRampFloor() {
	r, g, b := hexRGB(t.AccentHex)
	t.rampFloor = [3]int{}
	t.rampFloor[0], t.rampFloor[1], t.rampFloor[2] = liftToLuminance(
		int(float64(r)*0.35), int(float64(g)*0.35), int(float64(b)*0.35), rampFloorLuminance)
}

// rampColor is the accent at position f (0..1) along the ramp. 0 is a
// visible ember (never near-black), 0.6 the accent. Above 0.6 the top pushes
// toward pale gold, or — when AccentHex2 is set — blends all the way into
// the second hue. Used by the wave's per-level gradient and the dial.
//
// An accent darker than the ember floor (a user-configured near-black) makes
// the lower half flat rather than inverted; the gradient then comes entirely
// from the pale push above 0.6. Flat and visible beats a ramp that runs
// backwards.
func (t Theme) rampColor(f float64) (int, int, int) {
	r, g, b := hexRGB(t.AccentHex)
	if f <= 0.6 {
		fr, fg, fb := t.rampFloor[0], t.rampFloor[1], t.rampFloor[2]
		k := f / 0.6
		return int(float64(fr) + (float64(r)-float64(fr))*k),
			int(float64(fg) + (float64(g)-float64(fg))*k),
			int(float64(fb) + (float64(b)-float64(fb))*k)
	}
	if t.AccentHex2 != "" {
		r2, g2, b2 := hexRGB(t.AccentHex2)
		k := (f - 0.6) / 0.4
		br := float64(r) + (float64(r2)-float64(r))*k
		bg := float64(g) + (float64(g2)-float64(g))*k
		bb := float64(b) + (float64(b2)-float64(b))*k
		// Hue and saturation travel to the second stop; brightness keeps
		// climbing regardless. A straight RGB blend inverts the ramp
		// whenever the second hue is dimmer than the first — Lagoon's blue
		// against its mint, Charm's violet against its pink — and an
		// inverted ramp makes a loud bar look quieter than a middling one.
		// Height is what the ramp encodes; the second hue is signature.
		// An accent already at or above the cap (Lagoon's mint sits exactly
		// on it) still has to climb, or the top three steps flatten and
		// integer rounding decides their order.
		l0 := relLuminance(r, g, b)
		lTop := math.Min(rampCeilLuminance,
			math.Max(l0+rampMinLuminanceRise, math.Min(rampTopLuminance, l0+rampLuminanceGain)))
		return setLuminance(int(br), int(bg), int(bb), l0+(lTop-l0)*k)
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
