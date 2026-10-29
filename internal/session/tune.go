package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

// tick runs the frame-clock watchdogs: a tune that never locks fails
// through the same path as a stream error, a buffer that stalls for good
// counts as a drop, an offline retry fires when its timer lapses, and a
// stable minute forgives drops.
func (s State) tick(msg Tick) (State, []Cmd) {
	s.Now = msg.Now
	switch {
	case s.Phase == Tuning && s.HaveStation && s.Now.Sub(s.TuneAt) > tuneStall:
		return s.failTune("no signal")
	case s.Phase == Buffering && !s.bufferAt.IsZero() && s.Now.Sub(s.bufferAt) > bufferStall:
		return s.dropStream("signal stalled")
	case s.Phase == Offline && !s.RetryAt.IsZero() && !s.Now.Before(s.RetryAt):
		return s.offlineRetry()
	}
	if s.Phase == Playing && s.drops > 0 && !s.lockAt.IsZero() && s.Now.Sub(s.lockAt) >= stableAfter {
		s.drops = 0
	}
	// The sleep timer shares the frame clock: the countdown, the fade
	// and the expiry all live in sleepTick.
	return s.sleepTick()
}

func (s State) tuned(msg Tuned) (State, []Cmd) {
	if msg.Err != nil {
		switch {
		case !s.HaveStation:
			s.Fatal = msg.Err.Error()
			s.Phase = Stopped
		case s.retryN > 0:
			// A pick that fails inside an offline retry reschedules
			// instead of stranding the session offline with no timer.
			s.Phase = Offline
			s.RetryAt = s.Now.Add(retryBackoff(s.retryN))
			s.retryN++
		default:
			s.Note = msg.Err.Error()
			// A skip that found nothing must not strand the still-
			// playing stream in Tuning, where the stall watchdog
			// would strike a healthy station every 12s.
			if !s.PlayStart.IsZero() && !s.Reconnecting {
				s.Phase = Playing
			}
		}
		return s, nil
	}
	return s.applyPick(msg.Pick)
}

func (s State) seeded(msg Seeded) (State, []Cmd) {
	s.Seeking = false
	if !msg.OK {
		s.Note = "no matches for " + msg.Result.Label
		return s, nil
	}
	return s.applyPick(msg.Result.Pick)
}

// playFailed handles a Play() that refused the URL — that is the station's
// fault, not the network's: strike it and move on, but never touch the
// offline bookkeeping. A page link that didn't resolve is the same case. A
// stale failure (the station already moved on) is ignored, and so is a
// superseded one: a newer Play took the player while this one was still
// resolving, which says nothing about the station, even when the listener
// has come straight back to it.
//
// The exception is a lookup that failed because the network is down (a
// discovery station's first song): that says nothing about the station
// either, so it takes the road a stream that never locks takes, without the
// strike, and three in a row mean offline.
func (s State) playFailed(msg PlayFailed) (State, []Cmd) {
	if !s.HaveStation || msg.UUID != s.Station.UUID || errors.Is(msg.Err, player.ErrSuperseded) {
		return s, nil
	}
	if errors.Is(msg.Err, player.ErrSourceNetwork) {
		return s.failTuneAs("song lookup failed", false)
	}
	s.core.MarkStationFailed(msg.UUID)
	s.Note = "stream rejected " + s.dot() + " moving on"
	return s, []Cmd{s.tuneDeadCmd()}
}

func (s State) next() (State, []Cmd) {
	if s.Phase == Stopped {
		return s, nil
	}
	if s.Phase == Offline {
		// Offline NEXT is "try now", not skip: fire the pending retry
		// early rather than tuning away from the outage.
		return s.offlineRetry()
	}
	if cmd, ok := s.beginNextSong(); ok {
		return s, []Cmd{cmd}
	}
	fb := "NEXT  Finding another station"
	switch {
	case s.Suspect:
		fb = "LEARNED  Break skip protected"
	case !s.PlayStart.IsZero() && s.Now.Sub(s.PlayStart) >= core.FastSkipWindow:
		fb = "LEARNED  Long listen saved"
	case !s.PlayStart.IsZero():
		fb = "LEARNED  Fast skip noted"
	}
	// beginSkip mutates s: call it before the return list, whose operand
	// order is unspecified.
	cmd := s.beginSkip(fb)
	return s, []Cmd{cmd}
}

