package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

// testState builds a session over a temp database and the stub player.
func testState(t *testing.T) (State, *stubPlayer) {
	t.Helper()
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	pl := newStubPlayer()
	return New(c, pl, Options{Dot: "·"}), pl
}

// run executes commands and collects the messages they produce. Listeners
// parked on the player event channel never return — they get the same
// 250 ms timeout as the TUI's runCmd gave them.
func run(t *testing.T, cmds []Cmd) []Msg {
	t.Helper()
	var out []Msg
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}
		done := make(chan Msg, 1)
		go func() { done <- cmd() }()
		select {
		case msg := <-done:
			if msg != nil {
				out = append(out, msg)
			}
		case <-time.After(250 * time.Millisecond):
		}
	}
	return out
}

// drive feeds msg through Update and recursively feeds back whatever the
// returned commands produce.
func drive(t *testing.T, s State, msg Msg) State {
	t.Helper()
	s, cmds := s.Update(msg)
	for _, out := range run(t, cmds) {
		s = drive(t, s, out)
	}
	return s
}

// playing puts the state on a locked stream: station applied, EventPlaying
// observed, listen clock running.
func playing(t *testing.T, s State) State {
	t.Helper()
	s = drive(t, s, Tuned{Pick: pickSeed(core.SeedStations()[0])})
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventPlaying}))
	if s.Phase != Playing {
		t.Fatalf("want Playing, got %v", s.Phase)
	}
	return s
}

func pickSeed(st core.Station) core.Pick {
	return core.Pick{Station: st, Reason: "test"}
}

func evStreamEnded() Msg {
	return PlayerEvent(player.Event{Type: player.EventStreamEnded})
}

func evStreamError() Msg {
	return PlayerEvent(player.Event{Type: player.EventStreamError, Err: errors.New("stream failed")})
}

// offlineStreak fails tunes until the state goes offline and returns the
// struck UUIDs.
func offlineStreak(t *testing.T, s State) (State, []string) {
	t.Helper()
	var struck []string
	for i := 0; i < offlineAfter; i++ {
		if s.Phase == Playing {
			// A stream that dies mid-play drops back into a tune first.
			s = drive(t, s, evStreamEnded())
		}
		if s.Phase != Tuning {
			t.Fatalf("failure %d: expected a tune in flight, got %v", i+1, s.Phase)
		}
		struck = append(struck, s.Station.UUID)
		s = drive(t, s, evStreamError())
	}
	return s, struck
}
