package player

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock is the player's clock in the test's hands: a song's position is
// wall time, so a test that wants it to be two minutes in says so instead of
// waiting two minutes.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Unix(1_800_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// songLines is a song log a test can read.
type songLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *songLines) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *songLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// waitFor polls until a line equals want.
func (l *songLines) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, line := range l.all() {
			if line == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no song line %q; lines so far:\n%s", want, strings.Join(l.all(), "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startResumeMPV is startQueueMPV with the player's clock under the test's
// control and its song log captured.
func startResumeMPV(t *testing.T, src *scriptSource) (m *MPV, logPath string, seeds func() []string, clk *testClock, lines *songLines) {
	t.Helper()
	m, logPath, seeds = startQueueMPV(t, src)
	clk, lines = newTestClock(), &songLines{}
	m.mu.Lock()
	m.now = clk.Now
	m.mu.Unlock()
	m.SetSongLog(lines.add)
	return m, logPath, seeds, clk, lines
}

func lasting(s Song, d time.Duration) Song {
	s.Duration = d
	return s
}

// resumeLine is the loadfile that picks s up at start (seconds, as mpv is
// told them).
func resumeLine(s Song, start string) string {
	title := titleOf(s)
	return fmt.Sprintf(`"loadfile","%s","replace",-1,"start=%s,force-media-title=%%%d%%%s"`, s.URL, start, len(title), title)
}

// countCommands counts the lines of the fake's log holding exactly the
// command sent: its words, as the client wrote them.
func countCommands(t *testing.T, logPath, words string) int {
	t.Helper()
	n := 0
	for _, line := range logLines(t, logPath) {
		if strings.Contains(line, `"command":[`+words+`]`) {
			n++
		}
	}
	return n
}

// waitPlayingAbove waits for the count of EventPlaying to pass n: the session
// leaves Tuning on it, so a reload that never produced one would strike the
// station.
func waitPlayingAbove(t *testing.T, rec *recorder, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for rec.count(EventPlaying) <= n {
		if time.Now().After(deadline) {
			t.Fatalf("no EventPlaying after %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The session reconnects a discovery station by playing its URL again. With
// a song in progress that must pick the song up, not start the next one.
func TestReconnectResumesTheSong(t *testing.T) {
	one := lasting(song("one", "ok"), 240*time.Second)
	src := &scriptSource{songs: []Song{one, song("two", "ok")}}
	m, logPath, seeds, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	playing := rec.count(EventPlaying)

	clk.Advance(100 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "100"))
	waitPlayingAbove(t, rec, playing)
	settle()

	if n := countLoads(t, logPath); n != 2 {
		t.Fatalf("%d loadfiles, want the song and its reload:\n%s", n, logText(t, logPath))
	}
	if got := src.skips(); len(got) != 0 {
		t.Errorf("a reconnect charged the source with skips %v", got)
	}
	if n := src.nextCalls(); n != 2 {
		t.Errorf("%d lookups, want the first song and the one prefetch the queue already had", n)
	}
	if got := seeds(); len(got) != 1 {
		t.Errorf("the source was opened %d times (%q): the queue must be kept", len(got), got)
	}
	// Nothing silences the song first: the reload replaces it in place.
	if n := countCommands(t, logPath, `"stop"`); n != 1 {
		t.Errorf("%d stops, want only the first Play's", n)
	}
	// mpv's pause survives loadfile, and a reconnect after a long pause is
	// this very call: the station must come back audible.
	if n := countCommands(t, logPath, `"set_property","pause",false`); n != 2 {
		t.Errorf("%d unpauses, want one per Play", n)
	}
	if n := rec.count(EventStreamEnded) + rec.count(EventStreamError) + rec.count(EventStopped); n != 0 {
		t.Errorf("%d stream-end events from a reconnect", n)
	}
}

// A song within its last seconds is as good as over: the reconnect moves on,
// from the queue it has, without charging the source a skip nobody pressed.
func TestReconnectNearTheEndAdvances(t *testing.T) {
	one := lasting(song("one", "ok"), 240*time.Second)
	two := song("two", "ok")
	src := &scriptSource{songs: []Song{one, two, song("three", "ok")}}
	m, logPath, seeds, clk, lines := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))

	clk.Advance(230 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(two))
	rec.waitTitle(t, titleOf(two))
	settle()

	if text := logText(t, logPath); strings.Contains(text, "start=") {
		t.Fatalf("a song near its end was reloaded:\n%s", text)
	}
	if got := src.skips(); len(got) != 0 {
		t.Errorf("a reconnect charged the source with skips %v", got)
	}
	if got := seeds(); len(got) != 1 {
		t.Errorf("the source was opened %d times: the queue must be kept", len(got))
	}
	// Told apart from a listener's NEXT in the log: that is the evidence the
	// next cut-off report needs.
	lines.waitFor(t, "song one youtube expected 240s played 230s ended reconnect")
}

// A song that keeps failing a reconnect is not worth a third: it plays no
// further, and the walk goes on.
func TestResumeAtMostTwice(t *testing.T) {
	one := lasting(song("one", "ok"), 600*time.Second)
	two := song("two", "ok")
	src := &scriptSource{songs: []Song{one, two}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))

	clk.Advance(10 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "10"))
	// The position carries on from where the reload began.
	clk.Advance(5 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "15"))

	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(two))
	rec.waitTitle(t, titleOf(two))
	if n := countLoads(t, logPath); n != 4 {
		t.Fatalf("%d loadfiles, want the song, two reloads and the next song:\n%s", n, logText(t, logPath))
	}
}