// beginNextSong is NEXT on a discovery station that is making sound: the
// player moves on to the next song and the listen carries on untouched, so
// the station is never charged a skip for a song change. It reports false
// when NEXT should keep its old meaning, a station skip: on radio, before
// the stream has locked (there is no song to leave), without a queue behind
// the player, and for a second NEXT inside songNextWindow, which is the
// listener asking for a different station. It mutates s, like beginSkip.
func (s *State) beginNextSong() (Cmd, bool) {
	if (s.Phase != Playing && s.Phase != Buffering) || !s.discovery() {
		return nil, false
	}
	sq, ok := s.pl.(player.SongQueue)
	if !ok {
		return nil, false
	}
	if !s.lastSongNextAt.IsZero() && s.Now.Sub(s.lastSongNextAt) < songNextWindow {
		s.lastSongNextAt = time.Time{} // the skip ends this run of NEXTs
		return nil, false
	}
	s.lastSongNextAt = s.Now
	s.setFeedback("NEXT SONG  Again for a new station")
	dot := s.dot()
	return func() Msg {
		if err := sq.NextSong(); err != nil {
			return Notify{Note: "next song failed " + dot + " " + err.Error()}
		}
		return nil
	}, true
}

// tuneTo plays a station by identity — library rows and preset recall all
// land here.
func (s State) tuneTo(msg TuneTo) (State, []Cmd) {
	if s.HaveStation && msg.UUID == s.Station.UUID {
		return s, nil // already playing it
	}
	pick, err := s.core.TuneTo(msg.UUID, time.Now())
	if err != nil {
		s.Note = err.Error()
		return s, nil
	}
	pick.Reason = msg.Reason
	return s.applyPick(pick)
}

func (s State) seed(msg Seed) (State, []Cmd) {
	s.Seeking = true
	s.Note = ""
	return s, []Cmd{s.seedCmd(msg.Query)}
}

// discover is FIND's Enter: a phrase or a link becomes a discovery station.
// The pick comes back as Tuned like any other, so it lands through
// applyPick; a blank query is nothing to start.
func (s State) discover(msg Discover) (State, []Cmd) {
	seed := strings.TrimSpace(msg.Query)
	if seed == "" {
		return s, nil
	}
	return s, []Cmd{s.discoverCmd(seed)}
}

// discoverCmd starts the station in core, which ends the current listen the
// way TuneTo does. A seed core refuses is the listener's typo, not a failed
// tune: Tuned's error branch would stop a session that has no station yet,
// so the refusal rides Notify as a note instead.
func (s State) discoverCmd(seed string) Cmd {
	c, dot := s.core, s.dot()
	return func() Msg {
		pick, err := c.StartDiscovery(seed, time.Now())
		if err != nil {
			return Notify{Note: "could not start discovery " + dot + " " + err.Error()}
		}
		return Tuned{Pick: pick}
	}
}

// wildcard is the virgin first-run shortcut: pick anything.
func (s State) wildcard() (State, []Cmd) {
	s.Phase = Tuning
	s.TuneAt = s.Now
	return s, []Cmd{s.tuneCmd()}
}

// resumeCmd re-tunes to the last station if it survived, else a fresh pick.
func (s State) resumeCmd() Cmd {
	c := s.core
	return func() Msg {
		if uuid := c.ResumeUUID(); uuid != "" {
			if st, ok := c.StationByUUID(uuid); ok && st.FailCount == 0 {
				return Tuned{Pick: core.Pick{Station: st, Reason: "where you left off"}}
			}
		}
		pick, err := c.Tune(time.Now())
		return Tuned{Pick: pick, Err: err}
	}
}

func (s State) tuneCmd() Cmd {
	c := s.core
	return func() Msg {
		pick, err := c.Tune(time.Now())
		return Tuned{Pick: pick, Err: err}
	}
}

// tuneDeadCmd moves on after a stream failure: the failure already cost
// the station a strike, and TuneDead skips the taste signal a deliberate
// skip would leave.
func (s State) tuneDeadCmd() Cmd {
	c := s.core
	return func() Msg {
		pick, err := c.TuneDead(time.Now())
		return Tuned{Pick: pick, Err: err}
	}
}

func (s State) seedCmd(q string) Cmd {
	c := s.core
	return func() Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, ok := c.Seed(ctx, q, time.Now())
		return Seeded{Result: res, OK: ok}
	}
}

func (s State) volumeCmd() Cmd {
	pl, volume := s.pl, s.Volume
	return func() Msg {
		return VolumeSet{Err: pl.SetVolume(volume)}
	}
}

