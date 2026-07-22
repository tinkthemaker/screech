package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"screech/internal/core"
)

func modelWithListens(t *testing.T) Model {
	t.Helper()
	m := testModel(t)
	// Log a finished listen so history has content. Reach into core the same
	// way the app does: StartListen + EndListen with a gap.
	now := time.Now().Add(-time.Hour)
	m.core.StartListen("seed:wfmu", now)
	m.core.EndListen(now.Add(30 * time.Minute))
	m.core.StartListen("seed:somafm-spacestation", now.Add(31*time.Minute))
	m.core.EndListen(now.Add(45 * time.Minute))
	return m
}

func TestHistoryOpenTuneClose(t *testing.T) {
	m := modelWithListens(t)
	// Save the top-listened station as a preset so it appears in the saved
	// list. Writing the slot directly avoids disturbing the listen math
	// the helper set up.
	m.core.StartListen("seed:wfmu", time.Now())
	m.core.TogglePreset(time.Now())
	m.core.EndListen(time.Now())
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)

	// Open the library, then move to station history.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = mm.(Model)
	if !m.history || len(m.saved) == 0 {
		t.Fatalf("station view should contain saved entries: %v %d", m.history, len(m.saved))
	}
	if m.saved[0].UUID != "seed:wfmu" {
		t.Fatalf("top saved entry should be the longest listen: %+v", m.saved[0])
	}

	// Enter tunes from the saved list and closes it.
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if m.history {
		t.Fatal("tuning from saved stations should close the view")
	}
	if m.st.UUID != "seed:wfmu" || m.reason != "from saved stations" {
		t.Fatalf("expected saved-station tune: %q %q", m.st.UUID, m.reason)
	}
	if cmd == nil {
		t.Fatal("saved-station tune should trigger playback")
	}

	// Reopen the library and close with esc.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mm.(Model)
	if m.history {
		t.Fatal("esc should close history")
	}
}

func TestEmptyLibraryStillOpens(t *testing.T) {
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = mm.(Model)
	m.start = m.now.Add(-time.Minute)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = mm.(Model)
	if !m.history || m.historyView != historyLoved {
		t.Fatal("empty loved library should remain discoverable")
	}
	if !strings.Contains(m.View(), "love a track") {
		t.Fatalf("missing useful empty state:\n%s", m.View())
	}
}

func TestHistoryViewNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {50, 12}, {24, 8}, {200, 50}}
	for _, sz := range sizes {
		m := testModel(t)
		mm, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = mm.(Model)
		m.start = m.now.Add(-time.Minute)
		m.history = true
		m.historyView = historyStations
		m.saved = []core.SavedStation{
			{UUID: "a", Name: "A Station With An Extremely Long Name That Overflows Everything", Total: 90 * time.Minute, PresetSlot: 1},
			{UUID: "b", Name: "B", Total: 60 * time.Minute},
			{UUID: "c", Name: "C", Total: 30 * time.Minute, LoveCount: 2},
			{UUID: "d", Name: "D", Total: 20 * time.Minute},
			{UUID: "e", Name: "E", Total: 10 * time.Minute},
			{UUID: "f", Name: "F", Total: 9 * time.Minute},
			{UUID: "g", Name: "G", Total: 8 * time.Minute},
			{UUID: "h", Name: "H", Total: 7 * time.Minute},
			{UUID: "i", Name: "I", Total: 6 * time.Minute},
		}
		m.presets = map[int]string{1: "a"}
		assertFits(t, m.View(), sz.w, sz.h)
	}
}

func TestRecentHistoryRendersTracksAndLove(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.history = true
	m.historyView = historyRecent
	m.recent = []core.RecentTrack{
		{StationName: "WFMU", Artist: "Broadcast", Title: "Come On Let's Go", Loved: true},
		{StationName: "NTS Radio 1", Title: "Unknown transmission"},
	}
	view := m.View()
	for _, want := range []string{"RECENT", "Broadcast", "WFMU", m.th.G.Heart} {
		if !strings.Contains(view, want) {
			t.Fatalf("recent view missing %q:\n%s", want, view)
		}
	}
	assertFits(t, view, 80, 24)
}

func modelWithLovedTracks(t *testing.T) Model {
	t.Helper()
	m := testModel(t)
	now := time.Now().Add(-time.Hour)
	m.core.StartListen("seed:wfmu", now)
	m.core.NoteTitle("Broadcast - Come On Let's Go", now)
	m.core.Love(now.Add(time.Minute))
	m.core.NoteTitle("Burial - Archangel", now.Add(2*time.Minute))
	m.core.Love(now.Add(3 * time.Minute))
	return m
}

