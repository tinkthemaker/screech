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

	case player.EventPlaying:
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
		case phPlay, phBuffer:
			nm, cmd := m.dropStream(reason)
			return nm, tea.Batch(m.listen(), cmd)
		case phTune:
			nm, cmd := m.failTune(reason)
			return nm, tea.Batch(m.listen(), cmd)
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
	return m, m.restartCmd()
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