// Seconds spent paused are not seconds of the song: resuming after ten
// minutes of pause must come back at 25 s, not past the end.
func TestPausedTimeNotCounted(t *testing.T) {
	one := lasting(song("one", "ok"), 240*time.Second)
	src := &scriptSource{songs: []Song{one, song("two", "ok")}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))

	clk.Advance(20 * time.Second)
	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "EventPaused", func(ev Event) bool { return ev.Type == EventPaused })
	clk.Advance(10 * time.Minute)
	playing := rec.count(EventPlaying)
	if err := m.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	waitPlayingAbove(t, rec, playing)
	clk.Advance(5 * time.Second)

	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "25"))
}

// Neither is a stall: while mpv waits on its cache nothing is playing, and a
// reconnect after a 20 s stall must not skip 20 s of the song.
func TestStalledTimeNotCounted(t *testing.T) {
	one := lasting(song("one", "ok"), 240*time.Second)
	src := &scriptSource{songs: []Song{one, song("two", "ok")}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))

	clk.Advance(30 * time.Second)
	// mpv reports a stall as core-idle: true.
	m.handleProperty(m.events, mpvMsg{Event: "property-change", Name: "core-idle", Data: []byte("true")})
	clk.Advance(25 * time.Second)

	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "30"))
}

// A reconnect to a song whose length nobody knows still resumes it, once
// there is a position to resume.
func TestReconnectUnknownDuration(t *testing.T) {
	t.Run("after audio", func(t *testing.T) {
		one := song("one", "ok")
		src := &scriptSource{songs: []Song{one, song("two", "ok")}}
		m, logPath, _, clk, _ := startResumeMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(one))
		clk.Advance(42 * time.Second)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		waitForLog(t, logPath, resumeLine(one, "42"))
	})
	t.Run("before any", func(t *testing.T) {
		one, two := song("one", "ok"), song("two", "ok")
		src := &scriptSource{songs: []Song{one, two, song("three", "ok")}}
		m, logPath, _, _, _ := startResumeMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(one))
		// Nothing has played: there is no position, and a song that never
		// got going is not worth reloading.
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		waitForLog(t, logPath, loadLine(two))
	})
}

