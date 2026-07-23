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
		"ENERGY NRJ Bulgaria 90s Only (OGG)": "ENERGY NRJ Bulgaria 90s Only",
		"Some Station [128k] (AAC)":          "Some Station",
		"Plain Name":                         "Plain Name",
		"Trailing Dash - ":                   "Trailing Dash",
		"(Only Parens)":                      "(Only Parens)", // never strip to empty
		"  Spaced  (mp3)  ":                  "Spaced",
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

func TestVolumeSliderControlsPlayerAndPersists(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.volume = 50

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = mm.(Model)
	if !m.volumeOpen || !strings.Contains(m.View(), "VOLUME") {
		t.Fatal("v should open the volume slider")
	}

	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = mm.(Model)
	if m.volume != 55 || cmd == nil {
		t.Fatalf("right should raise volume: %d", m.volume)
	}
	msg := cmd()
	if got := m.pl.(*stubPlayer).volume; got != 55 {
		t.Fatalf("player volume=%d, want 55", got)
	}
	mm, _ = m.Update(msg)
	m = mm.(Model)
	if got := m.core.Volume(); got != 55 {
		t.Fatalf("persisted volume=%d, want 55", got)
	}
	if !strings.Contains(m.View(), "55%") {
		t.Fatalf("slider does not show percentage:\n%s", m.View())
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if mm.(Model).volumeOpen {
		t.Fatal("esc should close the volume slider")
	}
}

// Regression: the wide receiver faceplate must host the volume slider too.
// mainRows hands wide terminals to receiverRows, which used to ignore
// volumeOpen entirely — pressing v changed the footer but no slider ever
// rendered.
func TestVolumeSliderRendersInReceiverFaceplate(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)
	m.volume = 50

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = mm.(Model)
	if !m.volumeOpen || !strings.Contains(m.View(), "VOLUME") {
		t.Fatal("v should open the volume slider in the wide receiver layout")
	}

	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = mm.(Model)
	if m.volume != 55 || cmd == nil {
		t.Fatalf("right should raise volume: %d", m.volume)
	}
	if !strings.Contains(m.View(), "55%") {
		t.Fatalf("receiver volume row should show the percentage:\n%s", m.View())
	}
	assertFits(t, m.View(), 110, 32)

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = mm.(Model)
	if m.volumeOpen {
		t.Fatal("v should close the volume slider")
	}
	if strings.Contains(m.View(), "VOLUME") {
		t.Fatal("closing should restore the status readout")
	}
}

// Regression: the wide receiver faceplate must host the seed prompt too.
// Pressing / set m.prompt and the footer showed ENTER tune / ESC cancel,
// but receiverRows never rendered promptRow — the user typed blind.
func TestSeedPromptRendersInReceiverFaceplate(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mm.(Model)
	if !m.prompt || !strings.Contains(m.View(), "SEED") {
		t.Fatal("/ should open the seed prompt in the wide receiver layout")
	}
	if !strings.Contains(m.View(), "genre or artist") {
		t.Fatalf("empty prompt should show the hint:\n%s", m.View())
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ambient")})
	m = mm.(Model)
	if !strings.Contains(m.View(), "ambient") {
		t.Fatalf("typed query should render in the receiver prompt:\n%s", m.View())
	}
	assertFits(t, m.View(), 110, 32)

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.prompt {
		t.Fatal("esc should close the seed prompt")
	}
	if strings.Contains(m.View(), "SEED") {
		t.Fatal("closing should restore the status readout")
	}
}

func TestVolumeSliderClampsAndFitsTinyTerminals(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 20, 6
	m.start = m.now.Add(-time.Minute)
	m.volumeOpen = true
	m.volume = 100
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = mm.(Model)
	if m.volume != 100 {
		t.Fatalf("high clamp=%d", m.volume)
	}
	m.volume = 0
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = mm.(Model)
	if m.volume != 0 {
		t.Fatalf("low clamp=%d", m.volume)
	}
	assertFits(t, m.View(), 20, 6)
}
