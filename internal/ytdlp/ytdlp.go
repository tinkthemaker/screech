// Package ytdlp is the one place Screech runs yt-dlp. Discovery, a tune's
// page link and doctor all come through here, so the process rules hold
// app-wide: one yt-dlp at a time, a fixed working folder, a JS runtime when
// the machine has one, and a hard stop when the caller's context ends.
//
// Nothing here caches, and nothing here is stored: the media URLs it hands
// back are signed and short-lived. Text in a Hit or Media is raw network
// text; callers run it through core.CleanText before they keep or show it.
package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner runs yt-dlp the way Screech wants it run. The zero Dir and
// JSRuntime are fine (no fixed folder, no runtime flag), which is how
// Version and tests use it.
type Runner struct {
	Path      string // the yt-dlp executable
	JSRuntime string // "" or "node:<path>" / "bun:<path>"; passed as --js-runtimes
	// Dir is where yt-dlp runs: the data folder, so whatever it drops
	// lands somewhere Screech owns and not wherever the listener happened
	// to launch from.
	Dir string

	// Protect and Adopt are how the app lends yt-dlp the player's
	// child-process protection without this package importing the player
	// (the player imports this one). Both are optional.
	//
	// Protect runs on the command before it starts, for what has to be set
	// up front (a parent-death signal, a hidden console).
	Protect func(cmd *exec.Cmd)
	// Adopt runs the moment the process has started and returns the release
	// to run when the run is over, or the context ends first: it is called
	// exactly once, before the app-wide turn is handed on. It exists
	// because a standalone yt-dlp is a launcher with a child: killing the
	// launcher when the context ends leaves the child running, still
	// holding the turn's worth of YouTube's patience. Adopt puts both in
	// one group (a Windows job object) and the release closes it.
	Adopt func(cmd *exec.Cmd) (release func())
}

// Hit is one entry of a flat search or mix: a video id and its title, which
// is all a flat listing carries (no views, no duration).
type Hit struct{ ID, Title string }

// Media is what yt-dlp found behind a page link. Fields yt-dlp doesn't know
// are zero; only URL is guaranteed.
type Media struct {
	URL      string  // the direct media URL: signed and short-lived, so never stored or logged
	Artist   string  // yt-dlp's "artist" metadata
	Track    string  // yt-dlp's "track" metadata
	Title    string  // the page's own title
	Channel  string  // the uploading channel (YouTube Music's "X - Topic" included)
	Duration float64 // seconds
	Views    int64
	Tags     []string
}

// Songs searches YouTube Music's songs tab for query, flat, and returns up
// to n hits. A song search returns real songs rather than the mixes and full
// albums a plain YouTube search is full of.
func (r Runner) Songs(ctx context.Context, query string, n int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" || n < 1 {
		return nil, nil
	}
	return r.flat(ctx, "https://music.youtube.com/search?q="+url.QueryEscape(query)+"#songs", n)
}

// Mix returns up to n entries of the YouTube Mix (the "RD" radio playlist)
// built around videoID: related songs, with the video itself usually first.
func (r Runner) Mix(ctx context.Context, videoID string, n int) ([]Hit, error) {
	if videoID == "" || n < 1 {
		return nil, nil
	}
	id := url.QueryEscape(videoID)
	return r.flat(ctx, "https://www.youtube.com/watch?v="+id+"&list=RD"+id, n)
}

func (r Runner) flat(ctx context.Context, page string, n int) ([]Hit, error) {
	out, err := r.run(ctx, "--flat-playlist", "-J", "--playlist-items", "1:"+strconv.Itoa(n), "--", page)
	if err != nil {
		return nil, err
	}
	return parseFlat(out)
}

// parseFlat reads the -J listing. yt-dlp can leave null entries or entries
// without an id in a playlist; those aren't songs and are dropped.
func parseFlat(out string) ([]Hit, error) {
	var doc struct {
		Entries []*struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, fmt.Errorf("yt-dlp: unreadable listing: %w", err)
	}
	hits := make([]Hit, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		if e != nil && e.ID != "" {
			hits = append(hits, Hit{ID: e.ID, Title: e.Title})
		}
	}
	return hits, nil
}

// tagPrefix marks the --print lines; whatever else yt-dlp prints that parses
// as an http(s) URL is the stream, so the parse doesn't depend on the order
// --get-url and --print come out in.
const tagPrefix = "screech-"

