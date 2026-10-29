package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// findModel is a settled model at a comfortable size, past the boot frame.
func findModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = mm.(Model)
	m.start = m.now.Add(-time.Minute)
	return m
}

func pressKey(t *testing.T, m Model, k tea.KeyType) Model {
	t.Helper()
	return driveUI(t, m, tea.KeyMsg{Type: k})
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	return driveUI(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

// withDiscoveries gives the database three discovery stations that were
// listened to, oldest phrase first, so the most recent is "jazz piano".
func withDiscoveries(t *testing.T, m Model) Model {
	t.Helper()
	base := time.Now().Add(-6 * time.Hour)
	for i, phrase := range []string{"lofi hip hop", "swedish death metal", "jazz piano"} {
		at := base.Add(time.Duration(i) * time.Hour)
		pick, err := m.core.StartDiscovery(phrase, at)
		if err != nil {
			t.Fatal(err)
		}
		m.core.StartListen(pick.Station.UUID, at)
		m.core.EndListen(at.Add(10 * time.Minute))
	}
	return m
}

func TestSlashOpensFindPrompt(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	if !m.prompt || m.promptMode != findMode {
		t.Fatalf("/ should open the prompt in FIND: prompt=%v mode=%v", m.prompt, m.promptMode)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "FIND") {
		t.Fatalf("FIND label missing:\n%s", view)
	}
	if !strings.Contains(view, "artist, song, genre or YouTube link") {
		t.Fatalf("FIND placeholder missing:\n%s", view)
	}
	assertFits(t, m.View(), 80, 24)
}

func TestFindPromptHostsTypedTextOnTheWideLayout(t *testing.T) {
	// The prompt used to live inside the faceplate and once failed to render
	// there at all, leaving the listener typing blind. It must show on the
	// wide layout, with what was typed, and closing must bring the
	// faceplate back.
	m := findModel(t, 110, 32)
	m = pressRune(t, m, '/')
	m = typeText(t, m, "ambient")
	view := stripANSI(m.View())
	if !strings.Contains(view, "FIND") || !strings.Contains(view, "ambient") {
		t.Fatalf("typed query should render in the FIND prompt:\n%s", view)
	}
	assertFits(t, m.View(), 110, 32)

	m = pressKey(t, m, tea.KeyEsc)
	if m.prompt {
		t.Fatal("esc should close the prompt")
	}
	view = stripANSI(m.View())
	if strings.Contains(view, "FIND") || !strings.Contains(view, "RECEIVER") {
		t.Fatalf("closing should restore the faceplate:\n%s", view)
	}
}

func TestTabTogglesRadio(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyTab)
	if m.promptMode != radioMode {
		t.Fatalf("tab should switch to RADIO, mode=%v", m.promptMode)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "RADIO") || !strings.Contains(view, "station, genre or country") {
		t.Fatalf("RADIO prompt missing:\n%s", view)
	}
	if strings.Contains(view, "artist, song") {
		t.Fatalf("FIND placeholder should be gone in RADIO:\n%s", view)
	}
	m = pressKey(t, m, tea.KeyTab)
	if m.promptMode != findMode || !strings.Contains(stripANSI(m.View()), "artist, song, genre or YouTube link") {
		t.Fatalf("tab again should return to FIND, mode=%v", m.promptMode)
	}
}

func TestSlashAlwaysOpensInFind(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyTab)
	m = pressKey(t, m, tea.KeyEsc)
	m = pressRune(t, m, '/')
	if m.promptMode != findMode {
		t.Fatalf("a reopened prompt starts in FIND, mode=%v", m.promptMode)
	}
}

func TestTypedTextSurvivesTab(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = typeText(t, m, "jazz")
	m = pressKey(t, m, tea.KeyTab)
	if string(m.buf) != "jazz" {
		t.Fatalf("switching modes should keep what was typed, got %q", string(m.buf))
	}
}

func TestEnterInFindSendsDiscover(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = typeText(t, m, "swedish death metal")
	m = pressKey(t, m, tea.KeyEnter)
	if m.prompt {
		t.Fatal("enter should close the prompt")
	}
	want := core.DiscoveryUUID("swedish death metal")
	if m.s.Station.UUID != want {
		t.Fatalf("FIND's enter should start the discovery station %s, tuned %q", want, m.s.Station.UUID)
	}
	if m.s.Seeking {
		t.Fatal("a discovery is not a radio search")
	}
	if !strings.HasPrefix(m.s.Reason, "discovering") {
		t.Fatalf("reason %q", m.s.Reason)
	}
}

func TestEnterInRadioSendsSeed(t *testing.T) {
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyTab)
	m = typeText(t, m, "ambient")
	// Update, not driveUI: the seed command would go to the radio
	// directory. Seeking is the session's first answer to a Seed intent.
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(Model)
	if m.prompt {
		t.Fatal("enter should close the prompt")
	}
	if !m.s.Seeking {
		t.Fatal("RADIO's enter should send Seed")
	}
	if m.s.HaveStation {
		t.Fatalf("a seed must not start a discovery: %q", m.s.Station.UUID)
	}
}

