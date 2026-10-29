package player

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptSource is a SongSource that hands out a fixed list, so a test
// decides exactly what the player is offered and when. Call i of Next
// returns songs[i], unless errs[i] says otherwise, and blocks on gates[i]
// first when one is set (giving up if its context ends, like a real lookup).
type scriptSource struct {
	mu        sync.Mutex
	songs     []Song
	errs      map[int]error
	gates     map[int]chan struct{}
	calls     int
	deadlines []time.Duration // time left on each Next call's context
	skipped   []bool
	loved     int
}

func (s *scriptSource) Next(ctx context.Context) (Song, error) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	gate, err := s.gates[i], s.errs[i]
	var out Song
	if i < len(s.songs) {
		out = s.songs[i]
	} else if err == nil {
		err = errors.New("script exhausted")
	}
	left := time.Duration(0)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	s.deadlines = append(s.deadlines, left)
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return Song{}, ctx.Err()
		}
	}
	if err != nil {
		return Song{}, err
	}
	return out, nil
}

func (s *scriptSource) Skipped(quick bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = append(s.skipped, quick)
}

func (s *scriptSource) Loved() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loved++
}

func (s *scriptSource) nextCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *scriptSource) skips() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.skipped...)
}

// song makes a distinct song whose URL ends in base, the segment the fake
// mpv keys its scripted outcome on ("ok", "eof", "error").
func song(name, base string) Song {
	return Song{
		Key:    name,
		Artist: "Artist " + name,
		Title:  "Title " + name,
		URL:    "http://stream.test/" + name + "/" + base,
	}
}

func titleOf(s Song) string { return s.Artist + " - " + s.Title }

func loadLine(s Song) string {
	t := titleOf(s)
	return fmt.Sprintf(`"loadfile","%s","replace",-1,"force-media-title=%%%d%%%s"`, s.URL, len(t), t)
}

func countLoads(t *testing.T, logPath string) int {
	t.Helper()
	n := 0
	for _, line := range logLines(t, logPath) {
		if strings.Contains(line, `"loadfile"`) {
			n++
		}
	}
	return n
}

