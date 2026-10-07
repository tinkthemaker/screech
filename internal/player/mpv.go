package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"screech/internal/version"
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

	// curFile is the unescaped URL basename of the current stream and
	// curFileEsc its still-escaped form: until an ICY title arrives, mpv
	// reports media-title as that filename, which must not be mistaken for
	// a track title.
	curFile, curFileEsc string

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

// observedProperties are subscribed right after IPC connects, before the
// first event can arrive.
var observedProperties = []string{
	"metadata", "media-title", "core-idle", "paused-for-cache",
	"af-metadata/lavfi.astats.Overall.RMS_level",
	"af-metadata/lavfi.astats.1.RMS_level",
	"af-metadata/lavfi.astats.2.RMS_level",
	"af-metadata/lavfi.astats.Overall.Peak_level",
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
		"--af=lavfi=[astats=metadata=1:reset=1]",
		// Let ffmpeg ride out brief mid-stream drops on ICY streams; the
		// app-level reconnect in the TUI is the backstop.
		"--stream-lavf-o=reconnect_streamed=1",
		// Radio streams never need yt-dlp: without this, mpv's ytdl hook
		// runs it (when installed) on every URL that fails to load,
		// delaying the failure the tune watchdog and offline logic rely on.
		"--ytdl=no",
		"--input-ipc-server=" + path,
	}
	args = append(args, testExtraArgs...)
	cmd := exec.Command(resolveMpv(m.mpvPath), args...)
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
	m.sawIcy, m.sawL, m.sawR = false, false, false
	m.curFile, m.curFileEsc = "", ""
	m.mu.Unlock()
	// The loop owns conn and the channel: it closes the channel itself, so
	// a restarted generation's death can never land in the new stream.
	go m.readLoop(conn, m.events, stderr)
	return nil
}

func (m *MPV) Events() <-chan Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events
}

func (m *MPV) Play(streamURL string) error {
	u, err := url.Parse(streamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid stream URL %q: only http/https are supported", streamURL)
	}
	m.mu.Lock()
	m.sawIcy = false
	// Channel reporting is a property of the stream, not the process: a
	// mono station following a stereo one must not inherit its stereo flag.
	m.sawL, m.sawR = false, false
	m.loadPending = true
	m.curFile, m.curFileEsc = "", ""
	if u.Path != "" && u.Path != "/" {
		m.curFile = path.Base(u.Path)
		m.curFileEsc = path.Base(u.EscapedPath())
	}
	m.mu.Unlock()
	return m.send("loadfile", streamURL)
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
func (m *MPV) readLoop(conn net.Conn, events chan Event, stderr *tailBuf) {
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
			case "error":
				err := fmt.Errorf("stream failed")
				if msg.FileError != "" {
					err = fmt.Errorf("stream failed: %s", msg.FileError)
				}
				emit(events, Event{Type: EventStreamError, Err: err})
			case "eof":
				emit(events, Event{Type: EventStreamEnded})
			}
		}
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		emit(events, Event{Type: EventDied, Err: fmt.Errorf("mpv exited%s", stderr.suffix())})
	}
	close(events)
}

func (m *MPV) handleProperty(events chan Event, msg mpvMsg) {
	switch msg.Name {
	case "metadata":
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
	case "media-title":
		// Fallback for streams without ICY metadata. mpv sets media-title to
		// the URL or station name otherwise, so only forward plausible titles.
		var title string
		if json.Unmarshal(msg.Data, &title) != nil {
			return
		}
		m.mu.Lock()
		saw := m.sawIcy
		curFile, curFileEsc := m.curFile, m.curFileEsc
		m.mu.Unlock()
		// Until an ICY title lands, mpv reports the URL's last path segment
		// ("aac-128", "kexp128.mp3") — a filename is not a track title.
		if curFile != "" && (title == curFile || title == curFileEsc) {
			return
		}
		if !saw && title != "" && !isURLish(title) {
			emit(events, Event{Type: EventTitle, Title: title})
		}
	case "core-idle":
		var idle bool
		if json.Unmarshal(msg.Data, &idle) != nil {
			return
		}
		if !idle {
			emit(events, Event{Type: EventPlaying})
		}
	case "paused-for-cache":
		var paused bool
		if json.Unmarshal(msg.Data, &paused) != nil {
			return
		}
		if paused {
			emit(events, Event{Type: EventBuffering})
		} else {
			emit(events, Event{Type: EventPlaying})
		}
	case "af-metadata/lavfi.astats.Overall.RMS_level",
		"af-metadata/lavfi.astats.1.RMS_level",
		"af-metadata/lavfi.astats.2.RMS_level",
		"af-metadata/lavfi.astats.Overall.Peak_level":
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
	case "af-metadata/lavfi.astats.Overall.RMS_level":
		m.lvlOverall = lv
	case "af-metadata/lavfi.astats.1.RMS_level":
		m.lvlL = lv
		m.sawL = true
	case "af-metadata/lavfi.astats.2.RMS_level":
		m.lvlR = lv
		m.sawR = true
	case "af-metadata/lavfi.astats.Overall.Peak_level":
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
