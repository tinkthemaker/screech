package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/discover"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/ytdlp"
)

// TestMain doubles the test binary as a fake yt-dlp, the way internal/ytdlp
// and internal/player do: a copy of it dropped in the data folder is "the
// yt-dlp Screech found", and SCREECH_FAKE_YTDLP says how it answers.
func TestMain(m *testing.M) {
	if mode := os.Getenv("SCREECH_FAKE_YTDLP"); mode != "" {
		os.Exit(runFakeYTDLP(mode))
	}
	os.Exit(m.Run())
}

func runFakeYTDLP(mode string) int {
	if p := os.Getenv("SCREECH_FAKE_YTDLP_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			cwd, _ := os.Getwd()
			fmt.Fprintln(f, "CWD "+cwd)
			_ = f.Close()
		}
	}
	switch mode {
	case "version":
		fmt.Println("2026.10.26")
	case "search":
		fmt.Println(`{"entries":[{"id":"knQEA9D-Qq0","title":"Left Hand Path"},{"id":"gZSzqfZ4udE","title":"Spiritual Healing"}]}`)
	case "resolve":
		fmt.Println("screech-artist:Entombed")
		fmt.Println("screech-track:Left Hand Path")
		fmt.Println("https://rr1.googlevideo.com/x")
		fmt.Println("screech-title:Entombed - Left Hand Path (Official Audio)")
		fmt.Println("screech-channel:Entombed - Topic")
		fmt.Println("screech-dur:356.0")
		fmt.Println("screech-views:1234")
		fmt.Println(`screech-tags:["death metal"]`)
	case "fail":
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] x: Video unavailable")
		return 1
	case "offline":
		fmt.Fprintln(os.Stderr, "WARNING: retrying")
		fmt.Fprintln(os.Stderr, "ERROR: [youtube] x: Unable to download API page: <urlopen error [Errno 11001] getaddrinfo failed>")
		return 1
	}
	return 0
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// putFakeYtdlp copies the test binary into dir as yt-dlp.
func putFakeYtdlp(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst := filepath.Join(dir, exeName("yt-dlp"))
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, src); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dst
}

// cleanPath leaves PATH holding only a stand-in node, so the machine's own
// yt-dlp and JS runtimes can't answer for the test.
func cleanPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, exeName("node")), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// fakeSink stands in for the player: it keeps what the app hands it.
type fakeSink struct {
	ex   player.Extractor
	open func(seed string) (player.SongSource, error)
}

func (f *fakeSink) SetExtractor(ex player.Extractor) { f.ex = ex }
func (f *fakeSink) SetSongSources(open func(string) (player.SongSource, error)) {
	f.open = open
}

// fakeBandcamp answers every tag with n tracks by n different bands.
type fakeBandcamp struct{ n int }

func (b fakeBandcamp) Tag(ctx context.Context, tag, slice string) ([]discover.BCItem, error) {
	var items []discover.BCItem
	for i := 1; i <= b.n; i++ {
		items = append(items, discover.BCItem{
			TrackID:   int64(i),
			Artist:    fmt.Sprintf("Band %c", 'A'+i-1),
			Title:     fmt.Sprintf("Song %d", i),
			StreamURL: fmt.Sprintf("https://bc.example/%d.mp3", i),
			Duration:  200,
		})
	}
	return items, nil
}

// wire runs wireDiscovery with a log it can read back.
func wire(t *testing.T, sink *fakeSink, cfg config.Config, exeDir string, bc discover.Bandcamp) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "screech.log")
	log := NewLog(logPath)
	log.Open()
	wireDiscovery(sink, discoveryDeps{Config: cfg, ExeDir: exeDir, Known: func(string) bool { return false }, Bandcamp: bc, Log: log})
	log.Close()
	b, _ := os.ReadFile(logPath)
	return string(b)
}

