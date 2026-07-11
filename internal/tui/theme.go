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

	Accent lipgloss.Style
	Bright lipgloss.Style
	Mid    lipgloss.Style
	Dim    lipgloss.Style
	Invert lipgloss.Style // one-frame flashes

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
}

func NewTheme(accentHex string, ascii bool) Theme {
	if !validHex(accentHex) {
		accentHex = "#FFB000"
	}
	t := Theme{
		AccentHex: accentHex,
		Accent:    lipgloss.NewStyle().Foreground(lipgloss.Color(accentHex)),
		Bright:    lipgloss.NewStyle().Foreground(lipgloss.Color("#E8E4D8")),
		Mid:       lipgloss.NewStyle().Foreground(lipgloss.Color("#8A8578")),
		Dim:       lipgloss.NewStyle().Foreground(lipgloss.Color("#4A463C")),
		Invert:    lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color(accentHex)),
		G:         unicodeGlyphs,
	}
	if ascii {
		t.G = asciiGlyphs
	}
	// Precompute the breathing gradient: accent -> 40% accent, 24 steps.
	r, g, b := hexRGB(accentHex)
	for i := 0; i < 24; i++ {
		f := 0.4 + 0.6*float64(i)/23.0
		t.breatheSteps = append(t.breatheSteps, lipgloss.Color(rgbHex(
			int(float64(r)*f), int(float64(g)*f), int(float64(b)*f))))
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
