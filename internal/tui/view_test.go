package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/session"
)

type stubPlayer struct {
	mu       sync.Mutex // like MPV's: the event channel swaps under it
	ch       chan player.Event
	volume   int
	playURLs []string
	playErr  error // injected Play failure
	paused   bool
	calls    []string // ordered log across Play/SetVolume/SetPaused/Restart
	restarts int
	closed   bool
}

func newStubPlayer() *stubPlayer { return &stubPlayer{ch: make(chan player.Event, 8)} }
func (s *stubPlayer) Play(url string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playURLs = append(s.playURLs, url)
	s.calls = append(s.calls, "play "+url)
	return s.playErr
}
func (s *stubPlayer) SetVolume(percent int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = percent
	s.calls = append(s.calls, fmt.Sprintf("vol %d", percent))
	return nil
}
func (s *stubPlayer) SetPaused(paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = paused
	s.calls = append(s.calls, fmt.Sprintf("pause %t", paused))
	return nil
}
func (s *stubPlayer) Events() <-chan player.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ch
}

// Restart swaps in a fresh event channel like a real backend respawn; the
// old channel is left open — nobody should be listening to it anymore.
func (s *stubPlayer) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts++
	s.calls = append(s.calls, "restart")
	s.ch = make(chan player.Event, 8)
	return nil
}
func (s *stubPlayer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	return nil
}

func testModel(t *testing.T) Model {
	t.Helper()
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return New(c, newStubPlayer(), Options{Accent: "#FFB000"})
}

// assertFits: every rendered line must fit the terminal — truncate with
// ellipsis, never wrap, never overflow. This is the resize contract.
func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("view has %d lines for height %d", len(lines), h)
	}
	for i, ln := range lines {
		if lw := lipgloss.Width(ln); lw > w {
			t.Errorf("line %d width %d exceeds terminal width %d: %q", i, lw, w, ln)
		}
	}
}

func TestViewNeverOverflows(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24}, {64, 16}, {50, 12}, {38, 10}, {24, 8}, {20, 6}, {200, 50},
	}
	for _, sz := range sizes {
		m := testModel(t)
		mm, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		m = mm.(Model)

		// Boot frame.
		assertFits(t, m.View(), sz.w, sz.h)

		// Pretend boot finished and a station with an obnoxiously long name
		// and track is playing.
		m.start = m.now.Add(-time.Minute)
		st := core.SeedStations()[1]
		st.Name = "The Interminable Frequencies Of The Outer Aetheric Reaches International"
		m.s.Station = st
		m.s.HaveStation = true
		m.s.Phase = session.Playing
		m.s.PlayStart = m.now.Add(-90 * time.Minute)
		m.setTrack("An Artist With A Very Long Name", "A Title That Goes On And On Forever And Then Some More")
		m.s.HaveTrack = true
		m.trackAt = m.now.Add(-10 * time.Second)
		m.s.Reason = "plays 3 artists you love and also this reason is extremely long"
		m.tw = NewTypewriter(m.s.Reason, m.now.Add(-time.Minute))
		assertFits(t, m.View(), sz.w, sz.h)

		// Decrypt mid-flight.
		m.decrypt = NewDecrypt(m.now.Add(-200 * time.Millisecond))
		assertFits(t, m.View(), sz.w, sz.h)

		// Loved flash frame.
		m.s.LovedTrack = true
		m.loveAt = m.now.Add(-50 * time.Millisecond)
		assertFits(t, m.View(), sz.w, sz.h)

		// Idle mode.
		m.lastKey = m.now.Add(-10 * time.Minute)
		assertFits(t, m.View(), sz.w, sz.h)
	}
}

