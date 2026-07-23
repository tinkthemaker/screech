package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"screech/internal/core"
)

// Daypart temperature: midday runs brighter and cooler, night dims and warms
// toward ember, evening is the neutral reference the themes were authored
// against.
func TestDaypartTempersTheme(t *testing.T) {
	eve := NewDaypartTheme("receiver", "#FFB000", false, core.DaypartEvening)
	day := NewDaypartTheme("receiver", "#FFB000", false, core.DaypartDay)
	if eve.AccentHex != "#FFB000" {
		t.Fatalf("evening is the neutral reference: %q", eve.AccentHex)
	}
	sum := func(hex string) int {
		r, g, b := hexRGB(hex)
		return r + g + b
	}
	if sum(day.AccentHex) <= sum(eve.AccentHex) {
		t.Errorf("midday should run brighter than evening: %q vs %q", day.AccentHex, eve.AccentHex)
	}

	// Warmth reads best in a hue that carries blue: azure at night pulls red
	// up and blue down; during the day it pushes cooler.
	azEve := NewDaypartTheme("azure", "", false, core.DaypartEvening)
	azNight := NewDaypartTheme("azure", "", false, core.DaypartNight)
	azDay := NewDaypartTheme("azure", "", false, core.DaypartDay)
	gap := func(hex string) int {
		r, _, b := hexRGB(hex)
		return r - b
	}
	if gap(azNight.AccentHex) <= gap(azEve.AccentHex) {
		t.Errorf("night should run warmer than evening: %q vs %q", azNight.AccentHex, azEve.AccentHex)
	}
	if gap(azDay.AccentHex) >= gap(azEve.AccentHex) {
		t.Errorf("midday should run cooler than evening: %q vs %q", azDay.AccentHex, azEve.AccentHex)
	}
	if sum(azNight.AccentHex) >= sum(azEve.AccentHex) {
		t.Errorf("night should dim the accent: %q vs %q", azNight.AccentHex, azEve.AccentHex)
	}

	// Austere has no hue to warm: daypart is a lightness shift only, and the
	// monochrome contract holds in every daypart.
	for _, dp := range []string{core.DaypartMorning, core.DaypartDay, core.DaypartEvening, core.DaypartNight} {
		th := NewDaypartTheme("austere", "", false, dp)
		for name, hex := range map[string]string{
			"accent": th.AccentHex,
			"mid":    fmt.Sprint(th.Mid.GetForeground()),
			"dim":    fmt.Sprint(th.Dim.GetForeground()),
			"border": fmt.Sprint(th.PanelBorder.GetForeground()),
			"lovebg": fmt.Sprint(th.LoveFill.GetBackground()),
		} {
			r, g, b := hexRGB(hex)
			if r != g || g != b {
				t.Errorf("austere %s in %s lost monochrome: %q", name, dp, hex)
			}
		}
	}
	auDay := NewDaypartTheme("austere", "", false, core.DaypartDay)
	auNight := NewDaypartTheme("austere", "", false, core.DaypartNight)
	if sum(auNight.AccentHex) >= sum(auDay.AccentHex) {
		t.Errorf("austere night should sit darker than day: %q vs %q", auNight.AccentHex, auDay.AccentHex)
	}

	// Gradient finishes keep their identity and temper both stops.
	chEve := NewDaypartTheme("charm", "", false, core.DaypartEvening)
	chNight := NewDaypartTheme("charm", "", false, core.DaypartNight)
	if chEve.AccentHex2 != "#B48CFF" {
		t.Fatalf("evening charm keeps the authored second stop: %q", chEve.AccentHex2)
	}
	if chNight.Name != "charm" {
		t.Errorf("tempered theme must keep its name: %q", chNight.Name)
	}
	if chNight.AccentHex2 == "" || chNight.AccentHex2 == chEve.AccentHex2 {
		t.Errorf("night charm should temper the second stop too: %q", chNight.AccentHex2)
	}
}