// Resolve turns a page link (a YouTube video, a SoundCloud track) into the
// stream behind it plus whatever metadata yt-dlp has.
func (r Runner) Resolve(ctx context.Context, page string) (Media, error) {
	out, err := r.run(ctx,
		// bestaudio first; YouTube live streams only offer muxed HLS, and
		// mpv plays those audio-only under --no-video.
		"-f", "bestaudio/best",
		"--no-playlist", "--playlist-items", "1",
		"--get-url",
		"--print", tagPrefix+"artist:%(artist|)s",
		"--print", tagPrefix+"track:%(track|)s",
		"--print", tagPrefix+"title:%(title|)s",
		"--print", tagPrefix+"channel:%(channel,uploader|)s",
		"--print", tagPrefix+"dur:%(duration|)s",
		"--print", tagPrefix+"views:%(view_count|)s",
		// A JSON list, so a tag with a comma in it survives.
		"--print", tagPrefix+"tags:%(tags|[])j",
		"--", page)
	if err != nil {
		return Media{}, err
	}
	return parseMedia(out)
}

// parseMedia reads Resolve's output. Metadata is best effort: a field that
// is missing or garbled costs that field, never the stream.
func parseMedia(out string) (Media, error) {
	var m Media
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		kv, tagged := strings.CutPrefix(line, tagPrefix)
		if !tagged {
			if m.URL == "" && validStreamURL(line) {
				m.URL = line
			}
			continue
		}
		key, val, ok := strings.Cut(kv, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "artist":
			m.Artist = val
		case "track":
			m.Track = val
		case "title":
			m.Title = val
		case "channel":
			m.Channel = val
		case "dur":
			m.Duration, _ = strconv.ParseFloat(val, 64)
		case "views":
			m.Views, _ = strconv.ParseInt(val, 10, 64)
		case "tags":
			var tags []string
			if json.Unmarshal([]byte(val), &tags) == nil && len(tags) > 0 {
				m.Tags = tags
			}
		}
	}
	if m.URL == "" {
		return Media{}, errors.New("yt-dlp printed no stream URL")
	}
	return m, nil
}

// Version asks the yt-dlp at path for its version, for doctor. It takes its
// turn like any other run, so it never overlaps a lookup.
func Version(ctx context.Context, path string) (string, error) {
	out, err := Runner{Path: path}.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	v, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if v = strings.TrimSpace(v); v == "" {
		return "", errors.New("yt-dlp printed no version")
	}
	return v, nil
}

// run is the one place a yt-dlp process starts. It waits for the app-wide
// turn, runs with the Runner's folder and JS runtime, and returns stdout.
func (r Runner) run(ctx context.Context, args ...string) (string, error) {
	argv := []string{"--no-warnings"}
	if r.JSRuntime != "" {
		argv = append(argv, "--js-runtimes", r.JSRuntime)
	}
	argv = append(argv, args...)

	if err := lock.acquire(ctx, isForeground(ctx)); err != nil {
		return "", fmt.Errorf("yt-dlp gave up waiting its turn: %w", err)
	}
	defer lock.release()
	// The grant and the context's end can land together, and acquire then
	// returns with the turn and a dead context. A process started for a
	// caller that has already given up is pure waste, and it would hold up
	// the lookups queued behind it.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("yt-dlp gave up waiting its turn: %w", err)
	}

	cmd := exec.CommandContext(ctx, r.Path, argv...)
	cmd.Dir = r.Dir
	// yt-dlp's standalone build is a launcher with a child holding the
	// pipes; once the context kills the launcher, don't wait on the child.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	noWindow(cmd)
	if r.Protect != nil {
		r.Protect(cmd)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting yt-dlp (%s): %w", r.Path, err)
	}
	var once sync.Once
	closeGroup := func() {}
	if r.Adopt != nil {
		if release := r.Adopt(cmd); release != nil {
			closeGroup = func() { once.Do(release) }
		}
	}
	// Close the group the moment the context ends, not after Wait: with the
	// launcher dead and its child alive, Wait sits out the pipe delay, and
	// the turn must not pass to the next lookup while the child still runs.
	// The close after Wait covers a normal exit and is a no-op if this one
	// already ran; the turn is released after both, by the defer above.
	stop := context.AfterFunc(ctx, closeGroup)
	err := cmd.Wait()
	stop()
	closeGroup()
	if err != nil && ctx.Err() != nil {
		return "", fmt.Errorf("yt-dlp gave up: %w", ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("yt-dlp: %s", lastLine(stderr.String(), err.Error()))
	}
	return stdout.String(), nil
}

// lastLine is yt-dlp's last word on stderr, which is the reason it failed;
// the lines before it are warnings and progress.
func lastLine(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// validStreamURL is the rule for what mpv may be handed: http(s) with a host.
func validStreamURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