// startQueueMPV is startFakeMPV with src behind every discover: URL; seeds
// reports what Play handed the opener.
func startQueueMPV(t *testing.T, src *scriptSource) (m *MPV, logPath string, seeds func() []string) {
	t.Helper()
	m, logPath = startFakeMPV(t, "default")
	var mu sync.Mutex
	var got []string
	m.SetSongSources(func(seed string) (SongSource, error) {
		mu.Lock()
		got = append(got, seed)
		mu.Unlock()
		return src, nil
	})
	return m, logPath, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// recorder keeps every event the player emits, so a test can assert on
// what never happened as well as on what did.
type recorder struct {
	mu  sync.Mutex
	evs []Event
}

func record(m *MPV) *recorder {
	r := &recorder{}
	ch := m.Events()
	go func() {
		for ev := range ch {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *recorder) count(typ EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// waitFor polls until an event satisfies pred.
func (r *recorder) waitFor(t *testing.T, what string, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		for _, ev := range r.evs {
			if pred(ev) {
				r.mu.Unlock()
				return ev
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("never saw %s; events: %+v", what, r.evs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *recorder) waitTitle(t *testing.T, title string) Event {
	t.Helper()
	return r.waitFor(t, "title "+title, func(ev Event) bool { return ev.Type == EventTitle && ev.Title == title })
}

func endFile(t *testing.T, m *MPV, verb string) {
	t.Helper()
	if err := m.send("script-message", "fake-external", verb); err != nil {
		t.Fatal(err)
	}
}

// settle gives events that must NOT happen a moment to show up.
func settle() { time.Sleep(300 * time.Millisecond) }

func TestQueuePlaysFirstSongWithTitle(t *testing.T) {
	first := Song{Key: "k1", Artist: "A", Title: "T", URL: "http://stream.test/one/ok"}
	src := &scriptSource{songs: []Song{first, song("two", "ok")}}
	m, logPath, seeds := startQueueMPV(t, src)
	rec := record(m)

	if err := m.Play("discover:swedish death metal"); err != nil {
		t.Fatal(err)
	}
	waitForLog(t, logPath, `"loadfile","http://stream.test/one/ok","replace",-1,"force-media-title=%5%A - T"`)
	if got := seeds(); len(got) != 1 || got[0] != "swedish death metal" {
		t.Fatalf("the source was opened with %q, want the text after discover:", got)
	}
	ev := rec.waitTitle(t, "A - T")
	if ev.NewToYou {
		t.Error("a song not marked new must not carry NewToYou")
	}
	// Like any station, a discovery must never start silent, and the old
	// station is silenced before the lookup, not after it.
	text := waitForLog(t, logPath, `"set_property","pause",false`)
	stop, load, unpause := strings.Index(text, `"stop"`), strings.Index(text, `"loadfile"`), strings.Index(text, `"set_property","pause",false`)
	if stop < 0 || stop > load || load > unpause {
		t.Fatalf("want stop, then loadfile, then unpause:\n%s", text)
	}
	if strings.Contains(text, `"loadfile","discover:`) {
		t.Fatal("the sentinel itself reached mpv")
	}
}

func TestQueueAdvancesOnEOFWithoutStreamEnded(t *testing.T) {
	songs := []Song{song("one", "ok"), song("two", "ok"), song("three", "ok")}
	src := &scriptSource{songs: songs}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(songs[0]))

	endFile(t, m, "end-eof")
	waitForLog(t, logPath, loadLine(songs[1]))
	rec.waitTitle(t, titleOf(songs[1]))

	// And again: the prefetch chain keeps going, one song ahead.
	endFile(t, m, "end-eof")
	waitForLog(t, logPath, loadLine(songs[2]))
	rec.waitTitle(t, titleOf(songs[2]))

	settle()
	// The session reads EventStreamEnded as a dropped radio stream and
	// reconnects the same URL: between songs it must never appear.
	if n := rec.count(EventStreamEnded); n != 0 {
		t.Fatalf("%d EventStreamEnded between songs", n)
	}
	if n := rec.count(EventStreamError); n != 0 {
		t.Fatalf("%d EventStreamError between healthy songs", n)
	}
	if n := countLoads(t, logPath); n != 3 {
		t.Fatalf("%d loadfiles, want 3", n)
	}
}

func TestQueueSkipsFailedSongThenErrorsAfterThree(t *testing.T) {
	t.Run("three in a row", func(t *testing.T) {
		src := &scriptSource{songs: []Song{
			song("one", "error"), song("two", "error"), song("three", "error"), song("four", "error"),
		}}
		m, logPath, _ := startQueueMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		ev := rec.waitFor(t, "EventStreamError", func(ev Event) bool { return ev.Type == EventStreamError })
		if ev.Err == nil {
			t.Fatal("EventStreamError without an Err")
		}
		settle()
		if n := rec.count(EventStreamError); n != 1 {
			t.Fatalf("%d EventStreamError, want exactly one", n)
		}
		if n := rec.count(EventStreamEnded); n != 0 {
			t.Fatalf("%d EventStreamEnded", n)
		}
		// Each failure moved on to the next song, and the third stopped
		// the walk: a fourth load would mean the queue never gave up.
		if n := countLoads(t, logPath); n != 3 {
			t.Fatalf("%d loadfiles, want 3", n)
		}
	})

	t.Run("a song that played resets the count", func(t *testing.T) {
		// error, error, a song that plays through, error, error, then one
		// that stays: five failures, but never three together.
		songs := []Song{
			song("one", "error"), song("two", "error"), song("three", "eof"),
			song("four", "error"), song("five", "error"), song("six", "ok"),
		}
		src := &scriptSource{songs: songs}
		m, logPath, _ := startQueueMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(songs[5]))
		settle()
		if n := rec.count(EventStreamError); n != 0 {
			t.Fatalf("%d EventStreamError, want none: a success between resets the count", n)
		}
		if n := countLoads(t, logPath); n != 6 {
			t.Fatalf("%d loadfiles, want 6", n)
		}
	})

	t.Run("a song that won't resolve counts too", func(t *testing.T) {
		// The prefetch fails to resolve three times running: the same
		// verdict as three songs mpv refused.
		boom := errors.New("Video unavailable")
		src := &scriptSource{
			songs: []Song{song("one", "ok")},
			errs:  map[int]error{1: boom, 2: boom, 3: boom},
		}
		m, logPath, _ := startQueueMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(src.songs[0]))
		endFile(t, m, "end-eof")
		ev := rec.waitFor(t, "EventStreamError", func(ev Event) bool { return ev.Type == EventStreamError })
		if errors.Is(ev.Err, ErrSourceNetwork) {
			t.Fatalf("a plain lookup failure was reported as network trouble: %v", ev.Err)
		}
		settle()
		if n := rec.count(EventStreamError); n != 1 {
			t.Fatalf("%d EventStreamError, want exactly one", n)
		}
		if n := countLoads(t, logPath); n != 1 {
			t.Fatalf("%d loadfiles, want only the first song", n)
		}
	})
}

func TestQueueSourceNetworkErrorIsDrop(t *testing.T) {
	t.Run("on the first song", func(t *testing.T) {
		src := &scriptSource{errs: map[int]error{0: fmt.Errorf("bandcamp: %w", ErrSourceNetwork)}}
		m, logPath, _ := startQueueMPV(t, src)
		err := m.Play("discover:x")
		if !errors.Is(err, ErrSourceNetwork) {
			t.Fatalf("Play = %v, want it to wrap ErrSourceNetwork (the session's offline path)", err)
		}
		if errors.Is(err, ErrResolve) {
			t.Fatalf("Play = %v: network trouble must not read as ErrResolve, which strikes the station", err)
		}
		waitForLog(t, logPath, `"stop"`)
		time.Sleep(100 * time.Millisecond)
		if strings.Contains(logText(t, logPath), "loadfile") {
			t.Fatal("nothing may load when the lookup failed")
		}
	})

	t.Run("a lookup that just failed is a resolve error", func(t *testing.T) {
		src := &scriptSource{errs: map[int]error{0: errors.New("no source has songs")}}
		m, _, _ := startQueueMPV(t, src)
		err := m.Play("discover:x")
		if !errors.Is(err, ErrResolve) || errors.Is(err, ErrSourceNetwork) {
			t.Fatalf("Play = %v, want ErrResolve only: that one earns the strike", err)
		}
		if !strings.Contains(err.Error(), "no source has songs") {
			t.Fatalf("Play = %v, want the source's reason", err)
		}
	})

	t.Run("mid-station", func(t *testing.T) {
		// The network goes while a song plays: the prefetch fails, and when
		// the song ends the player reports it so the session can go offline
		// instead of striking or reconnecting blind.
		src := &scriptSource{
			songs: []Song{song("one", "ok")},
			errs:  map[int]error{1: fmt.Errorf("youtube: %w", ErrSourceNetwork)},
		}
		m, logPath, _ := startQueueMPV(t, src)
		rec := record(m)
		if err := m.Play("discover:x"); err != nil {
			t.Fatal(err)
		}
		rec.waitTitle(t, titleOf(src.songs[0]))
		endFile(t, m, "end-eof")
		ev := rec.waitFor(t, "EventStreamError", func(ev Event) bool { return ev.Type == EventStreamError })
		if !errors.Is(ev.Err, ErrSourceNetwork) {
			t.Fatalf("EventStreamError.Err = %v, want it to wrap ErrSourceNetwork", ev.Err)
		}
		settle()
		if n := rec.count(EventStreamError); n != 1 {
			t.Fatalf("%d EventStreamError, want exactly one", n)
		}
		if n := countLoads(t, logPath); n != 1 {
			t.Fatalf("%d loadfiles, want only the first song", n)
		}
	})
}

func TestQueueNextSongCallsSkipped(t *testing.T) {
	songs := []Song{song("one", "ok"), song("two", "ok"), song("three", "ok")}
	src := &scriptSource{songs: songs}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(songs[0]))

	if err := m.NextSong(); err != nil {
		t.Fatalf("NextSong: %v", err)
	}
	waitForLog(t, logPath, loadLine(songs[1]))
	rec.waitTitle(t, titleOf(songs[1]))
	if got := src.skips(); len(got) != 1 || !got[0] {
		t.Fatalf("skips = %v, want [true]: skipped inside the first minute", got)
	}

	// A song that has played a while was heard, not dodged.
	m.mu.Lock()
	m.queue.loadedAt = time.Now().Add(-2 * time.Minute)
	m.mu.Unlock()
	if err := m.NextSong(); err != nil {
		t.Fatalf("NextSong: %v", err)
	}
	waitForLog(t, logPath, loadLine(songs[2]))
	if got := src.skips(); len(got) != 2 || got[1] {
		t.Fatalf("skips = %v, want [true false]", got)
	}
	settle()
	if n := rec.count(EventStreamEnded) + rec.count(EventStreamError); n != 0 {
		t.Fatalf("%d stream-end events from NEXT", n)
	}
}

func TestQueueNextWhileResolving(t *testing.T) {
	songs := []Song{song("one", "ok"), song("two", "ok"), song("three", "ok")}
	gate := make(chan struct{})
	// Call 1 is the prefetch of song two: it blocks, as a slow yt-dlp does.
	src := &scriptSource{songs: songs, gates: map[int]chan struct{}{1: gate}}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(songs[0]))
	deadline := time.Now().Add(3 * time.Second)
	for src.nextCalls() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := countLoads(t, logPath); n != 1 {
		t.Fatalf("%d loadfiles while the next song was still resolving, want 1", n)
	}

	close(gate)
	waitForLog(t, logPath, loadLine(songs[1]))
	settle()
	if n := countLoads(t, logPath); n != 2 {
		t.Fatalf("%d loadfiles after two NEXTs on one lookup, want exactly 2", n)
	}
	// The second press changed nothing, so it skipped nothing either.
	if got := src.skips(); len(got) != 1 {
		t.Fatalf("skips = %v, want one", got)
	}
	if strings.Contains(logText(t, logPath), songs[2].URL) {
		t.Fatal("the song after next was loaded too")
	}
}

func TestQueueTitleEventCarriesNewToYou(t *testing.T) {
	fresh, known := song("fresh", "ok"), song("known", "ok")
	fresh.NewToYou = true
	src := &scriptSource{songs: []Song{fresh, known, song("more", "ok")}}
	m, _, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	if ev := rec.waitTitle(t, titleOf(fresh)); !ev.NewToYou {
		t.Error("the first song is new to the listener and its title event must say so")
	}
	endFile(t, m, "end-eof")
	if ev := rec.waitTitle(t, titleOf(known)); ev.NewToYou {
		t.Error("the second song is not new; NewToYou leaked over from the first")
	}

	// A radio station afterwards: the flag goes with the queue.
	if err := m.Play("http://stream.test/radio"); err != nil {
		t.Fatal(err)
	}
	ev := rec.waitTitle(t, "Fake Artist - Fake Title")
	if ev.NewToYou {
		t.Error("a radio title must never carry NewToYou")
	}
}

func TestPlayStillRejectsNonHTTP(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	// No SetSongSources: the sentinel is just another bad URL.
	for _, bad := range []string{"ftp://stream.test/x", "discover:", "discover:swedish death metal"} {
		if err := m.Play(bad); err == nil || !strings.Contains(err.Error(), "only http/https") {
			t.Errorf("Play(%q) without song sources = %v, want it rejected", bad, err)
		}
	}
	// With them, the sentinel opens, but nothing else gets in.
	m.SetSongSources(func(string) (SongSource, error) { return &scriptSource{}, nil })
	for _, bad := range []string{"ftp://stream.test/x", "file:///etc/passwd", "rtmp://live.example/x", "discoverx:y", "Discover:x"} {
		if err := m.Play(bad); err == nil || !strings.Contains(err.Error(), "only http/https") {
			t.Errorf("Play(%q) with song sources = %v, want it rejected", bad, err)
		}
	}
	waitForLog(t, logPath, "observe_property")
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(logText(t, logPath), "loadfile") {
		t.Fatalf("a rejected URL reached mpv:\n%s", logText(t, logPath))
	}
}

func TestIsDiscoverURL(t *testing.T) {
	for u, want := range map[string]bool{
		"discover:swedish death metal": true,
		"discover:":                    true,
		"discover:https://youtu.be/x":  true,
		"http://stream.test/x":         false,
		"https://discover:x":           false,
		"Discover:x":                   false,
		"":                             false,
	} {
		if got := IsDiscoverURL(u); got != want {
			t.Errorf("IsDiscoverURL(%q) = %v, want %v", u, got, want)
		}
	}
}

// The first song has to play inside the tune watchdog's 12 s, so its lookup
// gets 8; the prefetch, which nobody is waiting on, gets 20.
func TestQueueLookupBudgets(t *testing.T) {
	src := &scriptSource{songs: []Song{song("one", "ok"), song("two", "ok")}}
	m, _, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(src.songs[0]))
	deadline := time.Now().Add(3 * time.Second)
	for src.nextCalls() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.deadlines) < 2 {
		t.Fatalf("%d Next calls, want the first song and a prefetch", len(src.deadlines))
	}
	if d := src.deadlines[0]; d <= 7*time.Second || d > 8*time.Second {
		t.Errorf("first-song lookup had %v, want 8s", d)
	}
	if d := src.deadlines[1]; d <= 19*time.Second || d > 20*time.Second {
		t.Errorf("prefetch had %v, want 20s", d)
	}
}

