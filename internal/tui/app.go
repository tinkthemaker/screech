package tui

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"screech/internal/core"
	"screech/internal/player"
)

const (
	fps         = 20
	frameDur    = time.Second / fps
	bootDur     = 900 * time.Millisecond
	idleAfter   = 3 * time.Minute
	tuneStall   = 12 * time.Second
	maxQuery    = 40
	feedbackDur = 2400 * time.Millisecond

	defaultSyncLimit = 20000
	libraryLimit     = 500
)

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
	phDead
)

type Options struct {
	Accent    string
	ASCII     bool
	SyncLimit int
}

type Model struct {
	core *core.Core
	pl   player.Player
	th   Theme

	w, h    int
	start   time.Time
	now     time.Time
	lastKey time.Time

	ph        phase
	syncing   bool
	virgin    bool
	syncLimit int

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

	presets      map[int]string
	prompt       bool
	buf          []rune
	seeking      bool
	history      bool
	hist         []core.HistEntry
	recent       []core.RecentTrack
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

	tuneAt     time.Time
	playStart  time.Time
	note       string
	feedback   string
	feedbackAt time.Time
	fatal      string
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
		n          int
		pruned     int
		err        error
		background bool
	}
	seededMsg struct {
		res core.SeedResult
		ok  bool
	}
	volumeMsg struct{ err error }
)

func New(c *core.Core, pl player.Player, opts Options) Model {
	now := time.Now()
	syncing := c.NeedsSync()
	virgin := c.IsVirgin()
	sl := opts.SyncLimit
	if sl <= 0 {
		sl = defaultSyncLimit
	}
	return Model{
		syncLimit: sl,
		core:      c,
		pl:        pl,
		th:        NewTheme(opts.Accent, opts.ASCII),
		start:     now,
		now:       now,
		lastKey:   now,
		wave:      NewWave(32),
		dial:      NewSpring(0.5),
		dialTgt:   0.5,
		syncing:   syncing,
		virgin:    virgin,
		prompt:    false,
		presets:   c.Presets(),
		volume:    c.Volume(),
	}
}

func Run(c *core.Core, pl player.Player, opts Options) error {
	_, err := tea.NewProgram(New(c, pl, opts), tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick(), m.listen(), m.volumeCmd()}
	cmds = append(cmds, m.resumeCmd())
	if m.syncing || m.core.SyncStale(m.syncLimit) {
		// Weekly directory refresh, in the background. Playback never waits.
		cmds = append(cmds, m.syncCmd(true))
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

func (m Model) syncCmd(background bool) tea.Cmd {
	c := m.core
	limit := m.syncLimit
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		n, pruned, err := c.Sync(ctx, limit)
		return syncedMsg{n: n, pruned: pruned, err: err, background: background}
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
	m.history = false
	m.note = ""
	m.reason = pick.Reason
	m.ph = phTune
	m.tuneAt = m.now
	m.decrypt = NewDecrypt(heroText(cleanStationName(m.st.Name), 72, m.th.G.Ellipsis), m.now)
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
		m.wave.Resize(m.waveRenderWidth())
		return m, nil

	case tea.KeyMsg:
		m.lastKey = m.now
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
		m.dial.Step(m.dialTgt, dt)
		// A station that never locks is a corpse: mark it and move on. The
		// dead listen closes without skip semantics — a stream that never
		// played teaches nothing about taste.
		if m.ph == phTune && m.haveSt && m.now.Sub(m.tuneAt) > tuneStall {
			m.core.MarkStationFailed(m.st.UUID)
			m.note = "no signal " + m.th.G.Dot + " moving on"
			m.tuneAt = m.now
			return m, tea.Batch(m.tick(), m.tuneDeadCmd())
		}
		return m, m.tick()

	case syncedMsg:
		m.syncing = false
		if msg.background {
			// Refresh landed while playing; absorb quietly.
			m.presets = m.core.Presets()
			if msg.err != nil {
				m.setFeedback("DIRECTORY  Refresh failed; using cache")
			} else {
				m.setFeedback(fmt.Sprintf("DIRECTORY  %d stations refreshed", msg.n))
			}
			return m, nil
		}
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
			m.note = "no matches for " + msg.res.Label
			return m, nil
		}
		return m.applyPick(msg.res.Pick)

	case volumeMsg:
		if msg.err != nil {
			m.note = "volume control failed " + m.th.G.Dot + " " + msg.err.Error()
		}
		return m, nil

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
		if m.ph == phDead {
			return m, nil
		}
		m.ph = phTune
		m.tuneAt = m.now
		m.note = ""
		switch {
		case m.suspect:
			m.setFeedback("LEARNED  Break skip protected")
		case !m.playStart.IsZero() && m.now.Sub(m.playStart) >= 90*time.Second:
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
		_, track, lovedNow := m.core.Love(time.Now())
		m.lovedTrack = lovedNow
		if lovedNow {
			m.loveAt = m.now
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
		slot, saved, full := m.core.TogglePreset(time.Now())
		m.presets = m.core.Presets()
		switch {
		case full:
			m.reason = "presets full " + m.th.G.Dot + " f on a saved station frees a slot"
			m.setFeedback("PRESETS  All nine slots are full")
		case saved:
			m.reason = fmt.Sprintf("saved "+m.th.G.Dot+" press %d to return", slot)
			m.setFeedback(fmt.Sprintf("SAVED  Preset %d", slot))
		case slot > 0:
			m.reason = fmt.Sprintf("preset %d cleared", slot)
			m.setFeedback(fmt.Sprintf("CLEARED  Preset %d", slot))
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

	case "v":
		m.volumeOpen = true
		return m, nil

	case "-", "[":
		m.volumeOpen = true
		return m.adjustVolume(-5)

	case "+", "=", "]":
		m.volumeOpen = true
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

func (m Model) handleVolumeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case "esc", "enter", "v":
		m.volumeOpen = false
		return m, nil
	case "left", "down", "-", "[":
		return m.adjustVolume(-5)
	case "right", "up", "+", "=", "]":
		return m.adjustVolume(5)
	}
	return m, nil
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
	return m, nil
}

// handleSavedFindKey drives the STATIONS view's incremental search.
func (m Model) handleSavedFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
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

func (m *Model) setFeedback(text string) {
	m.feedback = text
	m.feedbackAt = m.now
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
		tr, ok, suspect, lovedNow := m.core.NoteTitle(ev.Title, time.Now())
		m.suspect = suspect
		if ok {
			text := tr.Title
			if tr.Artist != "" {
				text = tr.Artist + " " + m.th.G.Dot + " " + tr.Title
			}
			if text != m.track {
				m.track = text
				m.trackAt = m.now
			}
			m.haveTrack = true
			m.lovedTrack = lovedNow
		} else {
			m.haveTrack = false
			m.track = ""
			m.lovedTrack = false
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
		return m, tea.Batch(m.listen(), m.tuneDeadCmd())

	case player.EventDied:
		m.ph = phDead
		m.fatal = "mpv exited " + m.th.G.Dot + " restart screech"
		return m, nil
	}
	return m, m.listen()
}

func (m Model) innerWidth() int {
	iw := m.w - 4
	if iw > 92 {
		iw = 92
	}
	if iw < 16 {
		iw = maxI(m.w-2, 10)
	}
	return iw
}

// waveRenderWidth follows the active responsive composition. Wide terminals
// give the signal its own instrument bay; compact terminals retain the full
// stacked width.
func (m Model) waveRenderWidth() int {
	iw := m.innerWidth()
	if iw >= 78 && m.h >= 16 {
		_, _, right := receiverColumns(iw)
		return right
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

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
