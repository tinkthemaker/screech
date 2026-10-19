package session

import (
	"fmt"
	"math"
	"time"
)

// pause holds the output without unloading the stream and banks the listen
// segment that just ended — paused wall time never counts as listening.
// external means someone else drove mpv's pause property (media keys, the
// OS flyout, MPRIS): mpv is already paused, so nothing is sent back.
func (s State) pause(external bool) (State, []Cmd) {
	if !s.HaveStation || (s.Phase != Playing && s.Phase != Buffering && s.Phase != Tuning && s.Phase != Offline) {
		return s, nil
	}
	s.core.EndListen(time.Now())
	if s.Phase == Offline {
		// Pausing an outage means "stop retrying": nothing is loaded, so
		// the resume has to reload — and a tune that still fails strikes
		// and re-enters offline like any other failure.
		s.pauseReload = true
		s.retryN = 0
		s.retryAt = time.Time{}
	}
	s.Phase = Paused
	s.PausedAt = s.Now
	s.Reconnecting = false
	s.Resuming = false
	s.bufferAt = time.Time{}
	s.PlayStart = time.Time{} // the readout clock restarts with the next segment
	if external {
		s.setFeedback("PAUSED  From media controls")
		return s, nil
	}
	s.setFeedback("PAUSED  P resumes")
	pl := s.pl
	return s, []Cmd{func() Msg { return PauseSet{Err: pl.SetPaused(true)} }}
}

// resume comes back from a pause. A short pause that kept the stream
// loaded unpauses in place and picks up from mpv's buffer; anything longer
// — or a pause that lost the stream underneath — reloads to rejoin live.
// Either way the listen reopens as a continuation: ResumeListen keeps the
// session's dwell and the track on screen.
func (s State) resume(external bool) (State, []Cmd) {
	if s.Phase != Paused {
		return s, nil
	}
	inPlace := s.Now.Sub(s.PausedAt) < resumeInPlace && !s.pauseReload
	s.Phase = Tuning
	s.TuneAt = s.Now
	s.Resuming = true
	s.Note = ""
	s.Feedback = "" // the PAUSED hint dies with the paused face
	s.pauseReload = false
	switch {
	case !inPlace:
		pl, c, st := s.pl, s.core, s.Station
		return s, []Cmd{func() Msg {
			if err := pl.Play(st.StreamURL()); err != nil {
				return PlayFailed{UUID: st.UUID, Err: err}
			}
			c.ResumeListen(st.UUID, time.Now())
			return nil
		}}
	case external:
		// Released the same way it was held: mpv is already playing
		// again, only the listen needs reopening.
		s.core.ResumeListen(s.Station.UUID, time.Now())
		return s, nil
	default:
		pl, c, uuid := s.pl, s.core, s.Station.UUID
		return s, []Cmd{func() Msg {
			if err := pl.SetPaused(false); err != nil {
				return PauseSet{Err: err}
			}
			c.ResumeListen(uuid, time.Now())
			return nil
		}}
	}
}

// togglePause is the face's pause key: one pause state — mpv's own —
// whether it was a key, a media button or the OS flyout that held it.
func (s State) togglePause() (State, []Cmd) {
	if s.Phase == Paused {
		return s.resume(false)
	}
	return s.pause(false)
}

// cycleSleep steps the sleep timer through its ladder and off the far end.
func (s State) cycleSleep() (State, []Cmd) {
	step := s.SleepStep + 1
	if step >= len(SleepSteps) {
		s.SleepStep, s.SleepAt = -1, time.Time{}
		s.setFeedback("SLEEP  Off")
	} else {
		s.SleepStep, s.SleepAt = step, s.Now.Add(SleepSteps[step])
		s.setFeedback(fmt.Sprintf("SLEEP  %d min", int(SleepSteps[step].Minutes())))
	}
	if s.sleepVol >= 0 {
		// A fade was underway: the listener's volume comes back now,
		// not at the next tick.
		pl, v := s.pl, s.Volume
		s.sleepVol = -1
		return s, []Cmd{func() Msg { return VolumeSet{Err: pl.SetVolume(v)} }}
	}
	return s, nil
}