// The listener moves on while the first song is still being found: the
// newer Play owns the player and the late song loads nothing.
func TestQueueSupersededWhileResolvingFirstSong(t *testing.T) {
	gate := make(chan struct{})
	src := &scriptSource{songs: []Song{song("late", "ok")}, gates: map[int]chan struct{}{0: gate}}
	m, logPath, _ := startQueueMPV(t, src)
	done := make(chan error, 1)
	go func() { done <- m.Play("discover:x") }()
	deadline := time.Now().Add(3 * time.Second)
	for src.nextCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Play("http://stream.test/radio"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrSuperseded) {
			t.Fatalf("the late Play = %v, want ErrSuperseded (no strike, no trace)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the superseded Play never returned: its lookup was not cancelled")
	}
	close(gate)
	waitForLog(t, logPath, `"loadfile","http://stream.test/radio"`)
	settle()
	if strings.Contains(logText(t, logPath), "stream.test/late") {
		t.Fatalf("a superseded song loaded over the newer station:\n%s", logText(t, logPath))
	}
}

// Choosing a radio station ends the queue: its songs stop advancing, and a
// stream that ends is a dropped stream again.
func TestQueueEndsWhenRadioTakesOver(t *testing.T) {
	src := &scriptSource{songs: []Song{song("one", "ok"), song("two", "ok")}}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(src.songs[0]))

	if err := m.Play("http://stream.test/eof"); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "EventStreamEnded", func(ev Event) bool { return ev.Type == EventStreamEnded })
	settle()
	if n := countLoads(t, logPath); n != 2 {
		t.Fatalf("%d loadfiles, want the song and the radio station only", n)
	}
	if err := m.NextSong(); err == nil {
		t.Error("NextSong with no discovery station playing must say so")
	}
}