// With yt-dlp in the data folder and nothing configured, startup finds it,
// hands the player an extractor that runs it in that folder with the app's
// child protection, and teaches the player the discover: sentinel.
func TestStartWiresSongSources(t *testing.T) {
	cleanPath(t)
	dataDir := t.TempDir()
	ytPath := putFakeYtdlp(t, dataDir)
	logFile := filepath.Join(t.TempDir(), "yt.log")
	t.Setenv("SCREECH_FAKE_YTDLP_LOG", logFile)

	sink := &fakeSink{}
	logText := wire(t, sink, config.Config{DataDir: dataDir}, t.TempDir(), fakeBandcamp{n: 3})

	if want := "discovery: yt-dlp " + ytPath + " (node), bandcamp on"; !strings.Contains(logText, want) {
		t.Errorf("log lacks %q:\n%s", want, logText)
	}
	if strings.Contains(logText, "googlevideo") || strings.Contains(logText, "bc.example") {
		t.Errorf("a media URL reached the log:\n%s", logText)
	}

	y, ok := sink.ex.(player.YTDLP)
	if !ok {
		t.Fatalf("extractor = %T, want player.YTDLP", sink.ex)
	}
	if y.Runner.Path != ytPath || y.Runner.Dir != dataDir || !strings.HasPrefix(y.Runner.JSRuntime, "node:") {
		t.Errorf("runner = path %q dir %q js %q", y.Runner.Path, y.Runner.Dir, y.Runner.JSRuntime)
	}
	if y.Runner.Protect == nil || y.Runner.Adopt == nil {
		t.Error("the runner has no child-process protection")
	}

	// A page link resolves through that yt-dlp, in the data folder.
	t.Setenv("SCREECH_FAKE_YTDLP", "resolve")
	r, err := sink.ex.Extract(context.Background(), "https://soundcloud.com/a/b")
	if err != nil || r.URL != "https://rr1.googlevideo.com/x" || r.Title != "Entombed - Left Hand Path" {
		t.Fatalf("Extract = %+v, %v", r, err)
	}
	b, _ := os.ReadFile(logFile)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(strings.TrimPrefix(strings.Split(string(b), "\n")[0], "CWD ")))
	want, _ := filepath.EvalSymlinks(dataDir)
	if got != want {
		t.Errorf("yt-dlp ran in %q, want the data folder %q", got, want)
	}

	// A discovery station opens into a source that yields songs, and
	// Bandcamp carries it when YouTube has nothing.
	if sink.open == nil {
		t.Fatal("the player was not taught the discover: sentinel")
	}
	t.Setenv("SCREECH_FAKE_YTDLP", "fail")
	src, err := sink.open("dungeon synth")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := src.Next(ctx)
	if err != nil {
		t.Fatalf("Next = %v", err)
	}
	if !strings.HasPrefix(s.Key, "bc:") || !strings.HasPrefix(s.URL, "https://bc.example/") || s.Artist == "" || s.Title == "" {
		t.Errorf("song = %+v", s)
	}
}

