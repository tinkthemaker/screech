package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tinkthemaker/screech/internal/core"
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

	states := []struct {
		name  string
		apply func(m *Model)
	}{
		{"standby", func(m *Model) {}},
		// Boot is only safe if it fits too — it paints before the first
		// real frame and used to overflow small terminals outright.
		{"boot", func(m *Model) { m.start = m.now.Add(-300 * time.Millisecond) }},
		{"playing", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.st.Name = "The Interminable Frequencies Of The Outer Aetheric Reaches International"
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-90 * time.Minute)
			m.setTrack("An Artist With A Very Long Name", "A Title That Goes On And On Forever And Then Some More")
			m.trackAt = m.now.Add(-10 * time.Second)
			m.reason = "plays 3 artists you love and also this reason is extremely long"
			m.tw = NewTypewriter(m.reason, m.now.Add(-time.Minute))
			m.presets = map[int]string{1: "a", 3: "b", 7: "c"}
		}},
		{"reduced-motion", func(m *Model) {
			m.reduceMotion = true
			m.st = core.SeedStations()[1]
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-90 * time.Minute)
			m.setTrack("Broadcast", "Come On Let's Go")
			m.trackAt = m.now.Add(-10 * time.Second)
		}},
		{"playing-with-history", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.st.Tags = "ambient electronic downtempo"
			m.haveSt = true
			m.ph = phPlay
			m.setTrack("Broadcast", "Come On Let's Go")
			m.trackAt = m.now.Add(-10 * time.Second)
			// Nonzero totals put the density strip on the dial, which
			// costs the receiver a row of headroom.
			m.listenTotals = map[string]time.Duration{"a": time.Hour, "b": time.Minute}
			m.rebuildDensity()
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
		{"paused", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.haveSt = true
			m.ph = phPaused
			m.pausedAt = m.now.Add(-4 * time.Minute)
			m.setTrack("An Artist", "A Title")
			m.trackAt = m.now.Add(-time.Minute)
		}},
		{"resuming", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.haveSt = true
			m.ph = phTune
			m.resuming = true
			m.tuneAt = m.now
		}},
		{"sleep-timer", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-time.Hour)
			m.sleepStep = 1
			m.sleepAt = m.now.Add(29 * time.Minute)
		}},
		{"help", func(m *Model) {
			m.helpOpen = true
		}},
		{"info-loading", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
		}},
		{"info-loaded", func(m *Model) {
			m.st = core.SeedStations()[1]
			m.st.Name = "The Interminable Frequencies Of The Outer Aetheric Reaches International"
			m.st.Country = "Japan"
			m.st.Codec = "aac"
			m.st.Bitrate = 320
			m.st.Tags = "free jazz, turntablism, sound collage, radio art"
			m.st.Homepage = "https://example.org/avery/long/path/on/the/radio/"
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
			m.info = core.StationInfo{
				Listened: 4*time.Hour + 12*time.Minute, Visits: 23,
				FirstHeard: m.now.Add(-40 * 24 * time.Hour), LastHeard: m.now.Add(-2 * time.Hour),
				LovedTracks: 7, Heard: true, Match: 0.71, Daypart: "evening",
			}
		}},
		{"info-error", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
			m.infoErr = fmt.Errorf("database read failed")
		}},
		{"info-never-heard", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
			m.info = core.StationInfo{Daypart: "day"}
		}},
		{"info-strikes", func(m *Model) {
			m.st = core.SeedStations()[0]
			m.st.FailCount = 1
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
			m.info = core.StationInfo{Daypart: "day", Heard: true, Match: 0.4}
		}},
	}

	for _, st := range states {
		st := st
		t.Run(st.name, func(t *testing.T) {
			// Each state owns its model, db and sizes — they sweep in
			// parallel, which is what keeps the full grid cheap.
			t.Parallel()
			m := testModel(t)
			m.start = m.now.Add(-time.Minute) // boot over
			st.apply(&m)
			for _, w := range widths {
				for _, h := range heights {
					mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
					got := mm.(Model)
					var view string
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Fatalf("panic at %dx%d: %v", w, h, r)
							}
						}()
						view = got.View()
					}()
					assertFits(t, view, w, h)
					// A panel frame is whole or absent: a top-left corner
					// without a bottom-left one means the slice cut the
					// panel mid-height — the truncation bug this guards.
					plain := stripANSI(view)
					if strings.Contains(plain, "╭") && !strings.Contains(plain, "╰") {
						t.Fatalf("truncated panel frame at %dx%d", w, h)
					}
					if t.Failed() {
						t.Fatalf("state %s fails at %dx%d", st.name, w, h)
					}
				}
			}
		})
	}
}