func (s State) playCmd() Cmd {
	pl, c, st := s.pl, s.core, s.Station
	return func() Msg {
		if err := pl.Play(st.StreamURL()); err != nil {
			return PlayFailed{UUID: st.UUID, Err: err}
		}
		c.StartListen(st.UUID, time.Now())
		return nil
	}
}

// applyPick is the shared transition into a new station: state changes
// here; the face runs its own ceremony on PickSeq.
func (s State) applyPick(pick core.Pick) (State, []Cmd) {
	s.Station = pick.Station
	s.HaveStation = true
	s.Virgin = false
	s.Note = ""
	s.Reason = pick.Reason
	if core.IsDiscovery(pick.Station.UUID) && strings.HasPrefix(pick.Reason, "discovering") {
		// Core spells a discovery's reason with a literal middle dot. The
		// separator is the session's to choose, so the terminal's ASCII
		// mode stays seven-bit.
		s.Reason = s.discoveringReason(pick.Station.Name)
	}
	s.Phase = Tuning
	s.TuneAt = s.Now
	s.Artist, s.Title, s.TrackKey = "", "", ""
	s.HaveTrack = false
	s.LovedTrack = false
	s.NewToYou = false
	s.named = false
	s.lastSongNextAt = time.Time{}
	s.Suspect = false
	s.PlayStart = time.Time{}
	s.drops = 0
	s.Reconnecting = false
	s.Resuming = false
	s.lockAt = time.Time{}
	s.bufferAt = time.Time{}
	// retryN deliberately survives: during an offline retry this pick IS
	// the retry, and retryN > 0 is how a failure finds its way back to the
	// backoff branch instead of striking the station.
	s.PickSeq++
	return s, []Cmd{s.playCmd()}
}

// beginSkip is what a skip does once its guards pass: tune state,
// feedback, and the tune command. Auto-hop runs the same path — a hopped
// break is a skip and gets a skip's discounted penalty.
func (s *State) beginSkip(feedback string) Cmd {
	s.Phase = Tuning
	s.TuneAt = s.Now
	s.Note = ""
	s.setFeedback(feedback)
	return s.tuneCmd()
}

// failTune counts a station that never produced audio — a strike, a move
// to the next station, or offline when it's clearly our network. Inside an
// offline retry it extends the backoff instead of punishing the retried
// station.
func (s State) failTune(reason string) (State, []Cmd) {
	return s.failTuneAs(reason, true)
}

// failTuneAs is failTune with the strike optional. A failure that is plainly
// the network's (a discovery station's song lookup finding no connection)
// still counts toward the offline streak and still moves on, but earns the
// station nothing: there is no strike to forgive later, and a station that
// did nothing wrong is not marked in the meantime.
func (s State) failTuneAs(reason string, strike bool) (State, []Cmd) {
	if s.retryN > 0 {
		s.core.EndListen(time.Now())
		s.RetryAt = s.Now.Add(retryBackoff(s.retryN))
		s.retryN++
		s.Phase = Offline
		s.Reconnecting = false
		s.Resuming = false
		return s, nil
	}
	if strike {
		s.core.MarkStationFailed(s.Station.UUID)
		s.failStreak = append(s.failStreak, s.Station.UUID)
	} else {
		s.netFails++
	}
	if len(s.failStreak)+s.netFails >= offlineAfter {
		// Three different stations, three failures: it's the network.
		// Forgive the strikes and wait for it to come back.
		for _, uuid := range s.failStreak {
			s.core.ForgiveFailure(uuid)
		}
		s.failStreak = nil
		s.netFails = 0
		s.core.EndListen(time.Now())
		s.retryN = 1
		s.RetryAt = s.Now.Add(retryBackoff(0))
		s.Phase = Offline
		s.Reconnecting = false
		s.Resuming = false
		return s, nil
	}
	s.Note = reason + " " + s.dot() + " moving on"
	return s, []Cmd{s.tuneDeadCmd()}
}

// offlineRetry fires when the backoff lapses (or NEXT asks early). Odd
// retries go back to the last station that worked — maybe it's fixed;
// even ones try the pool again. retryN makes the outcome legible in
// failTune: a retry failure backs off, it doesn't strike.
func (s State) offlineRetry() (State, []Cmd) {
	s.RetryAt = time.Time{} // consume the timer before the async part
	if s.retryN%2 == 1 {
		uuid := s.Station.UUID
		if s.haveLastGood {
			uuid = s.lastGood.UUID
		}
		if pick, err := s.core.TuneTo(uuid, time.Now()); err == nil {
			pick.Reason = "where you left off"
			return s.applyPick(pick)
		}
	}
	return s, []Cmd{s.tuneDeadCmd()}
}
