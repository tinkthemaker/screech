package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"screech/internal/core"
	"screech/internal/player"
)

// runCmd executes a command tree and collects the messages it produces.
// Listeners parked on the player event channel never return — they get the
// timeout — and tick commands are discarded because tests drive the clock
// by feeding tickMsg directly.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, sub := range batch {
				out = append(out, runCmd(t, sub)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		if _, isTick := msg.(tickMsg); isTick {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(250 * time.Millisecond):
		return nil
	}
}

// drive feeds msg through Update and recursively feeds back whatever the
// returned commands produce.
func drive(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	for _, out := range runCmd(t, cmd) {
		m = drive(t, m, out)
	}
	return m
}

// playing puts the model on a locked stream: station applied, EventPlaying
// observed, listen clock running.
func playing(t *testing.T, m Model) Model {
	t.Helper()
	seeds := testSeeds()
	m = drive(t, m, tunedMsg{pick: pickSeed(seeds[0])})
	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("want phPlay, got %v", m.ph)
	}
	return m
}

func testSeeds() []core.Station { return core.SeedStations() }

func pickSeed(st core.Station) core.Pick {
	return core.Pick{Station: st, Reason: "test"}
}

func evStreamEnded() tea.Msg {
	return evMsg(player.Event{Type: player.EventStreamEnded})
}

func evStreamError() tea.Msg {
	return evMsg(player.Event{Type: player.EventStreamError, Err: errors.New("stream failed")})
}

func lastPlayURL(m Model) string {
	return m.pl.(*stubPlayer).lastPlay()
}

func TestStreamDropReconnectsInPlace(t *testing.T) {
	m := playing(t, testModel(t))
	st := m.st
	playStart := m.playStart

	nm, _ := m.Update(evStreamEnded())
	m = nm.(Model)
	if !m.reconnecting {
		t.Error("reconnecting should be set after a drop")
	}
	if m.ph != phTune {
		t.Fatalf("drop should re-enter phTune, got %v", m.ph)
	}
	if m.st.UUID != st.UUID {
		t.Fatalf("drop must keep the station: %q != %q", m.st.UUID, st.UUID)
	}
	if got := lastPlayURL(m); got != st.StreamURL() {
		t.Fatalf("reconnect should Play the same URL %q, got %q", st.StreamURL(), got)
	}
	if got := m.st.FailCount; got != 0 {
		t.Fatalf("a drop must not strike the station, FailCount=%d", got)
	}
	st2, _ := m.core.StationByUUID(st.UUID)
	if st2.FailCount != 0 {
		t.Fatalf("core must not record a strike, FailCount=%d", st2.FailCount)
	}

	// The reconnect locks: the listen clock is preserved.
	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("reconnect should land back in phPlay, got %v", m.ph)
	}
	if !m.playStart.Equal(playStart) {
		t.Fatalf("reconnect must keep playStart %v, got %v", playStart, m.playStart)
	}
	if m.reconnecting {
		t.Error("reconnecting should clear on lock")
	}
}

func TestFourDropsStrikeAndMoveOn(t *testing.T) {
	m := playing(t, testModel(t))
	st := m.st
	for i := 0; i < maxDrops; i++ {
		m = drive(t, m, evStreamEnded())
		m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	}
	if m.st.UUID != st.UUID {
		t.Fatalf("drops 1..%d should stay on the station", maxDrops)
	}
	m = drive(t, m, evStreamEnded()) // one too many
	if st2, _ := m.core.StationByUUID(st.UUID); st2.FailCount != 1 {
		t.Fatalf("the %dth drop should strike: FailCount=%d", maxDrops+1, st2.FailCount)
	}
	if m.st.UUID == st.UUID {
		t.Fatal("after the strike it should have moved on to another station")
	}
	if m.ph != phTune || m.reconnecting {
		t.Fatalf("expected a fresh tune, got ph=%v reconnecting=%v", m.ph, m.reconnecting)
	}
}

func TestStableMinuteForgivesDrops(t *testing.T) {
	m := playing(t, testModel(t))
	m = drive(t, m, evStreamEnded())
	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.drops != 1 {
		t.Fatalf("drops = %d, want 1", m.drops)
	}
	m = drive(t, m, tickMsg(m.now.Add(stableAfter+time.Second)))
	if m.drops != 0 {
		t.Fatalf("a stable minute should reset drops, got %d", m.drops)
	}
}