// "off" turns yt-dlp off even with one in the data folder; discovery still
// has its Bandcamp half. Nothing found says so in the log, without a
// complaint: finding none is the ordinary state of a fresh install.
func TestWiringWithoutYtdlp(t *testing.T) {
	cleanPath(t)
	dataDir := t.TempDir()
	putFakeYtdlp(t, dataDir)

	sink := &fakeSink{}
	logText := wire(t, sink, config.Config{DataDir: dataDir, YtdlpPath: "off"}, t.TempDir(), fakeBandcamp{n: 3})
	if !strings.Contains(logText, "discovery: yt-dlp off, bandcamp on") {
		t.Errorf("log = %q", logText)
	}
	if sink.ex != nil {
		t.Errorf("an extractor was installed with yt-dlp off: %T", sink.ex)
	}
	if sink.open == nil {
		t.Fatal("discover: must still be taught: Bandcamp needs no yt-dlp")
	}
	src, err := sink.open("dungeon synth")
	if err != nil {
		t.Fatal(err)
	}
	if s, err := src.Next(context.Background()); err != nil || !strings.HasPrefix(s.Key, "bc:") {
		t.Fatalf("Next = %+v, %v; want a Bandcamp song", s, err)
	}
	// A link can't start without yt-dlp, and that is "nothing to play", not
	// a dead network.
	link, err := sink.open("https://youtu.be/abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	_, err = link.Next(context.Background())
	if !errors.Is(err, discover.ErrNoSources) || errors.Is(err, player.ErrSourceNetwork) {
		t.Fatalf("a link without yt-dlp = %v, want ErrNoSources and not a network error", err)
	}

	empty := t.TempDir()
	sink = &fakeSink{}
	logText = wire(t, sink, config.Config{DataDir: empty}, t.TempDir(), fakeBandcamp{n: 1})
	if !strings.Contains(logText, "discovery: yt-dlp not found, bandcamp on") || sink.ex != nil || sink.open == nil {
		t.Errorf("nothing found: log = %q, extractor %T, open set %v", logText, sink.ex, sink.open != nil)
	}

	// A path the listener wrote down is used as given, and the log says if
	// it isn't there.
	sink = &fakeSink{}
	missing := filepath.Join(empty, "no-such-yt-dlp")
	logText = wire(t, sink, config.Config{DataDir: empty, YtdlpPath: missing}, t.TempDir(), fakeBandcamp{n: 1})
	if !strings.Contains(logText, "yt-dlp not found") || !strings.Contains(logText, missing) || sink.ex == nil {
		t.Errorf("explicit missing path: log = %q, extractor %T", logText, sink.ex)
	}
}

// The player calls open on every Play and every reconnect. A station that
// forgot what it had played each time would repeat itself the moment the
// stream hiccuped, so one queue lives per seed for the whole process, and
// all the ways of writing the same seed are the same station.
func TestOpenKeepsOneQueuePerSeed(t *testing.T) {
	s := newSongSources(nil, fakeBandcamp{n: 1}, nil)
	a, b := s.queue("Dungeon Synth"), s.queue("dungeon synth")
	if a != b {
		t.Error("two spellings of one seed got two queues")
	}
	if s.queue("swedish death metal") == a {
		t.Error("two seeds shared a queue")
	}

	sink := &fakeSink{}
	wire(t, sink, config.Config{DataDir: t.TempDir(), YtdlpPath: "off"}, t.TempDir(), fakeBandcamp{n: 8})
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		// A fresh open each time, as a reconnect makes.
		src, err := sink.open("dungeon synth")
		if err != nil {
			t.Fatal(err)
		}
		song, err := src.Next(context.Background())
		if err != nil {
			t.Fatalf("Next #%d: %v", i+1, err)
		}
		if seen[song.Key] {
			t.Fatalf("Next #%d repeated %s after a reopen", i+1, song.Key)
		}
		seen[song.Key] = true
	}
}

// stubQueue records how it was called and answers from a script.
type stubQueue struct {
	mu         sync.Mutex
	foreground []bool
	err        error
	skips      []bool
	loved      int
}

func (q *stubQueue) Next(ctx context.Context) (discover.Song, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.foreground = append(q.foreground, ytdlp.IsForeground(ctx))
	if q.err != nil {
		return discover.Song{}, q.err
	}
	return discover.Song{Key: "yt:abcdefghijk", Artist: "A", Title: "T", URL: "https://media.example/a", NewToYou: true}, nil
}
func (q *stubQueue) Skipped(quick bool) { q.mu.Lock(); q.skips = append(q.skips, quick); q.mu.Unlock() }
func (q *stubQueue) Loved()             { q.mu.Lock(); q.loved++; q.mu.Unlock() }

// A tune's own lookup jumps the yt-dlp line; the prefetches after it wait
// their turn. "First" belongs to the source, which is per open: a reconnect
// is a tune again.
func TestFirstNextIsForeground(t *testing.T) {
	q := &stubQueue{}
	src := &songAdapter{q: q}
	for i := 0; i < 3; i++ {
		if _, err := src.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	again := &songAdapter{q: q}
	if _, err := again.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, true}
	if fmt.Sprint(q.foreground) != fmt.Sprint(want) {
		t.Fatalf("foreground per Next = %v, want %v", q.foreground, want)
	}
}

func TestAdapterPassesSongsAndFeedback(t *testing.T) {
	q := &stubQueue{}
	src := &songAdapter{q: q}
	s, err := src.Next(context.Background())
	want := player.Song{Key: "yt:abcdefghijk", Artist: "A", Title: "T", URL: "https://media.example/a", NewToYou: true}
	if err != nil || s != want {
		t.Fatalf("Next = %+v, %v; want %+v", s, err, want)
	}
	src.Skipped(true)
	src.Skipped(false)
	src.Loved()
	if fmt.Sprint(q.skips) != "[true false]" || q.loved != 1 {
		t.Errorf("skips = %v loved = %d", q.skips, q.loved)
	}
}