// A signed URL lasts hours, not days; past the hour it is trusted no more,
// and the source cannot hand the same song out again.
func TestReconnectStaleURLAdvances(t *testing.T) {
	one := lasting(song("one", "ok"), 4*time.Hour)
	three := song("three", "ok")
	src := &scriptSource{songs: []Song{one, song("two", "ok"), three}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	clk.Advance(61 * time.Minute)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	// The prefetched "two" is as old as the song and is asked for afresh, as
	// it would be when the song simply played out.
	waitForLog(t, logPath, loadLine(three))
	if text := logText(t, logPath); strings.Contains(text, "start=") {
		t.Fatalf("an hour-old URL was reloaded:\n%s", text)
	}
}

// A reconnect while the next song is already on its way changes nothing but
// the pause: that song replaces this one in a moment, and a reload now would
// only be replaced again.
func TestReconnectWhileAdvancingLoadsNothing(t *testing.T) {
	gate := make(chan struct{})
	one := lasting(song("one", "ok"), 240*time.Second)
	two := song("two", "ok")
	src := &scriptSource{songs: []Song{one, two}, gates: map[int]chan struct{}{1: gate}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	clk.Advance(30 * time.Second)
	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	settle()
	if n := countLoads(t, logPath); n != 1 {
		t.Fatalf("%d loadfiles while the next song was still being found, want 1", n)
	}
	if n := countCommands(t, logPath, `"set_property","pause",false`); n != 2 {
		t.Errorf("%d unpauses, want one per Play", n)
	}
	close(gate)
	waitForLog(t, logPath, loadLine(two))
}

// Another station is a new station: its first song, from a fresh start.
func TestDifferentSeedStillStartsFresh(t *testing.T) {
	one := lasting(song("one", "ok"), 240*time.Second)
	three := song("three", "ok")
	src := &scriptSource{songs: []Song{one, song("two", "ok"), three}}
	m, logPath, seeds, clk, lines := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	clk.Advance(60 * time.Second)

	if err := m.Play("discover:y"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(three))
	if got := seeds(); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("sources opened for %q, want x then y", got)
	}
	if text := logText(t, logPath); strings.Contains(text, "start=") {
		t.Fatalf("another station's first song started part-way:\n%s", text)
	}
	if n := countCommands(t, logPath, `"stop"`); n != 2 {
		t.Errorf("%d stops, want one per fresh start", n)
	}
	lines.waitFor(t, "song one youtube expected 240s played 60s ended new station")
}

// The song log says how every song ended, whichever way, and never says where
// it came from.
func TestSongLogLines(t *testing.T) {
	s1 := lasting(song("one", "ok?c=WEB&expire=1"), 200*time.Second)
	s1.Key = "yt:aaa"
	s2 := lasting(song("two", "ok"), 180*time.Second)
	s2.Key = "bc:42"
	s3 := lasting(song("three", "ok?c=VISIONOS"), 240*time.Second)
	s3.Key = "yt:ccc"
	s4 := song("four", "ok")
	s4.Key = "yt:ddd"
	src := &scriptSource{songs: []Song{s1, s2, s3, s4, song("five", "ok"), song("six", "ok")}}
	m, logPath, _, clk, lines := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(s1))

	// It plays out.
	clk.Advance(200 * time.Second)
	endFile(t, m, "end-eof")
	rec.waitTitle(t, titleOf(s2))

	// The listener presses NEXT.
	clk.Advance(60 * time.Second)
	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(s3))

	// A reconnect picks it up, and then mpv gives up on it.
	clk.Advance(30 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(s3, "30"))
	clk.Advance(10 * time.Second)
	endFile(t, m, "end-error")
	rec.waitTitle(t, titleOf(s4))

	// Another station takes over.
	clk.Advance(7 * time.Second)
	if err := m.Play("discover:y"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(song("six", "ok")))

	want := []string{
		"song yt:aaa c=WEB expected 200s played 200s ended eof",
		"song bc:42 bandcamp expected 180s played 60s ended next",
		"song yt:ccc c=VISIONOS expected 240s played 30s ended resumed",
		"song yt:ccc c=VISIONOS expected 240s played 40s ended error: loading failed",
		"song yt:ddd youtube expected ?s played 7s ended new station",
	}
	got := lines.all()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("song log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, line := range got {
		if strings.Contains(line, "http") || strings.Contains(line, "stream.test") || strings.Contains(line, "expire") {
			t.Errorf("a song line names the URL: %q", line)
		}
	}
}

// Stopping from outside and a dying mpv end a song too, and the log must not
// call either a station change.
func TestSongLogStopAndDeath(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		s1 := lasting(song("one", "ok"), 200*time.Second)
		src := &scriptSource{songs: []Song{s1, song("two", "ok")}}
		m, _, _, clk, lines := startResumeMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(s1))
		clk.Advance(12 * time.Second)
		endFile(t, m, "stop")
		lines.waitFor(t, "song one youtube expected 200s played 12s ended stopped")
	})
	t.Run("death", func(t *testing.T) {
		s1 := lasting(song("one", "ok"), 200*time.Second)
		src := &scriptSource{songs: []Song{s1, song("two", "ok")}}
		m, _, _, clk, lines := startResumeMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(s1))
		clk.Advance(12 * time.Second)
		if err := m.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		lines.waitFor(t, "song one youtube expected 200s played 12s ended died")
	})
}