func TestEnterOnAnEmptyPromptJustCloses(t *testing.T) {
	for _, tab := range []bool{false, true} {
		m := findModel(t, 80, 24)
		m = pressRune(t, m, '/')
		if tab {
			m = pressKey(t, m, tea.KeyTab)
		}
		m = pressKey(t, m, tea.KeyEnter)
		if m.prompt || m.s.HaveStation || m.s.Seeking {
			t.Fatalf("an empty enter starts nothing (radio=%v): %+v", tab, m.s.Station)
		}
	}
}

func TestFindPromptTakesLongLinks(t *testing.T) {
	// A pasted YouTube link is longer than the radio search's 40 columns.
	link := "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ"
	m := findModel(t, 80, 24)
	m = pressRune(t, m, '/')
	m = typeText(t, m, link)
	if string(m.buf) != link {
		t.Fatalf("FIND truncated the link: %q", string(m.buf))
	}
	assertFits(t, m.View(), 80, 24)
	// RADIO keeps its short limit, and switching to it trims to fit.
	m = pressKey(t, m, tea.KeyTab)
	if len(m.buf) > maxQuery {
		t.Fatalf("RADIO holds %d runes, limit %d", len(m.buf), maxQuery)
	}
}

func TestFindPromptListsRecentDiscoveries(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	view := stripANSI(m.View())
	if !strings.Contains(view, "pick up where you left off") {
		t.Fatalf("recent label missing:\n%s", view)
	}
	for _, name := range []string{"jazz piano", "swedish death metal", "lofi hip hop"} {
		if !strings.Contains(view, name) {
			t.Fatalf("%q missing from the recent list:\n%s", name, view)
		}
	}
	// Newest first.
	if strings.Index(view, "jazz piano") > strings.Index(view, "swedish death metal") ||
		strings.Index(view, "swedish death metal") > strings.Index(view, "lofi hip hop") {
		t.Fatalf("recent list should run newest first:\n%s", view)
	}

	// The first row is selected on open; down moves to the second, and
	// enter resumes that station through the engine's TuneTo.
	m = pressKey(t, m, tea.KeyDown)
	m = pressKey(t, m, tea.KeyEnter)
	if m.prompt {
		t.Fatal("enter on a recent should close the prompt")
	}
	want := core.DiscoveryUUID("swedish death metal")
	if m.s.Station.UUID != want {
		t.Fatalf("down+enter should resume %s, tuned %q", want, m.s.Station.UUID)
	}
	if !strings.HasPrefix(m.s.Reason, "discovering") {
		t.Fatalf("a resumed discovery keeps the discovering reason, got %q", m.s.Reason)
	}
}

func TestFindEnterResumesTheNewestByDefault(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyEnter)
	if want := core.DiscoveryUUID("jazz piano"); m.s.Station.UUID != want {
		t.Fatalf("enter on the open list resumes the newest, want %s got %q", want, m.s.Station.UUID)
	}
}

func TestFindRecentSelectionClamps(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	for i := 0; i < 6; i++ {
		m = pressKey(t, m, tea.KeyDown)
	}
	if m.findPos != 2 {
		t.Fatalf("down stops at the last row, pos=%d", m.findPos)
	}
	for i := 0; i < 6; i++ {
		m = pressKey(t, m, tea.KeyUp)
	}
	if m.findPos != 0 {
		t.Fatalf("up stops at the first row, pos=%d", m.findPos)
	}
}

func TestFindRecentsLeaveWhenTyping(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	m = typeText(t, m, "x")
	if view := stripANSI(m.View()); strings.Contains(view, "pick up where you left off") {
		t.Fatalf("the list shows only while the bar is empty:\n%s", view)
	}
	// With text in the bar, enter starts that text, not a row of the list.
	m = pressKey(t, m, tea.KeyEnter)
	if want := core.DiscoveryUUID("x"); m.s.Station.UUID != want {
		t.Fatalf("enter with text discovers the text, want %s got %q", want, m.s.Station.UUID)
	}

	m = withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	m = typeText(t, m, "x")
	m = pressKey(t, m, tea.KeyBackspace)
	if view := stripANSI(m.View()); !strings.Contains(view, "pick up where you left off") {
		t.Fatalf("emptying the bar brings the list back:\n%s", view)
	}
	if m.findPos != 0 {
		t.Fatalf("the selection starts over, pos=%d", m.findPos)
	}
}

func TestFindRecentsAreFindOnly(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyTab)
	if view := stripANSI(m.View()); strings.Contains(view, "pick up where you left off") {
		t.Fatalf("RADIO has no discovery list:\n%s", view)
	}
	// Enter on an empty RADIO bar must not resume a hidden row.
	m = pressKey(t, m, tea.KeyEnter)
	if m.s.HaveStation {
		t.Fatalf("RADIO's empty enter tuned %q", m.s.Station.UUID)
	}
}

