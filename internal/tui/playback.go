package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/player"
)

func (m Model) handlePlayerEvent(ev player.Event) (tea.Model, tea.Cmd) {
	// Player state changes are signs of life and restore a settled room;
	// the per-frame audio level is not — it would keep the room lit forever.
	if ev.Type != player.EventLevel {
		m.lastAct = m.now
	}
	// extra carries any work a branch wants done alongside the next listen.
	var extra tea.Cmd
	switch ev.Type {
	case player.EventTitle:
		tr, ok, suspect, lovedNow := m.core.NoteTitle(ev.Title, time.Now())
		m.suspect = suspect
		if ok {
			text := tr.Title
			if tr.Artist != "" {
				text = tr.Artist + " " + m.th.G.Dot + " " + tr.Title
			}
			if text != m.track {
				m.setTrack(tr.Artist, tr.Title)
				m.trackAt = m.now
				// A new title invalidates the play count. Zero it now and
				// let the query land, rather than showing the last track's.
				m.trackKey, m.trackTitle, m.trackPlays = tr.ArtistKey, tr.Title, 0
				extra = m.trackStatsCmd(tr.ArtistKey, tr.Title)
			}
			m.haveTrack = true
			m.lovedTrack = lovedNow
		} else {
			m.haveTrack = false
			m.track, m.trackArtist = "", ""
			m.lovedTrack = false
			m.trackKey, m.trackTitle, m.trackPlays = "", "", 0
		}
		// Auto-hop only on the stream's own break marker — the opt-in
		// promise is "the title said so", never suspicion alone. The hop
		// goes through Tune on purpose: a skip during a break earns
		// endListenLocked's discounted beta, same as a manual one.
		if m.autoHop && m.ph == phPlay && m.haveSt && m.core.AdBreak() &&
			(m.hopAt.IsZero() || m.now.Sub(m.hopAt) >= autoHopCooldown) {
			m.hopAt = m.now
			// beginSkip mutates m; call it before the return so the
			// returned copy carries the tune state (operand order in a
			// return list is unspecified).
			cmd := m.beginSkip("HOPPED  Ad break skipped")
			return m, tea.Batch(m.listen(), cmd)
		}

	case player.EventPlaying:
		if m.ph == phPaused {
			// mpv reporting playing while we're paused contradicts the
			// pause state — hold the phase and let resume() own the way back.
			return m, m.listen()
		}
		if m.ph == phTune || m.ph == phOffline {
			if m.ph == phOffline {
				// Going offline closed the listen; a lock that lands late
				// is still a session worth crediting.
				m.core.StartListen(m.st.UUID, time.Now())
			}
			m.core.MarkStationHealthy(m.st.UUID)
			// A reconnect keeps the listen clock: playStart only opens
			// once per tune, not once per stream.
			if m.playStart.IsZero() {
				m.playStart = m.now
			}
			m.lockAt = m.now
			m.failStreak = nil
			m.reconnecting = false
			m.resuming = false
			m.lastGood = m.st
			m.haveLastGood = true
			if m.retryN > 0 {
				m.retryN = 0
				m.retryAt = time.Time{}
				m.setFeedback("ONLINE  Signal restored")
			}
			m.note = ""
		}
		if m.ph != phDead {
			m.ph = phPlay
		}
		m.wave.SetEnergy(1)

	case player.EventBuffering:
		if m.ph == phPaused {
			return m, m.listen()
		}
		if m.ph == phPlay {
			m.ph = phBuffer
			m.bufferAt = m.now
		}
		m.wave.SetEnergy(0.2)

	case player.EventLevel:
		l, r := ev.LevelL, ev.LevelR
		if l == 0 && r == 0 && ev.Level > 0 {
			// A backend that only measures a mono mixdown.
			l, r = ev.Level, ev.Level
		}
		m.stereo = ev.Stereo
		m.wave.SetStereoLevels(l, r, ev.Peak, m.now.Sub(m.start).Seconds())

	case player.EventStreamError, player.EventStreamEnded:
		reason := "signal lost"
		if ev.Type == player.EventStreamError {
			reason = "stream failed"
		}
		switch m.ph {
		case phPaused:
			// The stream died underneath the pause: a resume can no
			// longer unpause in place, it has to reload.
			m.pauseReload = true
		case phPlay, phBuffer:
			nm, cmd := m.dropStream(reason)
			return nm, tea.Batch(m.listen(), cmd)
		case phTune:
			nm, cmd := m.failTune(reason)
			return nm, tea.Batch(m.listen(), cmd)
		}
		return m, m.listen()

	case player.EventPaused:
		if m.ph == phPlay || m.ph == phBuffer || m.ph == phTune {
			// Someone else held the output — media keys, the OS flyout,
			// MPRIS. Same pause, same accounting.
			nm, cmd := m.pause(true)
			return nm, tea.Batch(m.listen(), cmd)
		}
		// Already paused: the echo of our own SetPaused.
		return m, m.listen()

	case player.EventResumed:
		if m.ph == phPaused {
			nm, cmd := m.resume(true)
			return nm, tea.Batch(m.listen(), cmd)
		}
		// The echo of our own SetPaused(false), or nothing to do.
		return m, m.listen()

	case player.EventStopped:
		if m.ph == phPlay || m.ph == phBuffer || m.ph == phTune {
			nm, cmd := m.pause(true)
			nm.setFeedback("PAUSED  Stopped from media controls")
			// Nothing is loaded any more: resume must reload.
			nm.pauseReload = true
			return nm, tea.Batch(m.listen(), cmd)
		}
		if m.ph == phPaused {
			m.pauseReload = true
		}
		return m, m.listen()

	case player.EventDied:
		// The dead process's channel is gone; restartCmd brings up the
		// replacement and playerRestartedMsg re-arms listen() on it.
		return m.backendDied(ev.Err)
	}
	if extra != nil {
		return m, tea.Batch(m.listen(), extra)
	}
	return m, m.listen()
}