// A lookup that failed for want of a network must not strike the station:
// the player passes ErrSourceNetwork up and the session goes offline
// instead. A lookup that simply found nothing, or was cancelled, must not
// look like that.
func TestNetworkErrorsMapToErrSourceNetwork(t *testing.T) {
	dns := &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "bandcamp.com", IsNotFound: true}}
	post := &url.Error{Op: "Post", URL: "https://bandcamp.com/api/discover/1/discover_web", Err: dns}
	for _, tc := range []struct {
		name string
		err  error
		net  bool
	}{
		{"dns", dns, true},
		{"http transport", post, true},
		{"joined with another source's failure", fmt.Errorf("discover: %w", errors.Join(errors.New("yt-dlp: Video unavailable"), post)), true},
		{"the lookup's own deadline", fmt.Errorf("discover: %w", context.DeadlineExceeded), true},
		{"the queue's own network mark", fmt.Errorf("discover: %w", fmt.Errorf("resolving: %w", discover.ErrNetwork)), true},
		{"bandcamp pacing", fmt.Errorf("discover: %w", discover.ErrPaced), true},
		{"a timeout from deeper in", fmt.Errorf("x: %w", &url.Error{Op: "Get", URL: "https://bandcamp.com/", Err: os.ErrDeadlineExceeded}), true},
		{"already marked", fmt.Errorf("%w: x", player.ErrSourceNetwork), true},

		{"no sources", discover.ErrNoSources, false},
		{"a gone video", errors.New("discover: yt-dlp: Video unavailable"), false},
		{"a private video", errors.New("discover: yt-dlp: [youtube] x: Private video"), false},
		{"a refused status", fmt.Errorf("discover: %w", &discover.StatusError{Code: 403}), false},
		{"a throttling status", fmt.Errorf("discover: %w", &discover.StatusError{Code: 429}), false},
		{"yt-dlp wouldn't start", errors.New("starting yt-dlp (x): file not found"), false},
		{"cancelled", fmt.Errorf("discover: %w", context.Canceled), false},
		{"cancelled over a dns failure", errors.Join(context.Canceled, dns), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &songAdapter{q: &stubQueue{err: tc.err}}
			_, err := src.Next(context.Background())
			if err == nil {
				t.Fatal("Next succeeded")
			}
			if got := errors.Is(err, player.ErrSourceNetwork); got != tc.net {
				t.Fatalf("errors.Is(ErrSourceNetwork) = %v for %v, want %v", got, err, tc.net)
			}
			// The cause stays reachable either way.
			if !errors.Is(err, tc.err) {
				t.Errorf("the original error was lost: %v", err)
			}
		})
	}
}

// The adapter turns yt-dlp's flat rows and resolved media into the queue's
// own types, field for field.
func TestYouTubeAdapter(t *testing.T) {
	r := ytdlp.Runner{Path: os.Args[0]}
	yt := ytAdapter{r}
	ctx := context.Background()

	t.Setenv("SCREECH_FAKE_YTDLP", "search")
	hits, err := yt.Songs(ctx, "x", 2)
	want := []discover.YTHit{{ID: "knQEA9D-Qq0", Title: "Left Hand Path"}, {ID: "gZSzqfZ4udE", Title: "Spiritual Healing"}}
	if err != nil || fmt.Sprint(hits) != fmt.Sprint(want) {
		t.Fatalf("Songs = %v, %v; want %v", hits, err, want)
	}
	if mix, err := yt.Mix(ctx, "knQEA9D-Qq0", 2); err != nil || len(mix) != 2 {
		t.Fatalf("Mix = %v, %v", mix, err)
	}

	t.Setenv("SCREECH_FAKE_YTDLP", "resolve")
	m, err := yt.Resolve(ctx, "https://www.youtube.com/watch?v=knQEA9D-Qq0")
	if err != nil {
		t.Fatal(err)
	}
	if m.URL != "https://rr1.googlevideo.com/x" || m.Artist != "Entombed" || m.Track != "Left Hand Path" ||
		m.Title == "" || m.Channel != "Entombed - Topic" || m.Duration != 356 || m.Views != 1234 ||
		fmt.Sprint(m.Tags) != "[death metal]" {
		t.Errorf("media = %+v", m)
	}

	t.Setenv("SCREECH_FAKE_YTDLP", "fail")
	if _, err := yt.Resolve(ctx, "https://www.youtube.com/watch?v=gone"); err == nil {
		t.Error("a failed run must fail Resolve")
	}
}

