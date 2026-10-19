package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

// paused puts the state in Paused the way a TogglePause does.
func paused(t *testing.T, s State) State {
	t.Helper()
	s = drive(t, s, TogglePause{})
	if s.Phase != Paused {
		t.Fatalf("TogglePause should pause, got %v", s.Phase)
	}
	return s
}

// offline parks the state mid-outage the way the connectivity tests leave
// it: a station held, retries armed.
func offline(t *testing.T, s State) State {
	t.Helper()
	s.Phase = Offline
	s.retryN = 1
	s.retryAt = s.Now.Add(20 * time.Second)
	return s
}

func lastCall(pl *stubPlayer) string {
	c := pl.callsList()
	if len(c) == 0 {
		return ""
	}
	return c[len(c)-1]
}

func hasCall(pl *stubPlayer, prefix string) bool {
	for _, c := range pl.callsList() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// TogglePause holds mpv's pause, ends the current listen, and swaps the
// phase: paused wall time never counts as listening.
func TestPauseKeyPausesAndBanksTheListen(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	if lastCall(pl) != "pause true" {
		t.Fatalf("pause should send SetPaused(true), calls %v", pl.callsList())
	}
	if s.core.ListenOpen() {
		t.Fatal("pausing must end the current listen — paused time is not listening")
	}
	if s.PausedAt.IsZero() {
		t.Fatal("pausedAt should mark when the hold began")
	}
}

// A pause shorter than the buffer window unpauses in place: SetPaused, no
// reload, and the listen reopens as a continuation on the next lock.
func TestShortResumeUnpausesInPlace(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	s.Now = s.Now.Add(10 * time.Second)

	s = drive(t, s, TogglePause{})
	if s.Phase != Tuning || !s.Resuming {
		t.Fatalf("short resume should enter a resuming tune, got phase=%v resuming=%v", s.Phase, s.Resuming)
	}
	if lastCall(pl) != "pause false" {
		t.Fatalf("short resume should unpause in place, calls %v", pl.callsList())
	}
	// The only recorded Play is the original tune before the pause.
	plays := 0
	for _, c := range pl.callsList() {
		if strings.HasPrefix(c, "play") {
			plays++
		}
	}
	if plays != 1 {
		t.Fatalf("in-place resume must not reload, calls %v", pl.callsList())
	}
	if !s.core.ListenOpen() {
		t.Fatal("resume should reopen the listen")
	}

	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("stream lock should land Playing, got %v", s.Phase)
	}
	if s.Resuming {
		t.Fatal("resuming should clear on lock")
	}
}

// Past the buffer window the cached stream is stale: resume reloads the
// station and rejoins the live edge.
func TestLongResumeReloads(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	url := s.Station.StreamURL()
	s.Now = s.Now.Add(resumeInPlace + time.Second)

	s = drive(t, s, TogglePause{})
	if s.Phase != Tuning {
		t.Fatalf("reload resume should pass through Tuning, got %v", s.Phase)
	}
	if lastCall(pl) != "play "+url {
		t.Fatalf("long pause should reload the stream, calls %v", pl.callsList())
	}
	if !s.core.ListenOpen() {
		t.Fatal("a reloaded resume should reopen the listen")
	}
}

// The OS flyout / media keys set mpv's pause property directly: the event
// arrives instead of a key, and screech answers by ending the listen, not
// by sending another pause back.
func TestExternalPauseAndResume(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPaused}))
	if s.Phase != Paused {
		t.Fatalf("external pause should enter Paused, got %v", s.Phase)
	}
	if hasCall(pl, "pause true") {
		t.Fatal("external pause must not echo SetPaused back to mpv")
	}
	if s.core.ListenOpen() {
		t.Fatal("external pause still ends the listen")
	}

	// Released externally inside the buffer window: no command at all,
	// just the resuming tune and a reopened listen.
	callsBefore := len(pl.callsList())
	s.Now = s.Now.Add(10 * time.Second)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventResumed}))
	if s.Phase != Tuning || !s.Resuming {
		t.Fatalf("external resume should enter a resuming tune, got phase=%v resuming=%v", s.Phase, s.Resuming)
	}
	if len(pl.callsList()) != callsBefore {
		t.Fatalf("in-place external resume needs no player command, calls %v", pl.callsList())
	}
	if !s.core.ListenOpen() {
		t.Fatal("external resume should reopen the listen")
	}
}