func TestBufferStallReconnects(t *testing.T) {
	m := playing(t, testModel(t))
	st := m.st
	m = drive(t, m, evMsg(player.Event{Type: player.EventBuffering}))
	if m.ph != phBuffer {
		t.Fatalf("want phBuffer, got %v", m.ph)
	}
	m = drive(t, m, tickMsg(m.now.Add(bufferStall+time.Second)))
	if !m.reconnecting || m.ph != phTune {
		t.Fatalf("stalled buffer should reconnect, got ph=%v reconnecting=%v", m.ph, m.reconnecting)
	}
	if got := lastPlayURL(m); got != st.StreamURL() {
		t.Fatalf("buffer reconnect should keep the URL, got %q", got)
	}
	if st2, _ := m.core.StationByUUID(st.UUID); st2.FailCount != 0 {
		t.Fatalf("a stall must not strike, FailCount=%d", st2.FailCount)
	}
}

// offlineStreak fails tunes until the model goes offline and returns the
// struck UUIDs.
func offlineStreak(t *testing.T, m Model) (Model, []string) {
	t.Helper()
	var struck []string
	for i := 0; i < offlineAfter; i++ {
		if m.ph == phPlay {
			// A stream that dies mid-play drops back into a tune first.
			m = drive(t, m, evStreamEnded())
		}
		if m.ph != phTune {
			t.Fatalf("failure %d: expected a tune in flight, got ph=%v", i+1, m.ph)
		}
		struck = append(struck, m.st.UUID)
		m = drive(t, m, evStreamError())
	}
	return m, struck
}

func TestThreeFailuresGoOffline(t *testing.T) {
	m := playing(t, testModel(t))
	m, struck := offlineStreak(t, m)
	if m.ph != phOffline {
		t.Fatalf("three consecutive failures should go offline, got ph=%v", m.ph)
	}
	if d := m.retryAt.Sub(m.now); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("retryAt should be ~now+5s, got %v", d)
	}
	for _, uuid := range struck {
		if st, _ := m.core.StationByUUID(uuid); st.FailCount != 0 {
			t.Errorf("offline must forgive the strike on %s, FailCount=%d", uuid, st.FailCount)
		}
	}
}

func TestOfflineRetryGoesBackToLastGood(t *testing.T) {
	m := playing(t, testModel(t))
	lastGood := m.st
	m, _ = offlineStreak(t, m)
	// Past the timer, the tick fires the retry.
	m = drive(t, m, tickMsg(m.retryAt.Add(time.Second)))
	if m.ph != phTune {
		t.Fatalf("retry should tune, got ph=%v", m.ph)
	}
	if m.st.UUID != lastGood.UUID {
		t.Fatalf("first retry should go back to last good %s, got %s", lastGood.UUID, m.st.UUID)
	}
	if got := lastPlayURL(m); got != lastGood.StreamURL() {
		t.Fatalf("retry should Play %q, got %q", lastGood.StreamURL(), got)
	}
}

func TestOfflineFailureBacksOffWithoutStriking(t *testing.T) {
	m := playing(t, testModel(t))
	lastGood := m.st
	m, _ = offlineStreak(t, m)
	m = drive(t, m, tickMsg(m.retryAt.Add(time.Second))) // fires retry
	// The retry fails too: outage persists, station is innocent.
	m = drive(t, m, evStreamError())
	if m.ph != phOffline {
		t.Fatalf("a failed retry should stay offline, got ph=%v", m.ph)
	}
	if d := m.retryAt.Sub(m.now); d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("second offline failure should back off ~10s, got %v", d)
	}
	if st, _ := m.core.StationByUUID(lastGood.UUID); st.FailCount != 0 {
		t.Fatalf("offline retry must not strike, FailCount=%d", st.FailCount)
	}
}

func TestEventPlayingClearsOffline(t *testing.T) {
	m := playing(t, testModel(t))
	m, _ = offlineStreak(t, m)
	m = drive(t, m, tickMsg(m.retryAt.Add(time.Second)))
	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("a lock should restore phPlay, got %v", m.ph)
	}
	if m.retryN != 0 || !m.retryAt.IsZero() {
		t.Fatal("lock should clear offline bookkeeping")
	}
	if !strings.Contains(m.feedback, "ONLINE") {
		t.Fatalf("want ONLINE feedback, got %q", m.feedback)
	}
}

