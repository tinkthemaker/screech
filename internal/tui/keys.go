package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit

	case " ", "n":
		if m.ph == phDead {
			return m, nil
		}
		if m.ph == phOffline {
			// Offline SPACE is "tries now", not skip: fire the pending
			// retry early rather than tuning away from the outage.
			return m.offlineRetry()
		}
		fb := "NEXT  Finding another station"
		switch {
		case m.suspect:
			fb = "LEARNED  Break skip protected"
		case !m.playStart.IsZero() && m.now.Sub(m.playStart) >= core.FastSkipWindow:
			fb = "LEARNED  Long listen saved"
		case !m.playStart.IsZero():
			fb = "LEARNED  Fast skip noted"
		}
		// beginSkip mutates m: call it before the return list, whose
		// operand order is unspecified.
		cmd := m.beginSkip(fb)
		return m, cmd

	case "l":
		if !m.haveSt || m.ph == phDead {
			return m, nil
		}
		_, track, lovedNow, err := m.core.Love(time.Now())
		if err != nil {
			m.note = "could not save love " + m.th.G.Dot + " " + err.Error()
			return m, nil
		}
		m.lovedTrack = lovedNow
		if lovedNow {
			if !m.reduceMotion {
				m.loveAt = m.now
			}
			if track {
				m.setFeedback("LOVED  Track and station")
			} else {
				m.setFeedback("LOVED  Station")
			}
		} else {
			m.setFeedback("UNLOVED  Love returned")
		}
		return m, nil

	case "f":
		if !m.haveSt || m.ph == phDead {
			return m, nil
		}
		slot, saved, full, err := m.core.TogglePreset(time.Now())
		if err != nil {
			m.note = "could not save preset " + m.th.G.Dot + " " + err.Error()
			return m, nil
		}
		m.presets = m.core.Presets()
		// Feedback only — the WHY row still describes the pick.
		switch {
		case full:
			m.setFeedback("PRESETS  All nine full " + m.th.G.Dot + " F on a saved one frees a slot")
		case saved:
			m.setFeedback(fmt.Sprintf("SAVED  Preset %d "+m.th.G.Dot+" press %d to return", slot, slot))
		case slot > 0:
			m.setFeedback(fmt.Sprintf("CLEARED  Preset %d", slot))
		}
		return m, nil

	case "/":
		if m.ph == phDead {
			return m, nil
		}
		m.prompt = true
		m.buf = nil
		return m, nil

	case "?":
		m.helpOpen = true
		return m, nil

	case "i", "I":
		if !m.haveSt {
			m.setFeedback("INFO  Nothing tuned yet")
			return m, nil
		}
		m.infoOpen = true
		m.infoFor = m.st.UUID
		m.infoErr = nil
		m.info = core.StationInfo{}
		return m, m.stationInfoCmd(m.st.UUID)

	case "p", "P":
		// One pause state — mpv's own — whether it was p, a media key,
		// a headset button or the OS flyout that held it.
		if m.ph == phPaused {
			return m.resume(false)
		}
		return m.pause(false)

	case "z", "Z":
		step := m.sleepStep + 1
		if step >= len(sleepSteps) {
			m.sleepStep, m.sleepAt = -1, time.Time{}
			m.setFeedback("SLEEP  Off")
		} else {
			m.sleepStep, m.sleepAt = step, m.now.Add(sleepSteps[step])
			m.setFeedback(fmt.Sprintf("SLEEP  %d min", int(sleepSteps[step].Minutes())))
		}
		if m.sleepVol >= 0 {
			// A fade was underway: the listener's volume comes back now,
			// not at the next tick.
			pl, v := m.pl, m.volume
			m.sleepVol = -1
			return m, func() tea.Msg { return volumeMsg{err: pl.SetVolume(v)} }
		}
		return m, nil

	case "v":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m, nil

	case "-", "[":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m.adjustVolume(-5)

	case "+", "=", "]":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m.adjustVolume(5)

	case "h":
		if !m.loadHistory(historyRecent) {
			m.note = "could not open history"
		}
		return m, nil

	case "H":
		if !m.loadHistory(historyLoved) {
			m.note = "could not open library"
		}
		return m, nil

	case "t", "T":
		m.themeOpen = true
		m.themeOriginal = m.themeName
		m.themePos = themeChoiceIndex(m.themeName)
		return m, nil
	}

	// Digits 1-9: preset recall, deterministic.
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		slot := int(key[0] - '0')
		uuid, ok := m.presets[slot]
		if !ok {
			m.setFeedback(fmt.Sprintf("PRESET  %d is empty "+m.th.G.Dot+" F saves", slot))
			return m, nil
		}
		if m.haveSt && uuid == m.st.UUID {
			return m, nil // already playing it
		}
		pick, err := m.core.TuneTo(uuid, time.Now())
		if err != nil {
			m.note = err.Error()
			return m, nil
		}
		pick.Reason = fmt.Sprintf("preset %d", slot)
		return m.applyPick(pick)
	}
	return m, nil
}

func (m Model) handleVolumeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case "esc", "enter", "v":
		m.volumeOpen = false
		return m, nil
	case "left", "down", "-", "[":
		m.volumeAt = m.now
		return m.adjustVolume(-5)
	case "right", "up", "+", "=", "]":
		m.volumeAt = m.now
		return m.adjustVolume(5)
	}
	// Not a volume key: the strip isn't a mode, it's a readout. Fold it
	// away and let the key do what it always does.
	m.volumeOpen = false
	return m.handleKey(msg)
}

func (m Model) adjustVolume(delta int) (tea.Model, tea.Cmd) {
	m.volume += delta
	if m.volume < 0 {
		m.volume = 0
	}
	if m.volume > 100 {
		m.volume = 100
	}
	if err := m.core.SetVolume(m.volume); err != nil {
		m.note = "could not save volume " + m.th.G.Dot + " " + err.Error()
	}
	return m, m.volumeCmd()
}

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit

	case tea.KeyEsc:
		m.prompt = false
		m.buf = nil
		return m, nil

	case tea.KeyEnter:
		q := strings.TrimSpace(string(m.buf))
		m.prompt = false
		m.buf = nil
		if q == "" {
			return m, nil
		}
		m.seeking = true
		m.note = ""
		return m, m.seedCmd(q)

	case tea.KeyBackspace:
		if len(m.buf) > 0 {
			m.buf = m.buf[:len(m.buf)-1]
		}
		return m, nil

	case tea.KeySpace:
		// Virgin shortcut: empty prompt + space = wildcard tune.
		if m.virgin && !m.haveSt && len(m.buf) == 0 {
			m.prompt = false
			m.ph = phTune
			m.tuneAt = m.now
			return m, m.tuneCmd()
		}
		if len(m.buf) < maxQuery {
			m.buf = append(m.buf, ' ')
		}
		return m, nil

	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if len(m.buf) >= maxQuery {
				break
			}
			m.buf = append(m.buf, r)
		}
		return m, nil
	}
	return m, nil
}

// beginSkip is what SPACE does once its guards pass: tune state, feedback,
// calm the wave, and the tune command. Auto-hop runs the same path — a
// hopped break is a skip and gets a skip's discounted penalty.
func (m *Model) beginSkip(feedback string) tea.Cmd {
	m.ph = phTune
	m.tuneAt = m.now
	m.note = ""
	m.setFeedback(feedback)
	m.wave.SetEnergy(0.05)
	return m.tuneCmd()
}
