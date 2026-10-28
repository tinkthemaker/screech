package player

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// runFakeYTDLP is the test binary standing in for yt-dlp (TestMain routes
// here when SCREECH_FAKE_YTDLP is set). It checks the argv the real client
// builds, then answers per mode, so the exec path runs offline.
func runFakeYTDLP(mode string, args []string) int {
	page := ""
	if i := slices.Index(args, "--"); i >= 0 && i+1 < len(args) {
		page = args[i+1]
	}
	for _, want := range []string{"bestaudio/best", "--no-playlist", "--get-url"} {
		if !slices.Contains(args, want) {
			fmt.Fprintf(os.Stderr, "ERROR: fake yt-dlp: missing %s in %q\n", want, args)
			return 2
		}
	}
	switch mode {
	case "track":
		// --print lines and the URL in yt-dlp's own order, which the
		// client must not depend on.
		fmt.Println("screech-artist:Nils Frahm")
		fmt.Println("http://stream.test/resolved")
		fmt.Println("screech-track:Says")
		fmt.Println("screech-title:Nils Frahm - Says (Live at the Barbican)")
	case "title":
		fmt.Println("screech-artist:")
		fmt.Println("screech-track:")
		fmt.Println("screech-title:lofi beats to study to")
		fmt.Println("http://stream.test/resolved")
	case "fail":
		fmt.Fprintln(os.Stderr, "WARNING: something minor")
		fmt.Fprintf(os.Stderr, "ERROR: [youtube] %s: Video unavailable\n", page)
		return 1
	case "slow":
		time.Sleep(30 * time.Second)
	}
	return 0
}

func fakeYTDLP(t *testing.T, mode string) YTDLP {
	t.Helper()
	t.Setenv("SCREECH_FAKE_YTDLP", mode)
	return YTDLP{Path: os.Args[0]}
}

func TestYTDLPExtract(t *testing.T) {
	ctx := context.Background()
	r, err := fakeYTDLP(t, "track").Extract(ctx, "https://www.youtube.com/watch?v=a")
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "http://stream.test/resolved" || r.Title != "Nils Frahm - Says" {
		t.Errorf("track = %+v, want the stream and artist - track", r)
	}
	r, err = fakeYTDLP(t, "title").Extract(ctx, "https://www.youtube.com/watch?v=b")
	if err != nil || r.Title != "lofi beats to study to" {
		t.Errorf("title-only = %+v, %v; want the page title", r, err)
	}
	_, err = fakeYTDLP(t, "fail").Extract(ctx, "https://www.youtube.com/watch?v=c")
	if err == nil || !strings.Contains(err.Error(), "Video unavailable") {
		t.Errorf("failure = %v, want yt-dlp's last error line", err)
	}
}

// The hard timeout holds even when yt-dlp hangs: the tune path can't wait
// on it past its budget.
func TestYTDLPTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := fakeYTDLP(t, "slow").Extract(ctx, "https://www.youtube.com/watch?v=d")
	if err == nil {
		t.Fatal("a hung yt-dlp must fail")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v to give up on a hung yt-dlp", d)
	}
}

func TestParseYTDLPNeedsAStream(t *testing.T) {
	for _, out := range []string{"", "screech-title:x\n", "file:///etc/passwd\nscreech-title:x\n", "rtmp://live.example/x\n"} {
		if _, err := parseYTDLP(out); err == nil {
			t.Errorf("parseYTDLP(%q) found a stream", out)
		}
	}
}

// stubExtractor counts calls, answers from a table, and can be held at a
// gate to model a slow yt-dlp.
type stubExtractor struct {
	mu    sync.Mutex
	calls int
	r     Resolved
	err   error
	gate  chan struct{}
}

func (s *stubExtractor) Extract(ctx context.Context, page string) (Resolved, error) {
	s.mu.Lock()
	s.calls++
	gate, r, err := s.gate, s.r, s.err
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return r, err
}

