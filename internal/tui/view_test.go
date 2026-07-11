package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"screech/internal/core"
	"screech/internal/player"
)

type stubPlayer struct{ ch chan player.Event }

func newStubPlayer() *stubPlayer            { return &stubPlayer{ch: make(chan player.Event, 8)} }
func (s *stubPlayer) Play(url string) error { return nil }
func (s *stubPlayer) Events() <-chan player.Event {
	return s.ch
}
func (s *stubPlayer) Close() error { close(s.ch); return nil }

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
		m.st = st
		m.haveSt = true
		m.ph = phPlay
		m.playStart = m.now.Add(-90 * time.Minute)
		m.track = "An Artist With A Very Long Name " + "· A Title That Goes On And On Forever And Then Some More"
		m.haveTrack = true
		m.trackAt = m.now.Add(-10 * time.Second)
		m.reason = "plays 3 artists you love and also this reason is extremely long"
		m.tw = NewTypewriter(m.reason, m.now.Add(-time.Minute))
		assertFits(t, m.View(), sz.w, sz.h)

		// Decrypt mid-flight.
		m.decrypt = NewDecrypt(strings.ToUpper(st.Name), m.now.Add(-200*time.Millisecond))
		assertFits(t, m.View(), sz.w, sz.h)

		// Loved flash frame.
		m.lovedTrack = true
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
