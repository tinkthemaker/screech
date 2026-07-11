package tui

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"screech/internal/core"
	"screech/internal/player"
)

const (
	fps       = 20
	frameDur  = time.Second / fps
	bootDur   = 900 * time.Millisecond
	idleAfter = 3 * time.Minute
	tuneStall = 12 * time.Second
	syncLimit = 4000
	maxQuery  = 40
)

type phase int

const (
	phIdleStart phase = iota // nothing tuned yet
	phTune                   // waiting for the stream to lock
	phPlay
	phBuffer
	phDead
)

type Options struct {
	Accent string
	ASCII  bool
}

type Model struct {
	core *core.Core
	pl   player.Player
	th   Theme

	w, h    int
	start   time.Time
	now     time.Time
	lastKey time.Time

	ph      phase
	syncing bool
	virgin  bool

	st     core.Station
	haveSt bool
	reason string

	decrypt Decrypt
	tw      Typewriter
	wave    *Wave
	dial    *Spring
	dialTgt float64

	track      string
	haveTrack  bool
	trackAt    time.Time
	lovedTrack bool
	loveAt     time.Time
	suspect    bool

	presets map[int]string
	prompt  bool
	buf     []rune
	seeking bool

	tuneAt    time.Time
	playStart time.Time
	note      string
	fatal     string
}

type (
	tickMsg     time.Time
	evMsg       player.Event
	evClosedMsg struct{}
	tunedMsg    struct {
		pick core.Pick
		err  error
	}
	syncedMsg struct {
		n   int
		err error
	}
	seededMsg struct {
		res core.SeedResult
		ok  bool
	}
)

func New(c *core.Core, pl player.Player, opts Options) Model {
	now := time.Now()
	syncing := c.NeedsSync()
	virgin := c.IsVirgin()
	return Model{
		core:    c,
		pl:      pl,
		th:      NewTheme(opts.Accent, opts.ASCII),
		start:   now,
		now:     now,
		lastKey: now,
		wave:    NewWave(56),
		dial:    NewSpring(0.5),
		dialTgt: 0.5,
		syncing: syncing,
		virgin:  virgin,
		prompt:  virgin && !syncing,
		presets: c.Presets(),
	}
}

func Run(c *core.Core, pl player.Player, opts Options) error {
	_, err := tea.NewProgram(New(c, pl, opts), tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick(), m.listen()}
	switch {
	case m.syncing:
		cmds = append(cmds, m.syncCmd())
	case m.virgin:
		// First run: open on the seed prompt instead of a wildcard drop.
	default:
		cmds = append(cmds, m.resumeCmd())
	}
	return tea.Batch(cmds...)
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(frameDur, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) listen() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.pl.Events()
		if !ok {
			return evClosedMsg{}
		}
		return evMsg(ev)
	}
}

func (m Model) syncCmd() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		n, err := c.Sync(ctx, syncLimit)
		return syncedMsg{n: n, err: err}
	}
}

func (m Model) resumeCmd() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		if uuid := c.ResumeUUID(); uuid != "" {
			if st, ok := c.StationByUUID(uuid); ok && st.FailCount == 0 {
				return tunedMsg{pick: core.Pick{Station: st, Reason: "where you left off"}}
			}
		}
		pick, err := c.Tune(time.Now())
		return tunedMsg{pick: pick, err: err}
	}
}

func (m Model) tuneCmd() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		pick, err := c.Tune(time.Now())
		return tunedMsg{pick: pick, err: err}
	}
}

func (m Model) seedCmd(q string) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, ok := c.Seed(ctx, q, time.Now())
		return seededMsg{res: res, ok: ok}
	}
}

func (m Model) playCmd() tea.Cmd {
	pl, c := m.pl, m.core
	st := m.st
	return func() tea.Msg {
		if err := pl.Play(st.StreamURL()); err != nil {
			return evMsg(player.Event{Type: player.EventStreamError, Err: err})
		}
		c.StartListen(st.UUID, time.Now())
		return nil
	}
}

