package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

func TestThreeFailuresGoOffline(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s, struck := offlineStreak(t, s)
	if s.Phase != Offline {
		t.Fatalf("three consecutive failures should go offline, got %v", s.Phase)
	}
	if d := s.RetryAt.Sub(s.Now); d < 4*time.Second || d > 6*time.Second {
		t.Fatalf("retryAt should be ~now+5s, got %v", d)
	}
	for _, uuid := range struck {
		if st, _ := s.core.StationByUUID(uuid); st.FailCount != 0 {
			t.Errorf("offline must forgive the strike on %s, FailCount=%d", uuid, st.FailCount)
		}
	}
}

func TestOfflineRetryGoesBackToLastGood(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	lastGood := s.Station
	s, _ = offlineStreak(t, s)
	// Past the timer, the tick fires the retry.
	s = drive(t, s, Tick{Now: s.RetryAt.Add(time.Second)})
	if s.Phase != Tuning {
		t.Fatalf("retry should tune, got %v", s.Phase)
	}
	if s.Station.UUID != lastGood.UUID {
		t.Fatalf("first retry should go back to last good %s, got %s", lastGood.UUID, s.Station.UUID)
	}
	if got := pl.lastPlay(); got != lastGood.StreamURL() {
		t.Fatalf("retry should Play %q, got %q", lastGood.StreamURL(), got)
	}
}

func TestOfflineFailureBacksOffWithoutStriking(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	lastGood := s.Station
	s, _ = offlineStreak(t, s)
	s = drive(t, s, Tick{Now: s.RetryAt.Add(time.Second)}) // fires retry
	// The retry fails too: outage persists, station is innocent.
	s = drive(t, s, evStreamError())
	if s.Phase != Offline {
		t.Fatalf("a failed retry should stay offline, got %v", s.Phase)
	}
	if d := s.RetryAt.Sub(s.Now); d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("second offline failure should back off ~10s, got %v", d)
	}
	if st, _ := s.core.StationByUUID(lastGood.UUID); st.FailCount != 0 {
		t.Fatalf("offline retry must not strike, FailCount=%d", st.FailCount)
	}
}

func TestPlayErrorStrikesWithoutTouchingOffline(t *testing.T) {
	s, stub := testState(t)
	stub.failPlay(errors.New("bad URL"))

	seeds := core.SeedStations()
	s2, cmds := s.Update(Tuned{Pick: pickSeed(seeds[0])})
	s = s2
	msgs := run(t, cmds)
	var failed PlayFailed
	found := false
	for _, msg := range msgs {
		if fm, ok := msg.(PlayFailed); ok {
			failed, found = fm, true
		}
	}
	if !found {
		t.Fatal("a Play error must surface as PlayFailed")
	}
	if failed.UUID != seeds[0].UUID {
		t.Fatalf("PlayFailed uuid = %q, want %q", failed.UUID, seeds[0].UUID)
	}
	s2, _ = s.Update(failed)
	s = s2
	if st2, _ := s.core.StationByUUID(seeds[0].UUID); st2.FailCount != 1 {
		t.Fatalf("rejected stream should strike, FailCount=%d", st2.FailCount)
	}
	if len(s.failStreak) != 0 {
		t.Fatalf("a rejected URL is not a network event; failStreak=%v", s.failStreak)
	}
	if s.retryN != 0 {
		t.Fatalf("rejected URL must not touch offline state, retryN=%d", s.retryN)
	}
}

// A page link yt-dlp can't resolve is a station that won't play: Play's
// ErrResolve surfaces as PlayFailed, one strike, and on to the next pick,
// never the offline path.
func TestPageResolveFailureIsPlayFailed(t *testing.T) {
	s, stub := testState(t)
	stub.failPlay(fmt.Errorf("%w: yt-dlp: ERROR: Video unavailable", player.ErrResolve))
	seeds := core.SeedStations()
	s, cmds := s.Update(Tuned{Pick: pickSeed(seeds[0])})
	var failed *PlayFailed
	for _, msg := range run(t, cmds) {
		if fm, ok := msg.(PlayFailed); ok {
			failed = &fm
		}
	}
	if failed == nil || !errors.Is(failed.Err, player.ErrResolve) {
		t.Fatalf("a resolution failure must surface as PlayFailed, got %+v", failed)
	}
	s, cmds = s.Update(*failed)
	if st, _ := s.core.StationByUUID(seeds[0].UUID); st.FailCount != 1 {
		t.Fatalf("an unresolvable page should strike, FailCount=%d", st.FailCount)
	}
	if len(cmds) == 0 {
		t.Fatal("a strike must move on to the next station")
	}
	if len(s.failStreak) != 0 || s.retryN != 0 {
		t.Fatal("a page that won't resolve is not a network event")
	}
}

// A Play overtaken while it resolved is nobody's failure, even when the
// listener has come straight back to the same station.
func TestSupersededPlayLeavesNoTrace(t *testing.T) {
	s, _ := testState(t)
	seeds := core.SeedStations()
	s = drive(t, s, Tuned{Pick: pickSeed(seeds[0])})
	before := s.PickSeq
	s, cmds := s.Update(PlayFailed{UUID: seeds[0].UUID, Err: player.ErrSuperseded})
	if len(cmds) != 0 || s.PickSeq != before || s.Station.UUID != seeds[0].UUID {
		t.Fatal("a superseded Play must change nothing")
	}
	if st, _ := s.core.StationByUUID(seeds[0].UUID); st.FailCount != 0 {
		t.Fatalf("a superseded Play must not strike, FailCount=%d", st.FailCount)
	}
}

