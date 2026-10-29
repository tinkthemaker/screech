package tui

import (
	"errors"
	"hash/fnv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/session"
)

const (
	fps          = 20
	frameDur     = time.Second / fps
	bootDur      = 600 * time.Millisecond
	idleAfter    = 3 * time.Minute
	maxQuery     = 40
	libraryLimit = 500

	// settleAfter/settleFade drive the idle settle: five untouched minutes
	// and the chrome eases to an ember floor over half a minute. The signal
	// instruments (wave, dial marker, LIVE) never settle — the radio is
	// still on, only the room lights dim.
	settleAfter = 5 * time.Minute
	settleFade  = 30 * time.Second

	// staticDur is the wave bay's static collapse at the start of a retune:
	// long enough to read as a tuner between stations, short enough that
	// the happy path resolves well under a second.
	staticDur = 400 * time.Millisecond

	// volumeLinger keeps the volume strip up past the last adjustment:
	// long enough to glance at the number, short enough that a stale
	// overlay doesn't park on screen forever.
	volumeLinger = 4 * time.Second
)

type historyView int

const (
	historyRecent historyView = iota
	historyLoved
	historyStations
	historyTaste
)

type Options struct {
	Accent        string
	ASCII         bool
	ReducedMotion bool
	// Session carries the listening engine's own options — auto-hop,
	// sync limit, the startup notice. New fills in its Dot from the
	// resolved theme, so ASCII mode never leaks a multi-byte glyph.
	Session session.Options
}

type Model struct {
	// s is the shared listening engine: phase, station, connectivity,
	// sleep, presets, notice/note/feedback. The face reads it to draw and
	// feeds it intents; it never writes a second state machine here.
	s session.State

	core *core.Core
	th   Theme

	themeName     string
	themeOriginal string
	themeAccent   string
	themeASCII    bool
	themeOpen     bool
	themePos      int

	w, h    int
	start   time.Time
	now     time.Time
	lastKey time.Time
	lastAct time.Time // any sign of life: keys, overlays, tunes, player state
	daypart string    // core daypart the current theme is tempered for

	decrypt   Decrypt
	stDecrypt Decrypt // receiver BROADCAST row resolves with the lock
	tw        Typewriter
	wave      *Wave
	dial      *Spring
	dialTgt   float64

	track        string
	trackArtist  string // m.track is artist+title joined; the receiver needs them apart
	trackAt      time.Time
	loveAt       time.Time
	reduceMotion bool
	stereo       bool // the backend is metering two distinct channels

	// Panel readouts. Both are fetched off the UI thread on change and
	// cached here; a faceplate that blocked its own render on a SQL count
	// would be a poor trade for a number in the corner.
	stTotal    time.Duration // cumulative listen time on the current station
	trackKey   string        // artist key the play count belongs to
	trackTitle string        // title the play count belongs to
	trackPlays int

	// listenTotals feeds the dial's density strip; density is it bucketed
	// to the current bay width. Rebuilt on resize and whenever the totals
	// change, never in the render path, which has no way to cache.
	listenTotals map[string]time.Duration
	density      []float64

	prompt       bool
	promptMode   promptKind // which search the open prompt sends: FIND or RADIO
	buf          []rune
	finds        []core.DiscoveryRow // "pick up where you left off", read when / opens
	findPos      int                 // selected row of finds
	history      bool
	hist         []core.HistEntry
	recent       []core.RecentTrack
	recentTop    int // scroll offset — RECENT has no cursor, the window moves
	historyView  historyView
	library      []core.LovedTrack
	libraryQuery []rune
	libraryFind  bool
	libraryPos   int
	saved        []core.SavedStation
	savedQuery   []rune
	savedFind    bool
	savedPos     int
	taste        core.Taste
	forgetKey    string
	forgetAt     time.Time
	volumeOpen   bool
	volumeAt     time.Time // last volume touch; the overlay lingers briefly
	helpOpen     bool
	infoOpen     bool
	info         core.StationInfo
	infoFor      string // uuid the info sheet belongs to; it follows the radio
	infoErr      error  // last StationInfo failure — personal rows read unavailable
}

