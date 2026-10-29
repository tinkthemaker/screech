package discover

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/streamurl"
)

// YTHit is one row of a flat YouTube search or Mix: an id and a title, and
// nothing else. A flat listing carries no duration, views or artist; those
// cost a yt-dlp lookup of their own, which is what Resolve is.
type YTHit struct{ ID, Title string }

// YTMedia is what resolving one video gives: a direct media URL and the
// facts the queue judges it by.
type YTMedia struct {
	URL, Artist, Track, Title, Channel string
	Duration                           float64 // seconds
	Views                              int64
	Tags                               []string
}

// YouTube is the queue's view of yt-dlp. It is defined here, in the consumer,
// so the queue never imports the package that runs the process; a small
// adapter joins the two. A nil YouTube means "no yt-dlp on this machine" and
// the station carries on from Bandcamp.
type YouTube interface {
	// Songs searches YouTube Music for songs, best match first.
	Songs(ctx context.Context, query string, n int) ([]YTHit, error)
	// Mix lists the videos YouTube relates to one video.
	Mix(ctx context.Context, videoID string, n int) ([]YTHit, error)
	// Resolve turns a watch page into a playable media URL and its facts.
	Resolve(ctx context.Context, page string) (YTMedia, error)
}

const (
	// A song is 90 s to 12 min. Shorter is an intro or a clip; longer is a
	// mix, a full album or a live set, none of which a station should serve
	// as one "song".
	minSeconds = 90
	maxSeconds = 720

	// A discovery that resolves with more views than this is a hit, not a
	// find, and is labelled an anchor.
	popularViews = 1_000_000

	// The first anchorHits results of a search are the genre's best known;
	// discoveries start at discoveryFrom, past a gap of middling results.
	anchorHits    = 5
	discoveryFrom = 10
	searchSize    = 50

	// A station tries at most this many resolves per song asked for, and
	// at most newTries of them hunting for a band the listener hasn't heard.
	// Every resolve is a yt-dlp process, and those come one at a time.
	maxResolves = 6
	newTries    = 3
)

// ytVariants widen a search once its plain results are used up: the same
// phrase, leaning toward the small and the recent.
var ytVariants = []string{"underground", "demo", "new"}

// watchURL is the page Resolve is given. It is the canonical form, so the
// adapter never has to decide which of youtube.com's spellings it was handed.
func watchURL(id string) string { return "https://www.youtube.com/watch?v=" + id }

// VideoID returns the video id inside a YouTube link, whether it is a watch
// page, a youtu.be short link, a music.youtube.com page or a Short, and
// ignoring everything else in the link (&list=…, ?t=…). Anything that is not
// such a link, including a bare phrase, reports false.
//
// It is streamurl.YouTubeID and nothing else: config, `screech add` and the
// station's identity (core.DiscoveryKey) all decide "is this a video link"
// by that one parser, and a second reading here would let a link make a
// station the rest of Screech took for a phrase.
func VideoID(link string) (string, bool) { return streamurl.YouTubeID(link) }

type ytEntry struct {
	hits []YTHit
	at   time.Time
}

// search is Songs behind the 6-hour cache. An empty answer is not kept: it is
// more likely yt-dlp having a bad moment than the genre having no songs.
func (q *Queue) search(ctx context.Context, query string, n int) ([]YTHit, error) {
	return q.cachedHits(strconv.Itoa(n)+"\x00"+query, func() ([]YTHit, error) {
		return q.yt.Songs(ctx, query, n)
	})
}

// mix is Mix behind the same cache.
func (q *Queue) mix(ctx context.Context, id string) ([]YTHit, error) {
	return q.cachedHits("mix\x00"+id, func() ([]YTHit, error) {
		return q.yt.Mix(ctx, id, searchSize)
	})
}

func (q *Queue) cachedHits(key string, fetch func() ([]YTHit, error)) ([]YTHit, error) {
	now := q.now()
	if e, ok := q.ytCache[key]; ok && now.Sub(e.at) < searchTTL {
		return e.hits, nil
	}
	hits, err := fetch()
	if err != nil {
		return nil, err
	}
	if len(hits) > 0 {
		q.ytCache[key] = ytEntry{hits: hits, at: now}
	}
	return hits, nil
}