// The faceplate's own contract: whenever receiverShape claims the terminal,
// the panel must fit inside the content window it was measured against —
// the old fixed-height accounting could claim a shape it couldn't draw —
// and its bottom frame must actually reach the screen.
func TestReceiverFitsWheneverShapeSaysSo(t *testing.T) {
	for _, w := range []int{78, 80, 82, 100, 140} {
		// One model per width — the fixture mutates size and shape inputs,
		// then relayouts so the wave and density track the real geometry.
		m := testModel(t)
		m.w = w
		m.start = m.now.Add(-time.Minute)
		m.st = core.SeedStations()[0]
		m.haveSt = true
		m.ph = phPlay
		m.reason = "wildcard"
		for h := 10; h <= 40; h++ {
			// The row-accounting check runs for every combination; the
			// full-screen render only once per cell — its assertions are
			// about View() assembling the panel, not the inputs.
			frameChecked := false
			for _, tags := range []bool{false, true} {
				for _, density := range []bool{false, true} {
					m.h = h
					m.st.Tags = ""
					if tags {
						m.st.Tags = "ambient electronic"
					}
					m.listenTotals = nil
					if density {
						m.listenTotals = map[string]time.Duration{"a": time.Hour}
					}
					m.relayout()
					if !m.receiverShape() {
						continue
					}
					iw, _, _ := m.receiverMetrics()
					contentH := m.contentHeight()
					if n := len(m.receiverRows(iw, contentH)); n > contentH {
						t.Fatalf("receiver %d rows in %d at %dx%d (tags=%v density=%v)",
							n, contentH, w, h, tags, density)
					}
					if frameChecked {
						continue
					}
					frameChecked = true
					plain := stripANSI(m.View())
					if !strings.Contains(plain, m.th.G.FrameBL) {
						t.Fatalf("receiver frame bottom missing at %dx%d (tags=%v density=%v)", w, h, tags, density)
					}
					if !strings.Contains(plain, "WHY THIS STATION") {
						t.Fatalf("receiver reason row missing at %dx%d (tags=%v density=%v)", w, h, tags, density)
					}
				}
			}
		}
	}

	// The tall path must actually run: at 100x40 the receiver claims the
	// terminal and the meter grows past one row per channel.
	m := testModel(t)
	m.w, m.h = 100, 40
	m.start = m.now.Add(-time.Minute)
	m.st = core.SeedStations()[0]
	m.haveSt = true
	m.ph = phPlay
	m.relayout()
	if !m.receiverShape() {
		t.Fatal("100x40 should give the receiver")
	}
	if m.wave.Rows() <= 1 {
		t.Fatalf("tall receiver should grow the meter: %d rows per channel", m.wave.Rows())
	}

	// The same promise holds in standby: the plate exists, it's framed,
	// and it says so.
	for _, w := range []int{78, 80, 100} {
		m := testModel(t)
		m.w, m.h = w, 24
		m.start = m.now.Add(-time.Minute)
		if !m.receiverShape() {
			continue
		}
		plain := stripANSI(m.View())
		if !strings.Contains(plain, m.th.G.FrameBL) || !strings.Contains(plain, "STANDBY") {
			t.Fatalf("standby receiver broken at %dx%d", w, m.h)
		}
	}
}

