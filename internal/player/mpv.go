package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tinkthemaker/screech/internal/streamurl"
	"github.com/tinkthemaker/screech/internal/version"
)

// testExtraArgs appends to every spawn so tests can make mpv safe on
// machines without audio hardware (--ao=null) without touching the
// production arg list.
var testExtraArgs []string

// MPV drives a headless mpv process over its JSON IPC socket (unix socket on
// linux/mac, named pipe on windows — see ipc_*.go). mpv does the heavy
// lifting: codecs, ICY metadata, reconnects, buffering.
type MPV struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	conn    net.Conn
	events  chan Event
	exited  chan struct{} // closed when the current process is reaped
	reqID   int
	closed  bool
	sawIcy  bool
	ipcPath string
	stderr  *tailBuf
	gen     int

	// loadPending marks the window between sending loadfile and mpv's
	// start-file: an end-file landing inside it belongs to the file being
	// replaced, not to the new one.
	loadPending bool

	// paused and coreIdle hold the last values mpv reported for those
	// properties: playing is only playing when mpv is neither paused nor
	// idle, and both gates suppress their own echoes.
	paused, coreIdle bool

	// junkTitles are the media-title values mpv falls back to before any
	// ICY title: the stream URL's filename shapes (percent-decoded, query
	// included — mpv's rule is text after the last '/' of the whole URL)
	// plus the filename property it last reported, which covers playlist
	// entries whose URL we never saw.
	junkTitles  []string
	mpvFilename string

	// pages resolves page links (nil: ytdlp_path is off and they go to mpv
	// as-is, as before). page is the link behind the current load when it
	// was resolved, so a failure can forget the stale answer. playSeq
	// counts Play calls: a resolution that finishes after a newer Play
	// began must not load over it.
	pages   *pageResolver
	page    string
	playSeq int

	// openSource opens a discovery station's songs (nil: the discover:
	// sentinel is rejected like any non-http(s) URL). queue is the walk
	// through them while one plays, nil for radio. See queue.go.
	openSource func(seed string) (SongSource, error)
	queue      *songQueue

	mpvPath string
	dataDir string
	volume  int // survives restarts: spawn passes it back as --volume

	lastLevelEmit time.Time

	// Latest astats metering, combined into one EventLevel per emit window.
	lvlOverall float64
	lvlL, lvlR float64
	lvlPeak    float64
	sawL, sawR bool
}

// The labelled astats filter emits per-frame loudness into filter metadata
// under af-metadata/meter/…; an unlabelled lavfi never populates these keys
// on mpv 0.41 — the meter read zero forever.
const (
	propMeterOverallRMS = "af-metadata/meter/lavfi.astats.Overall.RMS_level"
	propMeterRMSL       = "af-metadata/meter/lavfi.astats.1.RMS_level"
	propMeterRMSR       = "af-metadata/meter/lavfi.astats.2.RMS_level"
	propMeterPeak       = "af-metadata/meter/lavfi.astats.Overall.Peak_level"
)

// observedProperties are subscribed right after IPC connects, before the
// first event can arrive. filename lands before media-title so a
// filename-as-title report is suppressed from its first emission.
var observedProperties = []string{
	"metadata", "filename", "media-title", "core-idle", "paused-for-cache", "pause",
	propMeterOverallRMS, propMeterRMSL, propMeterRMSR, propMeterPeak,
}

func NewMPV(mpvPath, dataDir string) (*MPV, error) {
	if mpvPath == "" {
		mpvPath = "mpv"
	}
	if dataDir == "" {
		return nil, fmt.Errorf("data directory is required for the mpv IPC socket")
	}
	if err := prepareIPCDir(dataDir); err != nil {
		return nil, fmt.Errorf("preparing IPC directory: %w", err)
	}
	m := &MPV{mpvPath: mpvPath, dataDir: dataDir, volume: 100}
	if err := m.spawn(); err != nil {
		return nil, err
	}
	return m, nil
}

