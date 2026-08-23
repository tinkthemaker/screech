package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"screech/internal/version"
)

// MPV drives a headless mpv process over its JSON IPC socket (unix socket on
// linux/mac, named pipe on windows — see ipc_*.go). mpv does the heavy
// lifting: codecs, ICY metadata, reconnects, buffering.
type MPV struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	conn    net.Conn
	events  chan Event
	reqID   int
	closed  bool
	sawIcy  bool
	ipcPath string

	lastLevelEmit time.Time

	// Latest astats metering, combined into one EventLevel per emit window.
	lvlOverall float64
	lvlL, lvlR float64
	lvlPeak    float64
	sawL, sawR bool
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
	path := ipcPath(dataDir)
	cmd := exec.Command(mpvPath,
		"--idle=yes",
		"--no-video",
		"--no-terminal",
		"--really-quiet",
		"--volume=100",
		"--cache=yes",
		"--network-timeout=15",
		"--user-agent="+version.UserAgent(),
		// astats injects per-frame loudness into filter metadata; the wave
		// visualizer reads it so its amplitude is real, not theatrical.
		"--af=lavfi=[astats=metadata=1:reset=1]",
		"--input-ipc-server="+path,
	)
	configureCmd(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mpv (%s): %w — is mpv installed and on PATH?", mpvPath, err)
	}

	// The IPC endpoint appears shortly after mpv boots; poll for it.
	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		conn, err = dialIPC(path)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("connecting to mpv IPC: %w", err)
	}

	m := &MPV{cmd: cmd, conn: conn, events: make(chan Event, 64), ipcPath: path}
	for i, prop := range []string{"metadata", "media-title", "core-idle", "paused-for-cache",
		"af-metadata/lavfi.astats.Overall.RMS_level",
		"af-metadata/lavfi.astats.1.RMS_level",
		"af-metadata/lavfi.astats.2.RMS_level",
		"af-metadata/lavfi.astats.Overall.Peak_level"} {
		if err := m.send("observe_property", i+1, prop); err != nil {
			m.Close()
			return nil, err
		}
	}
	go m.readLoop()
	go func() {
		_ = cmd.Wait() // reap; readLoop notices the EOF
	}()
	return m, nil
}

func (m *MPV) Events() <-chan Event { return m.events }

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
	return m.send("set_property", "volume", percent)
}

func (m *MPV) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	_ = m.send("quit")
	time.Sleep(150 * time.Millisecond)
	if m.conn != nil {
		_ = m.conn.Close()
	}
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	_ = cleanupIPC(m.ipcPath)
	return nil
}

func (m *MPV) send(command ...any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return fmt.Errorf("mpv: not connected")
	}
	m.reqID++
	msg, err := json.Marshal(map[string]any{"command": command, "request_id": m.reqID})
	if err != nil {
		return err
	}
	_ = m.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = m.conn.Write(append(msg, '\n'))
	return err
}

type mpvMsg struct {
	Event  string          `json:"event"`
	Name   string          `json:"name"`
	Data   json.RawMessage `json:"data"`
	Reason string          `json:"reason"`
	Error  string          `json:"error"`
}

func (m *MPV) readLoop() {
	sc := bufio.NewScanner(m.conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var msg mpvMsg
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Event {
		case "property-change":
			m.handleProperty(msg)
		case "end-file":
			if msg.Reason == "error" {
				m.emit(Event{Type: EventStreamError, Err: fmt.Errorf("stream failed")})
			}
		}
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		m.emit(Event{Type: EventDied, Err: fmt.Errorf("mpv exited")})
	}
	close(m.events)
}

func (m *MPV) handleProperty(msg mpvMsg) {
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
				m.emit(Event{Type: EventTitle, Title: v})
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
		m.mu.Unlock()
		if !saw && title != "" && !isURLish(title) {
			m.emit(Event{Type: EventTitle, Title: title})
		}
	case "core-idle":
		var idle bool
		if json.Unmarshal(msg.Data, &idle) != nil {
			return
		}
		if !idle {
			m.emit(Event{Type: EventPlaying})
		}
	case "paused-for-cache":
		var paused bool
		if json.Unmarshal(msg.Data, &paused) != nil {
			return
		}
		if paused {
			m.emit(Event{Type: EventBuffering})
		} else {
			m.emit(Event{Type: EventPlaying})
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
		m.handleLevel(msg.Name, db)
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
func (m *MPV) handleLevel(name string, db float64) {
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
	m.emit(ev)
}

func (m *MPV) emit(ev Event) {
	select {
	case m.events <- ev:
	default: // never block mpv reads on a slow UI; drop stale events instead
	}
}

func isURLish(s string) bool {
	return len(s) > 7 && (s[:7] == "http://" || (len(s) > 8 && s[:8] == "https://"))
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