func TestMarqueeContract(t *testing.T) {
	now := time.Now()
	anchor := now.Add(-time.Millisecond)
	short := marquee("short", 20, now, anchor, "…")
	if short != "short" {
		t.Errorf("short text must pass through: %q", short)
	}
	long := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for dt := time.Duration(0); dt < 12*time.Second; dt += 333 * time.Millisecond {
		got := marquee(long, 10, anchor.Add(dt), anchor, "…")
		if w := lipgloss.Width(got); w > 10 {
			t.Fatalf("marquee frame at %v exceeds width: %q (%d)", dt, got, w)
		}
	}
	// During the initial pause the head of the string must show.
	head := marquee(long, 10, anchor.Add(500*time.Millisecond), anchor, "…")
	if !strings.HasPrefix(head, "0123") {
		t.Errorf("expected head during pre-pause, got %q", head)
	}
	// One pass, then it rests: well past a full cycle the title sits on its
	// ellipsis-truncated head — and stays identical forever after.
	rest := marquee(long, 10, anchor.Add(20*time.Second), anchor, "…")
	if rest != runewidth.Truncate(long, 10, "…") {
		t.Fatalf("after the pass the title should rest truncated, got %q", rest)
	}
	if marquee(long, 10, anchor.Add(60*time.Second), anchor, "…") != rest {
		t.Fatal("the rest state must be permanent, not a loop")
	}
	// During the closing pause the offset clamps at overflow — the tail
	// holds full-width instead of shrinking off the left edge.
	overflow := runewidth.StringWidth(long) - 10
	holdAt := 2*time.Second + time.Duration(overflow)*140*time.Millisecond + time.Second
	tail := marquee(long, 10, anchor.Add(holdAt), anchor, "…")
	if tail != sliceCols(long, overflow, 10) {
		t.Fatalf("end pause should hold the tail, got %q", tail)
	}
	if w := lipgloss.Width(tail); w != 10 {
		t.Fatalf("clamped tail must fill the width: %q is %d cols", tail, w)
	}
}

func TestSliceColsWideRunes(t *testing.T) {
	// CJK runes are two columns wide; slicing must respect that.
	s := "abc漢字def"
	if got := sliceCols(s, 0, 5); lipgloss.Width(got) > 5 {
		t.Errorf("slice too wide: %q", got)
	}
	if got := sliceCols(s, 4, 4); lipgloss.Width(got) > 4 {
		t.Errorf("offset slice too wide: %q", got)
	}
}

func TestHeroTextFallsBack(t *testing.T) {
	if got := heroText("Soma", 40, "…"); got != "S O M A" {
		t.Errorf("short names letterspace: %q", got)
	}
	long := "A Station Name Far Too Long To Letterspace Into Forty Columns"
	got := heroText(long, 40, "…")
	if lipgloss.Width(got) > 40 {
		t.Errorf("hero overflows: %q", got)
	}
	if strings.Contains(got, "A  S T A T I O N") {
		t.Errorf("long names must not letterspace: %q", got)
	}
}

func TestHeroTextDoesNotTrackLongStationNames(t *testing.T) {
	got := heroText("DRGNU - Death Metal", 72, "…")
	if got != "DRGNU - DEATH METAL" {
		t.Fatalf("long station names should keep natural spacing: %q", got)
	}
}

// The faceplate contract: brand bar on the first row, key strip pinned to
// the very last row, content vertically centered between them. The app
// claims the whole terminal instead of floating as an island.
func TestFaceplateClaimsTheTerminal(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)
	m.s.Station = core.SeedStations()[0]
	m.s.HaveStation = true
	m.s.Phase = session.Playing
	m.s.Reason = "wildcard"
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.h {
		t.Fatalf("view must fill the terminal exactly: %d lines for height %d", len(lines), m.h)
	}
	if !strings.Contains(lines[0], wordmark) {
		t.Fatalf("brand bar missing from row 0: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "SPACE") || !strings.Contains(last, "quit") {
		t.Fatalf("key strip missing from the last row: %q", last)
	}
	// Content sits between the chrome, roughly centered: the hero station
	// name should be in the middle band of the screen, not hugging an edge.
	hero := -1
	for i, line := range lines {
		if strings.Contains(line, "GROOVE SALAD") {
			hero = i
		}
	}
	if hero < m.h/4 || hero > 3*m.h/4 {
		t.Fatalf("hero not centered between chrome: row %d of %d", hero, m.h)
	}
}

