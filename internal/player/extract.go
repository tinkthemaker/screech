package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Page links (a YouTube video, a SoundCloud track) aren't streams: mpv runs
// with --ytdl=no so a dead radio URL fails fast instead of waiting on
// yt-dlp, which means pages are resolved here, in the tune path, under a
// timeout of our own. Off unless the listener sets ytdlp_path.

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

// YTDLP runs yt-dlp at Path once per extraction.
type YTDLP struct{ Path string }

// ytdlpPrefix marks the --print lines; whatever else yt-dlp prints that
// parses as an http(s) URL is the stream, so the parse doesn't depend on
// the order --get-url and --print come out in.
const ytdlpPrefix = "screech-"

func ytdlpArgs(page string) []string {
	return []string{
		// bestaudio first; YouTube live streams only offer muxed HLS, and
		// mpv plays those audio-only under --no-video.
		"-f", "bestaudio/best",
		"--no-playlist", "--playlist-items", "1",
		"--no-warnings",
		"--get-url",
		"--print", ytdlpPrefix + "artist:%(artist|)s",
		"--print", ytdlpPrefix + "track:%(track|)s",
		"--print", ytdlpPrefix + "title:%(title|)s",
		"--", page,
	}
}

func (y YTDLP) Extract(ctx context.Context, page string) (Resolved, error) {
	cmd := exec.CommandContext(ctx, y.Path, ytdlpArgs(page)...)
	// yt-dlp's standalone build is a launcher with a child holding the
	// pipes; once the context kills the launcher, don't wait on the child.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureCmd(cmd)
	if err := cmd.Start(); err != nil {
		return Resolved{}, fmt.Errorf("starting yt-dlp (%s): %w", y.Path, err)
	}
	afterStart(cmd)
	err := cmd.Wait()
	if ctx.Err() != nil {
		return Resolved{}, fmt.Errorf("yt-dlp gave up: %w", ctx.Err())
	}
	if err != nil {
		return Resolved{}, fmt.Errorf("yt-dlp: %s", lastLine(stderr.String(), err.Error()))
	}
	return parseYTDLP(stdout.String())
}

// parseYTDLP reads yt-dlp's output: the stream URL, and the tagged artist,
// track and title lines. A real track ("Artist - Track") beats the page
// title, which for a video is often the uploader's own wording.
func parseYTDLP(out string) (Resolved, error) {
	var r Resolved
	meta := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if kv, ok := strings.CutPrefix(line, ytdlpPrefix); ok {
			if k, v, ok := strings.Cut(kv, ":"); ok {
				meta[k] = strings.TrimSpace(v)
			}
			continue
		}
		if r.URL == "" && validStreamURL(line) {
			r.URL = line
		}
	}
	if r.URL == "" {
		return Resolved{}, errors.New("yt-dlp printed no stream URL")
	}
	switch {
	case meta["artist"] != "" && meta["track"] != "":
		r.Title = meta["artist"] + " - " + meta["track"]
	default:
		r.Title = meta["title"]
	}
	return r, nil
}

func lastLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s[strings.LastIndexByte(s, '\n')+1:]
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
