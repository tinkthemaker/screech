package tui

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

const (
	fps         = 20
	frameDur    = time.Second / fps
	bootDur     = 600 * time.Millisecond
	idleAfter   = 3 * time.Minute
	tuneStall   = 12 * time.Second
	maxQuery    = 40
	feedbackDur = 2400 * time.Millisecond

	// settleAfter/settleFade drive the idle settle: five untouched minutes
	// and the chrome eases to an ember floor over half a minute. The signal
	// instruments (wave, dial marker, LIVE) never settle — the radio is
	// still on, only the room lights dim.
	settleAfter = 5 * time.Minute
	settleFade  = 30 * time.Second

	defaultSyncLimit = 20000
	libraryLimit     = 500

	// staticDur is the wave bay's static collapse at the start of a retune:
	// long enough to read as a tuner between stations, short enough that
	// the happy path resolves well under a second.
	staticDur = 400 * time.Millisecond

	// volumeLinger keeps the volume strip up past the last adjustment:
	// long enough to glance at the number, short enough that a stale
	// overlay doesn't park on screen forever.
	volumeLinger = 4 * time.Second

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
)

// retryBackoff spaces offline retries: 5s, 10s, 20s, … capped at a minute.
// n counts failures since the outage began.
func retryBackoff(n int) time.Duration {
	d := 5 * time.Second << n
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

type historyView int

const (
	historyRecent historyView = iota
	historyLoved
	historyStations
)

type phase int

const (
	phIdleStart phase = iota // nothing tuned yet
	phTune                   // waiting for the stream to lock
	phPlay
	phBuffer
	phOffline // every tune is failing — the network, not the stations
	phDead
)

type Options struct {
	Accent        string
	ASCII         bool
	SyncLimit     int
	ReducedMotion bool
	// Notice seeds the status line with something the user should see once
	// (e.g. a recovered database), without failing startup.
	Notice string
}

type Model struct {
	core *core.Core
	pl   player.Player
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

	ph        phase
	syncing   bool
	virgin    bool
	syncLimit int

	st     core.Station
	haveSt bool
	reason string

	decrypt   Decrypt
	stDecrypt Decrypt // receiver BROADCAST row resolves with the lock
	tw        Typewriter
	wave      *Wave
	dial      *Spring
	dialTgt   float64

	track        string
	trackArtist  string // m.track is artist+title joined; the receiver needs them apart
	haveTrack    bool
	trackAt      time.Time
	lovedTrack   bool
	loveAt       time.Time
	reduceMotion bool
	suspect      bool
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

	presets      map[int]string
	prompt       bool
	buf          []rune
	seeking      bool
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
	forgetKey    string
	forgetAt     time.Time
	volumeOpen   bool
	volume       int
	volumeAt     time.Time // last volume touch; the overlay lingers briefly

	tuneAt     time.Time
	playStart  time.Time
	notice     string // startup notice from Options; survives picks, dies on first key
	note       string
	feedback   string
	feedbackAt time.Time
	fatal      string
	quitting   bool // a quit is in flight; backend death is expected noise

	// Connectivity bookkeeping. retryN doubles as the offline flag:
	// nonzero means the current tune attempt came out of an offline retry
	// and its failure extends the backoff instead of striking the station.
	reconnecting bool
	drops        int
	lockAt       time.Time
	bufferAt     time.Time
	failStreak   []string
	retryN       int
	retryAt      time.Time
	lastGood     core.Station
	haveLastGood bool
	restarts     []time.Time
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
		n      int
		pruned int
		err    error
	}
	seededMsg struct {
		res core.SeedResult
		ok  bool
	}
	volumeMsg struct{ err error }
	// playFailedMsg reports a Play() that returned an error — the station's
	// own URL was rejected, which is a strike against the station, not a
	// network event. It deliberately isn't an evMsg: the listener goroutine
	// owns evMsg production, and a synthetic one would leave an extra
	// listen() armed per failure.
	playFailedMsg struct {
		uuid string
		err  error
	}
	playerRestartedMsg struct{ err error }
	hangupMsg          struct{}
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
	dialDensityMsg struct{ totals map[string]time.Duration }
)