func TestFindListSkipsTheStationPlayingNow(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	// jazz piano is the newest listen and is the one on air.
	pick, err := m.core.StartDiscovery("jazz piano", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, session.Tuned{Pick: pick})
	m = pressRune(t, m, '/')
	view := stripANSI(m.View())
	if !strings.Contains(view, "swedish death metal") || !strings.Contains(view, "lofi hip hop") {
		t.Fatalf("the others should still be offered:\n%s", view)
	}
	if strings.Count(view, "jazz piano") > 0 {
		t.Fatalf("resuming what is already playing is no pick-up:\n%s", view)
	}
}

func TestFindListShowsNoMoreThanThree(t *testing.T) {
	m := withDiscoveries(t, findModel(t, 80, 24))
	at := time.Now().Add(-time.Hour)
	pick, err := m.core.StartDiscovery("ambient drone", at)
	if err != nil {
		t.Fatal(err)
	}
	m.core.StartListen(pick.Station.UUID, at)
	m.core.EndListen(at.Add(5 * time.Minute))
	m = pressRune(t, m, '/')
	view := stripANSI(m.View())
	if !strings.Contains(view, "ambient drone") || strings.Contains(view, "lofi hip hop") {
		t.Fatalf("the list is the three newest:\n%s", view)
	}
}

func TestFindHintLine(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		m := findModel(t, 80, 24)
		if ascii {
			m.th = NewTheme("#FFB000", true)
		}
		m = pressRune(t, m, '/')
		want := "esc close " + m.th.G.Dot + " tab radio"
		if view := stripANSI(m.View()); !strings.Contains(view, want) {
			t.Fatalf("hint %q missing (ascii=%v):\n%s", want, ascii, view)
		}
		m = pressKey(t, m, tea.KeyTab)
		want = "esc close " + m.th.G.Dot + " tab find"
		if view := stripANSI(m.View()); !strings.Contains(view, want) {
			t.Fatalf("RADIO hint %q missing (ascii=%v):\n%s", want, ascii, view)
		}
	}
}

func TestFindDropsTheListBeforeAnythingElse(t *testing.T) {
	// The list is the first thing a short terminal gives up: the bar and
	// the hint outlast it, and the selection never points at a row that
	// isn't drawn.
	m := withDiscoveries(t, findModel(t, 80, 24))
	for h := 3; h <= 24; h++ {
		mm, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: h})
		s := mm.(Model)
		s = pressRune(t, s, '/')
		view := stripANSI(s.View())
		assertFits(t, s.View(), 80, h)
		// The sheet spends bar + hint + gap + label + three rows = 7 rows
		// of content, which a 10-row terminal has (header, rule, key
		// strip around it). Whole or gone, never a stub of it.
		listed := strings.Contains(view, "pick up where you left off")
		if h >= 10 && !listed {
			t.Fatalf("a %d-row terminal has room for the list:\n%s", h, view)
		}
		if h < 10 && listed {
			t.Fatalf("a %d-row terminal should have dropped the list:\n%s", h, view)
		}
		if listed {
			for _, name := range []string{"jazz piano", "swedish death metal", "lofi hip hop"} {
				if !strings.Contains(view, name) {
					t.Fatalf("a listed sheet is whole, %q missing at %d rows:\n%s", name, h, view)
				}
			}
		}
		// The hint outlasts the list, and the bar outlasts both.
		if h >= 4 && (!strings.Contains(view, "FIND") || !strings.Contains(view, "esc close")) {
			t.Fatalf("bar and hint must outlast the list at %d rows:\n%s", h, view)
		}
		if h >= 2 && !strings.Contains(view, "FIND") {
			t.Fatalf("the bar must outlast everything at %d rows:\n%s", h, view)
		}
		// However many rows are drawn, down+enter lands on one of them.
		shown := s.findShown()
		for i := 0; i < 5; i++ {
			s = pressKey(t, s, tea.KeyDown)
		}
		if shown > 0 && s.findPos != shown-1 {
			t.Fatalf("at %d rows %d are drawn but the selection is %d", h, shown, s.findPos)
		}
	}
}

func TestFindFitsAcrossSizesAndModes(t *testing.T) {
	sizes := []struct{ w, h int }{
		{110, 32}, {80, 24}, {64, 16}, {50, 12}, {38, 10}, {24, 8}, {20, 6}, {12, 4}, {5, 3},
	}
	for _, sz := range sizes {
		for _, radio := range []bool{false, true} {
			for _, text := range []string{"", "a very long phrase that overflows the bar entirely and then some"} {
				m := withDiscoveries(t, findModel(t, sz.w, sz.h))
				m = pressRune(t, m, '/')
				if radio {
					m = pressKey(t, m, tea.KeyTab)
				}
				m = typeText(t, m, text)
				assertFits(t, m.View(), sz.w, sz.h)
			}
		}
	}
}

func TestHelpReadsSlashAsFind(t *testing.T) {
	m := findModel(t, 80, 24)
	if rows := strings.Join(m.helpRows(78, 40), "\n"); !strings.Contains(stripANSI(rows), "find") {
		t.Fatalf("help should read / as find:\n%s", rows)
	}
}