// spawn boots one mpv process and installs it: new IPC endpoint (the
// generation in its name keeps a lingering socket file from colliding with
// the predecessor's), a fresh event channel, and the process's one Wait.
// Callers hold no lock; a previous generation must already be dead.
func (m *MPV) spawn() error {
	m.mu.Lock()
	m.gen++
	gen := m.gen
	volume := m.volume
	m.mu.Unlock()
	path := ipcPath(m.dataDir, gen)
	stderr := newTailBuf()
	args := []string{
		"--idle=yes",
		"--no-video",
		"--no-terminal",
		"--really-quiet",
		fmt.Sprintf("--volume=%d", volume),
		"--cache=yes",
		"--network-timeout=15",
		"--user-agent=" + version.UserAgent(),
		// astats injects per-frame loudness into filter metadata; the wave
		// visualizer reads it so its amplitude is real, not theatrical.
		// The @meter label is load-bearing: unlabelled, mpv reports these
		// keys under a path that never carries values.
		"--af=@meter:lavfi=[astats=metadata=1:reset=1]",
		// Let ffmpeg ride out brief mid-stream drops on ICY streams; the
		// app-level reconnect in the TUI is the backstop.
		"--stream-lavf-o=reconnect_streamed=1",
		// mpv never runs yt-dlp itself: its ytdl hook would run it (when
		// installed) on every URL that fails to load, delaying the failure
		// the tune watchdog and offline logic rely on. Page links, the one
		// case that needs it, are resolved before loadfile, under a timeout
		// of screech's own (extract.go).
		"--ytdl=no",
		// mpv still reads the user's mpv.conf — audio device, exclusive
		// mode, that stays — but these pin options a personal config can
		// set that break live radio: keep-open and looping swallow the eof
		// that drop detection keys on, pause=yes makes every tune look
		// dead, watch-later "resumes" a live stream at a stale offset, and
		// mpv's default demuxer caches (150MiB ahead, 50MiB back) only grow
		// for radio — about 55 minutes of never-seeked back buffer per hour
		// at 128k.
		"--pause=no",
		"--keep-open=no",
		"--loop-file=no",
		"--loop-playlist=no",
		"--resume-playback=no",
		"--save-position-on-quit=no",
		"--demuxer-max-bytes=32MiB",
		"--demuxer-max-back-bytes=4MiB",
		"--input-ipc-server=" + path,
	}
	args = append(args, testExtraArgs...)
	cmd := exec.Command(ResolveMpv(m.mpvPath), args...)
	cmd.Stderr = stderr
	configureCmd(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mpv (%s): %w — is mpv installed and on PATH?", m.mpvPath, err)
	}
	afterStart(cmd)
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait() // stderr drains through Wait; this is the only Wait
		close(exited)
	}()

	// Dial IPC while watching exited: a broken binary or a bad arg fails
	// in milliseconds with its stderr tail, not after a 10s poll of a
	// corpse.
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err = dialIPC(path)
		if err == nil {
			break
		}
		select {
		case <-exited:
			_ = cleanupIPC(path)
			return fmt.Errorf("mpv exited during startup: %w%s", err, stderr.suffix())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cleanupIPC(path)
			return fmt.Errorf("connecting to mpv IPC: %w%s", err, stderr.suffix())
		}
		select {
		case <-exited:
			_ = cleanupIPC(path)
			return fmt.Errorf("mpv exited during startup: %w%s", err, stderr.suffix())
		case <-time.After(200 * time.Millisecond):
		}
	}

	reqID := 0
	for _, prop := range observedProperties {
		reqID++
		if err := writeCmd(conn, reqID, "observe_property", reqID, prop); err != nil {
			_ = conn.Close()
			_ = cmd.Process.Kill()
			_ = cleanupIPC(path)
			return err
		}
	}

	m.mu.Lock()
	m.cmd, m.conn, m.events = cmd, conn, make(chan Event, 64)
	m.exited, m.ipcPath, m.stderr = exited, path, stderr
	m.reqID = reqID
	m.loadPending = false
	// A walk through songs belongs to the process that was playing it; the
	// session re-plays the station on the new one.
	m.dropQueueLocked()
	m.sawIcy, m.sawL, m.sawR = false, false, false
	m.junkTitles, m.mpvFilename = nil, ""
	m.paused, m.coreIdle = false, true
	m.mu.Unlock()
	// The loop owns conn and the channel: it closes the channel itself, so
	// a restarted generation's death can never land in the new stream.
	go m.readLoop(conn, m.events, stderr, exited)
	return nil
}

func (m *MPV) Events() <-chan Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

// SetExtractor turns page-link resolution on (ytdlp_path). Without it a
// page link goes to mpv as-is and fails there, as it always has.
func (m *MPV) SetExtractor(ex Extractor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pages = newPageResolver(ex)
}