// The header is the last instrument to give up: at widths where everything
// else has folded, the status word must still read.
func TestHeaderStatusSurvivesNarrowWidths(t *testing.T) {
	header := func(mutate func(m *Model), w int) string {
		m := testModel(t)
		m.w, m.h = w, 24
		m.start = m.now.Add(-time.Minute)
		mutate(&m)
		return stripANSI(m.headerBar())
	}

	live := func(m *Model) {
		m.st = core.SeedStations()[0]
		m.haveSt = true
		m.ph = phPlay
		m.playStart = m.now.Add(-time.Minute)
	}
	if got := header(live, 30); !strings.Contains(got, "LIVE") {
		t.Fatalf("30-col playing header lost the status: %q", got)
	}
	if got := header(func(m *Model) { m.ph = phOffline }, 20); !strings.Contains(got, "OFFLINE") {
		t.Fatalf("20-col offline header lost the status: %q", got)
	}
	if got := header(func(m *Model) {
		live(m)
		m.suspect = true
	}, 40); !strings.Contains(got, "BREAK") {
		t.Fatalf("40-col break header lost the chip: %q", got)
	}
	if got := header(func(m *Model) {
		live(m)
		m.syncing = true
	}, 30); !strings.Contains(got, "LIVE") {
		t.Fatalf("syncing must not evict LIVE: %q", got)
	}
	if got := header(func(m *Model) {
		live(m)
		m.syncing = true
	}, 100); !strings.Contains(got, "SYNCING DIRECTORY") || !strings.Contains(got, "LIVE") {
		t.Fatalf("wide syncing header: %q", got)
	}
}

// BUFFERING blinks by style, never by text — a row whose width moved with
// the beat would jitter the whole header every 600ms.
func TestBufferingBlinkKeepsWidth(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.ph = phBuffer
	m.bufferAt = m.now.Add(-time.Second)
	on := m.headerBar()
	m.now = m.now.Add(600 * time.Millisecond)
	off := m.headerBar()
	if stripANSI(on) != stripANSI(off) {
		t.Fatalf("blink must not change the text: %q vs %q", stripANSI(on), stripANSI(off))
	}
	if lipgloss.Width(on) != lipgloss.Width(off) {
		t.Fatalf("blink must not change the width: %d vs %d", lipgloss.Width(on), lipgloss.Width(off))
	}
}

// A station that never sends titles gets an honest hint in the artist
// slot once ten locked seconds have passed — before that the blank row
// still reads as patience.
func TestReceiverNoTitleHint(t *testing.T) {
	hintAt := func(playStarted time.Duration) string {
		m := testModel(t)
		m.w, m.h = 80, 24
		m.start = m.now.Add(-time.Minute)
		m.st = core.SeedStations()[0]
		m.haveSt = true
		m.ph = phPlay
		m.playStart = m.now.Add(-playStarted)
		return stripANSI(m.View())
	}
	if got := hintAt(30 * time.Second); !strings.Contains(got, "No track info") {
		t.Fatalf("a long title-less play should say why: %q", got)
	}
	if got := hintAt(2 * time.Second); strings.Contains(got, "No track info") {
		t.Fatalf("a fresh tune must not cry no-info yet: %q", got)
	}
}

// recentTop is bounded by what the screen can show — unclamped scrolling
// used to bury the list under dead keypresses.
func TestRecentScrollClamps(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.history = true
	m.historyView = historyRecent
	m.recent = make([]core.RecentTrack, 50)
	for i := range m.recent {
		m.recent[i] = core.RecentTrack{Title: fmt.Sprintf("Title %02d", i), HeardAt: m.now}
	}
	press := func(k tea.KeyType) {
		mm, _ := m.Update(tea.KeyMsg{Type: k})
		m = mm.(Model)
	}
	for i := 0; i < 5; i++ {
		press(tea.KeyUp)
	}
	if m.recentTop != 0 {
		t.Fatalf("k at the top must not go negative, got %d", m.recentTop)
	}
	press(tea.KeyDown)
	if m.recentTop != 1 {
		t.Fatalf("j should advance one, got %d", m.recentTop)
	}
	press(tea.KeyEnd)
	press(tea.KeyDown)
	if m.recentTop != m.recentMaxTop() {
		t.Fatalf("end should land on maxTop %d, got %d", m.recentMaxTop(), m.recentTop)
	}
	if !strings.Contains(stripANSI(m.View()), "Title 49") {
		t.Fatal("the last entry should be visible after end")
	}
}