type (
	tickMsg time.Time
	// sessMsg carries a session message back into Update: every command
	// the engine returns produces one, and they re-enter through step.
	sessMsg   struct{ m session.Msg }
	hangupMsg struct{}
	// Panel readouts arrive asynchronously and are matched against what's
	// playing now: a slow query returning after the user has already
	// skipped must not stamp the previous station's figures on the new one.
	stationStatsMsg struct {
		uuid  string
		total time.Duration
	}
	trackStatsMsg struct {
		key, title string
		plays      int
	}
	stationInfoMsg struct {
		uuid string
		info core.StationInfo
		err  error
	}
	dialDensityMsg struct{ totals map[string]time.Duration }
)

func New(c *core.Core, pl player.Player, opts Options) Model {
	now := time.Now()
	themeName := normalizeThemeName(c.Theme())
	dp := core.DaypartFor(now)
	th := NewDaypartTheme(themeName, opts.Accent, opts.ASCII, dp)
	// The engine writes its own notes and feedback; hand it the theme's
	// separator so the strings match the face's glyph set.
	opts.Session.Dot = th.G.Dot
	return Model{
		s:            session.New(c, pl, opts.Session),
		core:         c,
		th:           th,
		themeName:    themeName,
		themeAccent:  opts.Accent,
		themeASCII:   opts.ASCII,
		start:        now,
		now:          now,
		lastKey:      now,
		lastAct:      now,
		daypart:      dp,
		reduceMotion: opts.ReducedMotion,
		wave:         NewWave(32),
		dial:         NewSpring(0.5),
		dialTgt:      0.5,
	}
}

func Run(c *core.Core, pl player.Player, opts Options) error {
	p := tea.NewProgram(New(c, pl, opts), tea.WithAltScreen())
	stop := notifyHangup(p)
	_, err := p.Run()
	stop()
	// An external SIGINT is a clean exit, not a UI failure to report as
	// "screech: ui: program was interrupted".
	if errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick()}
	for _, c := range m.s.Init() {
		cmds = append(cmds, sessCmd(c))
	}
	cmds = append(cmds, m.dialDensityCmd())
	return tea.Batch(cmds...)
}

// sessCmd adapts a session command to a Bubble Tea command: whatever the
// engine produces rides back in as a sessMsg and re-enters through step.
func sessCmd(c session.Cmd) tea.Cmd {
	return func() tea.Msg {
		if msg := c(); msg != nil {
			return sessMsg{m: msg}
		}
		return nil
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(frameDur, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) stationStatsCmd(uuid string) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		total, err := c.StationTotal(uuid)
		if err != nil {
			return nil
		}
		return stationStatsMsg{uuid: uuid, total: total}
	}
}

// stationInfoCmd reads the station sheet's numbers off the UI goroutine;
// the msg is dropped if the radio has since moved on.
func (m Model) stationInfoCmd(uuid string) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		info, err := c.StationInfo(uuid, time.Now())
		return stationInfoMsg{uuid: uuid, info: info, err: err}
	}
}

func (m Model) dialDensityCmd() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		totals, err := c.ListenTotals()
		if err != nil {
			return nil
		}
		return dialDensityMsg{totals: totals}
	}
}

// rebuildDensity re-buckets the listen totals for the current bay width.
func (m *Model) rebuildDensity() {
	m.density = buildDialDensity(m.listenTotals, m.waveRenderWidth())
}

func (m Model) trackStatsCmd(key, title string) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		plays, err := c.TrackPlayCount(key, title)
		if err != nil {
			return nil
		}
		return trackStatsMsg{key: key, title: title, plays: plays}
	}
}