// Crossing a daypart boundary retempers the live theme — but never while the
// theme picker owns th.
func TestDaypartBoundaryRetempers(t *testing.T) {
	m := testModel(t)
	m.daypart = core.DaypartEvening
	m.th = NewDaypartTheme(m.themeName, m.themeAccent, m.themeASCII, m.daypart)

	mm, _ := m.Update(tickMsg(time.Date(2025, 1, 6, 23, 0, 0, 0, time.Local)))
	m = mm.(Model)
	if m.daypart != core.DaypartNight {
		t.Fatalf("23:00 should be night: %q", m.daypart)
	}
	if m.th.AccentHex == "#FFB000" {
		t.Error("night should retemper the live accent")
	}

	// While the picker is open, a boundary turn must not clobber the preview.
	m.daypart = core.DaypartEvening
	m.themeOpen = true
	before := m.th.AccentHex
	mm, _ = m.Update(tickMsg(time.Date(2025, 1, 6, 23, 30, 0, 0, time.Local)))
	m = mm.(Model)
	if m.daypart != core.DaypartEvening || m.th.AccentHex != before {
		t.Error("daypart turn must not touch the theme during preview")
	}
}

// The idle settle: five quiet minutes and the chrome eases to an ember floor
// over thirty seconds; any sign of life restores the room at once.
func TestIdleSettle(t *testing.T) {
	m := testModel(t)
	m.lastAct = m.now
	if k := m.settleScale(); k != 1 {
		t.Fatalf("active room should be fully lit: %v", k)
	}
	m.lastAct = m.now.Add(-(settleAfter + 15*time.Second))
	if k := m.settleScale(); k < 0.774 || k > 0.776 {
		t.Fatalf("half-way settle should be 0.775: %v", k)
	}
	m.lastAct = m.now.Add(-(settleAfter + settleFade + time.Minute))
	if k := m.settleScale(); k != 0.55 {
		t.Fatalf("settle floor should be 0.55: %v", k)
	}

	// The footer strip visibly dims with the room (needs a color profile —
	// without one, styles render no ANSI to compare).
	lipgloss.SetColorProfile(termenv.TrueColor)
	m.w = 110
	settled := m.footerBar()
	m.lastAct = m.now
	bright := m.footerBar()
	if settled == bright {
		t.Error("footer chrome should differ between settled and awake rooms")
	}

	// A keypress restores instantly.
	m.lastAct = m.now.Add(-(settleAfter + settleFade))
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = mm.(Model)
	if k := m.settleScale(); k != 1 {
		t.Fatalf("any key should restore the room: %v", k)
	}
}

// The love wash: a coral flash at the moment of love, cooling over two
// seconds into the permanent LoveFill surface — and the footer L chip keeps
// its coral while loved.
func TestLoveWashAndMaterial(t *testing.T) {
	m := testModel(t)
	m.lovedTrack = true
	base := fmt.Sprint(m.th.LoveFill.GetBackground())

	m.loveAt = m.now
	flash := fmt.Sprint(m.loveWash())
	if flash == base {
		t.Fatal("the moment of love should flash brighter than the resting wash")
	}
	m.loveAt = m.now.Add(-3 * time.Second)
	if got := fmt.Sprint(m.loveWash()); got != base {
		t.Fatalf("wash should cool to LoveFill after 2s: %q vs %q", got, base)
	}
	m.loveAt = m.now.Add(-1 * time.Second)
	mid := fmt.Sprint(m.loveWash())
	br, bg, bb := hexRGB(base)
	fr, fg, fb := hexRGB(flash)
	mr, mg, mb := hexRGB(mid)
	if !(br < mr && mr < fr) || !(bg < mg && mg < fg) || !(bb < mb && mb < fb) {
		t.Errorf("mid-wash %q should sit between base %q and flash %q", mid, base, flash)
	}

	// Rendered material: the loved row carries the coral-dark wash and the
	// footer chip the coral foreground, in true color.
	lipgloss.SetColorProfile(termenv.TrueColor)
	m.loveAt = m.now.Add(-3 * time.Second) // resting wash, past the flash
	m.w = 110
	m.haveTrack = true
	m.track = "Broadcast · Come On Let's Go"
	m.trackAt = m.now.Add(-time.Minute)
	// termenv rounds hex -> RGB (53 -> 52), so assert the rendered value.
	if row := m.trackRow(80, false); !strings.Contains(row, "48;2;52;23;15") {
		t.Errorf("loved row should sit on LoveFill #35170F: %q", row)
	}
	if foot := m.footerBar(); !strings.Contains(foot, "38;2;255;104;77") {
		t.Errorf("loved footer L chip should be coral #FF684D: %q", foot)
	}
}

