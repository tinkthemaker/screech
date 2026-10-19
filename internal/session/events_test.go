package session

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

func TestStreamDropReconnectsInPlace(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	st := s.Station
	playStart := s.PlayStart

	s2, _ := s.Update(evStreamEnded())
	s = s2
	if !s.Reconnecting {
		t.Error("reconnecting should be set after a drop")
	}
	if s.Phase != Tuning {
		t.Fatalf("drop should re-enter Tuning, got %v", s.Phase)
	}
	if s.Station.UUID != st.UUID {
		t.Fatalf("drop must keep the station: %q != %q", s.Station.UUID, st.UUID)
	}
	if got := pl.lastPlay(); got != st.StreamURL() {
		t.Fatalf("reconnect should Play the same URL %q, got %q", st.StreamURL(), got)
	}
	if got := s.Station.FailCount; got != 0 {
		t.Fatalf("a drop must not strike the station, FailCount=%d", got)
	}
	st2, _ := s.core.StationByUUID(st.UUID)
	if st2.FailCount != 0 {
		t.Fatalf("core must not record a strike, FailCount=%d", st2.FailCount)
	}

	// The reconnect locks: the listen clock is preserved.
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("reconnect should land back in Playing, got %v", s.Phase)
	}
	if !s.PlayStart.Equal(playStart) {
		t.Fatalf("reconnect must keep playStart %v, got %v", playStart, s.PlayStart)
	}
	if s.Reconnecting {
		t.Error("reconnecting should clear on lock")
	}
}

func TestFourDropsStrikeAndMoveOn(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	st := s.Station
	for i := 0; i < maxDrops; i++ {
		s = drive(t, s, evStreamEnded())
		s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	}
	if s.Station.UUID != st.UUID {
		t.Fatalf("drops 1..%d should stay on the station", maxDrops)
	}
	s = drive(t, s, evStreamEnded()) // one too many
	if st2, _ := s.core.StationByUUID(st.UUID); st2.FailCount != 1 {
		t.Fatalf("the %dth drop should strike: FailCount=%d", maxDrops+1, st2.FailCount)
	}
	if s.Station.UUID == st.UUID {
		t.Fatal("after the strike it should have moved on to another station")
	}
	if s.Phase != Tuning || s.Reconnecting {
		t.Fatalf("expected a fresh tune, got phase=%v reconnecting=%v", s.Phase, s.Reconnecting)
	}
}

func TestStableMinuteForgivesDrops(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = drive(t, s, evStreamEnded())
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.drops != 1 {
		t.Fatalf("drops = %d, want 1", s.drops)
	}
	s = drive(t, s, Tick{Now: s.Now.Add(stableAfter + time.Second)})
	if s.drops != 0 {
		t.Fatalf("a stable minute should reset drops, got %d", s.drops)
	}
}

func TestBufferStallReconnects(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	st := s.Station
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventBuffering}))
	if s.Phase != Buffering {
		t.Fatalf("want Buffering, got %v", s.Phase)
	}
	s = drive(t, s, Tick{Now: s.Now.Add(bufferStall + time.Second)})
	if !s.Reconnecting || s.Phase != Tuning {
		t.Fatalf("stalled buffer should reconnect, got phase=%v reconnecting=%v", s.Phase, s.Reconnecting)
	}
	if got := pl.lastPlay(); got != st.StreamURL() {
		t.Fatalf("buffer reconnect should keep the URL, got %q", got)
	}
	if st2, _ := s.core.StationByUUID(st.UUID); st2.FailCount != 0 {
		t.Fatalf("a stall must not strike, FailCount=%d", st2.FailCount)
	}
}

func TestEventPlayingClearsOffline(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s, _ = offlineStreak(t, s)
	s = drive(t, s, Tick{Now: s.retryAt.Add(time.Second)})
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("a lock should restore Playing, got %v", s.Phase)
	}
	if s.retryN != 0 || !s.retryAt.IsZero() {
		t.Fatal("lock should clear offline bookkeeping")
	}
	if !strings.Contains(s.Feedback, "ONLINE") {
		t.Fatalf("want ONLINE feedback, got %q", s.Feedback)
	}
}

