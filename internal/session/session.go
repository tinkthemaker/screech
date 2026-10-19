// Package session holds screech's listening state machine as a UI-free
// value type: faces feed it messages, get an updated State back, and run
// its commands off the draw path. The terminal renders it; a window can
// drive the same one.
package session

import (
	"context"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

const (
	tuneStall = 12 * time.Second

	// Connectivity: a stream drop reconnects in place; drops beyond
	// maxDrops without a stable minute earn a strike. Three stations
	// failing in a row means it's almost certainly our network, not
	// theirs — go offline and forgive the strikes.
	offlineAfter = 3
	maxDrops     = 3
	stableAfter  = 60 * time.Second
	bufferStall  = 20 * time.Second

	// mpv itself gets maxRestarts inside restartWindow before we call it
	// dead; a crash loop is reported, not chased forever.
	maxRestarts   = 3
	restartWindow = 5 * time.Minute

	// resumeInPlace is how long a pause can still pick up where it left
	// off: a short pause resumes from mpv's buffer a few seconds behind
	// live, like pausing live TV; past that the buffer is stale or the
	// server has hung up, so resume rejoins the live edge instead.
	resumeInPlace = 30 * time.Second

	// sleepFade is the volume's glide path to silence in the last stretch
	// before the sleep timer pauses the radio.
	sleepFade = 30 * time.Second

	defaultSyncLimit = 20000

	// FeedbackDuration is how long a face shows Feedback after FeedbackAt.
	FeedbackDuration = 2400 * time.Millisecond
)

// SleepSteps is the sleep timer's ladder; CycleSleep steps through it and
// off the far end.
var SleepSteps = []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute, 60 * time.Minute, 90 * time.Minute}

// autoHopCooldown bounds auto-hop: a run of stations all mid-break would
// otherwise chain hops faster than a listener can follow; after one hop
// the next break waits for the listener.
const autoHopCooldown = 30 * time.Second

