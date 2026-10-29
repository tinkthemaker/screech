package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain doubles the test binary as a fake mpv: when spawned back as the
// player backend (NewMPV(os.Args[0], ...) with SCREECH_FAKE_MPV in the env),
// it must run the fake before m.Run, since the mpv-shaped argv would kill
// the testing flag parser.
func TestMain(m *testing.M) {
	if os.Getenv("SCREECH_FAKE_MPV") != "" {
		os.Exit(runFakeMPV(os.Args[1:]))
	}
	if mode := os.Getenv("SCREECH_FAKE_YTDLP"); mode != "" {
		os.Exit(runFakeYTDLP(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runFakeMPV is the fake backend. It speaks just enough of mpv's JSON IPC to
// exercise the real client: one connection, per-command success replies, and
// a handful of scripted stream outcomes keyed off the URL's last segment.
// SCREECH_FAKE_MPV carries the mode (only "die-on-start" is special);
// SCREECH_FAKE_MPV_LOG, when set, gets the spawn argv and every received
// command line appended so tests can assert on what the client sent.
func runFakeMPV(args []string) int {
	var ipcPath string
	volume := 100
	for _, a := range args {
		if p, ok := strings.CutPrefix(a, "--input-ipc-server="); ok {
			ipcPath = p
		}
		if p, ok := strings.CutPrefix(a, "--volume="); ok {
			volume, _ = strconv.Atoi(p)
		}
	}
	logf := os.Getenv("SCREECH_FAKE_MPV_LOG")
	logLine := func(s string) {
		if logf == "" {
			return
		}
		f, err := os.OpenFile(logf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(f, s)
		_ = f.Sync()
		_ = f.Close()
	}
	logLine("SPAWN " + strings.Join(args, "\x1f"))

	if os.Getenv("SCREECH_FAKE_MPV") == "die-on-start" {
		fmt.Fprintln(os.Stderr, "fake mpv: bad option")
		return 1
	}

	ln, err := fakeListen(ipcPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake mpv: listen:", err)
		return 1
	}
	defer ln.Close()
	conn, err := ln.Accept()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake mpv: accept:", err)
		return 1
	}
	defer conn.Close()

	// Replies and events go through a writer goroutine: the client doesn't
	// read during the observe handshake, and a pending pipe write must not
	// stall the command reader or both sides deadlock.
	out := make(chan string, 256)
	go func() {
		for line := range out {
			if _, err := fmt.Fprintln(conn, line); err != nil {
				return
			}
		}
	}()
	send := func(format string, v ...any) {
		out <- fmt.Sprintf(format, v...)
	}

	var cur string
	// lateStart models a file whose start-file mpv has not reported yet: a
	// loadfile for ".../late" is accepted but reports nothing until a stop
	// (or another loadfile) forces the issue, the way a radio tune that is
	// only milliseconds old looks to a client that has since sent stop.
	var lateStart bool
	var paused bool
	// setPause mirrors how mpv treats the property: pause idles the output
	// even with a file loaded, so a change is followed by a core-idle flip.
	setPause := func(p bool) {
		if p == paused {
			return
		}
		paused = p
		send(`{"event":"property-change","name":"pause","data":%t}`, p)
		send(`{"event":"property-change","name":"core-idle","data":%t}`, p || cur == "")
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		logLine(line)
		var msg struct {
			Command   []any `json:"command"`
			RequestID int   `json:"request_id"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || len(msg.Command) == 0 {
			continue
		}
		name, _ := msg.Command[0].(string)
		if name == "get_property" && len(msg.Command) > 1 && msg.Command[1] == "volume" {
			send(`{"request_id":%d,"error":"success","data":%d}`, msg.RequestID, volume)
		} else {
			send(`{"request_id":%d,"error":"success","data":null}`, msg.RequestID)
		}

		switch name {
		case "stop":
			// Like mpv: the loaded file ends with reason stop, then idle. A
			// file that was still waiting to start reports its start-file
			// first: its end-file can't come before it.
			if lateStart {
				send(`{"event":"start-file"}`)
				lateStart = false
			}
			if cur != "" {
				send(`{"event":"end-file","reason":"stop"}`)
				cur = ""
				send(`{"event":"property-change","name":"core-idle","data":true}`)
			}
		case "loadfile":
			url, _ := msg.Command[1].(string)
			base := url[strings.LastIndex(url, "/")+1:]
			// loadfile <url> replace -1 force-media-title=%n%<title>: the
			// forced title replaces the ICY one, as mpv's media-title does.
			forced := ""
			if len(msg.Command) > 4 {
				opt, _ := msg.Command[4].(string)
				if v, ok := strings.CutPrefix(opt, "force-media-title=%"); ok {
					if n, rest, ok := strings.Cut(v, "%"); ok {
						if size, err := strconv.Atoi(n); err == nil && size <= len(rest) {
							forced = rest[:size]
						}
					}
				}
			}
			if lateStart {
				// Replaced before it ever started: mpv reports nothing for
				// it, no start-file and no end-file.
				lateStart = false
				cur = ""
			}
			if cur != "" {
				// Deliberately the worst case: the replaced file's end-file
				// lands inside the new load's window. The client must
				// swallow it (loadPending) or a stale EOF leaks as
				// EventStreamEnded for a stream that just started.
				send(`{"event":"end-file","reason":"eof"}`)
			}
			switch base {
			case "eof":
				send(`{"event":"start-file"}`)
				send(`{"event":"property-change","name":"core-idle","data":false}`)
				send(`{"event":"end-file","reason":"eof"}`)
			case "error":
				send(`{"event":"start-file"}`)
				send(`{"event":"end-file","reason":"error","file_error":"loading failed"}`)
			case "late":
				lateStart = true
			case "crash":
				fmt.Fprintln(os.Stderr, "fake mpv: boom")
				return 3
			case "aac-128":
				send(`{"event":"start-file"}`)
				send(`{"event":"property-change","name":"media-title","data":"aac-128"}`)
				send(`{"event":"property-change","name":"core-idle","data":false}`)
			default:
				send(`{"event":"start-file"}`)
				send(`{"event":"property-change","name":"core-idle","data":false}`)
				if forced != "" {
					title, _ := json.Marshal(forced)
					send(`{"event":"property-change","name":"media-title","data":%s}`, title)
				} else {
					send(`{"event":"property-change","name":"metadata","data":{"icy-title":"Fake Artist - Fake Title"}}`)
				}
			}
			cur = base
		case "set_property":
			if len(msg.Command) > 2 && msg.Command[1] == "volume" {
				if v, ok := msg.Command[2].(float64); ok {
					volume = int(v)
					// Real mpv echoes the change back as an event.
					send(`{"event":"property-change","name":"volume","data":%d}`, volume)
				}
			}
			if len(msg.Command) > 2 && msg.Command[1] == "pause" {
				if p, ok := msg.Command[2].(bool); ok {
					setPause(p)
				}
			}
		case "script-message":
			// `script-message fake-external <verb>` stands in for the OS
			// media controls, which drive the same properties directly.
			if len(msg.Command) > 2 && msg.Command[1] == "fake-external" {
				switch msg.Command[2] {
				case "pause":
					setPause(true)
				case "resume":
					setPause(false)
				case "stop":
					// The flyout's Stop runs `stop`: end-file with its
					// own reason, nothing loaded any more.
					send(`{"event":"end-file","reason":"stop"}`)
					cur = ""
					send(`{"event":"property-change","name":"core-idle","data":true}`)
				case "end-eof":
					// The loaded file plays out on its own: end-file eof,
					// then idle (--idle=yes). What a song reaching its last
					// second looks like to the client.
					if cur != "" {
						send(`{"event":"end-file","reason":"eof"}`)
						cur = ""
						send(`{"event":"property-change","name":"core-idle","data":true}`)
					}
				case "end-error":
					// The loaded file dies mid-song (a signed URL expiring,
					// a server hanging up).
					if cur != "" {
						send(`{"event":"end-file","reason":"error","file_error":"loading failed"}`)
						cur = ""
						send(`{"event":"property-change","name":"core-idle","data":true}`)
					}
				case "idle-flip":
					send(`{"event":"property-change","name":"core-idle","data":false}`)
				case "cache-flip":
					send(`{"event":"property-change","name":"paused-for-cache","data":true}`)
					send(`{"event":"property-change","name":"paused-for-cache","data":false}`)
				}
			}
		case "quit":
			return 0
		}
	}
	return 0
}

// startFakeMPV launches the test binary back at itself through the real
// production path. dir stays short on purpose: the IPC socket lives in it
// on unix, and macOS caps socket paths around a hundred bytes.
func startFakeMPV(t *testing.T, mode string) (*MPV, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sfm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	logPath := filepath.Join(dir, "mpv.log")
	t.Setenv("SCREECH_FAKE_MPV", mode)
	t.Setenv("SCREECH_FAKE_MPV_LOG", logPath)
	m, err := NewMPV(os.Args[0], dir)
	if err != nil {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("NewMPV: %v\nfake log: %s", err, b)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, logPath
}

func waitEvent(t *testing.T, ch <-chan Event, timeout time.Duration) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// collectFor drains the event channel for d, reporting whether it closed.
func collectFor(ch <-chan Event, d time.Duration) ([]Event, bool) {
	var evs []Event
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs, true
			}
			evs = append(evs, ev)
		case <-timer.C:
			return evs, false
		}
	}
}

func waitForEvent(t *testing.T, ch <-chan Event, timeout time.Duration, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ev := waitEvent(t, ch, time.Until(deadline))
		if pred(ev) {
			return ev
		}
	}
	t.Fatal("event never arrived")
	return Event{}
}

func logText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// waitForLog polls until the fake's log contains want; the child writes are
// already synced, so this is only a read-side poll.
func waitForLog(t *testing.T, path, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		text := logText(t, path)
		if strings.Contains(text, want) {
			return text
		}
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q; log so far:\n%s", want, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// spawnArgs extracts the argv vector from a SPAWN log line.
func spawnArgs(line string) []string {
	return strings.Split(strings.TrimPrefix(line, "SPAWN "), "\x1f")
}

func logLines(t *testing.T, path string) []string {
	var out []string
	for _, line := range strings.Split(logText(t, path), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestFakeHandshakeObservesProperties(t *testing.T) {
	_, logPath := startFakeMPV(t, "default")

	waitForLog(t, logPath, "SPAWN ")
	args := spawnArgs(logLines(t, logPath)[0])
	for _, want := range []string{"--idle=yes", "--ytdl=no", "--volume=100"} {
		found := false
		for _, a := range args {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("spawn args missing %q: %v", want, args)
		}
	}
	ipcSeen := false
	for _, a := range args {
		if strings.HasPrefix(a, "--input-ipc-server=") && len(a) > len("--input-ipc-server=") {
			ipcSeen = true
		}
	}
	if !ipcSeen {
		t.Errorf("spawn args missing --input-ipc-server: %v", args)
	}

	// The client subscribes every entry of observedProperties right after
	// the socket connects; wait for the whole set to land in the log.
	deadline := time.Now().Add(3 * time.Second)
	var got map[string]bool
	for time.Now().Before(deadline) {
		got = map[string]bool{}
		for _, line := range logLines(t, logPath) {
			var msg struct {
				Command []any `json:"command"`
			}
			if json.Unmarshal([]byte(line), &msg) != nil || len(msg.Command) < 3 {
				continue
			}
			if msg.Command[0] == "observe_property" {
				if prop, ok := msg.Command[2].(string); ok {
					got[prop] = true
				}
			}
		}
		if len(got) == len(observedProperties) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, prop := range observedProperties {
		if !got[prop] {
			t.Errorf("no observe_property for %q", prop)
		}
	}
}

func TestFakePlayYieldsTitle(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	var sawPlaying, sawTitle bool
	deadline := time.Now().Add(3 * time.Second)
	for !(sawPlaying && sawTitle) && time.Now().Before(deadline) {
		ev := waitEvent(t, m.Events(), time.Until(deadline))
		switch ev.Type {
		case EventPlaying:
			sawPlaying = true
		case EventTitle:
			sawTitle = true
			if ev.Title != "Fake Artist - Fake Title" {
				t.Fatalf("title = %q, want Fake Artist - Fake Title", ev.Title)
			}
		}
	}
	if !sawPlaying || !sawTitle {
		t.Fatalf("playing=%v title=%v", sawPlaying, sawTitle)
	}
}

func TestFakeMediaTitleFilenameSuppressed(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/aac-128"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	evs, _ := collectFor(m.Events(), 400*time.Millisecond)
	for _, ev := range evs {
		if ev.Type == EventTitle {
			t.Fatalf("filename media-title leaked as track title: %q", ev.Title)
		}
	}
}

func TestFakeStreamEndAndError(t *testing.T) {
	t.Run("eof", func(t *testing.T) {
		m, _ := startFakeMPV(t, "default")
		if err := m.Play("http://stream.test/eof"); err != nil {
			t.Fatal(err)
		}
		waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventStreamEnded })
	})
	t.Run("error", func(t *testing.T) {
		m, _ := startFakeMPV(t, "default")
		if err := m.Play("http://stream.test/error"); err != nil {
			t.Fatal(err)
		}
		ev := waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventStreamError })
		if ev.Err == nil || !strings.Contains(ev.Err.Error(), "loading failed") {
			t.Fatalf("error = %v, want it to contain %q", ev.Err, "loading failed")
		}
	})
}

func TestFakeReplaceSwallowsEndFile(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	if err := m.Play("http://stream.test/ok2"); err != nil {
		t.Fatal(err)
	}
	var sawPlaying bool
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				t.Fatal("event channel closed mid-replace")
			}
			if ev.Type == EventStreamEnded {
				t.Fatal("replaced file's end-file leaked as EventStreamEnded")
			}
			if ev.Type == EventPlaying {
				sawPlaying = true
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	if !sawPlaying {
		t.Fatal("no EventPlaying for the replacement stream")
	}
}

func TestFakeCrashReportsDied(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	// The fake dies on the loadfile; the trailing unpause write can land
	// after the pipe closed, so an error here is fine.
	_ = m.Play("http://stream.test/crash")
	ev := waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventDied })
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "boom") {
		t.Fatalf("died error = %v, want it to contain %q", ev.Err, "boom")
	}
	if _, ok := <-m.Events(); ok {
		t.Fatal("event channel still open after backend death")
	}
}

func TestFakeRestartRespawnsWithVolume(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	if err := m.SetVolume(40); err != nil {
		t.Fatal(err)
	}
	old := m.Events()
	_ = m.Play("http://stream.test/crash")
	waitForEvent(t, old, 3*time.Second, func(ev Event) bool { return ev.Type == EventDied })
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	text := waitForLog(t, logPath, "--volume=40")
	var spawns int
	for _, line := range logLines(t, logPath) {
		if strings.HasPrefix(line, "SPAWN ") {
			spawns++
			for _, a := range spawnArgs(line) {
				if a == "--volume=40" && spawns < 2 {
					t.Fatalf("first spawn unexpectedly carried --volume=40:\n%s", text)
				}
			}
		}
	}
	ch := m.Events()
	if ch == old {
		t.Fatal("Events() did not return a fresh channel after restart")
	}
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
}

func TestFakeDieOnStartFailsFast(t *testing.T) {
	dir, err := os.MkdirTemp("", "sfm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("SCREECH_FAKE_MPV", "die-on-start")
	start := time.Now()
	m, err := NewMPV(os.Args[0], dir)
	elapsed := time.Since(start)
	if err == nil {
		_ = m.Close()
		t.Fatal("NewMPV succeeded against a backend that dies on spawn")
	}
	if !strings.Contains(err.Error(), "bad option") {
		t.Fatalf("error = %v, want it to contain %q", err, "bad option")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("took %v to report a dead-on-arrival backend", elapsed)
	}
}

func TestFakeSetPausedEvents(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })

	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPaused })

	if err := m.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	// Resumed must surface before Playing: a core-idle flip that arrived
	// while paused was held back, and the resume replays it in order.
	ev := waitEvent(t, m.Events(), 3*time.Second)
	if ev.Type != EventResumed {
		t.Fatalf("first event after unpause = %v, want EventResumed", ev.Type)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
}

func TestFakePausedSuppressesPlaySignals(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPaused })

	// While paused, a core-idle or paused-for-cache flip must not surface
	// as playing/buffering.
	if err := m.send("script-message", "fake-external", "idle-flip"); err != nil {
		t.Fatal(err)
	}
	if err := m.send("script-message", "fake-external", "cache-flip"); err != nil {
		t.Fatal(err)
	}
	evs, _ := collectFor(m.Events(), 400*time.Millisecond)
	for _, ev := range evs {
		t.Fatalf("event leaked while paused: %v", ev.Type)
	}
}

func TestFakePlayUnpausesStream(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPaused })

	if err := m.Play("http://stream.test/ok2"); err != nil {
		t.Fatal(err)
	}
	ev := waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool {
		return ev.Type == EventResumed || ev.Type == EventPlaying
	})
	if ev.Type != EventResumed {
		t.Fatalf("first event = %v, want EventResumed", ev.Type)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	// The resume/playing events mean the fake has processed the unpause,
	// so its log now contains both commands. The new load must precede
	// the unpause so no stale buffer blips.
	text := waitForLog(t, logPath, `"loadfile","http://stream.test/ok2"`)
	load := strings.Index(text, `"loadfile","http://stream.test/ok2"`)
	unpause := strings.Index(text[load:], `"set_property","pause",false`)
	if load < 0 || unpause < 0 {
		t.Fatalf("expected loadfile before pause=false:\n%s", text)
	}
}

func TestFakeExternalPause(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	// Media keys / the OS flyout drive mpv's pause property directly —
	// the client sees the same property-change either way.
	if err := m.send("script-message", "fake-external", "pause"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPaused })
	if err := m.send("script-message", "fake-external", "resume"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventResumed })
}

func TestFakeExternalStop(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	if err := m.send("script-message", "fake-external", "stop"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventStopped })
}

func TestFakeCloseQuitsCleanly(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, `"quit"`)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				if err := m.Close(); err != nil {
					t.Fatalf("second Close = %v, want nil", err)
				}
				return
			}
			if ev.Type == EventDied {
				t.Fatalf("deliberate Close emitted EventDied: %v", ev.Err)
			}
		case <-deadline:
			t.Fatal("event channel never closed after Close")
		}
	}
}
