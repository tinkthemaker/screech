package player

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mpvTimePos asks the running mpv where it is in the file, over a second IPC
// connection of the test's own: the player keeps no reply channel, and mpv
// serves any number of clients.
func mpvTimePos(t *testing.T, m *MPV) float64 {
	t.Helper()
	m.mu.Lock()
	path := m.ipcPath
	m.mu.Unlock()
	conn, err := dialIPC(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := writeCmd(conn, 7001, "get_property", "time-pos"); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var reply struct {
			RequestID int      `json:"request_id"`
			Error     string   `json:"error"`
			Data      *float64 `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &reply) != nil || reply.RequestID != 7001 {
			continue // an event, not the answer
		}
		if reply.Error != "success" || reply.Data == nil {
			t.Fatalf("time-pos: %s", sc.Text())
		}
		return *reply.Data
	}
	t.Fatalf("no answer to time-pos: %v", sc.Err())
	return 0
}

// TestMPVResumeAfterStall plays a song through a real mpv whose server stops
// sending mid-song, the way a stalled connection does, and reconnects the
// station while mpv is waiting on its cache. The song must come back where the
// audio stopped: not at the start (loadfile's start= and the forced title
// share one option list, which only a real mpv can vouch for), and not at
// the wall time since the song began, which counts the stall. The first
// request's body is cut off after five seconds of audio (mpv will not start
// on less than its 128 KiB stream buffer, about four); later requests get
// the whole file.
func TestMPVResumeAfterStall(t *testing.T) {
	if testing.Short() {
		t.Skip("real mpv process; skipping in -short mode")
	}
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		t.Skipf("mpv not on PATH: %v", err)
	}
	testExtraArgs = []string{"--ao=null"}
	t.Cleanup(func() { testExtraArgs = nil })

	wav := sineWAV(20 * time.Second)
	const header, perSecond = 44, 8000 * 4
	var requests atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 1 {
			http.ServeContent(w, r, "song.wav", time.Time{}, bytes.NewReader(wav))
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", fmt.Sprint(len(wav)))
		_, _ = w.Write(wav[:header+5*perSecond])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	song := Song{Key: "yt:real", Artist: "Ólafur Arnalds", Title: `Saman, "live" 100%`,
		URL: srv.URL + "/song.wav", Duration: 60 * time.Second}
	src := &scriptSource{songs: []Song{song, {Key: "yt:next", Artist: "B", Title: "N", URL: srv.URL + "/next.wav"}}}
	m, err := NewMPV(mpvPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetSongSources(func(string) (SongSource, error) { return src, nil })
	lines := &songLines{}
	m.SetSongLog(lines.add)
	rec := record(m)

	if err := m.Play("discover:real"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(song))
	// The five seconds play out, and mpv waits for more.
	rec.waitFor(t, "EventBuffering", func(ev Event) bool { return ev.Type == EventBuffering })
	time.Sleep(1500 * time.Millisecond)
	playing := rec.count(EventPlaying)

	if err := m.Play("discover:real"); err != nil {
		t.Fatal(err)
	}
	// The reload locks, as the session needs to leave Tuning, and its second
	// request is the one that gets the whole file.
	waitPlayingAbove(t, rec, playing)
	if n := requests.Load(); n < 2 {
		t.Fatalf("%d requests: the song was never opened again", n)
	}
	time.Sleep(1200 * time.Millisecond)
	// Five seconds of audio played before the stall and 1.2 s since the
	// reload, give or take mpv's start-up. Restarting would read about 1; the
	// wall time of the stall would read about 8.
	pos := mpvTimePos(t, m)
	t.Logf("time-pos %.2f s after the reconnect", pos)
	if pos < 5.4 || pos > 7.0 {
		t.Errorf("time-pos %.1f s after the reconnect, want about 6.2", pos)
	}
	if n := rec.count(EventStreamEnded) + rec.count(EventStreamError) + rec.count(EventStopped); n != 0 {
		t.Errorf("%d stream-end events from a reconnect", n)
	}
	for _, line := range lines.all() {
		if strings.Contains(line, "http") || strings.Contains(line, "127.0.0.1") {
			t.Errorf("a song line names the URL: %q", line)
		}
	}
}