func (s *stubExtractor) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestPageResolverCaches(t *testing.T) {
	ex := &stubExtractor{r: Resolved{URL: "https://media.example/a.webm", Title: "A - B"}}
	p := newPageResolver(ex)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	page := "https://youtu.be/x"
	for i := 0; i < 3; i++ {
		if _, err := p.resolve(page); err != nil {
			t.Fatal(err)
		}
	}
	if ex.count() != 1 {
		t.Fatalf("%d extractions for one fresh page, want 1", ex.count())
	}
	now = now.Add(resolvedTTL)
	if _, err := p.resolve(page); err != nil || ex.count() != 2 {
		t.Fatalf("after the TTL: calls=%d err=%v, want a fresh extraction", ex.count(), err)
	}
	p.forget(page)
	if _, err := p.resolve(page); err != nil || ex.count() != 3 {
		t.Fatalf("after forget: calls=%d err=%v, want a fresh extraction", ex.count(), err)
	}

	bad := newPageResolver(&stubExtractor{r: Resolved{URL: "file:///etc/passwd"}})
	if _, err := bad.resolve(page); !errors.Is(err, ErrResolve) {
		t.Fatalf("a non-http answer = %v, want ErrResolve", err)
	}
	failing := &stubExtractor{err: errors.New("Video unavailable")}
	fp := newPageResolver(failing)
	for i := 0; i < 2; i++ {
		if _, err := fp.resolve(page); !errors.Is(err, ErrResolve) {
			t.Fatalf("a failed extraction = %v, want ErrResolve", err)
		}
	}
	if failing.count() != 2 {
		t.Fatal("a failure must not be cached")
	}
}

func TestFakePlayRejectsNonHTTP(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	m.SetExtractor(&stubExtractor{r: Resolved{URL: "http://stream.test/resolved"}})
	for _, bad := range []string{"file:///etc/passwd", "rtmp://live.example/x", "http:///no-host", "youtube.com/watch?v=x", ""} {
		if err := m.Play(bad); err == nil || !strings.Contains(err.Error(), "only http/https") {
			t.Errorf("Play(%q) = %v, want it rejected", bad, err)
		}
	}
	waitForLog(t, logPath, "observe_property")
	if strings.Contains(logText(t, logPath), "loadfile") {
		t.Fatalf("a rejected URL reached mpv:\n%s", logText(t, logPath))
	}
}

// A page link resolves before mpv sees anything: the old station stops
// first (its end-file swallowed, never read as Stop), the stream URL is
// loaded, and the page's title rides media-title intact, commas and all.
func TestFakePlayResolvesPageLink(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	title := `Ólafur Arnalds, "Saman" - live=1 100%`
	m.SetExtractor(&stubExtractor{r: Resolved{URL: "http://stream.test/resolved", Title: title}})
	if err := m.Play("http://stream.test/ok"); err != nil {
		t.Fatal(err)
	}
	// The old station's own title lands after it starts playing; take it
	// so the next title can only be the page's.
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventTitle })

	if err := m.Play("https://www.youtube.com/watch?v=x"); err != nil {
		t.Fatalf("Play(page): %v", err)
	}
	ev := waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool {
		if ev.Type == EventStopped {
			t.Fatal("the pre-resolve stop leaked as EventStopped")
		}
		return ev.Type == EventTitle
	})
	if ev.Title != title {
		t.Fatalf("title = %q, want %q", ev.Title, title)
	}
	text := logText(t, logPath)
	want := fmt.Sprintf(`"loadfile","http://stream.test/resolved","replace",-1,"force-media-title=%%%d%%`, len(title))
	if !strings.Contains(text, `"stop"`) || !strings.Contains(text, want) {
		t.Fatalf("log lacks the stop and %s:\n%s", want, text)
	}
	if strings.Contains(text, `"loadfile","https://www.youtube.com`) {
		t.Fatal("the page link itself must never reach mpv once an extractor is set")
	}
}