func (m *MPV) Play(streamURL string) error {
	if IsDiscoverURL(streamURL) {
		return m.playQueue(strings.TrimPrefix(streamURL, discoverScheme))
	}
	if !validStreamURL(streamURL) {
		return fmt.Errorf("invalid stream URL %q: only http/https are supported", streamURL)
	}
	m.mu.Lock()
	m.playSeq++
	seq, pages := m.playSeq, m.pages
	// A radio station ends any discovery walk: its songs must stop
	// advancing, and its stream's end is a dropped stream again.
	m.dropQueueLocked()
	m.mu.Unlock()

	target, title, page := streamURL, "", ""
	if pages != nil && streamurl.IsPage(streamURL) {
		// Silence the old station before the seconds yt-dlp takes: its
		// titles would otherwise land on the new station's listen. The
		// stop's end-file falls inside loadPending and is dropped, so it
		// never reads as the listener pressing Stop.
		m.mu.Lock()
		m.loadPending = true
		m.mu.Unlock()
		_ = m.send("stop")
		r, err := pages.resolve(streamURL)
		if err != nil {
			return err
		}
		target, title, page = r.URL, r.Title, streamURL
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.playSeq != seq {
		return ErrSuperseded
	}
	m.sawIcy = false
	// Channel reporting is a property of the stream, not the process: a
	// mono station following a stereo one must not inherit its stereo flag.
	m.sawL, m.sawR = false, false
	m.loadPending = true
	m.junkTitles = streamurl.MpvFilenames(target)
	if page != "" {
		m.junkTitles = append(m.junkTitles, streamurl.MpvFilenames(page)...)
	}
	m.mpvFilename = ""
	m.page = page
	load := []any{"loadfile", target}
	if title != "" {
		// A resolved URL carries no ICY metadata and its filename is junk,
		// so the page's own title rides the media-title path instead.
		load = append(load, "replace", -1, forceTitleOption(title))
	}
	// Sent under the lock that checked playSeq, so a newer Play can't slip
	// its loadfile in between and be replaced by this older one.
	if err := m.sendLocked(load...); err != nil {
		return err
	}
	// mpv's pause survives loadfile; choosing a station must never start
	// silent, whoever paused it.
	return m.sendLocked("set_property", "pause", false)
}

// forceTitleOption is loadfile's per-file option that pins mpv's media-title.
// %n% is mpv's length-prefixed quoting: commas, quotes and '=' in a title
// can't break the option list.
func forceTitleOption(title string) string {
	return fmt.Sprintf("force-media-title=%%%d%%%s", len(title), title)
}

func (m *MPV) SetVolume(percent int) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	m.mu.Lock()
	m.volume = percent
	m.mu.Unlock()
	return m.send("set_property", "volume", percent)
}

func (m *MPV) SetPaused(paused bool) error {
	return m.send("set_property", "pause", paused)
}

// Restart replaces a dead backend with a fresh process: the old one is
// killed if still alive, its IPC endpoint removed, and spawn() installs a
// new connection and event channel. A closed MPV stays closed.
func (m *MPV) Restart() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("mpv: closed")
	}
	cmd, conn, exited, path := m.cmd, m.conn, m.exited, m.ipcPath
	m.mu.Unlock()

	// Closing the conn unblocks the old readLoop first, then kill-and-reap
	// is strictly ordered so a zombie can't outlive the new spawn.
	if conn != nil {
		_ = conn.Close()
	}
	if exited != nil {
		select {
		case <-exited:
		default:
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-exited
		}
	}
	_ = cleanupIPC(path)
	return m.spawn()
}

func (m *MPV) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.dropQueueLocked()
	conn, cmd, path := m.conn, m.cmd, m.ipcPath
	m.mu.Unlock()
	_ = m.send("quit")
	time.Sleep(150 * time.Millisecond)
	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cleanupIPC(path)
	return nil
}

func (m *MPV) send(command ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendLocked(command...)
}

func (m *MPV) sendLocked(command ...any) error {
	if m.conn == nil {
		return fmt.Errorf("mpv: not connected")
	}
	m.reqID++
	return writeCmd(m.conn, m.reqID, command...)
}

func writeCmd(conn net.Conn, reqID int, command ...any) error {
	msg, err := json.Marshal(map[string]any{"command": command, "request_id": reqID})
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(append(msg, '\n'))
	return err
}

type mpvMsg struct {
	Event     string          `json:"event"`
	Name      string          `json:"name"`
	Data      json.RawMessage `json:"data"`
	Reason    string          `json:"reason"`
	Error     string          `json:"error"`
	FileError string          `json:"file_error"`
}

