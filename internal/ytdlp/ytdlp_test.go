package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles the test binary as a fake yt-dlp: a Runner pointed at
// os.Args[0] with SCREECH_FAKE_YTDLP in the env lands here instead of in the
// testing flag parser. Same trick as internal/player, so these tests cover
// the exec path without yt-dlp installed and without the network.
func TestMain(m *testing.M) {
	if mode := os.Getenv("SCREECH_FAKE_YTDLP"); mode != "" {
		os.Exit(runFakeYTDLP(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runFakeYTDLP records what the client asked (argv and working directory) in
// SCREECH_FAKE_YTDLP_LOG, then answers per mode with the shapes yt-dlp
// 2026.10.26 really prints.
func runFakeYTDLP(mode string, args []string) int {
	logLine := func(s string) {
		path := os.Getenv("SCREECH_FAKE_YTDLP_LOG")
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(f, s)
		_ = f.Sync()
		_ = f.Close()
	}
	cwd, _ := os.Getwd()
	logLine("ARGV " + strings.Join(args, "\x1f"))
	logLine("CWD " + cwd)
	page := ""
	if len(args) > 0 {
		page = args[len(args)-1]
	}

	switch mode {
	case "search":
		fmt.Println(`{"id":"q","title":"q","_type":"playlist","entries":[` +
			`{"id":"knQEA9D-Qq0","title":"Left Hand Path","url":"https://music.youtube.com/watch?v=knQEA9D-Qq0"},` +
			`{"id":"gZSzqfZ4udE","title":"Spiritual Healing","url":"https://music.youtube.com/watch?v=gZSzqfZ4udE"}]}`)
	case "resolve":
		// The URL lands between the tagged lines, as it may in yt-dlp's
		// own order; the client must not depend on where.
		fmt.Println("screech-artist:Entombed")
		fmt.Println("screech-track:Left Hand Path")
		fmt.Println("https://rr1.googlevideo.com/x")
		fmt.Println("screech-title:Entombed - Left Hand Path (Official Audio)")
		fmt.Println("screech-channel:Entombed - Topic")
		fmt.Println("screech-dur:356.0")
		fmt.Println("screech-views:1234567")
		fmt.Println(`screech-tags:["death metal", "swedish, old"]`)
	case "sleep":
		// START and END bracket the "process" so a test can see overlap.
		// The hold is long enough for a test to queue more callers behind it.
		hold := 200
		if ms, err := strconv.Atoi(os.Getenv("SCREECH_FAKE_YTDLP_HOLD_MS")); err == nil {
			hold = ms
		}
		logLine("START " + page)
		time.Sleep(time.Duration(hold) * time.Millisecond)
		logLine("END " + page)
		fmt.Println(`{"entries":[]}`)
	case "fail":
		fmt.Fprintln(os.Stderr, "WARNING: something minor")
		fmt.Fprintf(os.Stderr, "ERROR: [youtube] %s: Video unavailable\n", page)
		return 1
	case "hang":
		time.Sleep(30 * time.Second)
	case "nourl":
		fmt.Println("screech-title:no stream here")
	case "version":
		fmt.Println("2026.10.26")
	}
	return 0
}

// fake points a Runner at the test binary acting as yt-dlp in mode and
// returns the log it writes.
func fake(t *testing.T, mode string) (Runner, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "yt-dlp.log")
	t.Setenv("SCREECH_FAKE_YTDLP", mode)
	t.Setenv("SCREECH_FAKE_YTDLP_LOG", logPath)
	return Runner{Path: os.Args[0], Dir: dir}, logPath
}

func logLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// loggedArgs is the argv of the (first) fake yt-dlp process.
func loggedArgs(t *testing.T, path string) []string {
	t.Helper()
	for _, l := range logLines(t, path) {
		if v, ok := strings.CutPrefix(l, "ARGV "); ok {
			return strings.Split(v, "\x1f")
		}
	}
	t.Fatalf("the fake yt-dlp never ran; log:\n%s", strings.Join(logLines(t, path), "\n"))
	return nil
}

func loggedCWD(t *testing.T, path string) string {
	t.Helper()
	for _, l := range logLines(t, path) {
		if v, ok := strings.CutPrefix(l, "CWD "); ok {
			return v
		}
	}
	t.Fatal("the fake yt-dlp logged no working directory")
	return ""
}

// hasPair reports whether flag is immediately followed by value in args.
func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestSongsParsesFlatSearch(t *testing.T) {
	r, logPath := fake(t, "search")
	hits, err := r.Songs(context.Background(), "swedish death metal", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{{ID: "knQEA9D-Qq0", Title: "Left Hand Path"}, {ID: "gZSzqfZ4udE", Title: "Spiritual Healing"}}
	if !slices.Equal(hits, want) {
		t.Fatalf("hits = %+v, want %+v", hits, want)
	}
	args := loggedArgs(t, logPath)
	for _, w := range []string{"--flat-playlist", "-J", "--no-warnings", "https://music.youtube.com/search?q=swedish+death+metal#songs"} {
		if !slices.Contains(args, w) {
			t.Errorf("argv lacks %q: %q", w, args)
		}
	}
	if !hasPair(args, "--playlist-items", "1:2") {
		t.Errorf("argv lacks --playlist-items 1:2: %q", args)
	}
	if slices.Contains(args, "--js-runtimes") {
		t.Errorf("no JS runtime was configured, yet argv has --js-runtimes: %q", args)
	}
}

// A query is data: &, # and spaces must not leak into the URL's structure.
func TestSongsEscapesTheQuery(t *testing.T) {
	r, logPath := fake(t, "search")
	if _, err := r.Songs(context.Background(), "Mötley & Crüe #1", 5); err != nil {
		t.Fatal(err)
	}
	want := "https://music.youtube.com/search?q=M%C3%B6tley+%26+Cr%C3%BCe+%231#songs"
	if args := loggedArgs(t, logPath); !slices.Contains(args, want) {
		t.Fatalf("argv lacks %q: %q", want, args)
	}
}

func TestMixAsksForTheVideosRadio(t *testing.T) {
	r, logPath := fake(t, "search")
	hits, err := r.Mix(context.Background(), "knQEA9D-Qq0", 3)
	if err != nil || len(hits) != 2 {
		t.Fatalf("Mix = %+v, %v", hits, err)
	}
	args := loggedArgs(t, logPath)
	if !slices.Contains(args, "https://www.youtube.com/watch?v=knQEA9D-Qq0&list=RDknQEA9D-Qq0") {
		t.Errorf("argv lacks the RD mix URL: %q", args)
	}
	if !hasPair(args, "--playlist-items", "1:3") || !slices.Contains(args, "--flat-playlist") {
		t.Errorf("argv lacks the flat 1:3 request: %q", args)
	}
}

// Nothing asked for, nothing run.
func TestNothingToAskDoesNotRun(t *testing.T) {
	r, logPath := fake(t, "search")
	ctx := context.Background()
	if hits, err := r.Songs(ctx, "  ", 5); hits != nil || err != nil {
		t.Errorf("Songs(blank) = %v, %v", hits, err)
	}
	if hits, err := r.Songs(ctx, "x", 0); hits != nil || err != nil {
		t.Errorf("Songs(n=0) = %v, %v", hits, err)
	}
	if hits, err := r.Mix(ctx, "", 5); hits != nil || err != nil {
		t.Errorf("Mix(blank) = %v, %v", hits, err)
	}
	if n := len(logLines(t, logPath)); n != 0 {
		t.Fatalf("yt-dlp ran for an empty request: %v", logLines(t, logPath))
	}
}

// yt-dlp can leave holes (null entries, entries with no id); a hole is not
// a song, and the rest of the list still counts.
func TestParseFlatSkipsHoles(t *testing.T) {
	hits, err := parseFlat(`{"entries":[null,{"id":"","title":"x"},{"id":"a","title":null},{"id":"b","title":"B"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{{ID: "a"}, {ID: "b", Title: "B"}}
	if !slices.Equal(hits, want) {
		t.Fatalf("hits = %+v, want %+v", hits, want)
	}
	if hits, err := parseFlat(`{"entries":[]}`); err != nil || len(hits) != 0 {
		t.Errorf("an empty search = %v, %v; want no hits and no error", hits, err)
	}
	for _, bad := range []string{"", "not json", `[1,2]`} {
		if _, err := parseFlat(bad); err == nil {
			t.Errorf("parseFlat(%q) succeeded", bad)
		}
	}
}

func TestResolveParsesTaggedLines(t *testing.T) {
	r, logPath := fake(t, "resolve")
	page := "https://music.youtube.com/watch?v=knQEA9D-Qq0"
	m, err := r.Resolve(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	want := Media{
		URL:      "https://rr1.googlevideo.com/x",
		Artist:   "Entombed",
		Track:    "Left Hand Path",
		Title:    "Entombed - Left Hand Path (Official Audio)",
		Channel:  "Entombed - Topic",
		Duration: 356,
		Views:    1234567,
		Tags:     []string{"death metal", "swedish, old"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("media = %+v\nwant    %+v", m, want)
	}

	args := loggedArgs(t, logPath)
	if !hasPair(args, "-f", "bestaudio/best") || !slices.Contains(args, "--no-playlist") || !slices.Contains(args, "--get-url") {
		t.Errorf("argv lacks the audio-only single-video request: %q", args)
	}
	// The page rides after "--", so a link can never be read as a flag.
	if n := len(args); n < 2 || args[n-2] != "--" || args[n-1] != page {
		t.Errorf("argv should end with -- and the page: %q", args)
	}
	for _, tag := range []string{"artist", "track", "title", "channel", "dur", "views", "tags"} {
		found := false
		for i, a := range args {
			if a == "--print" && i+1 < len(args) && strings.HasPrefix(args[i+1], "screech-"+tag+":") {
				found = true
			}
		}
		if !found {
			t.Errorf("argv prints no screech-%s: line: %q", tag, args)
		}
	}
}

// Missing metadata is normal (a bare SoundCloud track has none): the
// stream is the only thing a Resolve cannot do without.
func TestParseMediaTolerance(t *testing.T) {
	m, err := parseMedia("screech-artist:\nscreech-track:\nscreech-title:No meta\nscreech-channel:\n" +
		"screech-dur:\nscreech-views:\nscreech-tags:[]\r\nhttps://rr1.googlevideo.com/y\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if m.URL != "https://rr1.googlevideo.com/y" || m.Title != "No meta" || m.Duration != 0 || m.Views != 0 || len(m.Tags) != 0 {
		t.Errorf("media = %+v", m)
	}
	// Garbage in a number or the tag list costs that field, not the song.
	m, err = parseMedia("screech-dur:soon\nscreech-views:NA\nscreech-tags:NA\nhttp://stream.test/a\n")
	if err != nil || m.URL != "http://stream.test/a" || m.Duration != 0 || m.Views != 0 || m.Tags != nil {
		t.Errorf("garbled fields = %+v, %v", m, err)
	}
	for _, out := range []string{"", "screech-title:x\n", "file:///etc/passwd\nscreech-title:x\n", "rtmp://live.example/x\n"} {
		if _, err := parseMedia(out); err == nil {
			t.Errorf("parseMedia(%q) found a stream", out)
		}
	}
}

func TestResolveWithoutAStreamFails(t *testing.T) {
	r, _ := fake(t, "nourl")
	if _, err := r.Resolve(context.Background(), "https://example.test/x"); err == nil {
		t.Fatal("a yt-dlp that printed no stream URL must fail Resolve")
	}
}

func TestRunnerPassesJSRuntimeAndDir(t *testing.T) {
	r, logPath := fake(t, "search")
	r.JSRuntime = "node:C:/n/node.exe"
	r.Dir = t.TempDir()
	if _, err := r.Songs(context.Background(), "x", 1); err != nil {
		t.Fatal(err)
	}
	args := loggedArgs(t, logPath)
	if !hasPair(args, "--js-runtimes", "node:C:/n/node.exe") {
		t.Errorf("argv lacks --js-runtimes node:C:/n/node.exe: %q", args)
	}
	got, want := loggedCWD(t, logPath), r.Dir
	// t.TempDir may sit behind a symlink (macOS /var) or a short name.
	if g, err := filepath.EvalSymlinks(got); err == nil {
		got = g
	}
	if w, err := filepath.EvalSymlinks(want); err == nil {
		want = w
	}
	if got != want {
		t.Errorf("yt-dlp ran in %q, want the data folder %q", got, want)
	}
}

// The reason yt-dlp gave is the last line it said, not the first warning.
func TestErrorsCarryTheLastStderrLine(t *testing.T) {
	r, _ := fake(t, "fail")
	_, err := r.Resolve(context.Background(), "https://www.youtube.com/watch?v=gone")
	if err == nil || !strings.Contains(err.Error(), "Video unavailable") || strings.Contains(err.Error(), "something minor") {
		t.Fatalf("error = %v, want only yt-dlp's last line", err)
	}
	if _, err := (Runner{Path: filepath.Join(t.TempDir(), "no-such-yt-dlp")}).Songs(context.Background(), "x", 1); err == nil {
		t.Fatal("a missing executable must fail")
	}
}

// A hung yt-dlp is stopped by the caller's context and the error says why;
// the turn it held goes to the next caller.
func TestContextEndsAHungRun(t *testing.T) {
	r, _ := fake(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Songs(ctx, "x", 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want one wrapping context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v to give up on a hung yt-dlp", d)
	}
	t.Setenv("SCREECH_FAKE_YTDLP", "search")
	next, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := r.Songs(next, "x", 1); err != nil {
		t.Fatalf("the turn was not handed on after the timeout: %v", err)
	}
}

func TestVersion(t *testing.T) {
	r, _ := fake(t, "version")
	v, err := Version(context.Background(), r.Path)
	if err != nil || v != "2026.10.26" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	if _, err := Version(context.Background(), filepath.Join(t.TempDir(), "no-such-yt-dlp")); err == nil {
		t.Fatal("Version of a missing executable must fail")
	}
}

// startEnd returns the START/END page sequence the fake logged.
func startEnd(t *testing.T, logPath string) []string {
	t.Helper()
	var out []string
	for _, l := range logLines(t, logPath) {
		if strings.HasPrefix(l, "START ") || strings.HasPrefix(l, "END ") {
			out = append(out, l)
		}
	}
	return out
}

func TestOneProcessAtATime(t *testing.T) {
	r, logPath := fake(t, "sleep")
	var wg sync.WaitGroup
	for _, q := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Songs(context.Background(), q, 1); err != nil {
				t.Errorf("Songs(%q): %v", q, err)
			}
		}()
	}
	wg.Wait()
	seq := startEnd(t, logPath)
	if len(seq) != 6 {
		t.Fatalf("saw %d start/end lines, want 6:\n%s", len(seq), strings.Join(seq, "\n"))
	}
	// Serial means each START is followed by its own END before the next
	// START: any overlap shows up as START, START.
	for i := 0; i < len(seq); i += 2 {
		page := strings.TrimPrefix(seq[i], "START ")
		if !strings.HasPrefix(seq[i], "START ") || seq[i+1] != "END "+page {
			t.Fatalf("yt-dlp processes overlapped:\n%s", strings.Join(seq, "\n"))
		}
	}
}

// waitWaiters polls until n callers are waiting for the lock, in either
// line: they are goroutines, so a test has to see them arrive before it
// moves on. Which line each is in is for the test's own assertions, not
// this helper's, so a broken priority fails on the order it checks.
func waitWaiters(t *testing.T, l *prioLock, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		got := len(l.fg) + len(l.bg)
		l.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers waiting for the lock, want %d", got, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestForegroundJumpsQueue(t *testing.T) {
	r, logPath := fake(t, "sleep")
	t.Setenv("SCREECH_FAKE_YTDLP_HOLD_MS", "500")
	var wg sync.WaitGroup
	call := func(ctx context.Context, q string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Songs(ctx, q, 1); err != nil {
				t.Errorf("Songs(%q): %v", q, err)
			}
		}()
	}

	call(context.Background(), "holder")
	// Wait for the holder's process to be running, so it has the turn.
	deadline := time.Now().Add(5 * time.Second)
	for len(startEnd(t, logPath)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first call never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	call(context.Background(), "background")
	waitWaiters(t, &lock, 1)
	call(Foreground(context.Background()), "foreground")
	waitWaiters(t, &lock, 2)
	wg.Wait()

	var order []string
	for _, l := range startEnd(t, logPath) {
		if page, ok := strings.CutPrefix(l, "START "); ok {
			switch {
			case strings.Contains(page, "q=holder"):
				order = append(order, "holder")
			case strings.Contains(page, "q=foreground"):
				order = append(order, "foreground")
			case strings.Contains(page, "q=background"):
				order = append(order, "background")
			}
		}
	}
	if want := []string{"holder", "foreground", "background"}; !slices.Equal(order, want) {
		t.Fatalf("ran in order %v, want %v", order, want)
	}
}

// The lock alone, without processes: foreground before background, and
// first come first served inside each line.
func TestPrioLockOrder(t *testing.T) {
	var l prioLock
	ctx := context.Background()
	if err := l.acquire(ctx, false); err != nil {
		t.Fatal(err)
	}
	granted := make(chan string, 4)
	queued := 0
	enqueue := func(name string, fg bool) {
		queued++
		go func() {
			if err := l.acquire(ctx, fg); err != nil {
				t.Errorf("acquire(%s): %v", name, err)
				return
			}
			granted <- name
			l.release()
		}()
		waitWaiters(t, &l, queued)
	}
	enqueue("bg1", false)
	enqueue("bg2", false)
	enqueue("fg1", true)
	enqueue("fg2", true)
	l.release()
	var got []string
	for range 4 {
		select {
		case n := <-granted:
			got = append(got, n)
		case <-time.After(5 * time.Second):
			t.Fatalf("lock stalled after %v", got)
		}
	}
	if want := []string{"fg1", "fg2", "bg1", "bg2"}; !slices.Equal(got, want) {
		t.Fatalf("granted %v, want %v", got, want)
	}
}

// A waiter whose context ends leaves the line without taking the turn or
// blocking the ones behind it.
func TestPrioLockCancelledWaiterLeaves(t *testing.T) {
	var l prioLock
	bg := context.Background()
	if err := l.acquire(bg, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	gone := make(chan error, 1)
	go func() { gone <- l.acquire(ctx, false) }()
	waitWaiters(t, &l, 1)
	cancel()
	if err := <-gone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
	}
	waitWaiters(t, &l, 0)
	l.release()
	// Free again: the next caller gets it at once.
	next, stop := context.WithTimeout(bg, 2*time.Second)
	defer stop()
	if err := l.acquire(next, false); err != nil {
		t.Fatalf("lock stuck after a waiter left: %v", err)
	}
}

// The holder lets go right behind a cancel, before the woken waiter has
// taken the mutex to leave the line, so the turn is handed to a waiter that
// has already given up. It must pass the turn on: whichever way the race
// falls, the lock ends up free.
func TestPrioLockCancelRacingHandOver(t *testing.T) {
	bg := context.Background()
	for i := range 200 {
		var l prioLock
		if err := l.acquire(bg, false); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() {
			err := l.acquire(ctx, false)
			if err == nil {
				l.release()
			}
			done <- err
		}()
		waitWaiters(t, &l, 1)
		cancel()
		l.release()
		<-done
		free, stop := context.WithTimeout(bg, 2*time.Second)
		err := l.acquire(free, false)
		stop()
		if err != nil {
			t.Fatalf("round %d: the lock leaked: %v", i, err)
		}
	}
}

// A context that is already over never takes the turn.
func TestPrioLockDeadContextDoesNotAcquire(t *testing.T) {
	var l prioLock
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.acquire(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire = %v, want context.Canceled", err)
	}
	if err := l.acquire(context.Background(), false); err != nil {
		t.Fatalf("lock was taken by a dead context: %v", err)
	}
}

// ytdlpName is the file name Find looks for on this platform.
func ytdlpName() string {
	if runtime.GOOS == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// touchExe makes a stand-in executable named name in dir (the content never
// runs; LookPath only needs the file, and on unix the exec bit).
func touchExe(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func sameFile(t *testing.T, got, want string) {
	t.Helper()
	g, err1 := os.Stat(got)
	w, err2 := os.Stat(want)
	if err1 != nil || err2 != nil || !os.SameFile(g, w) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindOrder(t *testing.T) {
	pathDir, exeDir, dataDir := t.TempDir(), t.TempDir(), t.TempDir()
	// Only our own PATH, so a yt-dlp installed on the machine running the
	// tests can't answer for it.
	t.Setenv("PATH", t.TempDir())
	name := ytdlpName()

	if p, ok := Find("", exeDir, dataDir); ok || p != "" {
		t.Fatalf("with nothing anywhere Find = %q, %v", p, ok)
	}
	wantData := touchExe(t, dataDir, name)
	if p, ok := Find("", exeDir, dataDir); !ok {
		t.Fatalf("Find missed the data folder")
	} else {
		sameFile(t, p, wantData)
	}
	wantExe := touchExe(t, exeDir, name)
	if p, ok := Find("", exeDir, dataDir); !ok {
		t.Fatalf("Find missed the executable's folder")
	} else {
		sameFile(t, p, wantExe)
	}
	wantPath := touchExe(t, pathDir, name)
	t.Setenv("PATH", pathDir)
	if p, ok := Find("", exeDir, dataDir); !ok {
		t.Fatalf("Find missed PATH")
	} else {
		sameFile(t, p, wantPath)
	}

	// A folder named like the program is not the program.
	dirOnly := t.TempDir()
	if err := os.Mkdir(filepath.Join(dirOnly, name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if p, ok := Find("", dirOnly, ""); ok {
		t.Errorf("Find took a directory for the program: %q", p)
	}

	// Off means off, however much is lying around; anything else is the
	// listener's own word and goes back unchanged.
	if p, ok := Find("off", exeDir, dataDir); ok || p != "" {
		t.Errorf(`Find("off") = %q, %v`, p, ok)
	}
	if p, ok := Find("OFF", exeDir, dataDir); ok || p != "" {
		t.Errorf(`Find("OFF") = %q, %v`, p, ok)
	}
	if p, ok := Find("C:/x/yt.exe", exeDir, dataDir); !ok || p != "C:/x/yt.exe" {
		t.Errorf(`Find("C:/x/yt.exe") = %q, %v`, p, ok)
	}
}

// On Windows a bare name is looked for in the working directory before PATH.
// A yt-dlp.exe that merely happens to sit wherever Screech was started is
// not one the listener chose, and must not be the one that runs.
func TestFindIgnoresTheWorkingDirectory(t *testing.T) {
	here := t.TempDir()
	touchExe(t, here, ytdlpName())
	touchExe(t, here, "node")
	touchExe(t, here, "node.exe")
	t.Chdir(here)
	t.Setenv("PATH", t.TempDir())
	if p, ok := Find("", "", ""); ok {
		t.Errorf("Find took %q from the working directory", p)
	}
	if got := DetectJSRuntime(); got != "" {
		t.Errorf("DetectJSRuntime took %q from the working directory", got)
	}
}

func TestDetectJSRuntime(t *testing.T) {
	exe := func(name string) string {
		if runtime.GOOS == "windows" {
			return name + ".exe"
		}
		return name
	}
	denoDir, nodeDir, bunDir := t.TempDir(), t.TempDir(), t.TempDir()
	touchExe(t, denoDir, exe("deno"))
	node := touchExe(t, nodeDir, exe("node"))
	bun := touchExe(t, bunDir, exe("bun"))
	join := func(dirs ...string) string { return strings.Join(dirs, string(os.PathListSeparator)) }

	cases := []struct {
		name   string
		path   string
		prefix string // "" means no JS runtime is passed
		file   string
	}{
		{"nothing", t.TempDir(), "", ""},
		{"node only", join(nodeDir), "node:", node},
		{"node beats bun", join(bunDir, nodeDir), "node:", node},
		{"bun only", join(bunDir), "bun:", bun},
		// yt-dlp finds deno by itself, so it needs no flag.
		{"deno wins", join(bunDir, nodeDir, denoDir), "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PATH", c.path)
			got := DetectJSRuntime()
			if c.prefix == "" {
				if got != "" {
					t.Fatalf("DetectJSRuntime = %q, want none", got)
				}
				return
			}
			file, ok := strings.CutPrefix(got, c.prefix)
			if !ok {
				t.Fatalf("DetectJSRuntime = %q, want a %q runtime", got, c.prefix)
			}
			sameFile(t, file, c.file)
		})
	}
}