// The typewriter is wired: after a pick, the reason line types itself into
// both the receiver readout and the compact status row, then holds.
func TestTypewriterWired(t *testing.T) {
	m := testModel(t)
	m.w, m.h = 110, 32
	m.start = m.now.Add(-time.Minute)

	mm, _ := m.applyPick(core.Pick{Station: core.SeedStations()[1], Reason: "wildcard"})
	m = mm
	iw := m.innerWidth()

	// The typewriter starts 300ms after the pick: nothing shows before that.
	if row := stripANSI(m.receiverStatusRow(iw)); strings.Contains(row, "Exploring") {
		t.Fatalf("reason should not start before the typewriter clock: %q", row)
	}

	// 400ms in (100ms on the clock at 20ms/char): a partial line types.
	m.now = m.now.Add(400 * time.Millisecond)
	row := stripANSI(m.receiverStatusRow(iw))
	if !strings.Contains(row, "Explo") || strings.Contains(row, "Exploring something new") {
		t.Errorf("receiver readout should type the reason in: %q", row)
	}
	compact := stripANSI(m.reasonRow(iw))
	if !strings.Contains(compact, "SELEC") || strings.Contains(compact, "wildcard") {
		t.Errorf("compact reason row should type the reason in: %q", compact)
	}
	assertFits(t, m.View(), m.w, m.h)

	// Well past the end, the full text holds.
	m.now = m.now.Add(3 * time.Second)
	if row := stripANSI(m.receiverStatusRow(iw)); !strings.Contains(row, "Exploring something new") {
		t.Errorf("typed reason should hold its full text: %q", row)
	}
	if compact := stripANSI(m.reasonRow(iw)); !strings.Contains(compact, "wildcard") {
		t.Errorf("compact reason should hold its full text: %q", compact)
	}
	assertFits(t, m.View(), m.w, m.h)
}

// Boot is a 600ms heartbeat that any key ends early — and the key still lands.
func TestBootSkippableAndShort(t *testing.T) {
	if bootDur != 600*time.Millisecond {
		t.Fatalf("boot should be 600ms: %v", bootDur)
	}
	m := testModel(t)
	if m.now.Sub(m.start) >= bootDur {
		t.Fatal("a fresh model should be booting")
	}
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	m = mm.(Model)
	if m.now.Sub(m.start) < bootDur {
		t.Error("any key should end the boot early")
	}
	if !m.volumeOpen {
		t.Error("the boot-skipping key should still land (v opens volume)")
	}
}

// The wave bay and the faceplate agree on the compact threshold: at h>=14 the
// wave renders at the receiver's right-bay width, below it at full width.
func TestWaveGateConsistency(t *testing.T) {
	m := testModel(t)
	m.w = 110
	iw := m.innerWidth()
	_, _, right := receiverColumns(iw)

	m.h = 15
	if got := m.waveRenderWidth(); got != right {
		t.Errorf("h=15 should render the wave in the right bay (%d), got %d", right, got)
	}
	m.h = 14
	if got := m.waveRenderWidth(); got != right {
		t.Errorf("h=14 should render the wave in the right bay (%d), got %d", right, got)
	}
	m.h = 13
	if got := m.waveRenderWidth(); got != iw {
		t.Errorf("h=13 is the compact layout: wave should take the full width (%d), got %d", iw, got)
	}
}