// An external release past the buffer window reloads like a long resume.
func TestExternalResumeAfterBufferReloads(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPaused}))
	url := s.Station.StreamURL()
	s.Now = s.Now.Add(resumeInPlace + time.Second)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventResumed}))
	if lastCall(pl) != "play "+url {
		t.Fatalf("stale external resume should reload, calls %v", pl.callsList())
	}
}

// Event echoes the state already knows about must not bounce the phase:
// pause while paused, resume while resuming or playing, playing/buffering
// chatter underneath a pause.
func TestPauseEchoesAreIgnored(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	for _, ev := range []player.Event{
		{Type: player.EventPaused},
		{Type: player.EventPlaying},
		{Type: player.EventBuffering},
	} {
		s2, _ := s.Update(PlayerEvent(ev))
		if s2.Phase != Paused {
			t.Fatalf("%v should not move Paused, got %v", ev.Type, s2.Phase)
		}
	}

	s.Now = s.Now.Add(10 * time.Second)
	s = drive(t, s, TogglePause{}) // resuming tune
	s2, _ := s.Update(PlayerEvent(player.Event{Type: player.EventResumed}))
	if s2.Phase != Tuning {
		t.Fatalf("EventResumed echo mid-resume should be ignored, got %v", s2.Phase)
	}

	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("resume should land Playing, got %v", s.Phase)
	}
	s2, _ = s.Update(PlayerEvent(player.Event{Type: player.EventResumed}))
	if s2.Phase != Playing {
		t.Fatalf("EventResumed while playing should be ignored, got %v", s2.Phase)
	}
}

// The flyout's Stop unloads the stream entirely: the state still pauses,
// but resume must reload regardless of how short the pause was.
func TestExternalStopPausesAndForcesReload(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventStopped}))
	if s.Phase != Paused {
		t.Fatalf("external stop should enter Paused, got %v", s.Phase)
	}
	if !s.pauseReload {
		t.Fatal("stop unloads the stream — resume must reload")
	}
	url := s.Station.StreamURL()
	s.Now = s.Now.Add(5 * time.Second)
	s = drive(t, s, TogglePause{})
	if lastCall(pl) != "play "+url {
		t.Fatalf("resume after stop must reload, calls %v", pl.callsList())
	}
}

// Paused is a held state, not a stalled one: no watchdog may strike the
// station or wander off tuning while the radio is parked.
func TestPausedNeverStrikesOrRetunes(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	uuid := s.Station.UUID
	playsBefore := len(pl.callsList())
	for i := 0; i < 6; i++ {
		s = drive(t, s, Tick{Now: s.Now.Add(time.Minute)})
	}
	if s.Phase != Paused {
		t.Fatalf("five paused minutes must not move the phase, got %v", s.Phase)
	}
	if st, _ := s.core.StationByUUID(uuid); st.FailCount != 0 {
		t.Fatalf("paused station earned a strike: %+v", st.FailCount)
	}
	if s.Station.UUID != uuid {
		t.Fatal("paused radio must not retune itself")
	}
	if len(pl.callsList()) != playsBefore {
		t.Fatalf("paused radio sent player commands: %v", pl.callsList())
	}
}

// Next while paused tunes like always — the paused segment was already
// banked as a clean close, so the station takes no skip penalty.
func TestSpaceWhilePausedLeavesBetaAlone(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	uuid := s.Station.UUID
	betaBefore := s.core.BanditBeta(uuid)

	s = drive(t, s, Next{})
	if !strings.HasPrefix(lastCall(pl), "play") {
		t.Fatalf("Next while paused should tune, calls %v", pl.callsList())
	}
	if got := s.core.BanditBeta(uuid); got != betaBefore {
		t.Fatalf("pause+skip charged beta %v, want unchanged %v", got, betaBefore)
	}
}