// sleepTick is the tail of tick: the timer counts down on the face's
// clock, the last stretch fades the player's volume out — never the
// stored volume, which stays the listener's — then expiry pauses the
// radio itself.
func (s State) sleepTick() (State, []Cmd) {
	if s.SleepAt.IsZero() {
		return s, nil
	}
	switch remaining := s.SleepAt.Sub(s.Now); {
	case remaining <= 0:
		s.SleepStep, s.SleepAt = -1, time.Time{}
		if s.Phase == Playing || s.Phase == Buffering || s.Phase == Tuning || s.Phase == Offline {
			s2, pcmds := s.pause(false)
			s2.setFeedback("SLEEP  Paused " + s2.dot() + " P resumes")
			s2.sleepVol = -1
			pl, v := s2.pl, s2.Volume
			// One Cmd, because a []Cmd list may be run concurrently by a
			// face: the pause lands strictly before the volume comes
			// back — a radio resuming at faded level would still be
			// asleep. If pause() declined (no station held) this
			// collapses to a bare restore, like the nil pcmd in the old
			// tea.Sequence. Both calls run even when the pause fails;
			// the first error surfaces as its own result message.
			return s2, []Cmd{func() Msg {
				var perr error
				for _, c := range pcmds {
					if c == nil {
						continue
					}
					if ps, ok := c().(PauseSet); ok {
						perr = ps.Err
					}
				}
				if err := pl.SetVolume(v); perr != nil {
					return PauseSet{Err: perr}
				} else if err != nil {
					return VolumeSet{Err: err}
				}
				return nil
			}}
		}
		if s.sleepVol >= 0 {
			// Expired outside a pausable phase but mid-fade: put the
			// listener's volume back.
			s.sleepVol = -1
			pl, v := s.pl, s.Volume
			return s, []Cmd{func() Msg { return VolumeSet{Err: pl.SetVolume(v)} }}
		}
		return s, nil
	case remaining <= sleepFade && (s.Phase == Playing || s.Phase == Buffering):
		v := int(math.Round(float64(s.Volume) * remaining.Seconds() / sleepFade.Seconds()))
		if v != s.sleepVol {
			s.sleepVol = v
			pl := s.pl
			return s, []Cmd{func() Msg { return VolumeSet{Err: pl.SetVolume(v)} }}
		}
	}
	return s, nil
}

func (s State) pauseSet(msg PauseSet) (State, []Cmd) {
	if msg.Err != nil {
		s.Note = "pause failed " + s.dot() + " " + msg.Err.Error()
	}
	return s, nil
}

// playerEvent pause handling — the branches that share the phase checks.
// Kept on State methods so handlePlayerEvent stays a dispatch table.
func (s State) externalPause() (State, []Cmd) {
	if s.Phase == Playing || s.Phase == Buffering || s.Phase == Tuning {
		s2, cmds := s.pause(true)
		return s2, append([]Cmd{s2.listen()}, cmds...)
	}
	// Already paused: the echo of our own SetPaused.
	return s, []Cmd{s.listen()}
}

func (s State) externalResume() (State, []Cmd) {
	if s.Phase == Paused {
		s2, cmds := s.resume(true)
		return s2, append([]Cmd{s2.listen()}, cmds...)
	}
	// The echo of our own SetPaused(false), or nothing to do.
	return s, []Cmd{s.listen()}
}

func (s State) externalStop() (State, []Cmd) {
	if s.Phase == Playing || s.Phase == Buffering || s.Phase == Tuning {
		s2, cmds := s.pause(true)
		s2.setFeedback("PAUSED  Stopped from media controls")
		// Nothing is loaded any more: resume must reload.
		s2.pauseReload = true
		return s2, append([]Cmd{s2.listen()}, cmds...)
	}
	if s.Phase == Paused {
		s.pauseReload = true
	}
	return s, []Cmd{s.listen()}
}