// readLoop owns conn and events: it parses until the connection dies, emits
// EventDied unless this MPV was closed deliberately, then closes the
// channel itself — the single owner means a restarted process's loop can
// never write or close the wrong generation's stream.
func (m *MPV) readLoop(conn net.Conn, events chan Event, stderr *tailBuf, exited chan struct{}) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg mpvMsg
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Event {
		case "property-change":
			m.handleProperty(events, msg)
		case "start-file":
			m.mu.Lock()
			m.loadPending = false
			m.mu.Unlock()
		case "end-file":
			m.mu.Lock()
			pending := m.loadPending
			m.mu.Unlock()
			if pending {
				// This end-file retires the file loadfile just replaced;
				// it says nothing about the new stream.
				continue
			}
			switch msg.Reason {
			case "stop":
				// mpv's `stop` command — the OS media flyout's Stop
				// button sends it. Screech never does. Nothing is loaded
				// any more, so a discovery walk must not load behind it.
				m.mu.Lock()
				m.dropQueueLocked()
				m.mu.Unlock()
				emit(events, Event{Type: EventStopped})
			case "error":
				// A song that fails is skipped; only the station's third
				// failure in a row is a stream error.
				if m.queueSongEnded(events, true, msg.FileError) {
					continue
				}
				m.forgetPage()
				err := fmt.Errorf("stream failed")
				if msg.FileError != "" {
					err = fmt.Errorf("stream failed: %s", msg.FileError)
				}
				emit(events, Event{Type: EventStreamError, Err: err})
			case "eof":
				// A song that ends is just the next one starting.
				if m.queueSongEnded(events, false, "") {
					continue
				}
				m.forgetPage()
				emit(events, Event{Type: EventStreamEnded})
			}
		}
	}
	m.mu.Lock()
	closed := m.closed
	if m.events == events {
		// Before the channel closes: a lookup running for the queue emits
		// only while the queue is registered, under this lock.
		m.dropQueueLocked()
	}
	m.mu.Unlock()
	if !closed {
		// stderr is fully drained only when cmd.Wait returns (exited
		// closes); the conn dies first, so wait briefly or the died
		// error can miss mpv's last words.
		select {
		case <-exited:
		case <-time.After(time.Second):
		}
		emit(events, Event{Type: EventDied, Err: fmt.Errorf("mpv exited%s", stderr.suffix())})
	}
	close(events)
}

// forgetPage drops the cached resolution behind a stream that failed or
// ended, so the reconnect or the next visit resolves the page afresh: an
// expired signature fails exactly like a dead server.
func (m *MPV) forgetPage() {
	m.mu.Lock()
	pages, page := m.pages, m.page
	m.mu.Unlock()
	if pages != nil && page != "" {
		pages.forget(page)
	}
}

func (m *MPV) handleProperty(events chan Event, msg mpvMsg) {
	switch msg.Name {
	case "metadata":
		m.mu.Lock()
		queued := m.queue != nil
		m.mu.Unlock()
		if queued {
			// A song's title is the source's, forced onto media-title; an
			// ICY title would also mute the media-title path below for
			// every song after it.
			return
		}
		var meta map[string]string
		if json.Unmarshal(msg.Data, &meta) != nil {
			return
		}
		for k, v := range meta {
			if equalFold(k, "icy-title") && v != "" {
				m.mu.Lock()
				m.sawIcy = true
				m.mu.Unlock()
				emit(events, Event{Type: EventTitle, Title: v})
				return
			}
		}
	case "filename":
		// mpv's own filename property, observed before media-title: any
		// media-title equal to it is the same fallback, not a track.
		var fn string
		if json.Unmarshal(msg.Data, &fn) != nil {
			return
		}
		m.mu.Lock()
		m.mpvFilename = fn
		m.mu.Unlock()
	case "media-title":
		// Fallback for streams without ICY metadata. mpv sets media-title to
		// the URL or station name otherwise, so only forward plausible titles.
		var title string
		if json.Unmarshal(msg.Data, &title) != nil {
			return
		}
		m.mu.Lock()
		saw := m.sawIcy
		junk := title != "" && title == m.mpvFilename
		for _, f := range m.junkTitles {
			if title == f {
				junk = true
				break
			}
		}
		newToYou := m.queue != nil && m.queue.haveCur && m.queue.cur.NewToYou
		m.mu.Unlock()
		// Until an ICY title lands, mpv reports a URL-derived filename —
		// the last path segment, or decoded text after the last '/' of the
		// whole URL, query included. A filename is not a track title.
		if junk {
			return
		}
		if !saw && title != "" && !isURLish(title) {
			emit(events, Event{Type: EventTitle, Title: title, NewToYou: newToYou})
		}
	case "pause":
		var paused bool
		if json.Unmarshal(msg.Data, &paused) != nil {
			return
		}
		m.mu.Lock()
		if paused == m.paused {
			m.mu.Unlock()
			return
		}
		m.paused = paused
		idle := m.coreIdle
		m.mu.Unlock()
		if paused {
			emit(events, Event{Type: EventPaused})
		} else {
			emit(events, Event{Type: EventResumed})
			if !idle {
				// core-idle=false may have arrived first and been held
				// back by the pause gate below.
				emit(events, Event{Type: EventPlaying})
			}
		}
	case "core-idle":
		var idle bool
		if json.Unmarshal(msg.Data, &idle) != nil {
			return
		}
		m.mu.Lock()
		m.coreIdle = idle
		paused := m.paused
		if !idle && m.queue != nil {
			// Audio is flowing: the song got past resolving and opening, so
			// whatever failed before it was a bad run of luck, not a
			// broken station.
			m.queue.fails = 0
		}
		m.mu.Unlock()
		if !idle && !paused {
			emit(events, Event{Type: EventPlaying})
		}
	case "paused-for-cache":
		var paused bool
		if json.Unmarshal(msg.Data, &paused) != nil {
			return
		}
		m.mu.Lock()
		held := m.paused
		m.mu.Unlock()
		if held {
			return
		}
		if paused {
			emit(events, Event{Type: EventBuffering})
		} else {
			emit(events, Event{Type: EventPlaying})
		}
	case propMeterOverallRMS, propMeterRMSL, propMeterRMSR, propMeterPeak:
		// Arrives per audio frame; throttle to ~20Hz so the UI channel
		// never floods. Values are dB, roughly -60 (silence) to 0 (loud).
		db, err := parseDB(msg.Data)
		if err != nil {
			return
		}
		m.handleLevel(events, msg.Name, db)
	}
}

