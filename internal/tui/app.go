package tui

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
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

	// resumeInPlace is how long a pause can still pick up where it left
	// off: a short pause resumes from mpv's buffer a few seconds behind
	// live, like pausing live TV; past that the buffer is stale or the
	// server has hung up, so resume rejoins the live edge instead.
	resumeInPlace = 30 * time.Second

	// sleepFade is the volume's glide path to silence in the last stretch
	// before the sleep timer pauses the radio.
	sleepFade = 30 * time.Second
)

// sleepSteps is the sleep timer's ladder; z cycles through it and off the
// far end.
var sleepSteps = []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute, 60 * time.Minute, 90 * time.Minute}

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
	phPaused // output paused by us or by the OS media controls
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
	helpOpen     bool
	infoOpen     bool
	info         core.StationInfo
	infoFor      string // uuid the info sheet belongs to; it follows the radio
	infoErr      error  // last StationInfo failure — personal rows read unavailable

	tuneAt      time.Time
	playStart   time.Time
	pausedAt    time.Time
	resuming    bool // this tune is a resume — header says RESUMING…
	pauseReload bool // resume must reload: backend restarted, or the stream stopped/ended while paused
	sleepStep   int  // index into sleepSteps, -1 = off
	sleepAt     time.Time
	sleepVol    int    // last faded volume sent to the player; -1 = none
	notice      string // startup notice from Options; survives picks, dies on first key
	note        string
	feedback    string
	feedbackAt  time.Time
	fatal       string
	quitting    bool // a quit is in flight; backend death is expected noise

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
	pauseMsg  struct{ err error }
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
	stationInfoMsg struct {
		uuid string
		info core.StationInfo
		err  error
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
		sleepStep:    -1,
		sleepVol:     -1,
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

// restartCmd brings up a fresh backend after a crash, then — unless play
// is false (the radio was paused) — re-points it at the current station so
// playback resumes without waiting for a retune.
func (m Model) restartCmd(play bool) tea.Cmd {
	pl, st, haveSt := m.pl, m.st, m.haveSt
	return func() tea.Msg {
		err := pl.Restart()
		if err == nil && haveSt && play {
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
	m.resuming = false
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
	cmds := []tea.Cmd{m.playCmd(), m.stationStatsCmd(m.st.UUID), m.dialDensityCmd()}
	if m.infoOpen {
		// The info sheet follows the radio: re-request for the new station
		// and show it loading in the meantime.
		m.infoFor = m.st.UUID
		m.infoErr = nil
		m.info = core.StationInfo{}
		cmds = append(cmds, m.stationInfoCmd(m.st.UUID))
	}
	return m, tea.Batch(cmds...)
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
		// The help and info sheets take keys first: their own keys close
		// them, anything else closes and falls through to whatever is
		// underneath — the same pass-through contract as the volume strip.
		if m.helpOpen {
			m.helpOpen = false
			switch key := msg.String(); key {
			case "?", "esc", "enter":
				return m, nil
			case "q", "ctrl+c":
				m.quitting = true
				m.core.EndListen(time.Now())
				_ = m.pl.Close()
				return m, tea.Quit
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
				m.quitting = true
				m.core.EndListen(time.Now())
				_ = m.pl.Close()
				return m, tea.Quit
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
		// The sleep timer counts down on the frame clock: the last stretch
		// fades the player's volume out — never the stored volume, which
		// stays the listener's — then pauses the radio itself.
		if !m.sleepAt.IsZero() {
			switch remaining := m.sleepAt.Sub(m.now); {
			case remaining <= 0:
				m.sleepStep, m.sleepAt = -1, time.Time{}
				if m.ph == phPlay || m.ph == phBuffer || m.ph == phTune || m.ph == phOffline {
					nm, pcmd := m.pause(false)
					nm.setFeedback("SLEEP  Paused " + m.th.G.Dot + " P resumes")
					nm.sleepVol = -1
					pl, v := m.pl, m.volume
					// Restore strictly after the pause lands: a radio
					// coming back at faded level would still be asleep.
					return nm, tea.Batch(m.tick(), tea.Sequence(pcmd,
						func() tea.Msg { return volumeMsg{err: pl.SetVolume(v)} }))
				}
				if m.sleepVol >= 0 {
					m.sleepVol = -1
					pl, v := m.pl, m.volume
					return m, tea.Batch(m.tick(), func() tea.Msg {
						return volumeMsg{err: pl.SetVolume(v)}
					})
				}
			case remaining <= sleepFade && (m.ph == phPlay || m.ph == phBuffer):
				v := int(math.Round(float64(m.volume) * remaining.Seconds() / sleepFade.Seconds()))
				if v != m.sleepVol {
					m.sleepVol = v
					pl := m.pl
					return m, tea.Batch(m.tick(), func() tea.Msg {
						return volumeMsg{err: pl.SetVolume(v)}
					})
				}
			}
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

	case pauseMsg:
		if msg.err != nil {
			m.note = "pause failed " + m.th.G.Dot + " " + msg.err.Error()
		}
		return m, nil

	case stationStatsMsg:
		if m.haveSt && msg.uuid == m.st.UUID {
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
		if m.ph == phPaused {
			// The respawned backend holds nothing: the pause survives,
			// but resuming has to reload the stream rather than unpause.
			m.pauseReload = true
			return m, m.listen()
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