// applyPick is the shared transition into a new station: decrypt, dial sweep,
// typewriter reason, then play.
func (m Model) applyPick(pick core.Pick) (Model, tea.Cmd) {
	m.st = pick.Station
	m.haveSt = true
	m.virgin = false
	m.reason = pick.Reason
	m.ph = phTune
	m.tuneAt = m.now
	m.decrypt = NewDecrypt(strings.ToUpper(cleanStationName(m.st.Name)), m.now)
	m.tw = NewTypewriter(m.reason, m.now.Add(300*time.Millisecond))
	m.track = ""
	m.haveTrack = false
	m.lovedTrack = false
	m.suspect = false
	m.playStart = time.Time{}
	m.dialTgt = stationDialPos(m.st.UUID)
	m.wave.SetEnergy(0.05)
	return m, m.playCmd()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.wave.Resize(m.innerWidth())
		return m, nil

	case tea.KeyMsg:
		m.lastKey = m.now
		if m.prompt {
			return m.handlePromptKey(msg)
		}
		return m.handleKey(msg)

	case tickMsg:
		prev := m.now
		m.now = time.Time(msg)
		dt := m.now.Sub(prev).Seconds()
		if dt <= 0 || dt > 0.25 {
			dt = 1.0 / fps
		}
		t := m.now.Sub(m.start).Seconds()
		m.wave.Step(t, dt)
		m.dial.Step(m.dialTgt, dt)
		// A station that never locks is a corpse: mark it and move on.
		if m.ph == phTune && m.haveSt && m.now.Sub(m.tuneAt) > tuneStall {
			m.core.MarkStationFailed(m.st.UUID)
			m.note = "no signal " + m.th.G.Dot + " moving on"
			m.tuneAt = m.now
			return m, tea.Batch(m.tick(), m.tuneCmd())
		}
		return m, m.tick()

	case syncedMsg:
		m.syncing = false
		if msg.err != nil {
			m.note = "directory unreachable " + m.th.G.Dot + " running on seed stations"
		}
		if m.virgin {
			m.prompt = true
			return m, nil
		}
		return m, m.resumeCmd()

	case tunedMsg:
		if msg.err != nil {
			if !m.haveSt {
				m.fatal = msg.err.Error()
				m.ph = phDead
			} else {
				m.note = msg.err.Error()
			}
			return m, nil
		}
		return m.applyPick(msg.pick)

	case seededMsg:
		m.seeking = false
		if !msg.ok {
			m.note = "nothing in the aether for " + msg.res.Label
			return m, nil
		}
		return m.applyPick(msg.res.Pick)

	case evMsg:
		return m.handlePlayerEvent(player.Event(msg))

	case evClosedMsg:
		if m.ph != phDead {
			m.ph = phDead
			m.fatal = "player backend closed"
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "ctrl+c":
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit

	case " ", "n":
		if m.syncing || m.ph == phDead {
			return m, nil
		}
		m.ph = phTune
		m.tuneAt = m.now
		m.note = ""
		m.wave.SetEnergy(0.05)
		return m, m.tuneCmd()

	case "l":
		if !m.haveSt || m.ph == phDead {
			return m, nil
		}
		m.core.Love(time.Now())
		m.lovedTrack = true
		m.loveAt = m.now
		return m, nil

	case "f":
		if !m.haveSt || m.ph == phDead {
			return m, nil
		}
		slot, saved, full := m.core.TogglePreset(time.Now())
		m.presets = m.core.Presets()
		switch {
		case full:
			m.reason = "presets full " + m.th.G.Dot + " f on a saved station frees a slot"
		case saved:
			m.reason = fmt.Sprintf("saved "+m.th.G.Dot+" press %d to return", slot)
		case slot > 0:
			m.reason = fmt.Sprintf("preset %d cleared", slot)
		}
		m.tw = NewTypewriter(m.reason, m.now)
		return m, nil

	case "/":
		if m.ph == phDead {
			return m, nil
		}
		m.prompt = true
		m.buf = nil
		return m, nil
	}

	// Digits 1-9: preset recall, deterministic.
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		slot := int(key[0] - '0')
		uuid, ok := m.presets[slot]
		if !ok {
			m.note = fmt.Sprintf("preset %d is empty "+m.th.G.Dot+" f saves", slot)
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

func (m Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
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

func (m Model) handlePlayerEvent(ev player.Event) (tea.Model, tea.Cmd) {
	switch ev.Type {
	case player.EventTitle:
		tr, ok, suspect := m.core.NoteTitle(ev.Title, time.Now())
		m.suspect = suspect
		if ok {
			text := tr.Title
			if tr.Artist != "" {
				text = tr.Artist + " " + m.th.G.Dot + " " + tr.Title
			}
			if text != m.track {
				m.track = text
				m.trackAt = m.now
				m.lovedTrack = false
			}
			m.haveTrack = true
		} else {
			m.haveTrack = false
			m.track = ""
		}

	case player.EventPlaying:
		if m.ph == phTune {
			m.core.MarkStationHealthy(m.st.UUID)
			m.playStart = m.now
		}
		if m.ph != phDead {
			m.ph = phPlay
		}
		m.wave.SetEnergy(1)

	case player.EventBuffering:
		if m.ph == phPlay {
			m.ph = phBuffer
		}
		m.wave.SetEnergy(0.2)

	case player.EventLevel:
		m.wave.SetLevel(ev.Level, m.now.Sub(m.start).Seconds())

	case player.EventStreamError:
		if m.haveSt {
			m.core.MarkStationFailed(m.st.UUID)
		}
		m.note = "stream failed " + m.th.G.Dot + " moving on"
		return m, tea.Batch(m.listen(), m.tuneCmd())

	case player.EventDied:
		m.ph = phDead
		m.fatal = "mpv exited " + m.th.G.Dot + " restart screech"
		return m, nil
	}
	return m, m.listen()
}

func (m Model) innerWidth() int {
	iw := m.w - 4
	if iw > 64 {
		iw = 64
	}
	if iw < 16 {
		iw = maxI(m.w-2, 10)
	}
	return iw
}

func (m Model) idle() bool {
	return m.ph == phPlay && !m.prompt && m.now.Sub(m.lastKey) > idleAfter
}

// currentSlot returns the preset slot of the playing station, 0 if unsaved.
func (m Model) currentSlot() int {
	if !m.haveSt {
		return 0
	}
	for s, u := range m.presets {
		if u == m.st.UUID {
			return s
		}
	}
	return 0
}

// stationDialPos maps a station identity to a stable position on the band
// line, like a frequency on a dial.
func stationDialPos(uuid string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(uuid))
	return 0.08 + 0.84*float64(h.Sum32()%10000)/9999.0
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}
