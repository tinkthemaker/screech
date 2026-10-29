package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/tinkthemaker/screech/internal/core"
)

// BCItem is one playable track out of Bandcamp's discover feed. Its StreamURL
// is a signed MP3 that mpv plays as it stands, with no yt-dlp in between.
type BCItem struct {
	TrackID       int64
	Artist, Title string
	StreamURL     string
	Duration      float64
}

// Bandcamp is the queue's view of Bandcamp's genre-tag discovery. slice is
// "new" (just released), "rand" (a shuffle) or "top" (best sellers).
type Bandcamp interface {
	Tag(ctx context.Context, tag, slice string) ([]BCItem, error)
}

// defaultEndpoint is Bandcamp's own discover page's JSON feed. It is
// undocumented and can change without notice; the queue treats a failure of
// it as one source missing, never as the station failing.
const defaultEndpoint = "https://bandcamp.com/api/discover/1/discover_web"

// BandcampClient asks the discover feed. The zero value works; set UserAgent
// to say who is asking.
type BandcampClient struct {
	HTTP      *http.Client // nil: a client with a 10 s timeout
	UserAgent string
	Endpoint  string // "" = defaultEndpoint; tests point it at httptest
}

// StatusError is Bandcamp's feed answering with something other than 200:
// 429 when it is throttling, 5xx when it is down, 4xx when the undocumented
// request has stopped being understood. The caller can tell them apart with
// errors.As.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("bandcamp discover: HTTP %d %s", e.Code, http.StatusText(e.Code))
}

var fallbackHTTP = &http.Client{Timeout: 10 * time.Second}

type bcRequest struct {
	CategoryID         int      `json:"category_id"`
	TagNormNames       []string `json:"tag_norm_names"`
	GeonameID          int      `json:"geoname_id"`
	Slice              string   `json:"slice"`
	Cursor             string   `json:"cursor"`
	Size               int      `json:"size"`
	IncludeResultTypes []string `json:"include_result_types"`
}

type bcResponse struct {
	Results []struct {
		BandName      string `json:"band_name"`
		FeaturedTrack *struct {
			ID        int64   `json:"id"`
			Title     string  `json:"title"`
			BandName  string  `json:"band_name"`
			StreamURL string  `json:"stream_url"`
			Duration  float64 `json:"duration"`
		} `json:"featured_track"`
	} `json:"results"`
}

// maxBandcampBody bounds what one answer may cost. Sixty albums come to about
// a hundred kilobytes.
const maxBandcampBody = 4 << 20

// Tag returns up to sixty albums' featured tracks for one genre tag.
//
// The feed also returns merch, and albums with no featured track to stream;
// both are dropped, as is anything without a stream URL. Where a label
// released the album, the track's own band_name is the artist, not the
// result's album_artist, which names the label.
func (c BandcampClient) Tag(ctx context.Context, tag, slice string) ([]BCItem, error) {
	if slice == "" {
		slice = "new"
	}
	body, err := json.Marshal(bcRequest{
		TagNormNames:       []string{tag},
		Slice:              slice,
		Cursor:             "*",
		Size:               60,
		IncludeResultTypes: []string{"a"},
	})
	if err != nil {
		return nil, err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = fallbackHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Code: resp.StatusCode}
	}
	var out bcResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBandcampBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("bandcamp discover: unreadable answer: %w", err)
	}

	items := make([]BCItem, 0, len(out.Results))
	for _, r := range out.Results {
		ft := r.FeaturedTrack
		if ft == nil || ft.StreamURL == "" || ft.ID == 0 || ft.Title == "" {
			continue
		}
		artist := ft.BandName
		if artist == "" {
			artist = r.BandName
		}
		items = append(items, BCItem{
			TrackID:   ft.ID,
			Artist:    core.CleanText(artist),
			Title:     core.CleanText(ft.Title),
			StreamURL: ft.StreamURL,
			Duration:  ft.Duration,
		})
	}
	return items, nil
}

// BandcampTags turns a phrase into the tags to try on Bandcamp, longest
// first: the whole phrase hyphenated, then with leading words dropped
// ("Swedish Death Metal" gives swedish-death-metal, death-metal, metal). The
// head of a genre name is the part that varies and the tail the part that
// generalises, so the shorter tags are the fallbacks.
func BandcampTags(phrase string) []string {
	var words []string
	for _, f := range strings.Fields(strings.ToLower(phrase)) {
		w := strings.Trim(strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
				return r
			}
			return -1
		}, f), "-")
		if w != "" {
			words = append(words, w)
		}
	}
	var tags []string
	for i := range words {
		tags = append(tags, strings.Join(words[i:], "-"))
	}
	return tags
}

