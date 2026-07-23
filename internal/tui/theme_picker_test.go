package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestAustereThemeIsMonochrome(t *testing.T) {
	th := NewAustereTheme(false)
	if th.Name != ThemeAustere {
		t.Fatalf("theme name=%q, want %q", th.Name, ThemeAustere)
	}
	colors := []lipgloss.TerminalColor{
		th.Accent.GetForeground(), th.AccentDim.GetForeground(),
		th.Bright.GetForeground(), th.Mid.GetForeground(), th.Dim.GetForeground(),
		th.Love.GetForeground(), th.PanelFill.GetBackground(),
		th.PanelRaised.GetBackground(), th.FootFill.GetBackground(),
	}
	for _, color := range colors {
		hex := fmt.Sprint(color)
		if len(hex) != 7 || hex[0] != '#' {
			t.Fatalf("expected truecolor hex, got %q", hex)
		}
		r, g, b := hexRGB(hex)
		if r != g || g != b {
			t.Errorf("austere color %s is chromatic", hex)
		}
	}
}

func TestHueThemesResolveAndStayChromatic(t *testing.T) {
	accents := map[string]string{
		ThemeVerdant: "#45DC78",
		ThemeAzure:   "#4FA8F5",
		ThemeViolet:  "#A98BFF",
		ThemeRose:    "#FF7AB8",
	}
	for id, hex := range accents {
		th := NewNamedTheme(id, "#FFB000", false)
		if th.Name != id || th.AccentHex != hex {
			t.Errorf("theme %q resolved to name=%q accent=%q, want accent %q", id, th.Name, th.AccentHex, hex)
		}
		// Derived surfaces should follow the accent hue, like the receiver's.
		if fmt.Sprint(th.Accent.GetForeground()) != hex {
			t.Errorf("theme %q accent foreground = %v, want %q", id, th.Accent.GetForeground(), hex)
		}
	}
	if got := normalizeThemeName("nope"); got != ThemeReceiver {
		t.Errorf("unknown theme fell back to %q, want %q", got, ThemeReceiver)
	}
}

func TestGradientThemesResolveAndBlend(t *testing.T) {
	stops := map[string][2]string{
		ThemeCharm:  {"#FF7EB6", "#B48CFF"},
		ThemeLagoon: {"#3FE8B0", "#4FA8F5"},
	}
	for id, want := range stops {
		th := NewNamedTheme(id, "#FFB000", false)
		if th.Name != id || th.AccentHex != want[0] || th.AccentHex2 != want[1] {
			t.Errorf("theme %q resolved to name=%q accents=%q/%q, want %q/%q",
				id, th.Name, th.AccentHex, th.AccentHex2, want[0], want[1])
		}
		// The ramp top lands exactly on the second hue.
		r, g, b := th.rampColor(1.0)
		wr, wg, wb := hexRGB(want[1])
		if r != wr || g != wg || b != wb {
			t.Errorf("theme %q ramp top = #%02X%02X%02X, want %q", id, r, g, b, want[1])
		}
		// The ember side stays home: it derives from the first accent only.
		r, g, b = th.rampColor(0.3)
		ar, ag, ab := hexRGB(want[0])
		k := 0.35 + (0.3/0.6)*0.65
		if r != int(float64(ar)*k) || g != int(float64(ag)*k) || b != int(float64(ab)*k) {
			t.Errorf("theme %q ember mid = #%02X%02X%02X, want first-accent ember", id, r, g, b)
		}
	}
	// Single-hue themes are untouched: no second stop, pale-gold ramp top.
	th := NewTheme("#FFB000", false)
	if th.AccentHex2 != "" {
		t.Fatalf("receiver theme gained a second stop %q", th.AccentHex2)
	}
	if r, g, b := th.rampColor(1.0); r != 255 || g != 211 || b != 114 {
		t.Errorf("receiver ramp top = #%02X%02X%02X, want pale gold #FFD372", r, g, b)
	}
}

func TestThemePickerPreviewsCancelsAndPersists(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-bootDur)

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = mm.(Model)
	if !m.themeOpen || !strings.Contains(m.View(), "APPEARANCE / THEME") {
		t.Fatal("t should open the theme picker")
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(Model)
	if m.themeName != ThemeAustere || m.th.Name != ThemeAustere {
		t.Fatalf("down should preview austere: model=%q palette=%q", m.themeName, m.th.Name)
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.themeOpen || m.themeName != ThemeReceiver || m.core.Theme() != "" {
		t.Fatalf("escape should restore without saving: open=%v name=%q saved=%q", m.themeOpen, m.themeName, m.core.Theme())
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if m.themeOpen || m.core.Theme() != ThemeAustere {
		t.Fatalf("enter should persist austere: open=%v saved=%q", m.themeOpen, m.core.Theme())
	}

	reloaded := New(m.core, newStubPlayer(), Options{Accent: "#FFB000"})
	if reloaded.themeName != ThemeAustere || reloaded.th.Name != ThemeAustere {
		t.Fatalf("new model did not restore persisted theme: %q %q", reloaded.themeName, reloaded.th.Name)
	}
}

func TestThemePickerNeverOverflows(t *testing.T) {
	for _, size := range []struct{ w, h int }{{110, 32}, {60, 16}, {40, 10}, {24, 8}} {
		m := testModel(t)
		m.w, m.h = size.w, size.h
		m.start = m.now.Add(-bootDur)
		m.themeOpen = true
		assertFits(t, m.View(), size.w, size.h)
	}
}
