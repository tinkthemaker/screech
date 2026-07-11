package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestCleanStationName(t *testing.T) {
	cases := map[string]string{
		"ENERGY NRJ Bulgaria 90s Only (OGG)":  "ENERGY NRJ Bulgaria 90s Only",
		"Some Station [128k] (AAC)":           "Some Station",
		"Plain Name":                          "Plain Name",
		"Trailing Dash - ":                    "Trailing Dash",
		"(Only Parens)":                       "(Only Parens)", // never strip to empty
		"  Spaced  (mp3)  ":                   "Spaced",
	}
	for in, want := range cases {
		if got := cleanStationName(in); got != want {
			t.Errorf("cleanStationName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptRendersAndFits(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {50, 12}, {24, 8}, {20, 6}}
	for _, sz := range sizes {
		m := testModel(t)
		mm, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = mm.(Model)
		m.start = m.now.Add(-time.Minute) // past boot

		// Virgin prompt, empty buffer.
		m.prompt = true
		m.virgin = true
		assertFits(t, m.View(), sz.w, sz.h)

		// Long query typed.
		m.buf = []rune("an extremely long seed query that overflows")
		assertFits(t, m.View(), sz.w, sz.h)
	}
}

func TestPromptKeyFlow(t *testing.T) {
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	m.prompt = true

	// Type "jazz" — including keys that are bound in normal mode (q, l, f, n).
	for _, r := range "jazzqlfn" {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm.(Model)
	}
	if string(m.buf) != "jazzqlfn" {
		t.Fatalf("prompt should swallow bound keys as text: %q", string(m.buf))
	}

	// Backspace works.
	for i := 0; i < 4; i++ {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = mm.(Model)
	}
	if string(m.buf) != "jazz" {
		t.Fatalf("backspace: %q", string(m.buf))
	}

	// Esc cancels.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.prompt {
		t.Fatal("esc should close the prompt")
	}
}

func TestPresetTicksRender(t *testing.T) {
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	m.start = m.now.Add(-time.Minute)
	m.presets = map[int]string{1: "uuid-a", 2: "uuid-b", 3: "uuid-c"}

	band := m.bandRow(64, false)
	ticks := strings.Count(band, m.th.G.Tick)
	if ticks < 2 { // 3 presets, minus possible marker overlap
		t.Fatalf("expected preset ticks on the band, found %d", ticks)
	}
	if w := lipgloss.Width(band); w != 64 {
		t.Fatalf("band width %d, want 64", w)
	}
}

func TestDigitRecallIgnoresEmptySlot(t *testing.T) {
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	m = mm.(Model)
	if cmd != nil {
		t.Fatal("empty preset slot should not tune")
	}
	if !strings.Contains(m.note, "preset 5 is empty") {
		t.Fatalf("note: %q", m.note)
	}
}