const (
	// A (tag, slice) answer is reused for half an hour: the feed changes
	// slowly, and a station makes a handful of requests an hour.
	bcCacheTTL = 30 * time.Minute

	// Bandcamp is asked at most once per this long per station. Asking for
	// something already cached costs nothing; it is the second, different
	// question that waits.
	bcGap = 10 * time.Second

	// The tags tried before a station gives up on Bandcamp. A phrase that is
	// really a song title would otherwise walk all the way down to its last
	// word.
	bcProbeCap = 4
)

type bcEntry struct {
	items []BCItem
	at    time.Time
}

// bcProbe is a tag to try, and the phrase it came from. A typed phrase has
// none: the typed words already are the station's name for itself.
type bcProbe struct{ tag, phrase string }

// bcFetch is Tag behind the cache and the gap. free skips the gap, which a
// probe uses to walk down its fallback tags after an empty answer: the
// station has asked once and heard "nothing here", so the next guess is the
// same question, not a new one.
//
// A request held back by the gap answers with the last failure if there was
// one, so a caller still sees a dead network as a dead network instead of as
// a quiet source.
func (q *Queue) bcFetch(ctx context.Context, tag, slice string, free bool) ([]BCItem, time.Time, error) {
	key := tag + "\x00" + slice
	now := q.now()
	if e, ok := q.bcCache[key]; ok && now.Sub(e.at) < bcCacheTTL {
		return e.items, e.at, nil
	}
	if !free && !q.bcLast.IsZero() && now.Sub(q.bcLast) < bcGap {
		if q.bcErr != nil {
			return nil, time.Time{}, q.bcErr
		}
		return nil, time.Time{}, ErrPaced
	}
	prev := q.bcLast
	q.bcLast = now
	items, err := q.bc.Tag(ctx, tag, slice)
	if err != nil {
		if ctx.Err() != nil {
			// The caller gave up, which says nothing about Bandcamp.
			q.bcLast = prev
			return nil, time.Time{}, err
		}
		q.bcErr = err
		return nil, time.Time{}, err
	}
	q.bcErr = nil
	q.bcCache[key] = bcEntry{items: items, at: now}
	return items, now, nil
}

// bcTagFor settles which tag this station uses on Bandcamp: the first of its
// probes that has anything under "new". If none does, Bandcamp sits out for
// the life of the station. A failed request settles nothing and is retried
// on a later call.
func (q *Queue) bcTagFor(ctx context.Context) (string, error) {
	free := false
	for q.bcTag == "" && !q.bcOut {
		if q.bcNext >= len(q.bcProbes) || q.bcNext >= bcProbeCap {
			q.bcOut = true
			break
		}
		p := q.bcProbes[q.bcNext]
		items, _, err := q.bcFetch(ctx, p.tag, "new", free)
		if err != nil {
			return "", err
		}
		if len(items) > 0 {
			q.bcTag = p.tag
			if q.linkID != "" && p.phrase != "" {
				// The tag that matched is the best word for this sound that
				// the link gave, so YouTube's anchors use it too.
				q.phrase = core.CleanText(p.phrase)
			}
			break
		}
		q.bcNext++
		free = true
	}
	return q.bcTag, nil
}

// pickBC chooses a Bandcamp track. Anchors come from the tag's top sellers;
// discoveries from its newest releases and a shuffle. Its items are already
// playable, so nothing is resolved.
func (q *Queue) pickBC(ctx context.Context, anchor bool) (Song, bool, error) {
	if q.bc == nil {
		return Song{}, false, nil
	}
	tag, err := q.bcTagFor(ctx)
	if err != nil {
		return Song{}, false, err
	}
	if tag == "" {
		return Song{}, false, nil
	}
	slices := []string{"new", "rand"}
	if anchor {
		slices = []string{"top"}
	}

	var cands []cand
	have := map[string]bool{}
	var firstErr error
	for _, slice := range slices {
		items, at, err := q.bcFetch(ctx, tag, slice, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, it := range items {
			key := fmt.Sprintf("bc:%d", it.TrackID)
			if have[key] || q.ruledOut(key) {
				continue
			}
			have[key] = true
			artist, title := CleanTitle(it.Artist, it.Title)
			ak := core.NormalizeArtist(artist)
			if title == "" || (ak != "" && ak == q.lastArtist) {
				continue
			}
			cands = append(cands, cand{key: key, artist: ak, song: Song{
				Key: key, Artist: artist, Title: title, URL: it.StreamURL, Resolved: at,
			}})
		}
	}
	if len(cands) == 0 {
		// One slice answering with nothing is not a failure; every slice
		// failing is.
		return Song{}, false, firstErr
	}
	s := cands[q.choose(cands, !anchor)].song
	s.Anchor = anchor
	s.NewToYou = !anchor && q.isNew(core.NormalizeArtist(s.Artist))
	return s, true, nil
}