func New(c *core.Core, pl player.Player, opts Options) Model {
	now := time.Now()
	syncing := c.NeedsSync()
	virgin := c.IsVirgin()
	sl := opts.SyncLimit
	if sl <= 0 {
		sl = defaultSyncLimit
	}
	themeName := normalizeThemeName(c.Theme())
	dp := core.DaypartFor(now)
	return Model{
		syncLimit:    sl,
		core:         c,
		pl:           pl,
		th:           NewDaypartTheme(themeName, opts.Accent, opts.ASCII, dp),
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
		syncing:      syncing,
		virgin:       virgin,
		prompt:       false,
		presets:      c.Presets(),
		volume:       c.Volume(),
		notice:       opts.Notice,
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
	cmds := []tea.Cmd{m.tick(), m.listen(), m.volumeCmd(), m.dialDensityCmd()}
	cmds = append(cmds, m.resumeCmd())
	if m.syncing || m.core.SyncStale(m.syncLimit) {
		// Weekly directory refresh, in the background. Playback never waits.
		cmds = append(cmds, m.syncCmd())
	}
	return tea.Batch(cmds...)
}

func (m Model) volumeCmd() tea.Cmd {
	pl, volume := m.pl, m.volume
	return func() tea.Msg {
		return volumeMsg{err: pl.SetVolume(volume)}
	}
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
	limit := m.syncLimit
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		n, pruned, err := c.Sync(ctx, limit)
		return syncedMsg{n: n, pruned: pruned, err: err}
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

// tuneDeadCmd moves on after a stream failure: no skip semantics, no taste
// signal. The failure already cost the station a strike.
func (m Model) tuneDeadCmd() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		pick, err := c.TuneDead(time.Now())
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

func (m Model) playCmd() tea.Cmd {
	pl, c := m.pl, m.core
	st := m.st
	return func() tea.Msg {
		if err := pl.Play(st.StreamURL()); err != nil {
			return playFailedMsg{uuid: st.UUID, err: err}
		}
		c.StartListen(st.UUID, time.Now())
		return nil
	}
}

// reconnectCmd re-points the player at the same stream after a drop. No
// StartListen: the listen in core stays open across the reconnect so the
// drop doesn't split the session or charge the station a skip.
func (m Model) reconnectCmd() tea.Cmd {
	pl, uuid, url := m.pl, m.st.UUID, m.st.StreamURL()
	return func() tea.Msg {
		if err := pl.Play(url); err != nil {
			return playFailedMsg{uuid: uuid, err: err}
		}
		return nil
	}
}

// restartCmd brings up a fresh backend after a crash, then re-points it at
// the current station so playback resumes without waiting for a retune.
func (m Model) restartCmd() tea.Cmd {
	pl, st, haveSt := m.pl, m.st, m.haveSt
	return func() tea.Msg {
		err := pl.Restart()
		if err == nil && haveSt {
			err = pl.Play(st.StreamURL())
		}
		return playerRestartedMsg{err: err}
	}
}

// applyPick is the shared transition into a new station: decrypt, dial sweep,
// typewriter reason, then play.
func (m Model) applyPick(pick core.Pick) (Model, tea.Cmd) {
	m.st = pick.Station
	m.haveSt = true
	m.virgin = false
	m.history = false
	m.note = ""
	m.reason = pick.Reason
	m.ph = phTune
	m.tuneAt = m.now
	m.lastAct = m.now
	if m.motion() {
		// Decrypts hold only the clock; the views pass the settled text at
		// render time so the ceremony resolves into exactly what's drawn.
		m.decrypt = NewDecrypt(m.now)
		m.stDecrypt = NewDecrypt(m.now)
	}
	m.tw = NewTypewriter(m.reason, m.now.Add(300*time.Millisecond))
	m.track, m.trackArtist = "", ""
	m.haveTrack = false
	m.lovedTrack = false
	m.suspect = false
	m.playStart = time.Time{}
	m.drops = 0
	m.reconnecting = false
	m.lockAt = time.Time{}
	m.bufferAt = time.Time{}
	// retryN deliberately survives: during an offline retry this pick IS
	// the retry, and retryN > 0 is how a failure finds its way back to the
	// backoff branch instead of striking the station.
	m.dialTgt = stationDialPos(m.st.UUID)
	m.wave.SetEnergy(0.05)
	// Readouts belong to the outgoing station; clear them so the new one
	// never briefly wears the old one's numbers.
	m.stTotal = 0
	m.trackKey, m.trackTitle, m.trackPlays = "", "", 0
	// Tags differ per station, which can flip the receiver's GENRE block
	// and its row accounting; relayout before the density cmd lands.
	m.relayout()
	// The outgoing listen has just been banked, so the distribution moved.
	return m, tea.Batch(m.playCmd(), m.stationStatsCmd(m.st.UUID), m.dialDensityCmd())
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
		m.notice = ""
		// Boot is a heartbeat, not a gate: any key ends it early and still
		// lands — the interface never swallows input.
		if m.now.Sub(m.start) < bootDur {
			m.start = m.now.Add(-bootDur)
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
		// Connectivity housekeeping lives on the frame clock: a tune that
		// never locks fails through the same path as a stream error, a
		// buffer that stalls for good counts as a drop, an offline retry
		// fires when its timer lapses, and a stable minute forgives drops.
		switch {
		case m.ph == phTune && m.haveSt && m.now.Sub(m.tuneAt) > tuneStall:
			nm, cmd := m.failTune("no signal")
			return nm, tea.Batch(m.tick(), cmd)
		case m.ph == phBuffer && !m.bufferAt.IsZero() && m.now.Sub(m.bufferAt) > bufferStall:
			nm, cmd := m.dropStream("signal stalled")
			return nm, tea.Batch(m.tick(), cmd)
		case m.ph == phOffline && !m.retryAt.IsZero() && !m.now.Before(m.retryAt):
			nm, cmd := m.offlineRetry()
			return nm, tea.Batch(m.tick(), cmd)
		}
		if m.ph == phPlay && m.drops > 0 && !m.lockAt.IsZero() && m.now.Sub(m.lockAt) >= stableAfter {
			m.drops = 0
		}
		return m, m.tick()

	case syncedMsg:
		m.syncing = false
		// Refresh landed while playing; absorb quietly.
		m.presets = m.core.Presets()
		if msg.err != nil {
			m.setFeedback("DIRECTORY  Refresh failed; using cache")
		} else {
			m.setFeedback(fmt.Sprintf("DIRECTORY  %d stations refreshed", msg.n))
		}
		return m, nil

	case tunedMsg:
		if msg.err != nil {
			switch {
			case !m.haveSt:
				m.fatal = msg.err.Error()
				m.ph = phDead
			case m.retryN > 0:
				// A pick that fails inside an offline retry reschedules
				// instead of stranding the model offline with no timer.
				m.ph = phOffline
				m.retryAt = m.now.Add(retryBackoff(m.retryN))
				m.retryN++
			default:
				m.note = msg.err.Error()
				// A skip that found nothing must not strand the still-
				// playing stream in phTune, where the stall watchdog
				// would strike a healthy station every 12s.
				if !m.playStart.IsZero() && !m.reconnecting {
					m.ph = phPlay
				}
			}
			return m, nil
		}
		return m.applyPick(msg.pick)

	case seededMsg:
		m.seeking = false
		if !msg.ok {
			m.note = "no matches for " + msg.res.Label
			return m, nil
		}
		return m.applyPick(msg.res.Pick)

	case volumeMsg:
		if msg.err != nil {
			m.note = "volume control failed " + m.th.G.Dot + " " + msg.err.Error()
		}
		return m, nil

	case stationStatsMsg:
		if m.haveSt && msg.uuid == m.st.UUID {
			m.stTotal = msg.total
		}
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

	case evMsg:
		return m.handlePlayerEvent(player.Event(msg))

	case playFailedMsg:
		// Play itself refused the URL — that is the station's fault, not
		// the network's: strike it and move on, but never touch the
		// offline bookkeeping. A stale failure (station already moved on)
		// is ignored. No listen() here: the event listener is already armed.
		if !m.haveSt || msg.uuid != m.st.UUID {
			return m, nil
		}
		m.core.MarkStationFailed(msg.uuid)
		m.note = "stream rejected " + m.th.G.Dot + " moving on"
		return m, m.tuneDeadCmd()

	case playerRestartedMsg:
		if msg.err != nil {
			m.ph = phDead
			m.fatal = "mpv exited " + m.th.G.Dot + " restart screech " + m.th.G.Dot + " " + msg.err.Error()
			m.wave.SetEnergy(0.05)
			return m, nil
		}
		if m.haveSt {
			m.ph = phTune
			m.tuneAt = m.now
			m.reconnecting = true
		}
		// Events() now yields the new process's channel; re-arm the
		// listener that died with the old one.
		return m, m.listen()

	case evClosedMsg:
		// The event channel closed: either EventDied was emitted (a full
		// buffer can drop it) or the backend went away silently. Both are
		// the same fact — treat it as a death.
		return m.backendDied(nil)

	case hangupMsg:
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	}
	return m, nil
}

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
		m.ph = phTune
		m.tuneAt = m.now
		m.note = ""
		switch {
		case m.suspect:
			m.setFeedback("LEARNED  Break skip protected")
		case !m.playStart.IsZero() && m.now.Sub(m.playStart) >= core.FastSkipWindow:
			m.setFeedback("LEARNED  Long listen saved")
		case !m.playStart.IsZero():
			m.setFeedback("LEARNED  Fast skip noted")
		default:
			m.setFeedback("NEXT  Finding another station")
		}
		m.wave.SetEnergy(0.05)
		return m, m.tuneCmd()

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

func themeChoiceIndex(name string) int {
	name = normalizeThemeName(name)
	for i, choice := range themeChoices {
		if choice.ID == name {
			return i
		}
	}
	return 0
}

func (m *Model) previewTheme(name string) {
	m.themeName = normalizeThemeName(name)
	// Previews render under the current daypart: what you see in the picker
	// is what the theme looks like right now, not an abstract noon version.
	m.th = NewDaypartTheme(m.themeName, m.themeAccent, m.themeASCII, m.daypart)
	m.wave.ResetPalette()
}

func (m Model) handleThemeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	choices := ThemeChoices()
	switch key {
	case "ctrl+c", "q":
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case "esc", "t", "T":
		m.previewTheme(m.themeOriginal)
		m.themeOpen = false
		return m, nil
	case "up", "k", "shift+tab":
		m.themePos = (m.themePos - 1 + len(choices)) % len(choices)
		m.previewTheme(choices[m.themePos].ID)
		return m, nil
	case "down", "j", "tab":
		m.themePos = (m.themePos + 1) % len(choices)
		m.previewTheme(choices[m.themePos].ID)
		return m, nil
	case "enter":
		m.previewTheme(choices[m.themePos].ID)
		if err := m.core.SetTheme(m.themeName); err != nil {
			m.note = "could not save theme " + m.th.G.Dot + " " + err.Error()
			return m, nil
		}
		m.themeOpen = false
		m.setFeedback("THEME  " + choices[m.themePos].Label + " applied")
		return m, nil
	}
	if len(key) == 1 && key[0] >= '1' && int(key[0]-'1') < len(choices) {
		m.themePos = int(key[0] - '1')
		m.previewTheme(choices[m.themePos].ID)
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

func (m *Model) loadHistory(view historyView) bool {
	switch view {
	case historyRecent:
		entries, err := m.core.RecentlyHeard(100)
		if err != nil {
			return false
		}
		m.recent = entries
		m.recentTop = 0
	case historyLoved:
		entries, err := m.core.LovedTracks(libraryLimit)
		if err != nil {
			return false
		}
		m.library = entries
		m.clampLibraryPos()
	case historyStations:
		entries, err := m.core.SavedStations()
		if err != nil {
			return false
		}
		m.saved = entries
		m.clampSavedPos()
		// The leaderboard still loads for the view's secondary section.
		if hist, err := m.core.TopListened(9); err == nil {
			m.hist = hist
		}
	}
	if view != historyLoved {
		entries, err := m.core.LovedTracks(libraryLimit)
		if err == nil {
			m.library = entries
			m.clampLibraryPos()
		}
	}
	m.historyView = view
	m.history = true
	m.libraryFind = false
	m.forgetKey = ""
	return true
}

// handleHistoryKey drives the unified recent/loved/stations library.
func (m Model) handleHistoryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.historyView == historyLoved && m.libraryFind {
		return m.handleLibraryFindKey(msg)
	}
	if m.historyView == historyStations && m.savedFind {
		return m.handleSavedFindKey(msg)
	}
	switch key {
	case "q", "ctrl+c":
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case "esc":
		if m.historyView == historyLoved && len(m.libraryQuery) > 0 {
			m.libraryQuery = nil
			m.libraryPos = 0
			m.forgetKey = ""
			return m, nil
		}
		// Same contract as LOVED: a live filter eats the first esc —
		// otherwise a stale query greets the next visit while the empty
		// state swears "esc clears the filter".
		if m.historyView == historyStations && len(m.savedQuery) > 0 {
			m.savedQuery = nil
			m.savedPos = 0
			m.forgetKey = ""
			return m, nil
		}
		m.history = false
		return m, nil
	case "h":
		m.loadHistory(historyRecent)
		return m, nil
	case "H":
		m.loadHistory(historyLoved)
		return m, nil
	case "tab":
		m.loadHistory((m.historyView + 1) % 3)
		return m, nil
	case "shift+tab":
		m.loadHistory((m.historyView + 2) % 3)
		return m, nil
	}

	if m.historyView == historyLoved {
		switch key {
		case "/":
			m.libraryFind = true
			m.forgetKey = ""
			return m, nil
		case "up", "k":
			m.moveLibrary(-1)
			return m, nil
		case "down", "j":
			m.moveLibrary(1)
			return m, nil
		case "pgup":
			m.moveLibrary(-5)
			return m, nil
		case "pgdown":
			m.moveLibrary(5)
			return m, nil
		case "home":
			m.libraryPos = 0
			m.forgetKey = ""
			return m, nil
		case "end":
			m.libraryPos = maxI(0, len(m.filteredLibrary())-1)
			m.forgetKey = ""
			return m, nil
		case "enter":
			entry, ok := m.selectedLovedTrack()
			if !ok {
				return m, nil
			}
			query := strings.TrimSpace(entry.Artist)
			if query == "" {
				query = strings.TrimSpace(entry.Title)
			}
			if query == "" {
				return m, nil
			}
			m.history = false
			m.seeking = true
			m.note = ""
			return m, m.seedCmd(query)
		case "r":
			entry, ok := m.selectedLovedTrack()
			if !ok || entry.StationUUID == "" {
				return m, nil
			}
			pick, err := m.core.TuneTo(entry.StationUUID, time.Now())
			if err != nil {
				m.note = err.Error()
				m.history = false
				return m, nil
			}
			pick.Reason = "from a loved track"
			return m.applyPick(pick)
		case "x":
			entry, ok := m.selectedLovedTrack()
			if !ok {
				return m, nil
			}
			entryKey := lovedTrackKey(entry)
			if m.forgetKey != entryKey || m.now.Sub(m.forgetAt) > 3*time.Second {
				m.forgetKey = entryKey
				m.forgetAt = m.now
				return m, nil
			}
			removed, err := m.core.ForgetLovedTrack(entry.ArtistKey, entry.Title)
			if err != nil {
				m.note = "could not remove loved track"
				return m, nil
			}
			if removed {
				m.loadHistory(historyLoved)
				m.setFeedback("LIBRARY  Track removed")
			}
			return m, nil
		}
	}

	if m.historyView == historyStations {
		switch key {
		case "/":
			m.savedFind = true
			m.forgetKey = ""
			return m, nil
		case "up", "k":
			m.moveSaved(-1)
			return m, nil
		case "down", "j":
			m.moveSaved(1)
			return m, nil
		case "pgup":
			m.moveSaved(-5)
			return m, nil
		case "pgdown":
			m.moveSaved(5)
			return m, nil
		case "home":
			m.savedPos = 0
			m.forgetKey = ""
			return m, nil
		case "end":
			m.savedPos = maxI(0, len(m.filteredSaved())-1)
			m.forgetKey = ""
			return m, nil
		case "enter":
			entry, ok := m.selectedSaved()
			if !ok {
				return m, nil
			}
			m.history = false
			if m.haveSt && entry.UUID == m.st.UUID {
				return m, nil // already playing it
			}
			pick, err := m.core.TuneTo(entry.UUID, time.Now())
			if err != nil {
				m.note = err.Error()
				return m, nil
			}
			pick.Reason = "from saved stations"
			return m.applyPick(pick)
		case "x":
			entry, ok := m.selectedSaved()
			if !ok {
				return m, nil
			}
			if m.forgetKey != entry.UUID || m.now.Sub(m.forgetAt) > 3*time.Second {
				m.forgetKey = entry.UUID
				m.forgetAt = m.now
				return m, nil
			}
			if err := m.core.RemoveStation(entry.UUID); err != nil {
				m.note = "could not remove station"
				return m, nil
			}
			m.presets = m.core.Presets()
			m.loadHistory(historyStations)
			m.setFeedback("STATIONS  Removed")
			return m, nil
		}
		// Digits still tune from the saved list when a preset holds that slot.
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			slot := int(key[0] - '0')
			uuid, ok := m.presets[slot]
			if !ok {
				return m, nil
			}
			if m.haveSt && uuid == m.st.UUID {
				m.history = false
				return m, nil
			}
			pick, err := m.core.TuneTo(uuid, time.Now())
			if err != nil {
				m.note = err.Error()
				m.history = false
				return m, nil
			}
			pick.Reason = fmt.Sprintf("preset %d", slot)
			return m.applyPick(pick)
		}
		return m, nil
	}

	// RECENT rows have no cursor — the whole window scrolls, clamped to
	// the range the renderer could actually use. Free-running offsets
	// used to bury the list under dead keypresses.
	if m.historyView == historyRecent {
		switch key {
		case "up", "k":
			m.recentTop = maxI(0, m.recentTop-1)
		case "down", "j":
			m.recentTop = minI(m.recentTop+1, m.recentMaxTop())
		case "pgup":
			m.recentTop = maxI(0, m.recentTop-5)
		case "pgdown":
			m.recentTop = minI(m.recentTop+5, m.recentMaxTop())
		case "home":
			m.recentTop = 0
		case "end":
			m.recentTop = m.recentMaxTop()
		}
	}
	return m, nil
}

// handleSavedFindKey drives the STATIONS view's incremental search.
func (m Model) handleSavedFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyEnter:
		m.savedFind = false
		return m, nil
	case tea.KeyBackspace:
		if len(m.savedQuery) > 0 {
			m.savedQuery = m.savedQuery[:len(m.savedQuery)-1]
			m.savedPos = 0
		}
		return m, nil
	case tea.KeyUp:
		m.moveSaved(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSaved(1)
		return m, nil
	case tea.KeySpace:
		if len(m.savedQuery) < maxQuery {
			m.savedQuery = append(m.savedQuery, ' ')
			m.savedPos = 0
		}
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if len(m.savedQuery) >= maxQuery {
				break
			}
			m.savedQuery = append(m.savedQuery, r)
		}
		m.savedPos = 0
		m.forgetKey = ""
		return m, nil
	}
	return m, nil
}

