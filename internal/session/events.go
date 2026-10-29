package session

import (
	"errors"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

// listen is the event pump: exactly one command parked on the player's
// channel at a time, re-armed by every event handler that returns. A
// missed re-arm silences the stream's events for good.
func (s State) listen() Cmd {
	pl := s.pl
	return func() Msg {
		ev, ok := <-pl.Events()
		if !ok {
			return EventsClosed{}
		}
		return PlayerEvent(ev)
	}
}

func (s State) handlePlayerEvent(ev player.Event) (State, []Cmd) {
	switch ev.Type {
	case player.EventTitle:
		tr, ok, suspect, lovedNow := s.core.NoteTitle(ev.Title, time.Now())
		s.Suspect = suspect
		if ok {
			s.Artist = tr.Artist
			s.Title = tr.Title
			s.TrackKey = tr.ArtistKey
			s.HaveTrack = true
			s.LovedTrack = lovedNow
		} else {
			s.Artist, s.Title, s.TrackKey = "", "", ""
			s.HaveTrack = false
			s.LovedTrack = false
		}
		// The queue says whether this song's artist is new; a title that
		// didn't parse as a track has no artist to be new.
		s.NewToYou = ok && ev.NewToYou
		s.nameDiscovery(ev.Title)
		// Auto-hop only on the stream's own break marker — the opt-in
		// promise is "the title said so", never suspicion alone. The hop
		// goes through Tune on purpose: a skip during a break earns the
		// discounted beta, same as a manual one.
		if s.opts.AutoHopAds && s.Phase == Playing && s.HaveStation && s.core.AdBreak() &&
			(s.hopAt.IsZero() || s.Now.Sub(s.hopAt) >= autoHopCooldown) {
			s.hopAt = s.Now
			// beginSkip mutates s; call it before the return list, whose
			// operand order is unspecified.
			cmd := s.beginSkip("HOPPED  Ad break skipped")
			return s, []Cmd{s.listen(), cmd}
		}

	case player.EventPlaying:
		if s.Phase == Paused {
			// Backend restart noise while we hold the pause; the resume
			// path owns the way back to Playing.
			return s, []Cmd{s.listen()}
		}
		if s.Phase == Tuning || s.Phase == Offline {
			if s.Phase == Offline {
				// Going offline closed the listen; a lock that lands late
				// is still a session worth crediting.
				s.core.StartListen(s.Station.UUID, time.Now())
			}
			s.core.MarkStationHealthy(s.Station.UUID)
			// A reconnect keeps the listen clock: PlayStart only opens
			// once per tune, not once per stream.
			if s.PlayStart.IsZero() {
				s.PlayStart = s.Now
			}
			s.lockAt = s.Now
			s.failStreak = nil
			s.netFails = 0
			s.Reconnecting = false
			s.Resuming = false
			s.lastGood = s.Station
			s.haveLastGood = true
			if s.retryN > 0 {
				s.retryN = 0
				s.RetryAt = time.Time{}
				s.setFeedback("ONLINE  Signal restored")
			}
			s.Note = ""
		}
		if s.Phase != Stopped {
			s.Phase = Playing
		}

	case player.EventBuffering:
		if s.Phase == Paused {
			return s, []Cmd{s.listen()}
		}
		if s.Phase == Playing {
			s.Phase = Buffering
			s.bufferAt = s.Now
		}

	case player.EventLevel:
		l, r := ev.LevelL, ev.LevelR
		if l == 0 && r == 0 && ev.Level > 0 {
			// A backend that only measures a mono mixdown.
			l, r = ev.Level, ev.Level
		}
		s.Level = Level{L: l, R: r, Peak: ev.Peak, Stereo: ev.Stereo}

	case player.EventStreamError, player.EventStreamEnded:
		reason := "signal lost"
		if ev.Type == player.EventStreamError {
			reason = "stream failed"
		}
		// A discovery queue that lost its network says so by wrapping
		// ErrSourceNetwork. That is the connection's fault, not the
		// station's, in any phase: it takes the same road, minus the strike.
		strike := !(ev.Type == player.EventStreamError && errors.Is(ev.Err, player.ErrSourceNetwork))
		switch s.Phase {
		case Paused:
			// The stream died underneath the pause: resuming can no
			// longer unpause in place, it has to reload.
			s.pauseReload = true
		case Playing, Buffering:
			s2, cmds := s.dropStreamAs(reason, strike)
			return s2, append([]Cmd{s2.listen()}, cmds...)
		case Tuning:
			s2, cmds := s.failTuneAs(reason, strike)
			return s2, append([]Cmd{s2.listen()}, cmds...)
		}
		return s, []Cmd{s.listen()}

	case player.EventPaused:
		return s.externalPause()

	case player.EventResumed:
		return s.externalResume()

	case player.EventStopped:
		return s.externalStop()

	case player.EventDied:
		// The dead process's channel is gone; restartCmd brings up the
		// replacement and Restarted re-arms listen() on it.
		return s.backendDied(ev.Err)
	}
	return s, []Cmd{s.listen()}
}

// nameDiscovery offers a discovery station's first title as its name. Core
// decides whether it applies (only a station started from a YouTube link,
// still named by that link, takes it), so every station's first title is
// offered once and a later one never is. A rename changes what the strip
// and the library show, so the pick reason follows the name it was built on.
func (s *State) nameDiscovery(title string) {
	if s.named || !s.discovery() || strings.TrimSpace(title) == "" {
		return
	}
	s.named = true
	old := s.Station.Name
	if err := s.core.RenameDiscovery(s.Station.UUID, title); err != nil {
		return // cosmetic: the station keeps the name it had
	}
	st, ok := s.core.StationByUUID(s.Station.UUID)
	if !ok || st.Name == old {
		return
	}
	if s.Reason == s.discoveringReason(old) {
		s.Reason = s.discoveringReason(st.Name)
	}
	s.Station = st
}

// dropStream handles a live stream that ended or errored underneath us:
// reconnect the same URL in place. Drops beyond maxDrops without a stable
// minute fail the tune instead.
func (s State) dropStream(reason string) (State, []Cmd) {
	return s.dropStreamAs(reason, true)
}

// dropStreamAs is dropStream with the strike optional, for the failure it
// may end in: see failTuneAs.
func (s State) dropStreamAs(reason string, strike bool) (State, []Cmd) {
	s.drops++
	if s.drops > maxDrops {
		return s.failTuneAs(reason, strike)
	}
	s.Phase = Tuning
	s.TuneAt = s.Now
	s.Reconnecting = true
	s.Resuming = false
	s.Note = reason + " " + s.dot() + " reconnecting"
	return s, []Cmd{s.reconnectCmd()}
}

// reconnectCmd re-points the player at the same stream after a drop. No
// StartListen: the listen stays open across the reconnect so the drop
// doesn't split the session or charge the station a skip.
func (s State) reconnectCmd() Cmd {
	pl, uuid, url := s.pl, s.Station.UUID, s.Station.StreamURL()
	return func() Msg {
		if err := pl.Play(url); err != nil {
			return PlayFailed{UUID: uuid, Err: err}
		}
		return nil
	}
}

// restartCmd brings up a fresh backend after a crash, then — unless play
// is false (the radio was paused) — re-points it at the current station so
// playback resumes without waiting for a retune.
func (s State) restartCmd(play bool) Cmd {
	pl, st, haveSt := s.pl, s.Station, s.HaveStation
	return func() Msg {
		err := pl.Restart()
		if err == nil && haveSt && play {
			err = pl.Play(st.StreamURL())
		}
		return Restarted{Err: err}
	}
}

// backendDied handles the backend process going away: within the restart
// budget, spawn a fresh one; past it, report the crash loop and stop.
func (s State) backendDied(err error) (State, []Cmd) {
	if s.Quitting {
		return s, nil // the event stream closing behind Close() is expected
	}
	cutoff := s.Now.Add(-restartWindow)
	recent := s.restarts[:0]
	for _, at := range s.restarts {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	s.restarts = recent
	if len(s.restarts) >= maxRestarts {
		s.Phase = Stopped
		s.Fatal = "mpv exited " + s.dot() + " restart screech"
		if err != nil {
			s.Fatal += " " + s.dot() + " " + err.Error()
		}
		return s, nil
	}
	s.restarts = append(s.restarts, s.Now)
	s.Note = "player crashed " + s.dot() + " restarting"
	return s, []Cmd{s.restartCmd(s.Phase != Paused)}
}

func (s State) restarted(msg Restarted) (State, []Cmd) {
	if msg.Err != nil {
		s.Phase = Stopped
		s.Fatal = "mpv exited " + s.dot() + " restart screech " + s.dot() + " " + msg.Err.Error()
		return s, nil
	}
	if s.Phase == Paused {
		// The respawned backend holds nothing: the pause survives, but
		// resuming has to reload the stream rather than unpause.
		s.pauseReload = true
		return s, []Cmd{s.listen()}
	}
	if s.HaveStation {
		s.Phase = Tuning
		s.TuneAt = s.Now
		s.Reconnecting = true
	}
	// Events() now yields the new process's channel; re-arm the listener
	// that died with the old one.
	return s, []Cmd{s.listen()}
}

// quit banks the open listen and stops the backend; faces exit after
// sending it.
func (s State) quit() (State, []Cmd) {
	s.Quitting = true
	s.core.EndListen(time.Now())
	_ = s.pl.Close()
	return s, nil
}