// A backend crash while paused restarts the player but does not start it
// playing — the hold survives, and the eventual resume reloads.
func TestBackendDeathWhilePaused(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventDied, Err: errors.New("gone")}))
	if s.Phase != Paused {
		t.Fatalf("backend death while paused should stay paused, got %v", s.Phase)
	}
	if lastCall(pl) != "restart" {
		t.Fatalf("paused backend death should only restart, calls %v", pl.callsList())
	}
	if !s.pauseReload {
		t.Fatal("a respawned backend holds nothing — resume must reload")
	}
	url := s.Station.StreamURL()
	s.Now = s.Now.Add(5 * time.Second)
	s = drive(t, s, TogglePause{})
	if lastCall(pl) != "play "+url {
		t.Fatalf("resume after backend restart must reload, calls %v", pl.callsList())
	}
}

// CycleSleep walks the sleep ladder 15/30/45/60/90 and off the far end,
// narrating each step.
func TestSleepTimerCycles(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	want := []string{"SLEEP  15 min", "SLEEP  30 min", "SLEEP  45 min", "SLEEP  60 min", "SLEEP  90 min", "SLEEP  Off"}
	for i, w := range want {
		s = drive(t, s, CycleSleep{})
		if s.Feedback != w {
			t.Fatalf("cycle %d: feedback %q, want %q", i, s.Feedback, w)
		}
		if i == len(want)-1 {
			if s.SleepStep != -1 || !s.SleepAt.IsZero() {
				t.Fatal("off the end of the ladder should disarm the timer")
			}
		} else if s.SleepStep != i {
			t.Fatalf("cycle %d: sleepStep %d", i, s.SleepStep)
		}
	}
}

// The last half-minute fades the player's volume toward silence — never
// the stored one — then expiry pauses first and restores volume strictly
// after.
func TestSleepFadeAndExpiry(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s.Volume = 100
	s = drive(t, s, CycleSleep{}) // 15m
	s = drive(t, s, Tick{Now: s.SleepAt.Add(-15 * time.Second)})
	if lastCall(pl) != "vol 50" {
		t.Fatalf("fade should send ≈half volume, calls %v", pl.callsList())
	}
	if s.core.Volume() != 100 {
		t.Fatalf("fade must not touch the stored volume, got %d", s.core.Volume())
	}

	s = drive(t, s, Tick{Now: s.SleepAt})
	if s.Phase != Paused {
		t.Fatalf("expiry should pause the radio, got %v", s.Phase)
	}
	if s.SleepStep != -1 || !s.SleepAt.IsZero() {
		t.Fatal("expiry should disarm the timer")
	}
	calls := pl.callsList()
	pauseAt, volAt := -1, -1
	for i, c := range calls {
		if c == "pause true" {
			pauseAt = i
		}
		if c == "vol 100" {
			volAt = i
		}
	}
	if pauseAt < 0 || volAt < 0 || pauseAt > volAt {
		t.Fatalf("expiry must pause before restoring volume, calls %v", calls)
	}
}

// Stepping the timer mid-fade puts the listener's volume back immediately,
// not at the next tick.
func TestSleepKeyRestoresDuringFade(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s.Volume = 100
	s = drive(t, s, CycleSleep{})
	s = drive(t, s, Tick{Now: s.SleepAt.Add(-10 * time.Second)}) // fade fires
	if !strings.HasPrefix(lastCall(pl), "vol ") {
		t.Fatalf("fade should be underway, calls %v", pl.callsList())
	}
	s = drive(t, s, CycleSleep{})
	if s.SleepStep != 1 {
		t.Fatalf("step mid-fade should still step the timer, got %d", s.SleepStep)
	}
	if lastCall(pl) != "vol 100" || s.sleepVol != -1 {
		t.Fatalf("step mid-fade should restore the saved volume now, calls %v sleepVol %d", pl.callsList(), s.sleepVol)
	}
}