func TestEventDiedRestartsAndRearms(t *testing.T) {
	s, stub := testState(t)
	s = playing(t, s)
	st := s.Station

	// Step the chain by hand: letting drive() run the whole thing would
	// park a listener on the new channel before we can observe it.
	s2, cmds := s.Update(PlayerEvent(player.Event{Type: player.EventDied, Err: errors.New("mpv exited")}))
	s = s2
	msgs := run(t, cmds)
	var restarted *Restarted
	for _, msg := range msgs {
		if prm, ok := msg.(Restarted); ok {
			m2 := prm
			restarted = &m2
		}
	}
	if restarted == nil {
		t.Fatal("EventDied should produce a Restarted")
	}
	if restarted.Err != nil {
		t.Fatalf("restart failed: %v", restarted.Err)
	}
	if stub.restartCount() != 1 {
		t.Fatalf("Restart should have run once, got %d", stub.restartCount())
	}
	if got := stub.lastPlay(); got != st.StreamURL() {
		t.Fatalf("restart should re-play the station, got %q", got)
	}
	// And in that order: respawn first, then re-point at the stream.
	calls := stub.callsList()
	if n := len(calls); n < 2 || calls[n-2] != "restart" || calls[n-1] != "play "+st.StreamURL() {
		t.Fatalf("call log should end restart → play, got %v", calls)
	}

	s2, lcmds := s.Update(*restarted)
	s = s2
	if s.Phase != Tuning || !s.Reconnecting {
		t.Fatalf("post-restart should be tuning again, got phase=%v reconnecting=%v", s.Phase, s.Reconnecting)
	}
	if len(lcmds) != 1 {
		t.Fatalf("Restarted must re-arm exactly one listener, got %d cmds", len(lcmds))
	}
	// The re-armed listener reads the NEW channel: an event pushed there
	// comes back as a PlayerEvent.
	stub.emit(player.Event{Type: player.EventTitle, Title: "proof"})
	got := run(t, lcmds)
	var found bool
	for _, msg := range got {
		if _, ok := msg.(PlayerEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("listener is not reading the new channel")
	}
}

func TestFourthDeathIsFatal(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	now := s.Now
	s.restarts = []time.Time{now.Add(-4 * time.Minute), now.Add(-3 * time.Minute), now.Add(-2 * time.Minute)}
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventDied, Err: errors.New("mpv exited")}))
	if s.Phase != Stopped {
		t.Fatalf("a %dth crash inside the window should be fatal, got %v", maxRestarts+1, s.Phase)
	}
	if !strings.Contains(s.Fatal, "restart screech") {
		t.Fatalf("fatal should explain the crash loop, got %q", s.Fatal)
	}
}

// Offline entry closed the listen, but a stream that locks late is still a
// session — the lock must reopen one.
func TestLateLockWhileOfflineReopensListen(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s, _ = offlineStreak(t, s)
	st := s.Station
	before, _ := s.core.StationTotal(st.UUID)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("late lock should restore Playing, got %v", s.Phase)
	}
	s.core.EndListen(s.Now.Add(30 * time.Second))
	after, _ := s.core.StationTotal(st.UUID)
	if after-before < 25*time.Second {
		t.Fatalf("a listen should have been opened: total went %v → %v", before, after)
	}
}

func TestStreamEventsIgnoredBeforeFirstTune(t *testing.T) {
	s, _ := testState(t)
	s = drive(t, s, evStreamError())
	s = drive(t, s, evStreamEnded())
	if s.Phase != Idle {
		t.Fatalf("stream events before any tune are noise, got %v", s.Phase)
	}
	if len(s.failStreak) != 0 || s.drops != 0 {
		t.Fatal("idle-phase stream events must not touch connectivity state")
	}
}

func TestAutoHopOnMarkerTitle(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s.opts.AutoHopAds = true
	uuid := s.Station.UUID
	betaBefore := s.core.BanditBeta(uuid)

	s2, cmds := s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	s = s2
	if s.Phase != Tuning {
		t.Fatalf("marker title should hop, got phase %v", s.Phase)
	}
	if len(cmds) == 0 {
		t.Fatal("hop should issue work")
	}
	if !strings.Contains(s.Feedback, "HOPPED") {
		t.Fatalf("feedback = %q, want HOPPED", s.Feedback)
	}
	if s.hopAt.IsZero() {
		t.Fatal("hopAt should be stamped")
	}

	// The hop is a skip during a break: Tune closes the listen with the
	// discounted beta, not the full fast-skip penalty. Running the cmds
	// executes tuneCmd, which is where the listen ends.
	for _, out := range run(t, cmds) {
		s = drive(t, s, out)
	}
	// BanditBeta sums two fresh rows (the daypart's and all-time's), each
	// prior 1 plus the discounted 0.25: 2.5 total. A full fast-skip would
	// have landed 4.0.
	if got := s.core.BanditBeta(uuid) - betaBefore; math.Abs(got-2.5) > 0.01 {
		t.Fatalf("hop beta delta = %v, want ~2.5 (discounted break skip on two rows)", got)
	}
}

