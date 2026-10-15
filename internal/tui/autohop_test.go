package tui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

func TestAutoHopOnMarkerTitle(t *testing.T) {
	m := playing(t, testModel(t))
	m.autoHop = true
	uuid := m.st.UUID
	betaBefore := m.core.BanditBeta(uuid)

	nm, cmd := m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	m = nm.(Model)
	if m.ph != phTune {
		t.Fatalf("marker title should hop, got ph %v", m.ph)
	}
	if cmd == nil {
		t.Fatal("hop should issue work")
	}
	if !strings.Contains(m.feedback, "HOPPED") {
		t.Fatalf("feedback = %q, want HOPPED", m.feedback)
	}
	if m.hopAt.IsZero() {
		t.Fatal("hopAt should be stamped")
	}

	// The hop is a skip during a break: Tune closes the listen with the
	// discounted beta, not the full fast-skip penalty. Running the batch
	// executes tuneCmd, which is where the listen ends.
	for _, out := range runCmd(t, cmd) {
		m = drive(t, m, out)
	}
	// BanditBeta sums two fresh rows (the daypart's and all-time's), each
	// prior 1 plus the discounted 0.25: 2.5 total. A full fast-skip would
	// have landed 4.0.
	if got := m.core.BanditBeta(uuid) - betaBefore; math.Abs(got-2.5) > 0.01 {
		t.Fatalf("hop beta delta = %v, want ~2.5 (discounted break skip on two rows)", got)
	}
}

func TestAutoHopCooldown(t *testing.T) {
	m := playing(t, testModel(t))
	m.autoHop = true

	nm, _ := m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	m = nm.(Model)
	if m.ph != phTune {
		t.Fatalf("first marker should hop, got %v", m.ph)
	}

	// Landed on the next station, still playing — another break marker
	// inside the cooldown must not chain a second hop.
	m.ph = phPlay
	m.suspect = false
	nm, _ = m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "Commercial Break - back soon"}))
	m = nm.(Model)
	if m.ph != phPlay {
		t.Fatalf("cooldown should hold the hop, got ph %v", m.ph)
	}

	m.now = m.hopAt.Add(autoHopCooldown + time.Second)
	nm, _ = m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "Werbung"}))
	m = nm.(Model)
	if m.ph != phTune {
		t.Fatalf("marker past the cooldown should hop again, got %v", m.ph)
	}
}

func TestAutoHopRespectsOptInAndStrictness(t *testing.T) {
	// Opted out: a marker title is still suspicion, never a hop.
	m := playing(t, testModel(t))
	nm, _ := m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	m = nm.(Model)
	if m.ph != phPlay {
		t.Fatalf("opted-out model must not hop, got %v", m.ph)
	}
	if !m.suspect {
		t.Fatal("marker title should still mark the break suspect")
	}

	// Opted in but an empty title: SuspectedAd territory only, no hop.
	// (A real track lands first — an empty title before any track is
	// treated as ICY silence, not suspicion.)
	m = playing(t, testModel(t))
	m.autoHop = true
	nm, _ = m.Update(evMsg(player.Event{Type: player.EventTitle, Title: "Artist - Title"}))
	m = nm.(Model)
	nm, _ = m.Update(evMsg(player.Event{Type: player.EventTitle, Title: ""}))
	m = nm.(Model)
	if m.ph != phPlay {
		t.Fatalf("empty title must not hop, got %v", m.ph)
	}
	if !m.suspect {
		t.Fatal("empty title should still be suspect")
	}
}