func TestWideReceiverComposition(t *testing.T) {
	m := testModel(t)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 32})
	m = mm.(Model)
	m.start = m.now.Add(-time.Minute)
	m.s.Station = core.SeedStations()[0]
	m.s.HaveStation = true
	m.s.Phase = session.Playing
	m.setTrack("Tycho", "A Walk")
	m.s.HaveTrack = true
	m.trackAt = m.now.Add(-time.Minute)
	m.s.Reason = "plays 3 artists you love"

	view := m.View()
	for _, want := range []string{
		"RECEIVER / NOW PLAYING", "TRACK", "SIGNAL", "A Walk", "Tycho",
		"BROADCAST", "STATION MEMORY", "WHY THIS STATION",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("wide receiver missing %q", want)
		}
	}
	assertFits(t, view, 110, 32)

	// Every row of the receiver must terminate in the same column. In
	// particular, the labeled top rail must not overhang the right edge.
	lines := strings.Split(view, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "RECEIVER / NOW PLAYING") {
			start = i
		}
		if start >= 0 && strings.Contains(line, m.th.G.FrameBR) {
			end = i
			break
		}
	}
	if start < 0 || end < start {
		t.Fatal("could not locate receiver frame")
	}
	wantWidth := lipgloss.Width(lines[start])
	for i := start + 1; i <= end; i++ {
		if got := lipgloss.Width(lines[i]); got != wantWidth {
			t.Errorf("receiver row %d ends at column %d; top rail ends at %d", i-start, got, wantWidth)
		}
	}
}

// The gray family derives from the accent: warm accents warm the grays,
// cool accents cool them. Two different accents must produce two different
// gray families, and the dim floor must stay legible (not near-black).
func TestThemeGraysFollowAccent(t *testing.T) {
	warm := NewTheme("#FFB000", false)
	cool := NewTheme("#7D56F4", false)
	if warm.Mid.GetForeground() == cool.Mid.GetForeground() {
		t.Error("grays must derive from the accent, not a fixed olive")
	}
	if warm.Dim.GetForeground() == nil || warm.Mid.GetForeground() == nil || warm.Bright.GetForeground() == nil {
		t.Fatal("gray styles must carry colors")
	}
}

// The wave ramp starts at a visible ember, not near-black, and climbs.
func TestThemeRampBaseIsVisible(t *testing.T) {
	th := NewTheme("#FFB000", false)
	base := th.Ramp[0].GetForeground()
	top := th.Ramp[len(th.Ramp)-1].GetForeground()
	if base == nil || top == nil {
		t.Fatal("ramp steps must carry colors")
	}
	if base == top {
		t.Error("ramp must climb from ember to pale, not sit flat")
	}
}

// statusRow assigns a semantic glyph per category: accent for seed/love,
// mid for recall, dim for wildcard.
func TestStatusRowCategoryGlyphs(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	cases := []struct {
		reason string
		want   string
	}{
		{"seeded: ambient", m.th.G.Pointer},
		{"preset 3", m.th.G.Tick},
		{"from your history", m.th.G.Tick},
		{"wildcard", m.th.G.Dot},
	}
	for _, tc := range cases {
		got := m.statusRow(statusReason(tc.reason), 64, false)
		if !strings.Contains(got, tc.want) {
			t.Errorf("reason %q should carry glyph %q: %q", tc.reason, tc.want, got)
		}
	}
}

func TestClockReadout(t *testing.T) {
	cases := map[time.Duration]string{
		0:                             "00:00",
		42 * time.Second:              "00:42",
		9*time.Minute + 3*time.Second: "09:03",
		2*time.Hour + 4*time.Minute:   "2:04:00",
	}
	for in, want := range cases {
		if got := fmtClock(in); got != want {
			t.Errorf("fmtClock(%v)=%q, want %q", in, got, want)
		}
	}
}

func TestDialPosStable(t *testing.T) {
	a := stationDialPos("some-uuid")
	b := stationDialPos("some-uuid")
	if a != b {
		t.Error("dial position must be stable per station")
	}
	if a < 0.05 || a > 0.95 {
		t.Errorf("dial position out of band: %v", a)
	}
}