// step feeds one message through the shared listening engine, then does
// the face work the engine knows nothing about: the landing ceremony,
// wave energy, and the track readouts.
func (m Model) step(msg session.Msg) (Model, tea.Cmd) {
	before := m.s
	s, scmds := m.s.Update(msg)
	m.s = s
	var cmds []tea.Cmd
	for _, c := range scmds {
		cmds = append(cmds, sessCmd(c))
	}

	// Wave energy follows the engine's transitions, not just its phase:
	// a lock is a surge, buffering sags, anything else settles to embers.
	pev, isPev := msg.(session.PlayerEvent)
	switch {
	case isPev && player.Event(pev).Type == player.EventPlaying && before.Phase != session.Paused:
		m.wave.SetEnergy(1)
	case isPev && player.Event(pev).Type == player.EventBuffering && before.Phase != session.Paused:
		m.wave.SetEnergy(0.2)
	case m.s.PickSeq != before.PickSeq ||
		(m.s.Phase != before.Phase &&
			(m.s.Phase == session.Tuning || m.s.Phase == session.Paused || m.s.Phase == session.Stopped)):
		m.wave.SetEnergy(0.05)
	}

	if isPev {
		if player.Event(pev).Type == player.EventLevel {
			l := m.s.Level
			m.wave.SetStereoLevels(l.L, l.R, l.Peak, m.now.Sub(m.start).Seconds())
			m.stereo = l.Stereo
		} else {
			// Player state changes are signs of life and restore a
			// settled room; the per-frame audio level is not.
			m.lastAct = m.now
		}
	}

	// The landing ceremony replays on every pick, re-picks included:
	// decrypt, dial sweep, typewriter reason, fresh readouts.
	if m.s.PickSeq != before.PickSeq {
		m.history = false
		m.lastAct = m.now
		if m.motion() {
			// Decrypts hold only the clock; the views pass the settled
			// text at render time so the ceremony resolves into exactly
			// what's drawn.
			m.decrypt = NewDecrypt(m.now)
			m.stDecrypt = NewDecrypt(m.now)
		}
		m.tw = NewTypewriter(m.s.Reason, m.now.Add(300*time.Millisecond))
		m.track, m.trackArtist = "", ""
		m.dialTgt = stationDialPos(m.s.Station.UUID)
		// Readouts belong to the outgoing station; clear them so the new
		// one never briefly wears the old one's numbers.
		m.stTotal = 0
		m.trackKey, m.trackTitle, m.trackPlays = "", "", 0
		// Tags differ per station, which can flip the receiver's GENRE
		// block and its row accounting; relayout before the density cmd
		// lands.
		m.relayout()
		// The outgoing listen has just been banked, so the distribution
		// moved.
		cmds = append(cmds, m.stationStatsCmd(m.s.Station.UUID), m.dialDensityCmd())
		if m.infoOpen {
			// The info sheet follows the radio: re-request for the new
			// station and show it loading in the meantime.
			m.infoFor = m.s.Station.UUID
			m.infoErr = nil
			m.info = core.StationInfo{}
			cmds = append(cmds, m.stationInfoCmd(m.s.Station.UUID))
		}
	}

	// Track display: the parsed parts live in the session; the joined
	// readout string and the play-count cache are the face's.
	if m.s.HaveTrack && (m.s.Artist != before.Artist || m.s.Title != before.Title) {
		m.setTrack(m.s.Artist, m.s.Title)
		m.trackAt = m.now
		// A new title invalidates the play count. Zero it now and let
		// the query land, rather than showing the last track's.
		m.trackKey, m.trackTitle, m.trackPlays = m.s.TrackKey, m.s.Title, 0
		cmds = append(cmds, m.trackStatsCmd(m.trackKey, m.trackTitle))
	}
	if before.HaveTrack && !m.s.HaveTrack {
		m.track, m.trackArtist = "", ""
		m.trackKey, m.trackTitle, m.trackPlays = "", "", 0
	}
	return m, tea.Batch(cmds...)
}