func TestPlayErrorStrikesWithoutTouchingOffline(t *testing.T) {
	m := testModel(t)
	stub := m.pl.(*stubPlayer)
	stub.failPlay(errors.New("bad URL"))

	seeds := testSeeds()
	nm, cmd := m.Update(tunedMsg{pick: pickSeed(seeds[0])})
	m = nm.(Model)
	msgs := runCmd(t, cmd)
	var failed playFailedMsg
	found := false
	for _, msg := range msgs {
		if fm, ok := msg.(playFailedMsg); ok {
			failed, found = fm, true
		}
	}
	if !found {
		t.Fatal("a Play error must surface as playFailedMsg")
	}
	if failed.uuid != seeds[0].UUID {
		t.Fatalf("playFailedMsg uuid = %q, want %q", failed.uuid, seeds[0].UUID)
	}
	nm, _ = m.Update(failed)
	m = nm.(Model)
	if st2, _ := m.core.StationByUUID(seeds[0].UUID); st2.FailCount != 1 {
		t.Fatalf("rejected stream should strike, FailCount=%d", st2.FailCount)
	}
	if len(m.failStreak) != 0 {
		t.Fatalf("a rejected URL is not a network event; failStreak=%v", m.failStreak)
	}
	if m.retryN != 0 {
		t.Fatalf("rejected URL must not touch offline state, retryN=%d", m.retryN)
	}
}

func TestEventDiedRestartsAndRearms(t *testing.T) {
	m := playing(t, testModel(t))
	stub := m.pl.(*stubPlayer)
	st := m.st

	// Step the chain by hand: letting drive() run the whole thing would
	// park a listener on the new channel before we can observe it.
	nm, cmd := m.Update(evMsg(player.Event{Type: player.EventDied, Err: errors.New("mpv exited")}))
	m = nm.(Model)
	msgs := runCmd(t, cmd)
	var restarted *playerRestartedMsg
	for _, msg := range msgs {
		if prm, ok := msg.(playerRestartedMsg); ok {
			m2 := prm
			restarted = &m2
		}
	}
	if restarted == nil {
		t.Fatal("EventDied should produce a playerRestartedMsg")
	}
	if restarted.err != nil {
		t.Fatalf("restart failed: %v", restarted.err)
	}
	if stub.restartCount() != 1 {
		t.Fatalf("Restart should have run once, got %d", stub.restartCount())
	}
	if got := lastPlayURL(m); got != st.StreamURL() {
		t.Fatalf("restart should re-play the station, got %q", got)
	}

	nm, lcmd := m.Update(*restarted)
	m = nm.(Model)
	if m.ph != phTune || !m.reconnecting {
		t.Fatalf("post-restart should be tuning again, got ph=%v reconnecting=%v", m.ph, m.reconnecting)
	}
	if lcmd == nil {
		t.Fatal("playerRestartedMsg must re-arm listen()")
	}
	// The re-armed listener reads the NEW channel: an event pushed there
	// comes back as an evMsg.
	got := make(chan tea.Msg, 1)
	go func() { got <- lcmd() }()
	stub.emit(player.Event{Type: player.EventTitle, Title: "proof"})
	select {
	case msg := <-got:
		if _, ok := msg.(evMsg); !ok {
			t.Fatalf("re-armed listener should yield evMsg, got %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("listener is not reading the new channel")
	}
}

func TestFourthDeathIsFatal(t *testing.T) {
	m := playing(t, testModel(t))
	now := m.now
	m.restarts = []time.Time{now.Add(-4 * time.Minute), now.Add(-3 * time.Minute), now.Add(-2 * time.Minute)}
	m = drive(t, m, evMsg(player.Event{Type: player.EventDied, Err: errors.New("mpv exited")}))
	if m.ph != phDead {
		t.Fatalf("a %dth crash inside the window should be fatal, got ph=%v", maxRestarts+1, m.ph)
	}
	if !strings.Contains(m.fatal, "restart screech") {
		t.Fatalf("fatal should explain the crash loop, got %q", m.fatal)
	}
}

// A fired retry consumes its timer: before tunedMsg lands, the next tick
// must not fire a second pick.
func TestOfflineRetryFiresOnce(t *testing.T) {
	m := playing(t, testModel(t))
	m, _ = offlineStreak(t, m)
	m.retryN = 2 // even: the retry path is a fresh pick via tuneDeadCmd

	nm, cmd := m.Update(tickMsg(m.retryAt.Add(time.Second)))
	m = nm.(Model)
	if !m.retryAt.IsZero() {
		t.Fatal("a fired retry must consume retryAt")
	}
	var picks int
	for _, msg := range runCmd(t, cmd) {
		if _, ok := msg.(tunedMsg); ok {
			picks++
		}
	}
	if picks != 1 {
		t.Fatalf("first tick should fire exactly one retry pick, got %d", picks)
	}
	// tunedMsg still hasn't landed; more ticks must not stack retries.
	nm, cmd = m.Update(tickMsg(m.now.Add(time.Second)))
	m = nm.(Model)
	for _, msg := range runCmd(t, cmd) {
		if _, ok := msg.(tunedMsg); ok {
			t.Fatal("a second tick must not fire another retry while one is in flight")
		}
	}
}

// A pick that errors inside an offline retry reschedules — the model can't
// be left offline with no timer.
func TestOfflineRetryPickErrorReschedules(t *testing.T) {
	m := playing(t, testModel(t))
	m, _ = offlineStreak(t, m)
	m = drive(t, m, tunedMsg{err: errors.New("no eligible stations")})
	if m.ph != phOffline {
		t.Fatalf("a failed offline pick should stay offline, got ph=%v", m.ph)
	}
	if m.retryN != 2 {
		t.Fatalf("retryN = %d, want 2", m.retryN)
	}
	if d := m.retryAt.Sub(m.now); d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("rescheduled retry should be ~now+10s, got %v", d)
	}
}

// A user skip that finds nothing returns to the still-playing stream —
// parked in phTune, the stall watchdog would strike a healthy station.
func TestFailedSkipRestoresPlaying(t *testing.T) {
	m := playing(t, testModel(t))
	st := m.st
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace}) // skip; drop its tuneCmd
	m = nm.(Model)
	if m.ph != phTune {
		t.Fatalf("skip should enter phTune, got %v", m.ph)
	}
	m = drive(t, m, tunedMsg{err: errors.New("no eligible stations")})
	if m.ph != phPlay {
		t.Fatalf("a failed skip over a live stream should restore phPlay, got %v", m.ph)
	}
	m = drive(t, m, tickMsg(m.now.Add(tuneStall+time.Second)))
	if st2, _ := m.core.StationByUUID(st.UUID); st2.FailCount != 0 {
		t.Fatalf("the stall watchdog must not strike a playing station, FailCount=%d", st2.FailCount)
	}
}