// parseDB reads an astats metadata value. mpv carries filter metadata as
// strings, but accept a bare number too so a future backend change is not
// a silent meter blackout.
func parseDB(raw json.RawMessage) (float64, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, err
	}
	return f, nil
}

// meterFloorDB / meterCeilDB bracket the range real broadcast audio actually
// occupies. Mapping the full -48..0 wasted most of the scale on levels no
// stream ever sends: a typical streaming master sits near -14 dBFS RMS,
// which landed at 0.71 of a range whose top the meter never reached, so the
// visible needle barely moved between a quiet ambient set and a loud one.
// -36 is below anything but a fade, -3 is essentially clipping.
const (
	meterFloorDB = -36.0
	meterCeilDB  = -3.0
)

// dbToUnit maps RMS dBFS onto 0..1 across the range broadcast audio uses.
func dbToUnit(db float64) float64 {
	lv := (db - meterFloorDB) / (meterCeilDB - meterFloorDB)
	if lv < 0 {
		return 0
	}
	if lv > 1 {
		return 1
	}
	return lv
}

// handleLevel folds one astats sample into the metering window and emits a
// combined stereo EventLevel at most every 50ms. A mono stream (or a build
// of astats that only reports channel 1) mirrors that channel into both.
func (m *MPV) handleLevel(events chan Event, name string, db float64) {
	lv := dbToUnit(db)
	m.mu.Lock()
	switch name {
	case propMeterOverallRMS:
		m.lvlOverall = lv
	case propMeterRMSL:
		m.lvlL = lv
		m.sawL = true
	case propMeterRMSR:
		m.lvlR = lv
		m.sawR = true
	case propMeterPeak:
		m.lvlPeak = lv
	}
	now := time.Now()
	if now.Sub(m.lastLevelEmit) < 50*time.Millisecond {
		m.mu.Unlock()
		return
	}
	m.lastLevelEmit = now
	l, r := m.lvlL, m.lvlR
	if !m.sawR {
		r = l
	}
	if !m.sawL {
		l = r
	}
	ev := Event{Type: EventLevel, Level: m.lvlOverall, LevelL: l, LevelR: r,
		Peak: m.lvlPeak, Stereo: m.sawL && m.sawR}
	m.mu.Unlock()
	emit(events, ev)
}

// emit drops rather than blocks: mpv reads must never wait on a slow UI.
// A lost event matters less than a wedged readLoop.
func emit(events chan Event, ev Event) {
	select {
	case events <- ev:
	default:
	}
}

func isURLish(s string) bool {
	return len(s) > 7 && (s[:7] == "http://" || (len(s) > 8 && s[:8] == "https://"))
}

// tailBuf is a bounded stderr sink: mpv writes diagnostics freely and only
// the tail matters — the reason it died.
type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

func newTailBuf() *tailBuf { return &tailBuf{} }

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-4096:]...)
	}
	return len(p), nil
}

// suffix renders the tail for an error message: "" when empty, otherwise
// ": " plus the last two lines.
func (t *tailBuf) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.buf))
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 2 {
		lines = lines[len(lines)-2:]
	}
	return ": " + strings.Join(lines, " ")
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