// dropStream handles a live stream that ended or errored underneath us:
// reconnect the same URL in place. Drops beyond maxDrops without a stable
// minute fail the tune instead.
func (m Model) dropStream(reason string) (Model, tea.Cmd) {
	m.drops++
	if m.drops > maxDrops {
		return m.failTune(reason)
	}
	m.ph = phTune
	m.tuneAt = m.now
	m.reconnecting = true
	m.resuming = false
	m.note = reason + " " + m.th.G.Dot + " reconnecting"
	m.wave.SetEnergy(0.05)
	return m, m.reconnectCmd()
}

// failTune is the single exit for a tune that can't play: during an offline
// retry it just extends the backoff, otherwise it strikes the station, and
// a third consecutive strike means the network is the culprit — forgive
// them all and go offline.
func (m Model) failTune(reason string) (Model, tea.Cmd) {
	if m.retryN > 0 {
		// This attempt came out of an offline retry: its failure is the
		// outage, not the station. Back off further and stay offline.
		m.core.EndListen(time.Now())
		m.retryAt = m.now.Add(retryBackoff(m.retryN))
		m.retryN++
		m.ph = phOffline
		m.reconnecting = false
		m.resuming = false
		return m, nil
	}
	m.core.MarkStationFailed(m.st.UUID)
	m.failStreak = append(m.failStreak, m.st.UUID)
	if len(m.failStreak) >= offlineAfter {
		for _, uuid := range m.failStreak {
			m.core.ForgiveFailure(uuid)
		}
		m.failStreak = nil
		m.core.EndListen(time.Now())
		m.retryN = 1
		m.retryAt = m.now.Add(retryBackoff(0))
		m.ph = phOffline
		m.reconnecting = false
		m.resuming = false
		return m, nil
	}
	m.note = reason + " " + m.th.G.Dot + " moving on"
	return m, m.tuneDeadCmd()
}

// offlineRetry fires the scheduled retry: odd attempts go back to the last
// station that actually locked, even ones take a fresh pick. retryN is left
// alone — it is what routes a failure back into the backoff branch.
// retryAt is consumed so the next tick can't fire a second attempt while
// this one is still in flight.
func (m Model) offlineRetry() (Model, tea.Cmd) {
	m.retryAt = time.Time{}
	if m.retryN%2 == 1 {
		uuid := m.st.UUID
		if m.haveLastGood {
			uuid = m.lastGood.UUID
		}
		if pick, err := m.core.TuneTo(uuid, time.Now()); err == nil {
			pick.Reason = "where you left off"
			return m.applyPick(pick)
		}
	}
	return m, m.tuneDeadCmd()
}