// ytCandidates lists the videos the next YouTube song may come from, minus
// everything already played or ruled out.
//
// Anchors are the first few results for the phrase. Discoveries are the
// results from the tenth on; when those are all played, the phrase is
// searched again with a word that leans small (underground, demo, new). A
// link seed's discoveries are its Mix instead: songs YouTube itself relates
// to the one pasted.
func (q *Queue) ytCandidates(ctx context.Context, anchor bool) ([]cand, error) {
	if anchor {
		if q.phrase == "" {
			return nil, nil
		}
		hits, err := q.search(ctx, q.phrase, anchorHits)
		if err != nil {
			return nil, err
		}
		return q.ytFilter(hits), nil
	}
	if q.linkID != "" {
		hits, err := q.mix(ctx, q.linkID)
		if err != nil {
			return nil, err
		}
		return q.ytFilter(hits), nil
	}
	if q.phrase == "" {
		return nil, nil
	}
	for stage := 0; stage <= len(ytVariants); stage++ {
		query := q.phrase
		if stage > 0 {
			query += " " + ytVariants[stage-1]
		}
		hits, err := q.search(ctx, query, searchSize)
		if err != nil {
			return nil, err
		}
		if stage == 0 {
			if len(hits) > discoveryFrom {
				hits = hits[discoveryFrom:]
			} else {
				hits = nil
			}
		}
		if c := q.ytFilter(hits); len(c) > 0 {
			return c, nil
		}
	}
	return nil, nil
}

// ytFilter drops what has played, what failed, and what the title already
// says is by the band that just played.
func (q *Queue) ytFilter(hits []YTHit) []cand {
	var out []cand
	for _, h := range hits {
		if h.ID == "" {
			continue
		}
		key := "yt:" + h.ID
		if q.ruledOut(key) {
			continue
		}
		c := cand{key: key, id: h.ID, artist: guessArtist(h.Title)}
		if c.artist != "" && c.artist == q.lastArtist {
			continue
		}
		out = append(out, c)
	}
	return out
}

// guessArtist reads an artist key off a search result's title, if the title
// is an "Artist - Title". YouTube Music rows are usually just the song's
// name, so most guesses are empty; the answer that counts comes from Resolve.
func guessArtist(title string) string {
	tr, ok := core.ParseTitle(title, "")
	if !ok || tr.Artist == "" {
		return ""
	}
	a, _ := CleanTitle(tr.Artist, "")
	return core.NormalizeArtist(a)
}

// pickYT resolves candidates until one is a song worth playing.
//
// A discovery whose band the listener already knows is held back while there
// are tries left to find a new one, because a search result can't say who it
// is by until it has been resolved. If none turns up, the held song plays:
// a known band beats a silent station.
func (q *Queue) pickYT(ctx context.Context, anchor bool, r *run) (Song, bool, error) {
	if q.yt == nil {
		return Song{}, false, nil
	}
	cands, err := q.ytCandidates(ctx, anchor)
	if err != nil {
		return Song{}, false, err
	}
	var held *Song
	tries := 0
	for len(cands) > 0 && r.left > 0 {
		i := q.choose(cands, !anchor)
		c := cands[i]
		cands = append(cands[:i], cands[i+1:]...)
		r.left--
		tries++

		m, err := q.yt.Resolve(ctx, watchURL(c.id))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			q.failed(c.key)
			r.errs = append(r.errs, err)
			continue
		}
		s, ok := q.ytSong(c.id, m, anchor)
		if !ok {
			q.rule(c.key)
			continue
		}
		if a := core.NormalizeArtist(s.Artist); a != "" && a == q.lastArtist {
			// Same band as the song just played. It stays a candidate for later.
			continue
		}
		if s.NewToYou || s.Anchor {
			return s, true, nil
		}
		if held == nil {
			held = &s
		}
		if tries >= newTries {
			break
		}
	}
	if held != nil {
		return *held, true, nil
	}
	return Song{}, false, nil
}

// ytSong builds the Song for a resolved video, or reports false if it is not
// one to play: no media URL, or not 90 s to 12 min long.
func (q *Queue) ytSong(id string, m YTMedia, anchor bool) (Song, bool) {
	if m.URL == "" || m.Duration < minSeconds || m.Duration > maxSeconds {
		return Song{}, false
	}
	artist, title := mediaNames(m)
	artist, title = CleanTitle(artist, title)
	if title == "" {
		return Song{}, false
	}
	anchor = anchor || m.Views > popularViews
	return Song{
		Key:      "yt:" + id,
		Artist:   artist,
		Title:    title,
		URL:      m.URL,
		Anchor:   anchor,
		NewToYou: !anchor && q.isNew(core.NormalizeArtist(artist)),
		Resolved: q.now(),
	}, true
}

// mediaNames picks the artist and title to show. yt-dlp's own artist and
// track fields win when YouTube Music supplied them; otherwise the video's
// title goes through the same "Artist - Title" split radio titles do, and
// the channel stands in for an artist the title doesn't name.
func mediaNames(m YTMedia) (artist, title string) {
	artist, title = strings.TrimSpace(m.Artist), strings.TrimSpace(m.Track)
	if artist != "" && title != "" {
		return artist, title
	}
	if tr, ok := core.ParseTitle(m.Title, ""); ok {
		if artist == "" {
			artist = tr.Artist
		}
		if title == "" {
			title = tr.Title
		}
	}
	if artist == "" {
		artist = m.Channel
	}
	if title == "" {
		title = m.Title
	}
	return artist, title
}