func TestAutoHopCooldown(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s.opts.AutoHopAds = true

	s2, _ := s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	s = s2
	if s.Phase != Tuning {
		t.Fatalf("first marker should hop, got %v", s.Phase)
	}

	// Landed on the next station, still playing — another break marker
	// inside the cooldown must not chain a second hop.
	s.Phase = Playing
	s.Suspect = false
	s2, _ = s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "Commercial Break - back soon"}))
	s = s2
	if s.Phase != Playing {
		t.Fatalf("cooldown should hold the hop, got %v", s.Phase)
	}

	s.Now = s.hopAt.Add(autoHopCooldown + time.Second)
	s2, _ = s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "Werbung"}))
	s = s2
	if s.Phase != Tuning {
		t.Fatalf("marker past the cooldown should hop again, got %v", s.Phase)
	}
}

func TestAutoHopRespectsOptInAndStrictness(t *testing.T) {
	// Opted out: a marker title is still suspicion, never a hop.
	s, _ := testState(t)
	s = playing(t, s)
	s2, _ := s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "ADVERTISEMENT"}))
	s = s2
	if s.Phase != Playing {
		t.Fatalf("opted-out session must not hop, got %v", s.Phase)
	}
	if !s.Suspect {
		t.Fatal("marker title should still mark the break suspect")
	}

	// Opted in but an empty title: SuspectedAd territory only, no hop.
	// (A real track lands first — an empty title before any track is
	// treated as ICY silence, not suspicion.)
	s, _ = testState(t)
	s = playing(t, s)
	s.opts.AutoHopAds = true
	s2, _ = s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: "Artist - Title"}))
	s = s2
	s2, _ = s.Update(PlayerEvent(player.Event{Type: player.EventTitle, Title: ""}))
	s = s2
	if s.Phase != Playing {
		t.Fatalf("empty title must not hop, got %v", s.Phase)
	}
	if !s.Suspect {
		t.Fatal("empty title should still be suspect")
	}
}

func TestQuitThenEventsClosedDoesNotRestart(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s, _ = s.Update(Quit{})
	_, cmds := s.Update(EventsClosed{})
	run(t, cmds)
	if pl.restartCount() != 0 {
		t.Fatal("the event stream closing behind Quit is expected, not a crash")
	}
}

func TestTitleSetsTrackAndClearsOnJunk(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventTitle, Title: "Boards of Canada - Roygbiv"}))
	if s.Artist != "Boards of Canada" || s.Title != "Roygbiv" || !s.HaveTrack {
		t.Fatalf("track = %q / %q (have=%v)", s.Artist, s.Title, s.HaveTrack)
	}
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventTitle, Title: s.Station.Name}))
	if s.HaveTrack || s.Artist != "" || s.Title != "" || s.TrackKey != "" {
		t.Fatalf("a station-name title clears the track, got %q / %q / %q", s.Artist, s.Title, s.TrackKey)
	}
}

func TestLevelMonoFallback(t *testing.T) {
	s, stub := testState(t)
	s = playing(t, s)
	s2, cmds := s.Update(PlayerEvent(player.Event{Type: player.EventLevel, Level: 0.6}))
	s = s2
	if s.Level.L != 0.6 || s.Level.R != 0.6 {
		t.Fatalf("mono mixdown should fill both channels, got %v/%v", s.Level.L, s.Level.R)
	}
	if len(cmds) != 1 {
		t.Fatalf("an event always re-arms exactly one listener, got %d cmds", len(cmds))
	}
	// And it really is the listener: an emitted event comes back. The
	// parked listener playing() left on the channel also gets one, so two
	// emits keep this deterministic.
	stub.emit(player.Event{Type: player.EventTitle, Title: "proof"})
	stub.emit(player.Event{Type: player.EventTitle, Title: "proof"})
	var found bool
	for _, msg := range run(t, cmds) {
		if _, ok := msg.(PlayerEvent); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("the returned command must be the re-armed listener")
	}
}