// A timer that runs out on an already-paused radio just disarms — there is
// nothing to pause.
func TestSleepExpiryWhilePaused(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = paused(t, s)
	s = drive(t, s, CycleSleep{})
	callsBefore := len(pl.callsList())
	s = drive(t, s, Tick{Now: s.SleepAt.Add(time.Second)})
	if s.SleepStep != -1 || !s.SleepAt.IsZero() {
		t.Fatal("expiry should clear the timer even while paused")
	}
	for _, c := range pl.callsList()[callsBefore:] {
		if strings.HasPrefix(c, "pause") {
			t.Fatalf("expiry on a paused radio must not send another pause, calls %v", pl.callsList())
		}
	}
}

// Pausing an outage parks the radio instead of retrying: no retry fires,
// nothing tunes itself, and resume reloads into a fresh try.
func TestPauseWhileOfflineStopsRetrying(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	s = offline(t, s)
	s = drive(t, s, TogglePause{})
	if s.Phase != Paused {
		t.Fatalf("TogglePause while offline should pause, got %v", s.Phase)
	}
	if s.retryN != 0 || !s.retryAt.IsZero() {
		t.Fatal("pausing an outage should disarm the retry")
	}
	playsBefore := len(pl.callsList())
	for i := 0; i < 3; i++ {
		s = drive(t, s, Tick{Now: s.Now.Add(time.Minute)})
	}
	if s.Phase != Paused || len(pl.callsList()) != playsBefore {
		t.Fatalf("paused-over-offline must not retry or tune: phase=%v calls %v", s.Phase, pl.callsList())
	}
	s = drive(t, s, TogglePause{})
	if lastCall(pl) != "play "+s.Station.StreamURL() {
		t.Fatalf("resume from paused-over-offline should reload, calls %v", pl.callsList())
	}
}

// The sleep timer reaching zero on an offline radio parks it — no full-
// volume surprise when the network comes back.
func TestSleepExpiryWhileOffline(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = offline(t, s)
	s = drive(t, s, CycleSleep{})
	// A retry armed for later at the expiry instant is the normal real-
	// world state — not one that raced ahead of the timer.
	s.retryAt = s.SleepAt.Add(30 * time.Second)
	s = drive(t, s, Tick{Now: s.SleepAt.Add(time.Second)})
	if s.Phase != Paused {
		t.Fatalf("expiry while offline should pause, got %v", s.Phase)
	}
	if s.retryN != 0 || !s.retryAt.IsZero() {
		t.Fatal("the parked outage must not keep a retry armed")
	}
	s = drive(t, s, Tick{Now: s.Now.Add(2 * time.Minute)})
	if s.Phase != Paused {
		t.Fatalf("no retry should fire under the pause, got %v", s.Phase)
	}
}

// However the timer runs out, the player ends at the listener's stored
// volume — never left at a faded level.
func TestSleepExpiryRestoresVolumeFromAnyPhase(t *testing.T) {
	for _, ph := range []Phase{Offline, Paused, Playing} {
		s, pl := testState(t)
		s = playing(t, s)
		s = drive(t, s, SetVolume{Percent: 70})
		s = drive(t, s, CycleSleep{}) // 15 min
		if ph == Offline {
			s, _ = offlineStreak(t, s)
			// Keep the armed retry out of the timer's way: it would lap
			// at the -10s tick and move the phase before expiry.
			s.retryAt = s.SleepAt.Add(30 * time.Second)
		}
		if ph == Paused {
			s = drive(t, s, TogglePause{})
		}
		s = drive(t, s, Tick{Now: s.SleepAt.Add(-10 * time.Second)})
		if s.Phase != ph {
			t.Fatalf("before expiry: want %v, got %v", ph, s.Phase)
		}
		s = drive(t, s, Tick{Now: s.SleepAt.Add(time.Second)})
		if pl.volumeLevel() != 70 {
			t.Fatalf("%v: player left at %d after sleep, want the listener's 70", ph, pl.volumeLevel())
		}
	}
}
