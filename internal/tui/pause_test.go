package tui

import (
	"strings"
	"testing"
	"time"
)

// Pause, resume and the sleep timer's state machine live in
// internal/session now, with their own tests. What remains here is the
// paused face and the sleep countdown the chrome draws off session state.

// p parks the radio on the paused face: PAUSED in the chrome, a resume
// offer in the footer.
func TestPauseKeyPausesAndBanksTheListen(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = paused(t, m)
	plain := stripANSI(m.View())
	if !strings.Contains(plain, "PAUSED") {
		t.Fatalf("paused header/status missing PAUSED: %q", plain)
	}
	if !strings.Contains(plain, "resume") {
		t.Fatalf("paused footer should offer resume: %q", plain)
	}
}

// A short resume re-enters the tune as RESUMING — the header names what
// the stream is doing while the buffer catches up.
func TestShortResumeUnpausesInPlace(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = paused(t, m)
	m.now = m.now.Add(10 * time.Second)

	m = pressRune(t, m, 'p')
	if !strings.Contains(stripANSI(m.headerBar()), "RESUMING") {
		t.Fatal("header should say RESUMING during a short resume")
	}
}

// The armed sleep timer counts down in the footer.
func TestSleepTimerCycles(t *testing.T) {
	m := playing(t, testModel(t))
	m.w, m.h = 80, 24
	m = pressRune(t, m, 'z')
	if got := stripANSI(m.footerBar()); !strings.Contains(got, "sleep 15m") {
		t.Fatalf("armed timer should count down in the footer: %q", got)
	}
}