// Stop from the OS flyout ends the walk: nothing may load behind it.
func TestQueueExternalStopEndsQueue(t *testing.T) {
	src := &scriptSource{songs: []Song{song("one", "ok"), song("two", "ok")}}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(src.songs[0]))
	endFile(t, m, "stop")
	rec.waitFor(t, "EventStopped", func(ev Event) bool { return ev.Type == EventStopped })
	if err := m.NextSong(); err == nil {
		t.Error("NextSong after Stop must not load anything")
	}
	settle()
	if n := countLoads(t, logPath); n != 1 {
		t.Fatalf("%d loadfiles after Stop, want 1", n)
	}
}

// Play(discover) right on the heels of another Play: the first file's
// start-file lands after the queue sent its own stop, so it clears
// loadPending and the stop's end-file is no longer swallowed by that flag.
// While the first song is still being looked for, that end-file is the
// queue's own silencing stop, never the listener's: it must not drop the
// queue (which cancels the lookup and reads as a failed station), and it
// must not tell the session playback was stopped from the media controls.
func TestQueueOwnStopSurvivesLateStartFile(t *testing.T) {
	gate := make(chan struct{})
	src := &scriptSource{songs: []Song{song("one", "ok")}, gates: map[int]chan struct{}{0: gate}}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)

	if err := m.Play("http://stream.test/late"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Play("discover:x") }()
	deadline := time.Now().Add(3 * time.Second)
	for src.nextCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// The fake answers the queue's stop with the radio's late start-file
	// and then end-file stop, while the lookup is still blocked.
	waitForLog(t, logPath, `"stop"`)
	settle()
	if n := rec.count(EventStopped); n != 0 {
		t.Fatalf("%d EventStopped: the queue's own stop reached the session as the listener's", n)
	}
	m.mu.Lock()
	alive := m.queue != nil && !m.queue.dead
	m.mu.Unlock()
	if !alive {
		t.Fatal("the queue's own stop dropped the queue it was silencing for")
	}

	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Play = %v, want the first song loaded (an error here strikes the station)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Play never returned")
	}
	first := src.songs[0]
	waitForLog(t, logPath, loadLine(first))
	rec.waitTitle(t, titleOf(first))
	if n := rec.count(EventStopped); n != 0 {
		t.Fatalf("%d EventStopped", n)
	}
}