// A fired retry consumes its timer: before Tuned lands, the next tick
// must not fire a second pick.
func TestOfflineRetryFiresOnce(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s, _ = offlineStreak(t, s)
	s.retryN = 2 // even: the retry path is a fresh pick via tuneDeadCmd

	s2, cmds := s.Update(Tick{Now: s.RetryAt.Add(time.Second)})
	s = s2
	if !s.RetryAt.IsZero() {
		t.Fatal("a fired retry must consume retryAt")
	}
	var picks int
	for _, msg := range run(t, cmds) {
		if _, ok := msg.(Tuned); ok {
			picks++
		}
	}
	if picks != 1 {
		t.Fatalf("first tick should fire exactly one retry pick, got %d", picks)
	}
	// Tuned still hasn't landed; more ticks must not stack retries.
	s2, cmds = s.Update(Tick{Now: s.Now.Add(time.Second)})
	s = s2
	for _, msg := range run(t, cmds) {
		if _, ok := msg.(Tuned); ok {
			t.Fatal("a second tick must not fire another retry while one is in flight")
		}
	}
}

// A pick that errors inside an offline retry reschedules — the state can't
// be left offline with no timer.
func TestOfflineRetryPickErrorReschedules(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s, _ = offlineStreak(t, s)
	s = drive(t, s, Tuned{Err: errors.New("no eligible stations")})
	if s.Phase != Offline {
		t.Fatalf("a failed offline pick should stay offline, got %v", s.Phase)
	}
	if s.retryN != 2 {
		t.Fatalf("retryN = %d, want 2", s.retryN)
	}
	if d := s.RetryAt.Sub(s.Now); d < 9*time.Second || d > 11*time.Second {
		t.Fatalf("rescheduled retry should be ~now+10s, got %v", d)
	}
}

// A listener skip that finds nothing returns to the still-playing stream —
// parked in Tuning, the stall watchdog would strike a healthy station.
func TestFailedSkipRestoresPlaying(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	st := s.Station
	s2, _ := s.Update(Next{}) // skip; drop its tuneCmd
	s = s2
	if s.Phase != Tuning {
		t.Fatalf("skip should enter Tuning, got %v", s.Phase)
	}
	s = drive(t, s, Tuned{Err: errors.New("no eligible stations")})
	if s.Phase != Playing {
		t.Fatalf("a failed skip over a live stream should restore Playing, got %v", s.Phase)
	}
	s = drive(t, s, Tick{Now: s.Now.Add(tuneStall + time.Second)})
	if st2, _ := s.core.StationByUUID(st.UUID); st2.FailCount != 0 {
		t.Fatalf("the stall watchdog must not strike a playing station, FailCount=%d", st2.FailCount)
	}
}

func TestStalePlayFailedIgnored(t *testing.T) {
	s, _ := testState(t)
	seeds := core.SeedStations()
	s = drive(t, s, Tuned{Pick: pickSeed(seeds[0])})
	s = drive(t, s, Tuned{Pick: pickSeed(seeds[1])})
	before := s.PickSeq
	s, cmds := s.Update(PlayFailed{UUID: seeds[0].UUID, Err: errors.New("x")})
	if len(cmds) != 0 || s.PickSeq != before || s.Station.UUID != seeds[1].UUID {
		t.Fatal("a failure for the station we left must change nothing")
	}
	if st, _ := s.core.StationByUUID(seeds[0].UUID); st.FailCount != 0 {
		t.Fatalf("a stale failure must not strike, FailCount=%d", st.FailCount)
	}
}

func TestDoubleNextLastPickWins(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	before := s.PickSeq
	s, _ = s.Update(Next{})
	s, _ = s.Update(Next{})
	seeds := core.SeedStations()
	s, _ = s.Update(Tuned{Pick: core.Pick{Station: seeds[1]}})
	s, _ = s.Update(Tuned{Pick: core.Pick{Station: seeds[2]}})
	if s.Station.UUID != seeds[2].UUID {
		t.Fatalf("the later pick must win, on %s", s.Station.UUID)
	}
	if s.Phase != Tuning {
		t.Fatalf("a landed pick tunes, got %v", s.Phase)
	}
	if s.PickSeq != before+2 {
		t.Fatalf("PickSeq = %d, want +2 from %d", s.PickSeq, before)
	}
}

func TestNextWhileStoppedIgnored(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s.Phase = Stopped
	s2, cmds := s.Update(Next{})
	if len(cmds) != 0 || s2.Phase != Stopped {
		t.Fatalf("stopped stays stopped, got %v", s2.Phase)
	}
}

func TestTuneToCurrentStationIsNoOp(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	before := s.PickSeq
	s2, cmds := s.Update(TuneTo{UUID: s.Station.UUID, Reason: "already here"})
	if len(cmds) != 0 || s2.PickSeq != before {
		t.Fatal("tuning to the current station changes nothing")
	}
}

func TestPickSeqCountsRepicks(t *testing.T) {
	s, _ := testState(t)
	seeds := core.SeedStations()
	s = drive(t, s, Tuned{Pick: pickSeed(seeds[0])})
	one := s.PickSeq
	s = drive(t, s, Tuned{Pick: pickSeed(seeds[0])})
	if s.PickSeq != one+1 {
		t.Fatalf("re-picks count too: PickSeq %d → %d", one, s.PickSeq)
	}
}