func TestLovedLibrarySearchNavigateAndRemove(t *testing.T) {
	m := modelWithLovedTracks(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = mm.(Model)
	if m.historyView != historyLoved || len(m.library) != 2 {
		t.Fatalf("loved library did not load: view=%v entries=%d", m.historyView, len(m.library))
	}
	view := m.View()
	for _, want := range []string{"LOVED 2", "Burial", "/ FIND", m.th.G.Pointer} {
		if !strings.Contains(view, want) {
			t.Fatalf("library missing %q:\n%s", want, view)
		}
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("broadcast")})
	m = mm.(Model)
	if got := m.filteredLibrary(); len(got) != 1 || got[0].Artist != "Broadcast" {
		t.Fatalf("ranked filter: %+v", got)
	}
	if !strings.Contains(m.View(), "FIND") || !strings.Contains(m.View(), "broadcast") {
		t.Fatalf("active search not rendered:\n%s", m.View())
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = mm.(Model)
	if m.forgetKey == "" || !strings.Contains(m.View(), "REMOVE?") {
		t.Fatal("first removal press should ask for confirmation")
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = mm.(Model)
	if len(m.library) != 1 || len(m.filteredLibrary()) != 0 {
		t.Fatalf("track was not removed from filtered library: %+v", m.library)
	}
}

// Loving the current track twice toggles the heart: first press loves,
// second press unloves, and the heart follows the ground truth both ways.
func TestLoveToggleInTUI(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	now := time.Now().Add(-time.Hour)
	m.core.StartListen("seed:wfmu", now)
	m.core.NoteTitle("Broadcast - Come On Let's Go", now)
	// Point the UI at the same station/track the core is playing.
	for _, st := range core.SeedStations() {
		if st.UUID == "seed:wfmu" {
			m.st = st
		}
	}
	m.haveSt = true
	m.ph = phPlay
	m.playStart = m.now.Add(-time.Minute)
	m.track = "Broadcast · Come On Let's Go"
	m.haveTrack = true
	m.trackAt = m.now

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = mm.(Model)
	if !m.lovedTrack {
		t.Fatal("first l should light the heart")
	}
	if !strings.Contains(m.View(), m.th.G.Heart) {
		t.Fatal("heart should render after love")
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = mm.(Model)
	if m.lovedTrack {
		t.Fatal("second l on the same track should put the heart out")
	}
	if !strings.Contains(m.feedback, "UNLOVED") {
		t.Fatalf("unlove feedback missing: %q", m.feedback)
	}
}

func TestLovedLibraryActions(t *testing.T) {
	m := modelWithLovedTracks(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.loadHistory(historyLoved)

	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	after := mm.(Model)
	if cmd == nil || after.history || !after.seeking {
		t.Fatal("enter should launch discovery from the selected artist")
	}

	m.loadHistory(historyLoved)
	mm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	after = mm.(Model)
	if cmd == nil || after.history || after.st.UUID != "seed:wfmu" || after.reason != "from a loved track" {
		t.Fatalf("origin station recall failed: station=%q reason=%q", after.st.UUID, after.reason)
	}
}

func TestLovedLibraryNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {50, 12}, {24, 8}, {200, 50}}
	for _, sz := range sizes {
		m := modelWithLovedTracks(t)
		m.w, m.h = sz.w, sz.h
		m.start = m.now.Add(-time.Minute)
		m.loadHistory(historyLoved)
		assertFits(t, m.View(), sz.w, sz.h)
	}
}

// The STATIONS view is the unified saved-stations list: searchable with /,
// navigable with j/k, tunable with enter, removable with x (guarded).
func TestSavedStationsSearchNavigateAndRemove(t *testing.T) {
	m := modelWithListens(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	// Two saved stations: wfmu (preset), spacestation (loved).
	m.core.StartListen("seed:wfmu", time.Now())
	m.core.TogglePreset(time.Now())
	m.core.EndListen(time.Now())
	m.core.StartListen("seed:somafm-spacestation", time.Now())
	m.core.NoteTitle("Artist X - Song Y", time.Now())
	m.core.Love(time.Now())
	m.core.EndListen(time.Now())

	// Open the STATIONS view.
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = mm.(Model)
	if m.historyView != historyStations || len(m.saved) != 2 {
		t.Fatalf("saved list did not load: view=%v entries=%d", m.historyView, len(m.saved))
	}
	view := m.View()
	if !strings.Contains(view, "FIND") || !strings.Contains(view, "Space Station") {
		t.Fatalf("saved view missing elements:\n%s", view)
	}

	// Search narrows to one.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = mm.(Model)
	for _, r := range "wfmu" {
		mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm.(Model)
	}
	if got := m.filteredSaved(); len(got) != 1 || got[0].UUID != "seed:wfmu" {
		t.Fatalf("saved filter: %+v", got)
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // close find
	m = mm.(Model)

	// x asks once, removes on the second press.
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = mm.(Model)
	if m.forgetKey == "" || !strings.Contains(m.View(), "REMOVE?") {
		t.Fatal("first removal press should ask for confirmation")
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = mm.(Model)
	if len(m.saved) != 1 || m.saved[0].UUID != "seed:somafm-spacestation" {
		t.Fatalf("station was not removed: %+v", m.saved)
	}
	if len(m.core.Presets()) != 0 {
		t.Fatal("removing the preset station should clear the slot")
	}
}

func TestLearningFeedbackExpiresBackToReason(t *testing.T) {
	m := testModel(t)
	m.haveSt = true
	m.feedback = "fast skip"
	m.feedbackAt = m.now
	m.reason = "wildcard"
	m.tw = NewTypewriter("wildcard", m.now.Add(-time.Minute))
	if got := m.reasonRow(64); !strings.Contains(got, "fast skip") {
		t.Fatalf("feedback not shown: %q", got)
	}
	m.now = m.now.Add(feedbackDur + time.Millisecond)
	if got := m.reasonRow(64); !strings.Contains(got, "wildcard") {
		t.Fatalf("reason did not return: %q", got)
	}
}

func TestFirstRunStartsWithoutBlockingPrompt(t *testing.T) {
	m := testModel(t)
	if !m.syncing || !m.virgin {
		t.Fatalf("expected seed-only first run: syncing=%v virgin=%v", m.syncing, m.virgin)
	}
	if m.prompt {
		t.Fatal("first run should play immediately; seed prompt remains available on /")
	}
	if m.Init() == nil {
		t.Fatal("first run should schedule playback and directory sync")
	}
}