// A queue that is dropped while its first song is still being found (the
// backend died, or playback was stopped from outside) leaves Play with a
// cancelled lookup. That is not a station that failed to resolve: Play
// answers ErrSuperseded, so the session leaves no strike.
func TestQueueDroppedWhileResolvingIsSuperseded(t *testing.T) {
	gate := make(chan struct{})
	src := &scriptSource{songs: []Song{song("late", "ok")}, gates: map[int]chan struct{}{0: gate}}
	m, logPath, _ := startQueueMPV(t, src)
	done := make(chan error, 1)
	go func() { done <- m.Play("discover:x") }()
	deadline := time.Now().Add(3 * time.Second)
	for src.nextCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	m.dropQueueLocked()
	m.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSuperseded) || errors.Is(err, ErrResolve) {
			t.Fatalf("Play = %v, want ErrSuperseded only", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Play never returned: the dropped queue's lookup was not cancelled")
	}
	close(gate)
	settle()
	if strings.Contains(logText(t, logPath), "stream.test/late") {
		t.Fatalf("a dropped queue's song was loaded:\n%s", logText(t, logPath))
	}
}

// A prefetched URL is signed and goes stale (a song longer than an hour
// outlives its successor's link): it is dropped and asked for afresh.
func TestQueueStalePrefetchIsAskedAgain(t *testing.T) {
	songs := []Song{song("one", "ok"), song("stale", "ok"), song("fresh", "ok"), song("after", "ok")}
	src := &scriptSource{songs: songs}
	m, logPath, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(songs[0]))

	m.mu.Lock()
	p := m.queue.next
	m.mu.Unlock()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the prefetch never finished")
	}
	m.mu.Lock()
	p.at = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	endFile(t, m, "end-eof")
	waitForLog(t, logPath, loadLine(songs[2]))
	if strings.Contains(logText(t, logPath), songs[1].URL) {
		t.Fatal("a stale URL was loaded")
	}
}

