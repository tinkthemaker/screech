package player

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/tinkthemaker/screech/internal/ytdlp"
)

// Page links (a YouTube video, a SoundCloud track) aren't streams: mpv runs
// with --ytdl=no so a dead radio URL fails fast instead of waiting on
// yt-dlp, which means pages are resolved here, in the tune path, under a
// timeout of our own. On whenever yt-dlp is found (ytdlp_path), off when the
// listener turned it off or none is installed.

// ErrResolve wraps every failure to turn a page link into a stream. Play
// returns it, and the session treats it like any rejected URL: a strike,
// and on to the next station.
var ErrResolve = errors.New("page link did not resolve")

// ErrSuperseded is Play's answer when another Play began while this one
// was resolving its page link. Nothing was loaded; the newer station owns
// the player and this one must leave no trace (no strike, no listen).
var ErrSuperseded = errors.New("play superseded by a newer one")

const (
	// resolveTimeout bounds one extraction. The tune watchdog gives a
	// station 12 s from the pick to lock; this leaves mpv half of it.
	resolveTimeout = 6 * time.Second
	// resolvedTTL keeps a resolved stream URL well inside the roughly six
	// hours a signed YouTube URL stays valid. A failure or end of stream
	// forgets it sooner.
	resolvedTTL = time.Hour
)

// Resolved is what an Extractor found behind a page link.
type Resolved struct {
	URL   string // the direct media URL: signed and short-lived, so never stored
	Title string // "Artist - Track", else the page's title, else ""
}

// Extractor turns a page link into something mpv can open. YTDLP is the
// real one; tests substitute their own, so unit tests never run yt-dlp
// and never touch the network.
type Extractor interface {
	Extract(ctx context.Context, page string) (Resolved, error)
}

// YTDLP resolves a page link through yt-dlp, by way of ytdlp.Runner: the one
// place Screech runs it, so a tune's lookup takes its turn behind whatever
// yt-dlp is already running, and under the same working folder, JS runtime
// and child protection as discovery's.
type YTDLP struct {
	// Path runs yt-dlp as it is, with nothing else set, for a caller that
	// has only a path. Runner, when its Path is set, is used instead.
	Path   string
	Runner ytdlp.Runner
}

func (y YTDLP) Extract(ctx context.Context, page string) (Resolved, error) {
	r := y.Runner
	if r.Path == "" {
		r.Path = y.Path
	}
	// Extract is only ever the tune path (pageResolver.resolve): the
	// listener is waiting on it, so it goes ahead of discovery's prefetches.
	m, err := r.Resolve(ytdlp.Foreground(ctx), page)
	if err != nil {
		return Resolved{}, err
	}
	return resolvedFrom(m), nil
}

// resolvedFrom reads the title a page link shows: a real track ("Artist -
// Track") beats the page title, which for a video is often the uploader's
// own wording.
func resolvedFrom(m ytdlp.Media) Resolved {
	r := Resolved{URL: m.URL, Title: m.Title}
	if m.Artist != "" && m.Track != "" {
		r.Title = m.Artist + " - " + m.Track
	}
	return r
}

// validStreamURL is Play's rule for what mpv may be handed.
func validStreamURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// pageResolver remembers what each page resolved to, in memory only: the
// URLs are signed and expire, so nothing here may outlive the process.
type pageResolver struct {
	ex      Extractor
	timeout time.Duration
	ttl     time.Duration
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cachedPage
}

type cachedPage struct {
	Resolved
	at time.Time
}

func newPageResolver(ex Extractor) *pageResolver {
	return &pageResolver{ex: ex, timeout: resolveTimeout, ttl: resolvedTTL, now: time.Now, cache: map[string]cachedPage{}}
}

// resolve answers from the cache while it's fresh, else runs the
// extractor under the timeout. Every failure wraps ErrResolve.
func (p *pageResolver) resolve(page string) (Resolved, error) {
	p.mu.Lock()
	c, ok := p.cache[page]
	p.mu.Unlock()
	if ok && p.now().Sub(c.at) < p.ttl {
		return c.Resolved, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	r, err := p.ex.Extract(ctx, page)
	if err != nil {
		return Resolved{}, fmt.Errorf("%w: %v", ErrResolve, err)
	}
	if !validStreamURL(r.URL) {
		return Resolved{}, fmt.Errorf("%w: not an http(s) stream: %q", ErrResolve, r.URL)
	}
	p.mu.Lock()
	p.cache[page] = cachedPage{Resolved: r, at: p.now()}
	p.mu.Unlock()
	return r, nil
}

// forget drops a page's answer, so the next Play asks again: the stream it
// pointed at failed or ended, and an expired signature looks just like that.
func (p *pageResolver) forget(page string) {
	p.mu.Lock()
	delete(p.cache, page)
	p.mu.Unlock()
}
