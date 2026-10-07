package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"screech/internal/core"
)

// The resize contract at every size: no panic, no overflow, no extra lines.
// Small terminals collapse into the compact layouts rather than refusing to
// start, so every cell of the grid has to hold up — not just the sizes a
// developer happens to run.
func TestViewFitsEveryTerminalSize(t *testing.T) {
	// Every w in 1..30 and every h in 1..16, sampled beyond to keep the
	// sweep under a few seconds.
	widths := make([]int, 0, 64)
	for w := 1; w <= 30; w++ {
		widths = append(widths, w)
	}
	for w := 32; w <= 140; w += 4 {
		widths = append(widths, w)
	}
	heights := make([]int, 0, 24)
	for h := 1; h <= 16; h++ {
		heights = append(heights, h)
	}
	for h := 20; h <= 50; h += 5 {
		heights = append(heights, h)
	}

	longTrack := "An Artist With A Very Long Name · A Title That Goes On And On Forever And Then Some More"
	states := []struct {
		name  string
		apply func(m *Model)
	}{
		{"standby", func(m *Model) {}},
		{"playing", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.st.Name = "The Interminable Frequencies Of The Outer Aetheric Reaches International"
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-90 * time.Minute)
			m.track = longTrack
			m.haveTrack = true
			m.trackAt = m.now.Add(-10 * time.Second)
			m.reason = "plays 3 artists you love and also this reason is extremely long"
			m.tw = NewTypewriter(m.reason, m.now.Add(-time.Minute))
			m.presets = map[int]string{1: "a", 3: "b", 7: "c"}
		}},
		{"history-recent", func(m *Model) {
			m.history = true
			m.historyView = historyRecent
			m.recent = []core.RecentTrack{
				{StationName: "A Station", Artist: "An Artist", Title: "A Title", HeardAt: m.now},
			}
		}},
		{"history-loved", func(m *Model) {
			m.history = true
			m.historyView = historyLoved
			m.library = []core.LovedTrack{
				{StationName: "A Station", Artist: "An Artist", Title: "A Title", LovedAt: m.now},
			}
		}},
		{"history-stations", func(m *Model) {
			m.history = true
			m.historyView = historyStations
			m.saved = []core.SavedStation{
				{UUID: "x", Name: "A Station", PresetSlot: 1},
			}
		}},
		{"theme-picker", func(m *Model) {
			m.themeOpen = true
		}},
		{"seed-prompt", func(m *Model) {
			m.prompt = true
			m.buf = []rune("corridos")
		}},
		{"volume", func(m *Model) {
			m.volumeOpen = true
		}},
		{"fatal", func(m *Model) {
			m.fatal = "player backend closed"
			m.ph = phDead
		}},
		{"offline", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phOffline
			m.retryN = 1
			m.retryAt = m.now.Add(8 * time.Second)
			m.reason = "because you love stations like this"
		}},
	}

	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			m := testModel(t)
			m.start = m.now.Add(-time.Minute) // boot over
			st.apply(&m)
			for _, w := range widths {
				for _, h := range heights {
					mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
					got := mm.(Model)
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Fatalf("panic at %dx%d: %v", w, h, r)
							}
						}()
						assertFits(t, got.View(), w, h)
					}()
					if t.Failed() {
						t.Fatalf("state %s fails at %dx%d", st.name, w, h)
					}
				}
			}
		})
	}
}