// Offline entry closed the listen, but a stream that locks late is still a
// session — the lock must reopen one.
func TestLateLockWhileOfflineReopensListen(t *testing.T) {
	m := playing(t, testModel(t))
	m, _ = offlineStreak(t, m)
	st := m.st
	before, _ := m.core.StationTotal(st.UUID)
	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("late lock should restore phPlay, got %v", m.ph)
	}
	m.core.EndListen(m.now.Add(30 * time.Second))
	after, _ := m.core.StationTotal(st.UUID)
	if after-before < 25*time.Second {
		t.Fatalf("a listen should have been opened: total went %v → %v", before, after)
	}
}

func TestOfflineStatusReadsRetryingNow(t *testing.T) {
	m := testModel(t)
	m.st = testSeeds()[0]
	m.haveSt = true
	m.ph = phOffline
	m.retryN = 2
	m.retryAt = time.Time{} // a retry is in flight
	if row := stripANSI(m.reasonRow(60)); !strings.Contains(row, "retrying now") {
		t.Fatalf("status row should read 'retrying now', got %q", row)
	}
}

func TestStreamEventsIgnoredBeforeFirstTune(t *testing.T) {
	m := testModel(t)
	m = drive(t, m, evStreamError())
	m = drive(t, m, evStreamEnded())
	if m.ph != phIdleStart {
		t.Fatalf("stream events before any tune are noise, got ph=%v", m.ph)
	}
	if len(m.failStreak) != 0 || m.drops != 0 {
		t.Fatal("idle-phase stream events must not touch connectivity state")
	}
}
