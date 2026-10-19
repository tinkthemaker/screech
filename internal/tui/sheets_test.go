package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// The ? sheet opens over the main screen and esc puts it away.
func TestHelpOpensAndEscCloses(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = pressRune(t, m, '?')
	if !m.helpOpen {
		t.Fatal("? should open the help sheet")
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"KEYS", "LISTEN", "SPACE / N", "sleep timer"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("help sheet missing %q: %q", want, plain)
		}
	}
	m = driveUI(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.helpOpen {
		t.Fatal("esc should close the help sheet")
	}
}

// A non-sheet key closes help and still reaches what's underneath — the
// same pass-through contract as the volume strip.
func TestHelpPassesKeysThrough(t *testing.T) {
	m := playing(t, testModel(t))
	m.helpOpen = true
	m = pressRune(t, m, 'l')
	if m.helpOpen {
		t.Fatal("another key should close the sheet")
	}
	if !m.s.LovedTrack {
		t.Fatal("the l should still have loved after closing the sheet")
	}
}

// ? works from the library too, and closing the sheet lands back on the
// library view it came from.
func TestHelpFromLibraryReturnsToLibrary(t *testing.T) {
	m := playing(t, testModel(t))
	m = pressRune(t, m, 'H')
	if !m.history {
		t.Fatal("H should open the library")
	}
	m = pressRune(t, m, '?')
	if !m.helpOpen {
		t.Fatal("? should open help over the library")
	}
	m = driveUI(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.helpOpen {
		t.Fatal("esc should close help")
	}
	if !m.history || m.historyView != historyLoved {
		t.Fatal("closing help must land back on the library view")
	}
}

// Every main-screen key has a row on the sheet; a missing one is a key
// nobody can discover.
func TestHelpListsEveryMainKey(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 100, 30
	m.start = m.now.Add(-time.Minute) // boot over
	m.helpOpen = true
	plain := stripANSI(m.View())
	for _, key := range []string{"SPACE / N", "L", "F", "1-9", "P", "Z", "/", "H", "I", "V", "T", "Q"} {
		if !strings.Contains(plain, key) {
			t.Fatalf("help sheet missing key %q: %q", key, plain)
		}
	}
}

// i opens the sheet over the tuned station and the async query fills it.
func TestInfoOpensAndLoads(t *testing.T) {
	m := playing(t, testModel(t))
	m = pressRune(t, m, 'i')
	if !m.infoOpen || m.infoFor != m.s.Station.UUID {
		t.Fatalf("i should open info for the tuned station: open=%v for=%q", m.infoOpen, m.infoFor)
	}
	if m.info.Daypart == "" {
		t.Fatal("the info cmd should have loaded by now")
	}
}

// A stale answer — the radio moved before the query returned — is dropped.
func TestInfoMsgForOtherStationIgnored(t *testing.T) {
	m := playing(t, testModel(t))
	m = pressRune(t, m, 'i')
	stale := stationInfoMsg{uuid: "someone-else", info: core.StationInfo{
		Listened: time.Hour, Daypart: "night", Heard: true, Match: 0.9}}
	nm, _ := m.Update(stale)
	m = nm.(Model)
	if m.info.Listened == time.Hour || m.info.Match == 0.9 {
		t.Fatal("a msg for another uuid must not land on the sheet")
	}
}

// The sheet follows the radio: tuning while it's open re-requests for the
// station now playing.
func TestInfoFollowsTheRadio(t *testing.T) {
	m := playing(t, testModel(t))
	m = pressRune(t, m, 'i')
	seeds := testSeeds()
	m = drive(t, m, session.Tuned{Pick: pickSeed(seeds[1])})
	if !m.infoOpen {
		t.Fatal("the sheet should stay open across a tune")
	}
	if m.infoFor != seeds[1].UUID {
		t.Fatalf("info should re-request for %q, has %q", seeds[1].UUID, m.infoFor)
	}
}

// With nothing tuned there is no sheet, just the honest one-liner.
func TestInfoWithoutStationGivesFeedback(t *testing.T) {
	m := testModel(t)
	m = pressRune(t, m, 'i')
	if m.infoOpen {
		t.Fatal("no station, no sheet")
	}
	if !strings.Contains(m.s.Feedback, "Nothing tuned") {
		t.Fatalf("feedback %q", m.s.Feedback)
	}
}

// Sheet keys close it; another key closes and falls through to the main
// screen — v opens the volume strip underneath.
func TestInfoPassesKeysThrough(t *testing.T) {
	m := playing(t, testModel(t))
	m = pressRune(t, m, 'i')
	m = driveUI(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.infoOpen {
		t.Fatal("esc should close the info sheet")
	}

	m = pressRune(t, m, 'i')
	m = pressRune(t, m, 'v')
	if m.infoOpen {
		t.Fatal("another key should close the sheet")
	}
	if !m.volumeOpen {
		t.Fatal("the v should still have opened the volume strip")
	}
}

// The P chip is a state chip: no station, nothing to pause, no chip.
func TestFooterHidesPauseWithoutStation(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	if got := stripANSI(m.footerBar()); strings.Contains(got, " P ") {
		t.Fatalf("stationless strip shows a pause chip: %q", got)
	}
	m.s.Station = testSeeds()[0]
	m.s.HaveStation = true
	if got := stripANSI(m.footerBar()); !strings.Contains(got, " P ") {
		t.Fatalf("tuned strip lost the pause chip: %q", got)
	}
}

// Dead backend: only the keys that still do something stay on the strip.
func TestFooterDeadStrip(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.s.Phase = session.Stopped
	got := stripANSI(m.footerBar())
	for _, want := range []string{"H", "T", "?", "Q"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dead strip missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "SPACE") {
		t.Fatalf("dead strip still offers tuning: %q", got)
	}
}

// While a sheet is up the strip collapses to the one key that matters.
func TestFooterOverlayStripIsEscOnly(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m.helpOpen = true
	got := stripANSI(m.footerBar())
	if !strings.Contains(got, "ESC") || strings.Contains(got, "SPACE") || strings.Contains(got, " Q ") {
		t.Fatalf("overlay strip should be ESC alone: %q", got)
	}
	m.helpOpen = false
	m.infoOpen = true
	got = stripANSI(m.footerBar())
	if !strings.Contains(got, "ESC") || strings.Contains(got, "SPACE") {
		t.Fatalf("info strip should be ESC alone: %q", got)
	}
}