// A restarted mpv has forgotten the song; the session plays the station again
// on the new process, and that picks the song up where the old one stopped.
func TestRestartResumesTheSong(t *testing.T) {
	one := lasting(song("one", "ok"), 600*time.Second)
	// The walk's prefetch dies with the old process, so the new queue looks
	// ahead afresh: after "one" and the lost "two", it is "three" that follows.
	src := &scriptSource{songs: []Song{one, song("two", "ok"), song("three", "ok"), song("four", "ok")}}
	m, logPath, seeds, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	clk.Advance(100 * time.Second)

	if err := m.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "EventDied", func(ev Event) bool { return ev.Type == EventDied })
	// However long the session takes to notice, the clock is not running.
	clk.Advance(3 * time.Second)
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	rec2 := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "100"))
	rec2.waitTitle(t, titleOf(one))
	if got := seeds(); len(got) != 2 {
		t.Errorf("the source was opened %d times, want the Play and the one after the restart", len(got))
	}
	// The new process has nothing to silence.
	text := logText(t, logPath)
	after := text[strings.LastIndex(text, "SPAWN "):]
	if strings.Contains(after, `"command":["stop"]`) {
		t.Errorf("the restarted process was told to stop:\n%s", after)
	}

	// The count of reloads rides along, so a song cannot be reloaded for ever
	// by alternating crashes and reconnects.
	clk.Advance(5 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, resumeLine(one, "105"))
	clk.Advance(5 * time.Second)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(song("three", "ok")))
}

// Only the same station resumes: after a restart another seed plays its own
// first song, and the old song is forgotten.
func TestRestartOtherSeedStartsFresh(t *testing.T) {
	one := lasting(song("one", "ok"), 600*time.Second)
	three := song("three", "ok")
	src := &scriptSource{songs: []Song{one, song("two", "ok"), three}}
	m, logPath, _, clk, _ := startResumeMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(one))
	clk.Advance(100 * time.Second)
	if err := m.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "EventDied", func(ev Event) bool { return ev.Type == EventDied })
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := m.Play("discover:y"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, loadLine(three))

	// The old song is let go, not kept for a later Play of its station.
	m.mu.Lock()
	carried := m.carried
	m.mu.Unlock()
	if carried != nil {
		t.Error("a song from another station is still waiting to be resumed")
	}
}

func TestSongOptions(t *testing.T) {
	for _, c := range []struct {
		title string
		start time.Duration
		want  string
	}{
		{"", 0, ""},
		{"A - T", 0, "force-media-title=%5%A - T"},
		{"A - T", 100 * time.Second, "start=100,force-media-title=%5%A - T"},
		{"A, B = C", 90500 * time.Millisecond, "start=90.5,force-media-title=%8%A, B = C"},
		{"", 42 * time.Second, "start=42"},
		// Under a second is not worth a seek.
		{"A", 800 * time.Millisecond, "force-media-title=%1%A"},
	} {
		if got := songOptions(c.title, c.start); got != c.want {
			t.Errorf("songOptions(%q, %v) = %q, want %q", c.title, c.start, got, c.want)
		}
	}
}

func TestSongSourceNeverNamesTheURL(t *testing.T) {
	for _, c := range []struct {
		s    Song
		want string
	}{
		{Song{Key: "bc:1", URL: "https://t4.bcbits.com/stream/abc?c=WEB"}, "bandcamp"},
		{Song{Key: "yt:a", URL: "https://rr1.googlevideo.com/videoplayback?c=VISIONOS&id=1"}, "c=VISIONOS"},
		{Song{Key: "yt:a", URL: "https://rr1.googlevideo.com/videoplayback?id=1"}, "youtube"},
		{Song{Key: "yt:a", URL: "https://rr1.googlevideo.com/videoplayback?c=https://evil.test/x"}, "youtube"},
		{Song{Key: "yt:a", URL: "https://rr1.googlevideo.com/videoplayback?c="}, "youtube"},
		{Song{Key: "yt:a", URL: "not a url \x7f"}, "youtube"},
	} {
		if got := songSource(c.s); got != c.want {
			t.Errorf("songSource(%q) = %q, want %q", c.s.URL, got, c.want)
		}
	}
}
