package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/discover"
	"github.com/tinkthemaker/screech/internal/player"
	versionpkg "github.com/tinkthemaker/screech/internal/version"
	"github.com/tinkthemaker/screech/internal/ytdlp"
)

// The wiring for discovery stations: which yt-dlp there is, a Bandcamp client,
// and the adapters that join discover (which imports neither the player nor
// the process runner) to the player's SongSource and to ytdlp.Runner.

// discoverySink is the part of the player that discovery touches, so the
// wiring can be tested without an mpv.
type discoverySink interface {
	SetExtractor(ex player.Extractor)
	SetSongSources(open func(seed string) (player.SongSource, error))
}

// discoveryDeps is what wireDiscovery reads. Bandcamp is left nil by the real
// startup, which then builds the real client.
type discoveryDeps struct {
	Config   config.Config
	ExeDir   string
	Known    discover.Known
	Bandcamp discover.Bandcamp
	Log      *Log
}

// bandcampTimeout bounds one request to Bandcamp. A tune gives the whole
// first-song lookup 8 s, and the queue hands Bandcamp a quarter of that when
// YouTube is waiting; a request that can outlast the whole budget would make
// the budget the thing that fails.
const bandcampTimeout = 4 * time.Second

// newBandcamp is the client discovery stations ask Bandcamp's genre pages
// with.
func newBandcamp() discover.Bandcamp {
	return discover.BandcampClient{
		HTTP:      &http.Client{Timeout: bandcampTimeout},
		UserAgent: versionpkg.UserAgent(),
	}
}

// wireDiscovery decides what yt-dlp there is and teaches the player what to
// do with it: page links resolve through it, and the discover: sentinel opens
// song queues that use it when it is there and play from Bandcamp when it is
// not. Nothing here fails startup; at worst a station can't start and says so.
// It reports whether there is a yt-dlp to use, which the faces need in order
// to say why a YouTube link cannot start a station.
func wireDiscovery(sink discoverySink, d discoveryDeps) (haveYtdlp bool) {
	cfg := d.Config
	path, haveYt := ytdlp.Find(cfg.YtdlpPath, d.ExeDir, cfg.DataDir)

	var yt discover.YouTube // stays a nil interface when there's no yt-dlp
	var state string
	switch {
	case haveYt:
		runner := ytdlp.Runner{
			Path:      path,
			JSRuntime: ytdlp.DetectJSRuntime(),
			Dir:       cfg.DataDir,
			// Every yt-dlp is a child of Screech that must not outlive it,
			// nor a timed-out lookup: the player's protection, lent here.
			Protect: player.ProtectChild,
			Adopt:   player.AdoptChild,
		}
		sink.SetExtractor(player.YTDLP{Runner: runner})
		yt = ytAdapter{runner}
		if strings.TrimSpace(cfg.YtdlpPath) != "" {
			if _, err := exec.LookPath(path); err != nil {
				// Not fatal: a lookup that can't start fails on its own,
				// like any dead stream. The path is the listener's to fix.
				d.Log.Linef("yt-dlp not found (%v); YouTube songs and page links will fail", err)
			}
		}
		state = fmt.Sprintf("yt-dlp %s (%s)", path, ytdlp.JSRuntimeName())
	case strings.EqualFold(strings.TrimSpace(cfg.YtdlpPath), "off"):
		state = "yt-dlp off"
	default:
		state = "yt-dlp not found"
	}

	bc := d.Bandcamp
	if bc == nil {
		bc = newBandcamp()
	}
	sink.SetSongSources(newSongSources(yt, bc, d.Known).open)
	// Never a media URL: they are signed, and a log gets pasted.
	d.Log.Linef("discovery: %s, bandcamp on", state)
	return haveYt
}

// songSources opens a discovery station's songs.
//
// The player calls open on every Play and every reconnect, with the same
// seed each time. What a station has played, who played last, and what it
// has already fetched are the queue's state, so the queue is kept per seed
// for the life of the process; open hands out a fresh adapter over it.
// Otherwise a stream hiccup would start the station over and it would repeat
// itself.
type songSources struct {
	yt    discover.YouTube
	bc    discover.Bandcamp
	known discover.Known

	mu     sync.Mutex
	queues map[string]*discover.Queue
}

func newSongSources(yt discover.YouTube, bc discover.Bandcamp, known discover.Known) *songSources {
	return &songSources{yt: yt, bc: bc, known: known, queues: map[string]*discover.Queue{}}
}

// queue is the one queue of a seed. Keyed the way the station's identity is,
// so every way of writing the same seed is the same station.
func (s *songSources) queue(seed string) *discover.Queue {
	key := core.DiscoveryKey(seed)
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queues[key]
	if !ok {
		q = discover.NewQueue(seed, s.yt, s.bc, s.known, nil, nil)
		s.queues[key] = q
	}
	return q
}

func (s *songSources) open(seed string) (player.SongSource, error) {
	return &songAdapter{q: s.queue(seed)}, nil
}

// songQueue is the part of discover.Queue the adapter uses.
type songQueue interface {
	Next(ctx context.Context) (discover.Song, error)
	Skipped(quick bool)
	Loved()
}

// songAdapter is discover.Queue seen as the player's SongSource. One is made
// per open, over the seed's shared queue.
type songAdapter struct {
	q     songQueue
	asked atomic.Bool
}