// retryBackoff spaces offline retries: 5s, 10s, 20s, … capped at a minute.
// n counts failures since the outage began.
func retryBackoff(n int) time.Duration {
	d := 5 * time.Second << n
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

// Phase is where the radio sits in its tune-play-drop cycle.
type Phase int

const (
	Idle   Phase = iota // nothing tuned yet
	Tuning              // waiting for the stream to lock
	Playing
	Buffering
	Offline // every tune is failing — the network, not the stations
	Stopped
	Paused // output held by us or by the OS media controls
)

func (p Phase) String() string {
	names := [...]string{Idle: "idle", Tuning: "tuning", Playing: "playing", Buffering: "buffering", Offline: "offline", Stopped: "stopped", Paused: "paused"}
	if int(p) < 0 || int(p) >= len(names) {
		return "unknown"
	}
	return names[p]
}

type Options struct {
	AutoHopAds bool
	SyncLimit  int // <= 0 means defaultSyncLimit
	// Notice seeds the session with something the listener should see once
	// (e.g. a recovered database); the first Touch clears it.
	Notice string
	// Dot is the separator in notes and feedback; "" means "·".
	Dot string
}

// State is the whole listening session in one value. Faces read the
// exported fields to draw; the unexported ones carry the engine's own
// bookkeeping. Update takes and returns it by value so a face can compare
// before and after.
type State struct {
	Now         time.Time
	Phase       Phase
	Station     core.Station
	HaveStation bool
	Reason      string
	// PickSeq increments on every applied pick, including re-picks of the
	// same station — how a face knows to re-run its landing ceremony.
	PickSeq                        int
	Artist, Title, TrackKey        string
	HaveTrack, LovedTrack, Suspect bool
	Level                          Level
	TuneAt, PlayStart, PausedAt    time.Time
	Reconnecting, Resuming         bool
	Seeking, Syncing, Virgin       bool
	Volume                         int
	Presets                        map[int]string
	SleepStep                      int // index into SleepSteps, -1 off
	SleepAt                        time.Time
	Notice, Note, Feedback, Fatal  string
	FeedbackAt                     time.Time
	Quitting                       bool // a quit is in flight; backend death is expected noise

	core *core.Core
	pl   player.Player
	opts Options

	syncLimit int

	// Connectivity bookkeeping. retryN doubles as the offline flag:
	// nonzero means the current tune attempt came out of an offline retry
	// and its failure extends the backoff instead of striking the station.
	drops        int
	lockAt       time.Time
	bufferAt     time.Time
	failStreak   []string
	retryN       int
	retryAt      time.Time
	lastGood     core.Station
	haveLastGood bool
	restarts     []time.Time

	pauseReload bool      // resume must reload: backend restarted, or the stream stopped/ended while paused
	sleepVol    int       // last faded volume sent to the player; -1 = none
	hopAt       time.Time // last auto-hop; a run of mid-break stations gets one per cooldown
}

// Level is what the backend is metering right now, 0 (silence) to 1.
type Level struct {
	L, R, Peak float64
	Stereo     bool
}

func New(c *core.Core, pl player.Player, opts Options) State {
	now := time.Now()
	sl := opts.SyncLimit
	if sl <= 0 {
		sl = defaultSyncLimit
	}
	return State{
		Now:       now,
		core:      c,
		pl:        pl,
		opts:      opts,
		syncLimit: sl,
		Syncing:   c.NeedsSync(),
		Virgin:    c.IsVirgin(),
		Presets:   c.Presets(),
		Volume:    c.Volume(),
		SleepStep: -1,
		sleepVol:  -1,
		Notice:    opts.Notice,
	}
}

// Init returns the startup commands: the event listener, the persisted
// volume, the resume-or-tune pick, and a directory sync when one's due.
func (s State) Init() []Cmd {
	cmds := []Cmd{s.listen(), s.volumeCmd(), s.resumeCmd()}
	if s.Syncing || s.core.SyncStale(s.syncLimit) {
		// Weekly directory refresh, in the background. Playback never waits.
		cmds = append(cmds, s.syncCmd())
	}
	return cmds
}

func (s State) Update(msg Msg) (State, []Cmd) {
	switch msg := msg.(type) {
	case Tick:
		return s.tick(msg)
	case Tuned:
		return s.tuned(msg)
	case Seeded:
		return s.seeded(msg)
	case PlayFailed:
		return s.playFailed(msg)
	case Next:
		return s.next()
	case TogglePause:
		return s.togglePause()
	case CycleSleep:
		return s.cycleSleep()
	case Love:
		return s.love()
	case TogglePreset:
		return s.togglePreset()
	case RecallPreset:
		return s.recallPreset(msg)
	case StepVolume:
		return s.adjustVolume(msg.Delta)
	case SetVolume:
		return s.volumeTo(msg.Percent)
	case RefreshPresets:
		return s.refreshPresets()
	case TuneTo:
		return s.tuneTo(msg)
	case Seed:
		return s.seed(msg)
	case Wildcard:
		return s.wildcard()
	case Touch:
		return s.touch()
	case Synced:
		return s.synced(msg)
	case VolumeSet:
		return s.volumeSet(msg)
	case PauseSet:
		return s.pauseSet(msg)
	case PlayerEvent:
		return s.handlePlayerEvent(player.Event(msg))
	case EventsClosed:
		return s.backendDied(nil)
	case Restarted:
		return s.restarted(msg)
	case Quit:
		return s.quit()
	}
	return s, nil
}

// CurrentSlot returns the preset slot of the playing station, 0 if unsaved.
func (s State) CurrentSlot() int {
	if !s.HaveStation {
		return 0
	}
	for slot, u := range s.Presets {
		if u == s.Station.UUID {
			return slot
		}
	}
	return 0
}

func (s *State) setFeedback(text string) {
	s.Feedback = text
	s.FeedbackAt = s.Now
}

func (s State) dot() string {
	if s.opts.Dot == "" {
		return "·"
	}
	return s.opts.Dot
}

// syncCmd refreshes the station directory off the face's path, on the same
// 90 s budget the terminal always gave it.
func (s State) syncCmd() Cmd {
	c, limit := s.core, s.syncLimit
	return func() Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		n, pruned, err := c.Sync(ctx, limit)
		return Synced{N: n, Pruned: pruned, Err: err}
	}
}