// At 80 columns the main key strip keeps every key and every state label;
// that was the width that used to fall straight to bare letters.
func TestFooterKeepsLabelsAtEighty(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.st = core.SeedStations()[0]
	m.haveSt = true
	m.ph = phPlay
	m.volume = 100
	m.sleepStep = 1
	m.sleepAt = m.now.Add(29 * time.Minute)
	got := stripANSI(m.footerBar())
	for _, want := range []string{"SPACE", "L", "F", "P", "/", "H", "V", "Z", "?", "Q"} {
		if !strings.Contains(got, want) {
			t.Errorf("80-col footer missing chip %q: %q", want, got)
		}
	}
	// State labels are the ones worth keeping when the ladder trims:
	// love, save, pause-state, volume, sleep all outrank key legends.
	for _, want := range []string{"love", "save", "pause", "100%", "sleep 29m"} {
		if !strings.Contains(got, want) {
			t.Errorf("80-col footer dropped state label %q: %q", want, got)
		}
	}
}

// Feedback text splits on its double space: the label styles, the detail
// keeps its case. The receiver used to lowercase the whole line.
func TestReceiverFeedbackSplit(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)
	m.st = core.SeedStations()[0]
	m.haveSt = true
	m.ph = phPlay
	m.setFeedback("LOVED  Track and station")
	if !m.receiverShape() {
		t.Skip("needs the receiver layout")
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "LOVED") || !strings.Contains(plain, "Track and station") {
		t.Fatalf("feedback split lost: %q", plain)
	}
}

// The startup notice survives the first pick — that's the whole point of
// carrying it separately from m.note — and dies on the first keypress.
func TestStartupNoticeSurvivesPick(t *testing.T) {
	c, err := core.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m := New(c, newStubPlayer(), Options{Accent: "#FFB000", Notice: "database was damaged; moved aside"})
	m.w, m.h = 80, 24
	m.start = m.now.Add(-time.Minute)

	nm, _ := m.applyPick(core.Pick{Station: core.SeedStations()[0]})
	m = nm
	if !strings.Contains(stripANSI(m.View()), "NOTICE") {
		t.Fatal("notice must survive a pick")
	}
	km, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = km.(Model)
	if m.notice != "" {
		t.Fatal("first keypress must acknowledge the notice")
	}
}

// Reduced motion draws the settled frame immediately: the station name
// reads plainly, no scramble glyphs ever appear.
func TestReducedMotionSkipsTheCeremony(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 100, 30
	m.reduceMotion = true
	m.now = m.now.Add(time.Second)
	nm, _ := m.applyPick(core.Pick{Station: core.SeedStations()[0]})
	m = nm
	// No scramble was ever armed.
	if m.decrypt.Active(m.now) || m.stDecrypt.Active(m.now) {
		t.Fatal("reduced motion must not arm the decrypts")
	}
	// Mid-tune: without reduced motion this frame is mid-scramble. The
	// glyph check is scoped to the BROADCAST plate — the dial band legit-
	// imately shares characters with the scramble set.
	lines := strings.Split(stripANSI(m.View()), "\n")
	plate := ""
	for i, ln := range lines {
		if strings.Contains(ln, "BROADCAST") && i+1 < len(lines) {
			plate = lines[i+1]
		}
	}
	for _, g := range m.th.G.Decrypt {
		if strings.ContainsRune(plate, rune(g)) {
			t.Fatalf("reduced motion shows scramble glyphs on the plate: %q", plate)
		}
	}
	plain := strings.Join(lines, "\n")
	if !strings.Contains(plain, strings.ToUpper(cleanStationName(m.st.Name))) {
		t.Fatalf("settled name missing under reduced motion: %q", plain)
	}
}

