package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		return m.quit()

	case " ", "n":
		// Offline SPACE is "tries now", not skip; dead radio ignores it.
		// Both guards live in the engine's Next.
		return m.intent(session.Next{})

	case "l":
		wasLoved := m.s.LovedTrack
		m, cmd := m.step(session.Love{})
		// The pulse is a face effect: it fires only when this press
		// actually loved, and never under reduced motion.
		if !wasLoved && m.s.LovedTrack && !m.reduceMotion {
			m.loveAt = m.now
		}
		return m, cmd

	case "f":
		return m.intent(session.TogglePreset{})

	case "/":
		if m.s.Phase == session.Stopped {
			return m, nil
		}
		m.prompt = true
		m.buf = nil
		return m, nil

	case "?":
		m.helpOpen = true
		return m, nil

	case "i", "I":
		if !m.s.HaveStation {
			return m.intent(session.Notify{Feedback: "INFO  Nothing tuned yet"})
		}
		m.infoOpen = true
		m.infoFor = m.s.Station.UUID
		m.infoErr = nil
		m.info = core.StationInfo{}
		return m, m.stationInfoCmd(m.s.Station.UUID)

	case "p", "P":
		// One pause state — mpv's own — whether it was p, a media key,
		// a headset button or the OS flyout that held it.
		return m.intent(session.TogglePause{})

	case "z", "Z":
		return m.intent(session.CycleSleep{})

	case "v":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m, nil

	case "-", "[":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m.intent(session.StepVolume{Delta: -5})

	case "+", "=", "]":
		m.volumeOpen = true
		m.volumeAt = m.now
		return m.intent(session.StepVolume{Delta: 5})

	case "h":
		if !m.loadHistory(historyRecent) {
			return m.intent(session.Notify{Note: "could not open history"})
		}
		return m, nil

	case "H":
		if !m.loadHistory(historyLoved) {
			return m.intent(session.Notify{Note: "could not open library"})
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
		return m.intent(session.RecallPreset{Slot: int(key[0] - '0')})
	}
	return m, nil
}

func (m Model) handleVolumeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "esc", "enter", "v":
		m.volumeOpen = false
		return m, nil
	case "left", "down", "-", "[":
		m.volumeAt = m.now
		return m.intent(session.StepVolume{Delta: -5})
	case "right", "up", "+", "=", "]":
		m.volumeAt = m.now
		return m.intent(session.StepVolume{Delta: 5})
	}
	// Not a volume key: the strip isn't a mode, it's a readout. Fold it
	// away and let the key do what it always does.
	m.volumeOpen = false
	return m.handleKey(msg)
}

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.quit()

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
		return m.intent(session.Seed{Query: q})

	case tea.KeyBackspace:
		if len(m.buf) > 0 {
			m.buf = m.buf[:len(m.buf)-1]
		}
		return m, nil

	case tea.KeySpace:
		// Virgin shortcut: empty prompt + space = wildcard tune.
		if m.s.Virgin && !m.s.HaveStation && len(m.buf) == 0 {
			m.prompt = false
			return m.intent(session.Wildcard{})
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
