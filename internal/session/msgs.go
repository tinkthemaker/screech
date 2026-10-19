package session

import (
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

// Msg is everything that moves a session: listener intents a face sends
// in, asynchronous results its commands send back, and player events.
type Msg interface{ isMsg() }

// Cmd is one unit of session side effect, run off the face's update path.
// It produces at most one Msg — or none, like the parked event listener.
type Cmd func() Msg

// Intents — the things a listener can ask for.
type Next struct{}
type Love struct{}
type TogglePreset struct{}
type RecallPreset struct{ Slot int }
type TogglePause struct{}
type CycleSleep struct{}
type StepVolume struct{ Delta int }
type SetVolume struct{ Percent int }
type Seed struct{ Query string }
type TuneTo struct {
	UUID   string
	Reason string // shown as the pick reason
}
type Wildcard struct{} // the virgin first-run shortcut: pick anything
type RefreshPresets struct{}
type Touch struct{} // any sign of life; clears the startup notice
type Quit struct{}

// Tick carries the face's clock; the connectivity watchdogs and the sleep
// countdown run off it.
type Tick struct{ Now time.Time }

// Results and events produced by commands and the player.
type Tuned struct {
	Pick core.Pick
	Err  error
}
type Seeded struct {
	Result core.SeedResult
	OK     bool
}
type Synced struct {
	N, Pruned int
	Err       error
}

// PlayFailed reports a Play() that returned an error — the station's own
// URL was rejected, which is a strike against the station, not a network
// event. It deliberately isn't a PlayerEvent: the listener goroutine owns
// those, and a synthetic one would leave an extra listener armed per
// failure.
type PlayFailed struct {
	UUID string
	Err  error
}
type Restarted struct{ Err error }
type VolumeSet struct{ Err error }
type PauseSet struct{ Err error }

// PlayerEvent wraps a player.Event so the listener goroutine's output
// shares the Msg space with intents and results.
type PlayerEvent player.Event

// EventsClosed means the player event channel closed: either EventDied was
// emitted (a full buffer can drop it) or the backend went away silently.
type EventsClosed struct{}

func (Next) isMsg()           {}
func (Love) isMsg()           {}
func (TogglePreset) isMsg()   {}
func (RecallPreset) isMsg()   {}
func (TogglePause) isMsg()    {}
func (CycleSleep) isMsg()     {}
func (StepVolume) isMsg()     {}
func (SetVolume) isMsg()      {}
func (Seed) isMsg()           {}
func (TuneTo) isMsg()         {}
func (Wildcard) isMsg()       {}
func (RefreshPresets) isMsg() {}
func (Touch) isMsg()          {}
func (Quit) isMsg()           {}
func (Tick) isMsg()           {}
func (Tuned) isMsg()          {}
func (Seeded) isMsg()         {}
func (Synced) isMsg()         {}
func (PlayFailed) isMsg()     {}
func (Restarted) isMsg()      {}
func (VolumeSet) isMsg()      {}
func (PauseSet) isMsg()       {}
func (PlayerEvent) isMsg()    {}
func (EventsClosed) isMsg()   {}
