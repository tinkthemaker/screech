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
)

// MPV drives a headless mpv process over its JSON IPC socket (unix socket on
// linux/mac, named pipe on windows — see ipc_*.go). mpv does the heavy
// lifting: codecs, ICY metadata, reconnects, buffering.
type MPV struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	conn   net.Conn
	events chan Event
	reqID  int
	closed bool
	sawIcy bool

	lastLevelEmit time.Time
}

func NewMPV(mpvPath string) (*MPV, error) {
	if mpvPath == "" {
		mpvPath = "mpv"
	}
	path := ipcPath()
	cmd := exec.Command(mpvPath,
		"--idle=yes",
		"--no-video",
		"--no-terminal",
		"--really-quiet",
		"--volume=100",
		"--cache=yes",
		"--network-timeout=15",
		"--user-agent=screech/0.2",
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

	m := &MPV{cmd: cmd, conn: conn, events: make(chan Event, 64)}
	for i, prop := range []string{"metadata", "media-title", "core-idle", "paused-for-cache",
		"af-metadata/lavfi.astats.Overall.RMS_level"} {
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

func (m *MPV) Play(url string) error {
	m.mu.Lock()
	m.sawIcy = false
	m.mu.Unlock()
	return m.send("loadfile", url)
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
	case "af-metadata/lavfi.astats.Overall.RMS_level":
		// Arrives per audio frame; throttle to ~20Hz so the UI channel
		// never floods. Value is dB, roughly -60 (silence) to 0 (loud).
		now := time.Now()
		if now.Sub(m.lastLevelEmit) < 50*time.Millisecond {
			return
		}
		var raw string
		if json.Unmarshal(msg.Data, &raw) != nil {
			return
		}
		db, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return
		}
		lv := 1 + db/48
		if lv < 0 {
			lv = 0
		}
		if lv > 1 {
			lv = 1
		}
		m.lastLevelEmit = now
		m.emit(Event{Type: EventLevel, Level: lv})
	}
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
