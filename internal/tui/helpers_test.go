package tui

import (
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/session"
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
		// tea.Sequence yields an unexported []tea.Cmd the runtime drains in
		// order; reflection unwraps it so tests see the same ordering.
		if v := reflect.ValueOf(msg); v.IsValid() && v.Kind() == reflect.Slice &&
			v.Type().Elem() == reflect.TypeOf((tea.Cmd)(nil)) {
			if _, isBatch := msg.(tea.BatchMsg); !isBatch {
				var out []tea.Msg
				for i := 0; i < v.Len(); i++ {
					out = append(out, runCmd(t, v.Index(i).Interface().(tea.Cmd))...)
				}
				return out
			}
		}
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

// driveUI feeds a Bubble Tea message through Update and recursively feeds
// back whatever the returned commands produce.
func driveUI(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	for _, out := range runCmd(t, cmd) {
		m = driveUI(t, m, out)
	}
	return m
}

// drive feeds a session message through step — the face's engine port —
// and recursively feeds back whatever the returned commands produce.
func drive(t *testing.T, m Model, msg session.Msg) Model {
	t.Helper()
	m, cmd := m.step(msg)
	for _, out := range runCmd(t, cmd) {
		m = driveUI(t, m, out)
	}
	return m
}

// pressRune drives one rune keypress through Update.
func pressRune(t *testing.T, m Model, r rune) Model {
	t.Helper()
	return driveUI(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// playing puts the model on a locked stream: station applied, EventPlaying
// observed, listen clock running.
func playing(t *testing.T, m Model) Model {
	t.Helper()
	seeds := testSeeds()
	m = drive(t, m, session.Tuned{Pick: pickSeed(seeds[0])})
	m = drive(t, m, session.PlayerEvent(player.Event{Type: player.EventPlaying}))
	if m.s.Phase != session.Playing {
		t.Fatalf("want Playing, got %v", m.s.Phase)
	}
	return m
}

// paused puts the model in Paused the way pressing p does.
func paused(t *testing.T, m Model) Model {
	t.Helper()
	m = pressRune(t, m, 'p')
	if m.s.Phase != session.Paused {
		t.Fatalf("p should pause, got phase %v", m.s.Phase)
	}
	return m
}

func testSeeds() []core.Station { return core.SeedStations() }

func pickSeed(st core.Station) core.Pick {
	return core.Pick{Station: st, Reason: "test"}
}