func (m Model) handleLibraryFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyEnter:
		m.libraryFind = false
		return m, nil
	case tea.KeyBackspace:
		if len(m.libraryQuery) > 0 {
			m.libraryQuery = m.libraryQuery[:len(m.libraryQuery)-1]
			m.libraryPos = 0
		}
		return m, nil
	case tea.KeyUp:
		m.moveLibrary(-1)
		return m, nil
	case tea.KeyDown:
		m.moveLibrary(1)
		return m, nil
	case tea.KeySpace:
		if len(m.libraryQuery) < maxQuery {
			m.libraryQuery = append(m.libraryQuery, ' ')
			m.libraryPos = 0
		}
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if len(m.libraryQuery) >= maxQuery {
				break
			}
			m.libraryQuery = append(m.libraryQuery, r)
		}
		m.libraryPos = 0
		m.forgetKey = ""
		return m, nil
	}
	return m, nil
}

func (m *Model) moveLibrary(delta int) {
	count := len(m.filteredLibrary())
	if count == 0 {
		m.libraryPos = 0
		return
	}
	m.libraryPos += delta
	if m.libraryPos < 0 {
		m.libraryPos = 0
	}
	if m.libraryPos >= count {
		m.libraryPos = count - 1
	}
	m.forgetKey = ""
}