// intent sends listener intents through the engine in order; the face's
// own side effects (love pulse, overlay state) stay in the key handlers.
func (m Model) intent(msgs ...session.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	for _, msg := range msgs {
		nm, c := m.step(msg)
		m = nm
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

// quit ends the session — the engine banks the listen and closes the
// backend — then leaves the program.
func (m Model) quit() (tea.Model, tea.Cmd) {
	m, cmd := m.step(session.Quit{})
	return m, tea.Batch(cmd, tea.Quit)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.relayout()
		return m, nil

	case tea.KeyMsg:
		m.lastKey = m.now
		m.lastAct = m.now
		// The startup notice shows until the room proves it's alive —
		// the first keypress acknowledges it.
		m, _ = m.step(session.Touch{})
		// Boot is a heartbeat, not a gate: any key ends it early and still
		// lands — the interface never swallows input.
		if m.now.Sub(m.start) < bootDur {
			m.start = m.now.Add(-bootDur)
		}
		// The help and info sheets take keys first: their own keys close
		// them, anything else closes and falls through to whatever is
		// underneath — the same pass-through contract as the volume strip.
		if m.helpOpen {
			m.helpOpen = false
			switch key := msg.String(); key {
			case "?", "esc", "enter":
				return m, nil
			case "q", "ctrl+c":
				return m.quit()
			}
			if m.history {
				return m.handleHistoryKey(msg)
			}
			return m.handleKey(msg)
		}
		if m.infoOpen {
			m.infoOpen = false
			switch key := msg.String(); key {
			case "i", "I", "esc", "enter":
				return m, nil
			case "q", "ctrl+c":
				return m.quit()
			}
			if m.history {
				return m.handleHistoryKey(msg)
			}
			return m.handleKey(msg)
		}
		if m.themeOpen {
			return m.handleThemeKey(msg)
		}
		if m.prompt {
			return m.handlePromptKey(msg)
		}
		if m.history {
			return m.handleHistoryKey(msg)
		}
		if m.volumeOpen {
			return m.handleVolumeKey(msg)
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
		if m.reduceMotion {
			// No springing under reduced motion: the needle snaps to
			// its station like a mechanical dial finding the mark.
			m.dial.Pos, m.dial.Vel = m.dialTgt, 0
		} else {
			m.dial.Step(m.dialTgt, dt)
		}
		// The volume strip isn't modal: it lingers a few seconds after
		// the last touch, then folds itself away.
		if m.volumeOpen && m.now.Sub(m.volumeAt) > volumeLinger {
			m.volumeOpen = false
		}
		// Daypart turns retemper the theme (brighter midday, ember night).
		// Never while the picker is open: a preview in progress owns th.
		if dp := core.DaypartFor(m.now); dp != m.daypart && !m.themeOpen {
			m.daypart = dp
			m.previewTheme(m.themeName)
		}
		// The frame clock also drives the engine: tune stalls, stream
		// drops, offline retries and the sleep countdown live in session.
		m, cmd := m.step(session.Tick{Now: m.now})
		return m, tea.Batch(m.tick(), cmd)

	case sessMsg:
		return m.step(msg.m)

	case stationStatsMsg:
		if m.s.HaveStation && msg.uuid == m.s.Station.UUID {
			m.stTotal = msg.total
		}
		return m, nil

	case stationInfoMsg:
		if !m.infoOpen || msg.uuid != m.infoFor {
			return m, nil
		}
		m.info, m.infoErr = msg.info, msg.err
		return m, nil

	case trackStatsMsg:
		if msg.key == m.trackKey && msg.title == m.trackTitle {
			m.trackPlays = msg.plays
		}
		return m, nil

	case dialDensityMsg:
		m.listenTotals = msg.totals
		m.relayout()
		return m, nil

	case hangupMsg:
		return m.quit()
	}
	return m, nil
}

// setTrack records a heard track: the joined display string for the
// stacked layout, the split parts for the receiver's title/artist rows.
func (m *Model) setTrack(artist, title string) {
	m.track = title
	if artist != "" {
		m.track = artist + " " + m.th.G.Dot + " " + title
	}
	m.trackArtist = artist
	m.trackTitle = title
}

// relayout resizes the instruments that depend on terminal size and panel
// shape: wave width and depth, then the density strip re-bucketed to fit.
func (m *Model) relayout() {
	m.wave.Resize(m.waveRenderWidth())
	m.wave.SetRows(m.waveRenderRows())
	m.rebuildDensity()
}

func (m Model) idle() bool {
	return m.s.Phase == session.Playing && !m.prompt && m.now.Sub(m.lastKey) > idleAfter
}

// staticActive reports whether the tuning ceremony's static collapse is
// playing in the wave bay: a short, strictly bounded window from tune
// start that always ends at stream lock (or when the window elapses, so
// the failure path fizzles into the baseline instead of freezing).
func (m Model) staticActive() bool {
	if m.reduceMotion || (m.s.Phase != session.Tuning && m.s.Phase != session.Buffering) {
		return false
	}
	return m.now.Sub(m.s.TuneAt) < staticDur
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

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