// backendDied handles the backend process going away: within the restart
// budget, spawn a fresh one; past it, report the crash loop and stop.
func (m Model) backendDied(err error) (Model, tea.Cmd) {
	if m.quitting {
		return m, nil // the event stream closing behind Close() is expected
	}
	cutoff := m.now.Add(-restartWindow)
	recent := m.restarts[:0]
	for _, at := range m.restarts {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	m.restarts = recent
	if len(m.restarts) >= maxRestarts {
		m.ph = phDead
		m.fatal = "mpv exited " + m.th.G.Dot + " restart screech"
		if err != nil {
			m.fatal += " " + m.th.G.Dot + " " + err.Error()
		}
		m.wave.SetEnergy(0.05) // the meter fizzles to its ember baseline
		return m, nil
	}
	m.restarts = append(m.restarts, m.now)
	m.note = "player crashed " + m.th.G.Dot + " restarting"
	return m, m.restartCmd(m.ph != phPaused)
}

// staticActive reports whether the tuning ceremony's static collapse is
// playing in the wave bay: a short, strictly bounded window from tune
// start that always ends at stream lock (or when the window elapses, so
// the failure path fizzles into the baseline instead of freezing).
func (m Model) staticActive() bool {
	if m.reduceMotion || (m.ph != phTune && m.ph != phBuffer) {
		return false
	}
	return m.now.Sub(m.tuneAt) < staticDur
}

// pause holds the output without unloading the stream and banks the listen
// segment that just ended — paused wall time never counts as listening.
// external means someone else drove mpv's pause property (media keys, the
// OS flyout, MPRIS): mpv is already paused, so nothing is sent back.
func (m Model) pause(external bool) (Model, tea.Cmd) {
	if !m.haveSt || (m.ph != phPlay && m.ph != phBuffer && m.ph != phTune && m.ph != phOffline) {
		return m, nil
	}
	m.core.EndListen(time.Now())
	if m.ph == phOffline {
		// Pausing an outage means "stop retrying": nothing is loaded, so
		// the resume has to reload — and a tune that still fails strikes
		// and re-enters offline like any other failure.
		m.pauseReload = true
		m.retryN = 0
		m.retryAt = time.Time{}
	}
	m.ph = phPaused
	m.pausedAt = m.now
	m.reconnecting = false
	m.resuming = false
	m.bufferAt = time.Time{}
	m.playStart = time.Time{} // the readout clock restarts with the next segment
	m.wave.SetEnergy(0.05)
	if external {
		m.setFeedback("PAUSED  From media controls")
		return m, nil
	}
	m.setFeedback("PAUSED  P resumes")
	pl := m.pl
	return m, func() tea.Msg { return pauseMsg{err: pl.SetPaused(true)} }
}

// resume comes back from a pause. A short pause that kept the stream
// loaded unpauses in place and picks up from mpv's buffer; anything longer
// — or a pause that lost the stream underneath — reloads to rejoin live.
// Either way the listen reopens as a continuation: ResumeListen keeps the
// session's dwell and the track on screen.
func (m Model) resume(external bool) (Model, tea.Cmd) {
	if m.ph != phPaused {
		return m, nil
	}
	inPlace := m.now.Sub(m.pausedAt) < resumeInPlace && !m.pauseReload
	m.ph = phTune
	m.tuneAt = m.now
	m.resuming = true
	m.note = ""
	m.feedback = "" // the PAUSED hint dies with the paused face
	m.pauseReload = false
	m.wave.SetEnergy(0.05)
	switch {
	case !inPlace:
		pl, c, st := m.pl, m.core, m.st
		return m, func() tea.Msg {
			if err := pl.Play(st.StreamURL()); err != nil {
				return playFailedMsg{uuid: st.UUID, err: err}
			}
			c.ResumeListen(st.UUID, time.Now())
			return nil
		}
	case external:
		// Released the same way it was held: mpv is already playing
		// again, only the listen needs reopening.
		m.core.ResumeListen(m.st.UUID, time.Now())
		return m, nil
	default:
		pl, c, uuid := m.pl, m.core, m.st.UUID
		return m, func() tea.Msg {
			if err := pl.SetPaused(false); err != nil {
				return pauseMsg{err: err}
			}
			c.ResumeListen(uuid, time.Now())
			return nil
		}
	}
}