func (m *Model) clampLibraryPos() {
	count := len(m.filteredLibrary())
	if count == 0 {
		m.libraryPos = 0
	} else if m.libraryPos >= count {
		m.libraryPos = count - 1
	}
}

// --- saved-stations list (STATIONS view) ---

func (m *Model) moveSaved(delta int) {
	count := len(m.filteredSaved())
	if count == 0 {
		m.savedPos = 0
		return
	}
	m.savedPos += delta
	if m.savedPos < 0 {
		m.savedPos = 0
	}
	if m.savedPos >= count {
		m.savedPos = count - 1
	}
	m.forgetKey = ""
}

func (m *Model) clampSavedPos() {
	count := len(m.filteredSaved())
	if count == 0 {
		m.savedPos = 0
	} else if m.savedPos >= count {
		m.savedPos = count - 1
	}
}

func (m Model) filteredSaved() []core.SavedStation {
	query := strings.ToLower(strings.TrimSpace(string(m.savedQuery)))
	if query == "" {
		return m.saved
	}
	var out []core.SavedStation
	for _, e := range m.saved {
		name := strings.ToLower(e.Name)
		ok := true
		for _, term := range strings.Fields(query) {
			if !strings.Contains(name, term) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

func (m Model) selectedSaved() (core.SavedStation, bool) {
	entries := m.filteredSaved()
	if m.savedPos < 0 || m.savedPos >= len(entries) {
		return core.SavedStation{}, false
	}
	return entries[m.savedPos], true
}

func (m Model) selectedLovedTrack() (core.LovedTrack, bool) {
	entries := m.filteredLibrary()
	if m.libraryPos < 0 || m.libraryPos >= len(entries) {
		return core.LovedTrack{}, false
	}
	return entries[m.libraryPos], true
}

func (m Model) filteredLibrary() []core.LovedTrack {
	query := strings.ToLower(strings.TrimSpace(string(m.libraryQuery)))
	if query == "" {
		return m.library
	}
	type match struct {
		entry core.LovedTrack
		score int
	}
	var matches []match
	for _, entry := range m.library {
		score, ok := lovedMatchScore(entry, query)
		if ok {
			matches = append(matches, match{entry: entry, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].entry.LovedAt.After(matches[j].entry.LovedAt)
		}
		return matches[i].score > matches[j].score
	})
	out := make([]core.LovedTrack, len(matches))
	for i := range matches {
		out[i] = matches[i].entry
	}
	return out
}

func lovedMatchScore(entry core.LovedTrack, query string) (int, bool) {
	artist := strings.ToLower(entry.Artist)
	title := strings.ToLower(entry.Title)
	station := strings.ToLower(entry.StationName)
	all := artist + " " + title + " " + station
	score := 0
	for _, term := range strings.Fields(query) {
		if !strings.Contains(all, term) {
			return 0, false
		}
		switch {
		case strings.HasPrefix(title, term):
			score += 80
		case strings.HasPrefix(artist, term):
			score += 70
		case strings.Contains(title, term):
			score += 50
		case strings.Contains(artist, term):
			score += 40
		default:
			score += 10
		}
	}
	return score, true
}

func lovedTrackKey(entry core.LovedTrack) string {
	return entry.ArtistKey + "\x00" + strings.ToLower(strings.TrimSpace(entry.Title))
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
	m.haveTrack = title != ""
}

// relayout resizes the instruments that depend on terminal size and panel
// shape: wave width and depth, then the density strip re-bucketed to fit.
func (m *Model) relayout() {
	m.wave.Resize(m.waveRenderWidth())
	m.wave.SetRows(m.waveRenderRows())
	m.rebuildDensity()
}

func (m *Model) setFeedback(text string) {
	m.feedback = text
	m.feedbackAt = m.now
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

func (m Model) innerWidth() int {
	iw := m.w - 4
	if iw > 92 {
		iw = 92
	}
	if iw < 16 {
		// The 10-column floor only applies while it fits: a narrower
		// terminal gets iw == w rather than rows overflowing the edge.
		iw = maxI(m.w-2, minI(10, m.w))
	}
	return iw
}

// waveRenderWidth follows the active responsive composition. Wide terminals
// give the signal its own instrument bay; compact terminals retain the full
// stacked width.
func (m Model) waveRenderWidth() int {
	iw := m.innerWidth()
	if m.receiverShape() {
		_, _, right := receiverColumns(iw)
		return right
	}
	return iw
}

// waveRenderRows mirrors waveRenderWidth for the vertical axis: the signal
// bay only stretches inside the wide faceplate. The compact stacked layout
// keeps a two-row meter, where it sits directly under the track and has no
// room to grow.
func (m Model) waveRenderRows() int {
	contentH := m.contentHeight()
	if m.receiverShape() {
		_, leftRows, rightFixed := m.receiverMetrics()
		perChannel, _, _ := receiverLayout(contentH, leftRows, rightFixed)
		return perChannel
	}
	if m.innerWidth() >= 28 && m.h >= 14 && contentH >= 8 {
		return stackedWaveRows(contentH)
	}
	return 1
}

// contentHeight is the room between the pinned header and the pinned key
// strip. View computes the same number; both must agree or the wave will be
// sized for a layout the renderer isn't using.
func (m Model) contentHeight() int {
	top := 2 // header bar + rule
	if m.h < 8 {
		top = 0
	}
	return m.h - top - 1
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

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
