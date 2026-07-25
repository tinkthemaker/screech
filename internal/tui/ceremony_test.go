package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"screech/internal/core"
	"screech/internal/player"
)

// tuneModel returns a model mid-ceremony: a station picked, phase phTune,
// the ceremony clock anchored at m.now.
func tuneModel(t *testing.T) Model {
	t.Helper()
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)
	m.st = core.SeedStations()[1]
	m.haveSt = true
	m.ph = phTune
	m.tuneAt = m.now
	return m
}

// The static collapse plays only during tune/buffer inside its bounded
// window, and always ends at stream lock.
func TestStaticCollapseWindow(t *testing.T) {
	m := tuneModel(t)
	if !m.staticActive() {
		t.Fatal("phTune just after tuneAt should play static")
	}

	m.ph = phBuffer
	if !m.staticActive() {
		t.Fatal("phBuffer inside the window should keep the static")
	}

	m.ph = phPlay // EventPlaying arrived: the bay resolves to real audio
	if m.staticActive() {
		t.Fatal("stream lock must end the static immediately")
	}

	m.ph = phTune
	m.now = m.tuneAt.Add(staticDur + time.Millisecond)
	if m.staticActive() {
		t.Fatal("the window is bounded: static must end even without a lock")
	}

	m.ph = phDead
	m.now = m.tuneAt
	if m.staticActive() {
		t.Fatal("the dead path must not freeze on static")
	}
}

// Static renders exactly two rows of exactly the bay width, is a pure
// function of the ceremony clock, and fizzles toward the baseline.
func TestStaticRowsDeterministicAndFizzling(t *testing.T) {
	m := tuneModel(t)
	rows := m.staticRows(23, m.th)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	for i, r := range rows {
		if lw := lipgloss.Width(r); lw != 23 {
			t.Errorf("row %d width %d, want 23", i, lw)
		}
	}
	again := m.staticRows(23, m.th)
	if rows[0] != again[0] || rows[1] != again[1] {
		t.Error("same elapsed time must render the same frame")
	}

	// A later frame is a different noise cut.
	m.now = m.tuneAt.Add(50 * time.Millisecond)
	later := m.staticRows(23, m.th)
	if later[0] == rows[0] && later[1] == rows[1] {
		t.Error("static must churn frame to frame")
	}

	// Density fades: the last frame is mostly baseline, the first mostly
	// noise.
	m.now = m.tuneAt.Add(10 * time.Millisecond)
	early := noiseCells(m.staticRows(40, m.th), m.th)
	m.now = m.tuneAt.Add(staticDur - 20*time.Millisecond)
	late := noiseCells(m.staticRows(40, m.th), m.th)
	if late >= early {
		t.Errorf("static should fizzle out: early %d noise cells, late %d", early, late)
	}
}

func noiseCells(rows []string, th Theme) int {
	base := string(rune(brailleBlank + brailleBaselineBits))
	if th.G.Blocks[0] == '_' {
		base = string(th.G.Blocks[0])
	}
	n := 0
	for _, r := range rows {
		n += strings.Count(stripANSI(r), base)
	}
	return 80 - n // 2 rows x 40 cells minus baseline cells
}

// The 7-bit palette gets its own noise, never mojibake.
func TestStaticASCIIFallback(t *testing.T) {
	m := tuneModel(t)
	m.th = NewTheme("#FFB000", true)
	allowed := map[rune]bool{'#': true, '%': true, '*': true, '_': true}
	for _, r := range m.staticRows(16, m.th) {
		if lw := lipgloss.Width(r); lw != 16 {
			t.Fatalf("ascii static width %d, want 16", lw)
		}
		for _, ru := range stripANSI(r) {
			if !allowed[ru] {
				t.Errorf("ascii static holds glyph %#U", ru)
			}
		}
	}
}

// The ceremony must respect every render contract at every size.
func TestStaticFitsAllSizes(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{110, 32}, {80, 24}, {64, 16}, {40, 10}, {24, 8}, {20, 6}} {
		m := tuneModel(t)
		m.w, m.h = sz.w, sz.h
		assertFits(t, m.View(), sz.w, sz.h)
	}
}

// While the needle travels (or the stream hasn't locked), the dial carries
// the detune smear; settled on a lock, the plain warm bleed returns.
func TestDetuneSmearClearsOnSettledLock(t *testing.T) {
	m := testModel(t)
	m.dial = NewSpring(0.1)
	m.dialTgt = 0.9 // mid-sweep: not settled
	m.ph = phPlay

	if band := m.bandRowTheme(40, false, m.th); !strings.Contains(band, "░") {
		t.Fatal("a traveling needle should trail the detune smear")
	}

	// Settled but not yet locked: still detuning.
	m.dial.Pos, m.dial.Vel = 0.9, 0
	m.ph = phTune
	m.tuneAt = m.now
	if band := m.bandRowTheme(40, false, m.th); !strings.Contains(band, "░") {
		t.Fatal("a settled needle on an unlocked stream should keep the smear")
	}

	// Settled and locked: warm bleed only.
	m.ph = phPlay
	band := m.bandRowTheme(40, false, m.th)
	if strings.Contains(band, "░") {
		t.Fatal("settled on a lock should clear the smear")
	}
	if !strings.Contains(band, m.th.G.Marker) {
		t.Fatal("the marker itself must survive the ceremony")
	}
}

// The receiver's BROADCAST row decrypt-resolves the station name instead of
// swapping it in instantly.
func TestReceiverStationDecrypts(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)
	st := core.SeedStations()[1]

	mm, _ := m.applyPick(core.Pick{Station: st, Reason: "wildcard"})
	m = mm
	if !m.stDecrypt.Active(m.now) {
		t.Fatal("applyPick should arm the station-name decrypt")
	}
	row := m.broadcastRow(t)
	if strings.Contains(stripANSI(row), strings.ToUpper(st.Name)) {
		t.Fatal("the name must not be resolved at the start of the ceremony")
	}

	m.now = m.now.Add(time.Second) // decrypt dur elapsed
	row = m.broadcastRow(t)
	if !strings.Contains(stripANSI(row), strings.ToUpper(st.Name)) {
		t.Fatalf("the name should resolve with the lock:\n%s", row)
	}
}

// broadcastRow finds the station-name row by locating the BROADCAST label
// above it. The faceplate's row count now varies with terminal height, so
// indexing into it by a fixed number would silently start testing a
// different row on a different screen.
func (m Model) broadcastRow(t *testing.T) string {
	t.Helper()
	rows := m.receiverRows(m.innerWidth(), m.contentHeight())
	for i, r := range rows {
		if strings.Contains(stripANSI(r), "BROADCAST") && i+1 < len(rows) {
			return rows[i+1]
		}
	}
	t.Fatal("no BROADCAST label in the faceplate")
	return ""
}

// The dead path is intentional: the meter fizzles to its ember baseline.
func TestDeadPathFizzles(t *testing.T) {
	m := tuneModel(t)
	m.wave.SetEnergy(1)
	mm, _ := m.handlePlayerEvent(player.Event{Type: player.EventDied})
	m = mm.(Model)
	if m.ph != phDead {
		t.Fatal("EventDied should park the phase machine at phDead")
	}
	if m.wave.targetEnergy > 0.1 {
		t.Errorf("dead path energy = %.2f, want a fizzle toward the baseline", m.wave.targetEnergy)
	}
	if m.staticActive() {
		t.Error("phDead must not hold the static open")
	}
}