func (a *songAdapter) Next(ctx context.Context) (player.Song, error) {
	// The first Next after an open is the tune's own lookup, with a listener
	// waiting; it goes ahead of other stations' prefetches for yt-dlp. Every
	// later one is a prefetch for a song that isn't needed yet.
	if !a.asked.Swap(true) {
		ctx = ytdlp.Foreground(ctx)
	}
	s, err := a.q.Next(ctx)
	if err != nil {
		return player.Song{}, sourceError(err)
	}
	return player.Song{Key: s.Key, Artist: s.Artist, Title: s.Title, URL: s.URL, NewToYou: s.NewToYou}, nil
}

func (a *songAdapter) Skipped(quick bool) { a.q.Skipped(quick) }
func (a *songAdapter) Loved()             { a.q.Loved() }

// sourceError marks a lookup that failed for want of a network as
// player.ErrSourceNetwork, so the session goes offline instead of striking a
// station that did nothing wrong. Anything else, ErrNoSources included (that
// is "nothing to play", not "couldn't ask"), passes through as it is.
func sourceError(err error) error {
	if errors.Is(err, player.ErrSourceNetwork) || !isNetworkFailure(err) {
		return err
	}
	return fmt.Errorf("%w: %w", player.ErrSourceNetwork, err)
}

// isNetworkFailure reads discover's error, which joins what each source
// said. Bandcamp's are Go's own (DNS, dial, any transport error, all
// net.Error), and yt-dlp's are marked discover.ErrNetwork by ytAdapter. A
// cancelled lookup is neither; the player dropped it on purpose.
//
// A lookup that merely ran out of time is not an outage either, though
// context.DeadlineExceeded satisfies net.Error and errors.As would say so: the
// song or the tune failed and is struck like any other, where an outage would
// have the station wait and retry. Only a source's own words (ErrNetwork)
// make a deadline an outage, as when yt-dlp could not reach YouTube before the
// clock ran out. Bandcamp's own client timeout, with the caller still waiting,
// is marked that way by the client.
func isNetworkFailure(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	// ErrPaced is Bandcamp's gap, not trouble of its own, but it only comes
	// back when the context gave no room to wait it out, and the same slow
	// network that ate the time is why: retry shortly, offline-style, rather
	// than strike a station that did nothing wrong.
	if errors.Is(err, discover.ErrNetwork) || errors.Is(err, discover.ErrPaced) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// ytAdapter is ytdlp.Runner seen as discover.YouTube. The text it passes on
// is raw network text; discover cleans it before it is kept or shown.
type ytAdapter struct{ r ytdlp.Runner }

func (a ytAdapter) Songs(ctx context.Context, query string, n int) ([]discover.YTHit, error) {
	hits, err := a.r.Songs(ctx, query, n)
	return ytHits(hits), connectionTrouble(err)
}

func (a ytAdapter) Mix(ctx context.Context, videoID string, n int) ([]discover.YTHit, error) {
	hits, err := a.r.Mix(ctx, videoID, n)
	return ytHits(hits), connectionTrouble(err)
}

func (a ytAdapter) Resolve(ctx context.Context, page string) (discover.YTMedia, error) {
	m, err := a.r.Resolve(ctx, page)
	if err != nil {
		return discover.YTMedia{}, connectionTrouble(err)
	}
	return discover.YTMedia{
		URL: m.URL, Artist: m.Artist, Track: m.Track, Title: m.Title, Channel: m.Channel,
		Duration: m.Duration, Views: m.Views, Tags: m.Tags,
	}, nil
}

// connectionWords are what yt-dlp (Python's urllib and requests underneath)
// says on its last line when it never reached YouTube. Without a status in
// the line the words are the only signal; with one, connectionTrouble reads
// the status first. They are lowercase: the line is compared lowercased.
var connectionWords = []string{
	"unable to download",
	"getaddrinfo",
	"failed to resolve",
	"timed out",
	"connection refused",
	"connection reset",
	"connection aborted",
	"temporary failure in name resolution",
}

// httpStatus finds the status in a (lowercased) yt-dlp line like "HTTP Error
// 403: Forbidden".
var httpStatus = regexp.MustCompile(`http error (\d{3})`)

// connectionTrouble marks a yt-dlp failure that reads like a connection
// problem with discover.ErrNetwork, keeping yt-dlp's own words reachable.
// discover then doesn't count it against a video or a linked seed (an outage
// says nothing about them), and the song adapter hands the player
// ErrSourceNetwork. A video that is unavailable or private, and every other
// ordinary extraction error, comes back as it was.
//
// A status in the line settles it, because then the server answered and
// "Unable to download" only says the page didn't come. 429 (rate limited) and
// 5xx (the server's own trouble) are outages to wait out. Any other 4xx is a
// refusal: yt-dlp is blocked, or the video is gone. Calling that "offline"
// would have the station retry forever behind a block that never lifts.
func connectionTrouble(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	msg := strings.ToLower(err.Error())
	outage := false
	if m := httpStatus.FindStringSubmatch(msg); m != nil {
		code, _ := strconv.Atoi(m[1])
		switch {
		case code == http.StatusTooManyRequests || code >= 500:
			outage = true
		case code >= 400:
			return err
		}
	}
	for _, w := range connectionWords {
		if !outage && strings.Contains(msg, w) {
			outage = true
		}
	}
	if !outage {
		return err
	}
	return fmt.Errorf("%w: %w", discover.ErrNetwork, err)
}

func ytHits(in []ytdlp.Hit) []discover.YTHit {
	if in == nil {
		return nil
	}
	out := make([]discover.YTHit, len(in))
	for i, h := range in {
		out[i] = discover.YTHit{ID: h.ID, Title: h.Title}
	}
	return out
}