// Without metadata the page's stream gets no forced title: the URL is
// junk, and the filename filter keeps it out of the heard list.
func TestFakePlayPageWithoutTitle(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	m.SetExtractor(&stubExtractor{r: Resolved{URL: "http://stream.test/aac-128"}})
	if err := m.Play("https://soundcloud.com/a/b"); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	evs, _ := collectFor(m.Events(), 300*time.Millisecond)
	for _, ev := range evs {
		if ev.Type == EventTitle {
			t.Fatalf("filename leaked as a title: %q", ev.Title)
		}
	}
	if text := logText(t, logPath); strings.Contains(text, "force-media-title") {
		t.Fatalf("no metadata must mean no forced title:\n%s", text)
	}
}

// A page that won't resolve fails Play with ErrResolve, which the session
// turns into PlayFailed: a strike, and the next station.
func TestFakePlayResolveFailure(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	m.SetExtractor(&stubExtractor{err: errors.New("ERROR: Video unavailable")})
	err := m.Play("https://www.youtube.com/watch?v=gone")
	if !errors.Is(err, ErrResolve) || !strings.Contains(err.Error(), "Video unavailable") {
		t.Fatalf("Play = %v, want ErrResolve carrying yt-dlp's reason", err)
	}
	if strings.Contains(logText(t, logPath), "loadfile") {
		t.Fatal("nothing may load when resolution fails")
	}
}

// A stream that fails forgets its resolution, so the reconnect or the next
// visit asks yt-dlp again instead of replaying an expired URL.
func TestFakeFailedStreamForgetsResolution(t *testing.T) {
	m, _ := startFakeMPV(t, "default")
	ex := &stubExtractor{r: Resolved{URL: "http://stream.test/error", Title: "A - B"}}
	m.SetExtractor(ex)
	page := "https://vimeo.com/1"
	if err := m.Play(page); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventStreamError })
	if err := m.Play(page); err != nil {
		t.Fatal(err)
	}
	if ex.count() != 2 {
		t.Fatalf("%d extractions, want a fresh one after the stream failed", ex.count())
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventStreamError })

	ex.mu.Lock()
	ex.r.URL = "http://stream.test/ok"
	ex.mu.Unlock()
	if err := m.Play(page); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, m.Events(), 3*time.Second, func(ev Event) bool { return ev.Type == EventPlaying })
	if err := m.Play(page); err != nil || ex.count() != 3 {
		t.Fatalf("a healthy stream's answer should be reused within the TTL: calls=%d err=%v", ex.count(), err)
	}
}

// The listener moves on while yt-dlp is still working: the newer Play
// loads at once, and the late resolution loads nothing over it.
func TestFakePlaySupersededWhileResolving(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	ex := &stubExtractor{r: Resolved{URL: "http://stream.test/resolved", Title: "Late - Arrival"}, gate: make(chan struct{})}
	m.SetExtractor(ex)
	done := make(chan error, 1)
	go func() { done <- m.Play("https://www.youtube.com/watch?v=slow") }()
	deadline := time.Now().Add(3 * time.Second)
	for ex.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Play("http://stream.test/ok2"); err != nil {
		t.Fatal(err)
	}
	close(ex.gate)
	if err := <-done; !errors.Is(err, ErrSuperseded) {
		t.Fatalf("the late Play = %v, want ErrSuperseded", err)
	}
	waitForLog(t, logPath, `"loadfile","http://stream.test/ok2"`)
	if strings.Contains(logText(t, logPath), "stream.test/resolved") {
		t.Fatalf("a superseded resolution loaded over the newer station:\n%s", logText(t, logPath))
	}
}

// With ytdlp_path off nothing changes: a page link goes to mpv as-is.
func TestFakePlayPageWithoutExtractor(t *testing.T) {
	m, logPath := startFakeMPV(t, "default")
	if err := m.Play("https://www.youtube.com/watch?v=x"); err != nil {
		t.Fatal(err)
	}
	text := waitForLog(t, logPath, `"loadfile","https://www.youtube.com/watch?v=x"`)
	if strings.Contains(text, `"stop"`) {
		t.Fatal("no extractor, no pre-resolve stop")
	}
}