// Whatever a source hands over, mpv only ever opens http(s) (--ytdl=no
// keeps it from guessing anything else), and the URL stays out of the error.
func TestQueueRejectsNonHTTPSong(t *testing.T) {
	bad := song("bad", "ok")
	bad.URL = "file:///secret/passwd"
	src := &scriptSource{songs: []Song{bad}}
	m, logPath, _ := startQueueMPV(t, src)
	err := m.Play("discover:x")
	if err == nil {
		t.Fatal("a file:// song was accepted")
	}
	if strings.Contains(err.Error(), "passwd") {
		t.Fatalf("the song's URL leaked into the error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(logText(t, logPath), "loadfile") {
		t.Fatalf("a non-http song reached mpv:\n%s", logText(t, logPath))
	}
}

func TestQueueLovedReachesSource(t *testing.T) {
	src := &scriptSource{songs: []Song{song("one", "ok"), song("two", "ok")}}
	m, _, _ := startQueueMPV(t, src)
	m.SongLoved() // no queue yet: nothing to love, and nothing to panic over
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(src.songs[0]))
	m.SongLoved()
	src.mu.Lock()
	loved := src.loved
	src.mu.Unlock()
	if loved != 1 {
		t.Fatalf("Loved reached the source %d times, want 1", loved)
	}
}

// The mpv process dying ends the queue's walk and must not trip a send on
// the closed event channel from a background lookup.
func TestQueueSurvivesBackendDeath(t *testing.T) {
	gate := make(chan struct{})
	src := &scriptSource{
		songs: []Song{song("one", "ok"), song("two", "ok")},
		gates: map[int]chan struct{}{1: gate},
	}
	m, _, _ := startQueueMPV(t, src)
	rec := record(m)
	if err := m.Play("discover:x"); err != nil {
		t.Fatal(err)
	}
	rec.waitTitle(t, titleOf(src.songs[0]))
	if err := m.NextSong(); err != nil {
		t.Fatal(err)
	}
	// mpv goes away while the next song is still resolving.
	if err := m.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	rec.waitFor(t, "EventDied", func(ev Event) bool { return ev.Type == EventDied })
	close(gate)
	settle()
	if err := m.NextSong(); err == nil {
		t.Error("NextSong on a dead backend's queue must say so")
	}
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := m.NextSong(); err == nil {
		t.Error("a restarted player has no queue until Play opens one")
	}
}