// In the 7-bit palette nothing above ASCII may reach the screen — box
// corners, hearts, dots, all of it degrades to characters every terminal
// can draw.
func TestASCIIModeIsSevenBit(t *testing.T) {
	c, err := core.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	newASCII := func() Model {
		return New(c, newStubPlayer(), Options{Accent: "#FFB000", ASCII: true})
	}
	states := []func(m *Model){
		func(m *Model) {},
		func(m *Model) { // boot
			m.start = m.now
		},
		func(m *Model) { // playing with track, love, feedback
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-time.Minute)
			m.setTrack("An Artist", "A Title")
			m.trackAt = m.now.Add(-time.Second)
			m.lovedTrack = true
			m.setFeedback("LOVED  Track and station")
			m.presets = map[int]string{1: "a"}
			m.reason = "wildcard"
			m.listenTotals = map[string]time.Duration{"a": time.Hour}
			m.rebuildDensity()
		},
		func(m *Model) { // standby with notice
			m.notice = "database was damaged; moved aside"
		},
		func(m *Model) { // offline
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phOffline
			m.retryN = 1
			m.retryAt = m.now.Add(8 * time.Second)
		},
		func(m *Model) { // recent history
			m.history = true
			m.historyView = historyRecent
			m.recent = []core.RecentTrack{
				{StationName: "A Station", Artist: "An Artist", Title: "A Title", HeardAt: m.now},
			}
		},
		func(m *Model) { // loved history
			m.history = true
			m.historyView = historyLoved
			m.library = []core.LovedTrack{
				{StationName: "A Station", Artist: "An Artist", Title: "A Title", LovedAt: m.now, HeardCount: 3},
			}
		},
		func(m *Model) { // stations history
			m.history = true
			m.historyView = historyStations
			m.saved = []core.SavedStation{
				{UUID: "x", Name: "A Station", PresetSlot: 1, LoveCount: 2, Total: time.Hour},
			}
		},
		func(m *Model) { m.themeOpen = true }, // theme picker
		func(m *Model) { // seed prompt
			m.prompt = true
			m.buf = []rune("corridos")
		},
		func(m *Model) { // volume strip
			m.volumeOpen = true
			m.volume = 42
		},
		func(m *Model) { // paused
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phPaused
			m.pausedAt = m.now.Add(-2 * time.Minute)
		},
		func(m *Model) { // resuming
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phTune
			m.resuming = true
		},
		func(m *Model) { // sleep timer armed
			m.st = core.SeedStations()[0]
			m.haveSt = true
			m.ph = phPlay
			m.playStart = m.now.Add(-time.Hour)
			m.sleepStep = 0
			m.sleepAt = m.now.Add(14 * time.Minute)
		},
		func(m *Model) { // help sheet
			m.helpOpen = true
		},
		func(m *Model) { // info sheet, loaded
			m.st = core.SeedStations()[0]
			m.st.Country = "Japan"
			m.st.Bitrate = 128
			m.st.Tags = "jazz, soul"
			m.st.Homepage = "https://example.org/radio/"
			m.haveSt = true
			m.infoOpen = true
			m.infoFor = m.st.UUID
			m.info = core.StationInfo{
				Listened: 2 * time.Hour, Visits: 5,
				FirstHeard:  m.now.Add(-72 * time.Hour),
				LastHeard:   m.now.Add(-time.Hour),
				LovedTracks: 3, Heard: true, Match: 0.65, Daypart: "evening",
			}
		},
	}
	sizes := []struct{ w, h int }{{100, 30}, {80, 24}, {60, 20}, {30, 10}}
	for i, apply := range states {
		for _, sz := range sizes {
			m := newASCII()
			m.w, m.h = sz.w, sz.h
			m.start = m.now.Add(-time.Minute) // boot over
			apply(&m)
			for _, r := range stripANSI(m.View()) {
				if r >= 0x80 {
					t.Fatalf("state %d at %dx%d prints non-ASCII %q", i, sz.w, sz.h, string(r))
				}
			}
		}
	}
}
