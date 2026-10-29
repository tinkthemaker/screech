package player

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

// sineWAV returns a stereo PCM WAV of a mid-level sine: astats reports
// -inf on silence, so a meter assertion needs real signal.
func sineWAV(dur time.Duration) []byte {
	const rate, chans, bps = 8000, 2, 2
	n := int(dur.Seconds()) * rate
	data := make([]byte, n*chans*bps)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(data[i*4+2:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))              // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(chans))          // channels
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))           // rate
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*chans*bps)) // byte rate
	_ = binary.Write(&b, binary.LittleEndian, uint16(chans*bps))      // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(8*bps))          // bits
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	_, _ = b.Write(data)
	return b.Bytes()
}

// waitLevel fails unless an EventLevel carrying real signal arrives
// within the deadline.
func waitLevel(t *testing.T, m *MPV, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				t.Fatal("event channel closed before any EventLevel arrived")
			}
			if ev.Type == EventLevel && ev.LevelL > 0 {
				return
			}
			if ev.Type == EventStreamEnded {
				t.Fatal("stream ended before any EventLevel arrived")
			}
		case <-deadline:
			t.Fatal("no EventLevel with signal before the deadline")
		}
	}
}

// TestMPVLifecycle drives a real mpv through the whole spawn → play → eof →
// kill → restart arc. Skipped without mpv on PATH or under -short.
func TestMPVLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("real mpv process; skipping in -short mode")
	}
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		t.Skipf("mpv not on PATH: %v", err)
	}
	testExtraArgs = []string{"--ao=null"} // CI hosts can lack audio hardware
	t.Cleanup(func() { testExtraArgs = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(sineWAV(2500 * time.Millisecond))
	}))
	defer srv.Close()

	m, err := NewMPV(mpvPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Play(srv.URL + "/radio.wav"); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventPlaying)

	// The meter must carry real astats data: an unlabelled lavfi filter
	// left the observed keys empty and the wave flat forever.
	waitLevel(t, m, 5*time.Second)
	wantEvent(t, m, EventStreamEnded) // finite file ends on its own

	// Killing the process must surface as EventDied + a closed channel —
	// the readLoop's own teardown, not Close().
	if err := m.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventDied)
	select {
	case _, ok := <-m.Events():
		if ok {
			t.Fatal("channel must close after EventDied")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after process kill")
	}

	if err := m.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := m.Play(srv.URL + "/radio.wav"); err != nil {
		t.Fatal(err)
	}
	// Events() now yields the new process's stream — playing again proves
	// the restarted IPC link is live.
	wantEvent(t, m, EventPlaying)

	// Pause/resume round-trip against the real property.
	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventPaused)
	if err := m.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventResumed)

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(); err == nil {
		t.Error("Restart on a closed MPV should fail")
	}
}

// TestMPVForcedTitle checks against a real mpv that a resolved page's title
// survives loadfile's option list: mpv's %n% quoting must carry commas,
// quotes, '=' and '%' through untouched, or a page title becomes a broken
// option or a mangled track. The extractor is faked, so no network.
func TestMPVForcedTitle(t *testing.T) {
	if testing.Short() {
		t.Skip("real mpv process; skipping in -short mode")
	}
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		t.Skipf("mpv not on PATH: %v", err)
	}
	testExtraArgs = []string{"--ao=null"}
	t.Cleanup(func() { testExtraArgs = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(sineWAV(2500 * time.Millisecond))
	}))
	defer srv.Close()

	m, err := NewMPV(mpvPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	title := `Ólafur Arnalds, "Saman" - live=1, 100% [édit]`
	m.SetExtractor(&stubExtractor{r: Resolved{URL: srv.URL + "/videoplayback", Title: title}})
	if err := m.Play("https://www.youtube.com/watch?v=lifecycle"); err != nil {
		t.Fatal(err)
	}
	ev := waitForEvent(t, m.Events(), 10*time.Second, func(ev Event) bool {
		if ev.Type == EventStreamError {
			t.Fatalf("mpv refused the load: %v", ev.Err)
		}
		return ev.Type == EventTitle
	})
	if ev.Title != title {
		t.Fatalf("media-title = %q, want %q", ev.Title, title)
	}
}

// TestMPVSongQueue walks a discovery station through a real mpv: the song's
// own artist and title must survive loadfile's option list, a song that ends
// must hand over to the next without the stream-ended event the session
// would reconnect on, and NEXT must replace a song that is still playing.
// The source is scripted, so no network and no yt-dlp.
func TestMPVSongQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("real mpv process; skipping in -short mode")
	}
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		t.Skipf("mpv not on PATH: %v", err)
	}
	testExtraArgs = []string{"--ao=null"}
	t.Cleanup(func() { testExtraArgs = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(sineWAV(2 * time.Second))
	}))
	defer srv.Close()

	songs := []Song{
		{Key: "1", Artist: "Ólafur Arnalds", Title: `Saman, "live" 100%`, URL: srv.URL + "/one.wav", NewToYou: true},
		{Key: "2", Artist: "Entombed", Title: "Left Hand Path", URL: srv.URL + "/two.wav"},
		{Key: "3", Artist: "Nils Frahm", Title: "Says", URL: srv.URL + "/three.wav", NewToYou: true},
	}
	src := &scriptSource{songs: songs}
	m, err := NewMPV(mpvPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.SetSongSources(func(string) (SongSource, error) { return src, nil })
	rec := record(m)

	if err := m.Play("discover:lifecycle"); err != nil {
		t.Fatal(err)
	}
	if ev := rec.waitTitle(t, titleOf(songs[0])); !ev.NewToYou {
		t.Error("first song lost its NewToYou")
	}
	// The first song plays out (about two seconds) and the second follows.
	if ev := rec.waitTitle(t, titleOf(songs[1])); ev.NewToYou {
		t.Error("second song gained a NewToYou it never had")
	}
	// NEXT while the second is still playing replaces it.
	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	if ev := rec.waitTitle(t, titleOf(songs[2])); !ev.NewToYou {
		t.Error("third song lost its NewToYou")
	}
	if got := src.skips(); len(got) != 1 || !got[0] {
		t.Errorf("skips = %v, want one quick skip", got)
	}
	if n := rec.count(EventStreamEnded) + rec.count(EventStreamError) + rec.count(EventStopped); n != 0 {
		t.Errorf("%d stream-end events while songs handed over", n)
	}
}
