package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/player"
)

// pressRune drives one rune keypress through Update.
func pressRune(t *testing.T, m Model, r rune) Model {
	t.Helper()
	return drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// callsSoFar returns the stub player's ordered command log.
func callsSoFar(m Model) []string { return m.pl.(*stubPlayer).callsList() }

func lastCall(m Model) string {
	c := callsSoFar(m)
	if len(c) == 0 {
		return ""
	}
	return c[len(c)-1]
}

func hasCall(m Model, prefix string) bool {
	for _, c := range callsSoFar(m) {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// paused puts the model in phPaused the way pressing p does.
func paused(t *testing.T, m Model) Model {
	t.Helper()
	m = pressRune(t, m, 'p')
	if m.ph != phPaused {
		t.Fatalf("p should pause, got phase %v", m.ph)
	}
	return m
}

// p holds mpv's pause, ends the current listen, and swaps the chrome to
// the paused face: PAUSED in the header, resume in the footer.
func TestPauseKeyPausesAndBanksTheListen(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = paused(t, m)
	if lastCall(m) != "pause true" {
		t.Fatalf("pause should send SetPaused(true), calls %v", callsSoFar(m))
	}
	if m.core.ListenOpen() {
		t.Fatal("pausing must end the current listen — paused time is not listening")
	}
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "PAUSED") {
		t.Fatalf("paused header/status missing PAUSED: %q", plain)
	}
	if !strings.Contains(plain, "resume") {
		t.Fatalf("paused footer should offer resume: %q", plain)
	}
}

// A pause shorter than the buffer window unpauses in place: SetPaused, no
// reload, and the listen reopens as a continuation on the next lock.
func TestShortResumeUnpausesInPlace(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = paused(t, m)
	m.now = m.now.Add(10 * time.Second)

	m = pressRune(t, m, 'p')
	if m.ph != phTune || !m.resuming {
		t.Fatalf("short resume should enter a resuming tune, got ph=%v resuming=%v", m.ph, m.resuming)
	}
	if lastCall(m) != "pause false" {
		t.Fatalf("short resume should unpause in place, calls %v", callsSoFar(m))
	}
	// The only recorded Play is the original tune before the pause.
	plays := 0
	for _, c := range callsSoFar(m) {
		if strings.HasPrefix(c, "play") {
			plays++
		}
	}
	if plays != 1 {
		t.Fatalf("in-place resume must not reload, calls %v", callsSoFar(m))
	}
	if !strings.Contains(stripANSI(m.headerBar()), "RESUMING") {
		t.Fatal("header should say RESUMING during a short resume")
	}
	if !m.core.ListenOpen() {
		t.Fatal("resume should reopen the listen")
	}

	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("stream lock should land phPlay, got %v", m.ph)
	}
	if m.resuming {
		t.Fatal("resuming should clear on lock")
	}
}

// Past the buffer window the cached stream is stale: resume reloads the
// station and rejoins the live edge.
func TestLongResumeReloads(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	url := m.st.StreamURL()
	m.now = m.now.Add(resumeInPlace + time.Second)

	m = pressRune(t, m, 'p')
	if m.ph != phTune {
		t.Fatalf("reload resume should pass through phTune, got %v", m.ph)
	}
	if lastCall(m) != "play "+url {
		t.Fatalf("long pause should reload the stream, calls %v", callsSoFar(m))
	}
	if !m.core.ListenOpen() {
		t.Fatal("a reloaded resume should reopen the listen")
	}
}

// The OS flyout / media keys set mpv's pause property directly: the event
// arrives instead of a key, and screech answers by ending the listen, not
// by sending another pause back.
func TestExternalPauseAndResume(t *testing.T) {
	m := playing(t, testModel(t))
	m = drive(t, m, evMsg(player.Event{Type: player.EventPaused}))
	if m.ph != phPaused {
		t.Fatalf("external pause should enter phPaused, got %v", m.ph)
	}
	if hasCall(m, "pause true") {
		t.Fatal("external pause must not echo SetPaused back to mpv")
	}
	if m.core.ListenOpen() {
		t.Fatal("external pause still ends the listen")
	}

	// Released externally inside the buffer window: no command at all,
	// just the resuming tune and a reopened listen.
	callsBefore := len(callsSoFar(m))
	m.now = m.now.Add(10 * time.Second)
	m = drive(t, m, evMsg(player.Event{Type: player.EventResumed}))
	if m.ph != phTune || !m.resuming {
		t.Fatalf("external resume should enter a resuming tune, got ph=%v resuming=%v", m.ph, m.resuming)
	}
	if len(callsSoFar(m)) != callsBefore {
		t.Fatalf("in-place external resume needs no player command, calls %v", callsSoFar(m))
	}
	if !m.core.ListenOpen() {
		t.Fatal("external resume should reopen the listen")
	}
}

// An external release past the buffer window reloads like a long p resume.
func TestExternalResumeAfterBufferReloads(t *testing.T) {
	m := playing(t, testModel(t))
	m = drive(t, m, evMsg(player.Event{Type: player.EventPaused}))
	url := m.st.StreamURL()
	m.now = m.now.Add(resumeInPlace + time.Second)
	m = drive(t, m, evMsg(player.Event{Type: player.EventResumed}))
	if lastCall(m) != "play "+url {
		t.Fatalf("stale external resume should reload, calls %v", callsSoFar(m))
	}
}

// Event echoes the model already knows about must not bounce the phase:
// pause while paused, resume while resuming or playing, playing/buffering
// chatter underneath a pause.
func TestPauseEchoesAreIgnored(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	for _, ev := range []player.Event{
		{Type: player.EventPaused},
		{Type: player.EventPlaying},
		{Type: player.EventBuffering},
	} {
		nm, _ := m.Update(evMsg(ev))
		if nm.(Model).ph != phPaused {
			t.Fatalf("%v should not move phPaused, got %v", ev.Type, nm.(Model).ph)
		}
	}

	m.now = m.now.Add(10 * time.Second)
	m = pressRune(t, m, 'p') // resuming tune
	nm, _ := m.Update(evMsg(player.Event{Type: player.EventResumed}))
	if nm.(Model).ph != phTune {
		t.Fatalf("EventResumed echo mid-resume should be ignored, got %v", nm.(Model).ph)
	}

	m = drive(t, m, evMsg(player.Event{Type: player.EventPlaying}))
	if m.ph != phPlay {
		t.Fatalf("resume should land phPlay, got %v", m.ph)
	}
	nm, _ = m.Update(evMsg(player.Event{Type: player.EventResumed}))
	if nm.(Model).ph != phPlay {
		t.Fatalf("EventResumed while playing should be ignored, got %v", nm.(Model).ph)
	}
}

// The flyout's Stop unloads the stream entirely: the model still pauses,
// but resume must reload regardless of how short the pause was.
func TestExternalStopPausesAndForcesReload(t *testing.T) {
	m := playing(t, testModel(t))
	m = drive(t, m, evMsg(player.Event{Type: player.EventStopped}))
	if m.ph != phPaused {
		t.Fatalf("external stop should enter phPaused, got %v", m.ph)
	}
	if !m.pauseReload {
		t.Fatal("stop unloads the stream — resume must reload")
	}
	url := m.st.StreamURL()
	m.now = m.now.Add(5 * time.Second)
	m = pressRune(t, m, 'p')
	if lastCall(m) != "play "+url {
		t.Fatalf("resume after stop must reload, calls %v", callsSoFar(m))
	}
}

// Paused is a held state, not a stalled one: no watchdog may strike the
// station or wander off tuning while the radio is parked.
func TestPausedNeverStrikesOrRetunes(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	uuid := m.st.UUID
	playsBefore := len(callsSoFar(m))
	for i := 0; i < 6; i++ {
		m = drive(t, m, tickMsg(m.now.Add(time.Minute)))
	}
	if m.ph != phPaused {
		t.Fatalf("five paused minutes must not move the phase, got %v", m.ph)
	}
	if st, _ := m.core.StationByUUID(uuid); st.FailCount != 0 {
		t.Fatalf("paused station earned a strike: %+v", st.FailCount)
	}
	if m.st.UUID != uuid {
		t.Fatal("paused radio must not retune itself")
	}
	if len(callsSoFar(m)) != playsBefore {
		t.Fatalf("paused radio sent player commands: %v", callsSoFar(m))
	}
}

// SPACE while paused tunes like always — the paused segment was already
// banked as a clean close, so the station takes no skip penalty.
func TestSpaceWhilePausedLeavesBetaAlone(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	uuid := m.st.UUID
	betaBefore := m.core.BanditBeta(uuid)

	m = drive(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if !strings.HasPrefix(lastCall(m), "play") {
		t.Fatalf("SPACE while paused should tune, calls %v", callsSoFar(m))
	}
	if got := m.core.BanditBeta(uuid); got != betaBefore {
		t.Fatalf("pause+skip charged beta %v, want unchanged %v", got, betaBefore)
	}
}

// A backend crash while paused restarts the player but does not start it
// playing — the hold survives, and the eventual resume reloads.
func TestBackendDeathWhilePaused(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	m = drive(t, m, evMsg(player.Event{Type: player.EventDied, Err: errors.New("gone")}))
	if m.ph != phPaused {
		t.Fatalf("backend death while paused should stay paused, got %v", m.ph)
	}
	if lastCall(m) != "restart" {
		t.Fatalf("paused backend death should only restart, calls %v", callsSoFar(m))
	}
	if !m.pauseReload {
		t.Fatal("a respawned backend holds nothing — resume must reload")
	}
	url := m.st.StreamURL()
	m.now = m.now.Add(5 * time.Second)
	m = pressRune(t, m, 'p')
	if lastCall(m) != "play "+url {
		t.Fatalf("resume after backend restart must reload, calls %v", callsSoFar(m))
	}
}

// z walks the sleep ladder 15/30/45/60/90 and off the far end, narrating
// each step; the footer counts the armed timer down.
func TestSleepTimerCycles(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	want := []string{"SLEEP  15 min", "SLEEP  30 min", "SLEEP  45 min", "SLEEP  60 min", "SLEEP  90 min", "SLEEP  Off"}
	for i, w := range want {
		m = pressRune(t, m, 'z')
		if m.feedback != w {
			t.Fatalf("z press %d: feedback %q, want %q", i, m.feedback, w)
		}
		if i == len(want)-1 {
			if m.sleepStep != -1 || !m.sleepAt.IsZero() {
				t.Fatal("off the end of the ladder should disarm the timer")
			}
		} else if m.sleepStep != i {
			t.Fatalf("z press %d: sleepStep %d", i, m.sleepStep)
		}
	}
	m = pressRune(t, m, 'z')
	if got := stripANSI(m.footerBar()); !strings.Contains(got, "sleep 15m") {
		t.Fatalf("armed timer should count down in the footer: %q", got)
	}
}

// The last half-minute fades the player's volume toward silence — never
// the stored one — then expiry pauses first and restores volume strictly
// after.
func TestSleepFadeAndExpiry(t *testing.T) {
	m := playing(t, testModel(t))
	m.volume = 100
	m = pressRune(t, m, 'z') // 15m
	m.now = m.sleepAt.Add(-15 * time.Second)
	m = drive(t, m, tickMsg(m.now))
	if lastCall(m) != "vol 50" {
		t.Fatalf("fade should send ≈half volume, calls %v", callsSoFar(m))
	}
	if m.core.Volume() != 100 {
		t.Fatalf("fade must not touch the stored volume, got %d", m.core.Volume())
	}

	m = drive(t, m, tickMsg(m.sleepAt))
	if m.ph != phPaused {
		t.Fatalf("expiry should pause the radio, got %v", m.ph)
	}
	if m.sleepStep != -1 || !m.sleepAt.IsZero() {
		t.Fatal("expiry should disarm the timer")
	}
	calls := callsSoFar(m)
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

// Pressing z mid-fade steps the timer and puts the listener's volume back
// immediately, not at the next tick.
func TestSleepKeyRestoresDuringFade(t *testing.T) {
	m := playing(t, testModel(t))
	m.volume = 100
	m = pressRune(t, m, 'z')
	m.now = m.sleepAt.Add(-10 * time.Second)
	m = drive(t, m, tickMsg(m.now)) // fade sends vol ~67
	if !strings.HasPrefix(lastCall(m), "vol ") {
		t.Fatalf("fade should be underway, calls %v", callsSoFar(m))
	}
	m = pressRune(t, m, 'z')
	if m.sleepStep != 1 {
		t.Fatalf("z mid-fade should still step the timer, got %d", m.sleepStep)
	}
	if lastCall(m) != "vol 100" || m.sleepVol != -1 {
		t.Fatalf("z mid-fade should restore the saved volume now, calls %v sleepVol %d", callsSoFar(m), m.sleepVol)
	}
}

// A timer that runs out on an already-paused radio just disarms — there is
// nothing to pause.
func TestSleepExpiryWhilePaused(t *testing.T) {
	m := playing(t, testModel(t))
	m = paused(t, m)
	m = pressRune(t, m, 'z')
	callsBefore := len(callsSoFar(m))
	m.now = m.sleepAt.Add(time.Second)
	m = drive(t, m, tickMsg(m.now))
	if m.sleepStep != -1 || !m.sleepAt.IsZero() {
		t.Fatal("expiry should clear the timer even while paused")
	}
	for _, c := range callsSoFar(m)[callsBefore:] {
		if strings.HasPrefix(c, "pause") {
			t.Fatalf("expiry on a paused radio must not send another pause, calls %v", callsSoFar(m))
		}
	}
}