// yt-dlp reports a dead connection only in words on its last stderr line.
// The YouTube adapter marks those with discover.ErrNetwork, so the queue
// doesn't count them against a video and the session goes offline instead of
// striking the station; a video that is simply gone stays an ordinary error.
func TestYtdlpConnectionTroubleIsMarked(t *testing.T) {
	for _, tc := range []struct {
		msg string
		net bool
	}{
		{"yt-dlp: ERROR: [youtube:search] x: Unable to download API page: <urlopen error [Errno 11001] getaddrinfo failed>", true},
		{"yt-dlp: ERROR: [youtube] x: Failed to resolve 'www.youtube.com' ([Errno -2] Name or service not known)", true},
		{"yt-dlp: ERROR: <urlopen error [Errno -3] Temporary failure in name resolution>", true},
		{"yt-dlp: ERROR: HTTPSConnectionPool(host='www.youtube.com', port=443): Read timed out.", true},
		{"yt-dlp: ERROR: [Errno 111] Connection refused", true},
		{"yt-dlp: ERROR: ('Connection aborted.', ConnectionResetError(10054, 'An existing connection was forcibly closed'))", true},
		{"yt-dlp: ERROR: Connection reset by peer", true},
		{"yt-dlp: ERROR: unable to download video data: HTTP Error 503: Service Unavailable", true},
		{"yt-dlp: ERROR: [youtube] x: HTTP Error 502: Bad Gateway", true},
		{"YT-DLP: ERROR: UNABLE TO DOWNLOAD WEBPAGE", true},

		{"yt-dlp: ERROR: [youtube] gone: Video unavailable", false},
		{"yt-dlp: ERROR: [youtube] x: Private video. Sign in if you've been granted access to this video", false},
		{"yt-dlp: ERROR: [youtube] x: Sign in to confirm you're not a bot", false},
		{"yt-dlp: ERROR: [youtube] x: This video is not available in your country", false},
		{"yt-dlp printed no stream URL", false},
		{"yt-dlp: ERROR: HTTP Error 404: Not Found", false},
	} {
		src := errors.New(tc.msg)
		got := connectionTrouble(src)
		if errors.Is(got, discover.ErrNetwork) != tc.net {
			t.Errorf("connectionTrouble(%q) marked = %v, want %v", tc.msg, !tc.net, tc.net)
		}
		if !errors.Is(got, src) {
			t.Errorf("connectionTrouble(%q) lost the original error", tc.msg)
		}
	}
	if connectionTrouble(nil) != nil {
		t.Error("no error must stay no error")
	}
	// Our own deadline is already a net.Error; it is not re-labelled.
	if got := connectionTrouble(context.Canceled); errors.Is(got, discover.ErrNetwork) {
		t.Error("a cancelled lookup is not a network failure")
	}
}

// Through the adapter and a real process: a yt-dlp that couldn't reach
// YouTube comes out marked, and one that found the video gone does not.
func TestYouTubeAdapterMarksOfflineRuns(t *testing.T) {
	yt := ytAdapter{ytdlp.Runner{Path: os.Args[0]}}
	t.Setenv("SCREECH_FAKE_YTDLP", "offline")
	if _, err := yt.Songs(context.Background(), "x", 1); !errors.Is(err, discover.ErrNetwork) {
		t.Errorf("Songs offline = %v, want it marked discover.ErrNetwork", err)
	}
	if _, err := yt.Resolve(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk"); !errors.Is(err, discover.ErrNetwork) {
		t.Errorf("Resolve offline = %v, want it marked discover.ErrNetwork", err)
	}
	t.Setenv("SCREECH_FAKE_YTDLP", "fail")
	if _, err := yt.Resolve(context.Background(), "https://www.youtube.com/watch?v=gone"); err == nil || errors.Is(err, discover.ErrNetwork) {
		t.Errorf("a gone video = %v, want an ordinary error", err)
	}
}
