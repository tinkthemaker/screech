package discover

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

// --- fakes for song radio ----------------------------------------------------------

// fakeSearcher is Bandcamp's search and its tag feed in one: what a
// BandcampClient is. Its answers are keyed by matchKey, so "Chelsea Wolfe" and
// "chelsea wolfe" are one entry, as they are to Bandcamp.
type fakeSearcher struct {
	mu      sync.Mutex
	answers map[string]SearchResult
	err     error // every Search fails with it
	hang    bool  // every Search waits for its context to end
	feed    func(tag, slice string) []BCItem
	tagErr  error

	asked []string // Search texts, in order
	tags  []string // feed requests, "tag|slice"
}

func (f *fakeSearcher) Search(ctx context.Context, text string) (SearchResult, error) {
	f.mu.Lock()
	f.asked = append(f.asked, text)
	res := f.answers[matchKey(text)]
	err, hang := f.err, f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return SearchResult{}, context.Cause(ctx)
	}
	return res, err
}

func (f *fakeSearcher) Tag(ctx context.Context, tag, slice string) ([]BCItem, error) {
	f.mu.Lock()
	f.tags = append(f.tags, tag+"|"+slice)
	fn, err := f.feed, f.tagErr
	f.mu.Unlock()
	if err != nil || fn == nil {
		return nil, err
	}
	return fn(tag, slice), nil
}

func (f *fakeSearcher) searches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func (f *fakeSearcher) feedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tags...)
}

// feedTags is the set of tags the feed was asked about.
func (f *fakeSearcher) feedTags() map[string]bool {
	out := map[string]bool{}
	for _, c := range f.feedCalls() {
		tag, _, _ := strings.Cut(c, "|")
		out[tag] = true
	}
	return out
}

// radioNames says who a fixture video's slug is by.
var radioNames = map[string]string{
	"decarnate": "Decarnate", "organectomy": "Organectomy", "analepsy": "Analepsy",
	"maximize": "Maximize Bestiality", "release": "Release", "devourment": "Devourment",
	"cenotaph": "Cenotaph", "gorgasm": "Gorgasm", "gutrot": "Gutrot", "pyrexia": "Pyrexia",
	"chelsea": "Chelsea Wolfe", "wolfeparade": "Wolfe Parade", "obscure": "Obscure Band",
	"ingurgitating": "Ingurgitating Putrescence",
}

// radioMedia resolves a video named "<slug>.<nn>" to a song by that slug's
// artist, and anything else to defaultMedia's twelve bands.
func radioMedia(page string) YTMedia {
	id := idOf(page)
	slug, _, _ := strings.Cut(id, ".")
	name, ok := radioNames[slug]
	if !ok {
		return defaultMedia(page)
	}
	return YTMedia{
		URL: "https://media.example/" + id, Artist: name, Track: "Track " + id, Title: "Track " + id,
		Duration: 200, Views: 5_000,
	}
}

// songsBy lists n videos by one slug's artist, the way a search for them would.
func songsBy(slug string, n int) []YTHit {
	out := make([]YTHit, n)
	for i := range out {
		out[i] = YTHit{ID: fmt.Sprintf("%s.%02d", slug, i), Title: fmt.Sprintf("%s song %02d", slug, i)}
	}
	return out
}

// styleSongs answers a search of a style with sixty songs by the twelve default
// bands, which is what a tag search looks like next to a Mix.
func styleSongs(query string, n int) []YTHit {
	hits := hitsFor(strings.ReplaceAll(query, " ", "_")+"-", 60)
	return hits[:min(n, len(hits))]
}

// radioWorld builds Mixes the way the probe found them: mostly the artist of
// the video, with a few others.
type radioWorld struct {
	own    map[string]int      // songs of its own in an artist's Mix; 45 when unlisted
	others map[string][]string // slugs of the other artists in an artist's Mix
}

func (w radioWorld) mix(id string, n int) []YTHit {
	slug, _, _ := strings.Cut(id, ".")
	own, ok := w.own[slug]
	if !ok {
		own = 45
	}
	out := songsBy(slug, own)
	for _, o := range w.others[slug] {
		out = append(out, songsBy(o, 1)...)
	}
	return out
}

// probeWorld is the live probe's Mix: 45 of 50 the seed's own, and four others.
var probeWorld = radioWorld{
	others: map[string][]string{"decarnate": {"organectomy", "analepsy", "maximize", "release"}},
}

func radioYT(w radioWorld, songs func(query string, n int) []YTHit) *fakeYT {
	return &fakeYT{
		songs: songs,
		mix:   w.mix,
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			return radioMedia(page), nil
		},
	}
}

// newRadioQueue is newTestQueue with the seed 7 every test shares.
func newRadioQueue(seed string, yt YouTube, bc Bandcamp) (*Queue, *clock) {
	return newTestQueue(seed, yt, bc, nil)
}

func artistKeys(songs []Song) []string {
	out := make([]string, len(songs))
	for i, s := range songs {
		out[i] = core.NormalizeArtist(s.Artist)
	}
	return out
}

// checkSpaced holds songs to "an artist plays at most once in any span songs".
func checkSpaced(t *testing.T, songs []Song, span int) {
	t.Helper()
	keys := artistKeys(songs)
	for i := range keys {
		for j := i + 1; j < len(keys) && j < i+span; j++ {
			if keys[i] != "" && keys[i] == keys[j] {
				t.Errorf("%q plays at #%d and again at #%d, inside %d songs", keys[i], i+1, j+1, span)
			}
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// --- classifying -------------------------------------------------------------------

// "chelsea wolfe" is a band and a tag at once; it is the band that gets the
// station. The first song is the band's, passing over another band's song the
// search ranked above it, and from then on nothing is searched for by the
// phrase: the band's own tags carry the station.
func TestClassifyArtist(t *testing.T) {
	bc := &fakeSearcher{
		answers: map[string]SearchResult{matchKey("chelsea wolfe"): {
			Tags: []string{"chelsea-wolfe"},
			Bands: []SearchBand{{
				Name: "CHELSEA WOLFE", Genre: "Alternative", URL: "https://chelseawolfe.bandcamp.com",
				Tags: []string{"Alternative", "Folk", "rock-acoustic-alternative-rock"},
			}},
		}},
		feed: func(tag, slice string) []BCItem { return bcItems("Folk "+tag+slice, 5000, 40) },
	}
	yt := radioYT(radioWorld{}, func(query string, n int) []YTHit {
		if matchKey(query) == matchKey("chelsea wolfe") {
			return append([]YTHit{{ID: "wolfeparade.00", Title: "Wolfe Parade song"}}, songsBy("chelsea", 8)...)
		}
		return styleSongs(query, n)
	})
	q, clk := newRadioQueue("chelsea wolfe", yt, bc)

	songs := drain(t, q, clk, 16)
	if first := songs[0]; first.Key != "yt:chelsea.00" || first.Artist != "Chelsea Wolfe" {
		t.Errorf("first song = %+v, want Chelsea Wolfe's top song, not the other band's", first)
	}
	checkNoRepeats(t, songs)

	if got := bc.searches(); len(got) != 1 {
		t.Errorf("Bandcamp was searched %v; the band came with the first answer", got)
	}
	tags := bc.feedTags()
	for tag := range tags {
		if tag != "folk" && tag != "rock-acoustic-alternative-rock" {
			t.Errorf("the feed was asked about %q; the style is the band's tags", tag)
		}
	}
	if len(tags) == 0 {
		t.Error("the band's tags never reached the feed")
	}
	songsLog, _, _ := yt.logs()
	for _, c := range songsLog[1:] {
		if strings.Contains(c, "chelsea") || strings.Contains(c, "wolfe") {
			t.Errorf("YouTube Music was asked for %q after the first song", c)
		}
	}
}

// "engorged in putrescence" is a song. YouTube Music does not have it (it offers
// "Ingurgitating Putrescence"), so it is the band's name that finds it.
func TestClassifySong(t *testing.T) {
	bc := &fakeSearcher{
		answers: map[string]SearchResult{
			matchKey("engorged in putrescence"): {Tracks: []SearchTrack{
				{Name: "Engorged in Putrescence", Band: "Decarnate", Album: true,
					URL: "https://decarnate.bandcamp.com/album/engorged-in-putrescence"},
				{Name: "Engorged in Putrescence", Band: "Decarnate", ID: 2222,
					URL: "https://decarnate.bandcamp.com/track/engorged-in-putrescence"},
			}},
			matchKey("Decarnate"): {Bands: []SearchBand{{
				Name: "Decarnate", Genre: "Metal", Tags: []string{"Metal", "Death Metal", "Brutal Death Metal"},
			}}},
		},
		feed: func(tag, slice string) []BCItem { return bcItems("DM "+tag+slice, 7000, 40) },
	}
	yt := radioYT(probeWorld, func(query string, n int) []YTHit {
		switch matchKey(query) {
		case matchKey("engorged in putrescence"):
			return songsBy("ingurgitating", 5)
		case matchKey("Decarnate Engorged in Putrescence"):
			return []YTHit{
				{ID: "ingurgitating.00", Title: "Ingurgitating Putrescence"},
				{ID: "decarnate.07", Title: "Some Other Decarnate Song"},
				{ID: "decarnate.03", Title: "Engorged in Putrescence (Official Audio)"},
			}
		}
		return styleSongs(query, n)
	})
	q, clk := newRadioQueue("engorged in putrescence", yt, bc)

	songs := drain(t, q, clk, 14)
	if first := songs[0]; first.Key != "yt:decarnate.03" || first.Artist != "Decarnate" {
		t.Errorf("first song = %+v, want the song that was typed", first)
	}
	checkNoRepeats(t, songs)

	if got := bc.searches(); len(got) != 2 || got[1] != "Decarnate" {
		t.Errorf("Bandcamp searches = %v, want the phrase and then the band", got)
	}
	tags := bc.feedTags()
	if len(tags) == 0 {
		t.Error("the band's tags never reached the feed")
	}
	for tag := range tags {
		if tag != "death-metal" && tag != "brutal-death-metal" {
			t.Errorf("the feed was asked about %q; the style is the band's, without its genre", tag)
		}
	}
}

// blockingYT is a YouTube whose search for one phrase waits to be told to stop,
// and says when it started and when it was stopped.
type blockingYT struct {
	*fakeYT
	phrase  string
	started chan struct{}
	stopped chan struct{}
}

func (b *blockingYT) Songs(ctx context.Context, query string, n int) ([]YTHit, error) {
	if query != b.phrase {
		return b.fakeYT.Songs(ctx, query, n)
	}
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return nil, ctx.Err()
}

// The search of YouTube Music for the phrase starts beside Bandcamp's, not
// after it, and when the phrase turns out to be a song, which YouTube Music is
// not asked for by its own name, it is stopped: it would otherwise hold the one
// yt-dlp turn the band's search needs.
func TestClassifyRunsBesideTheFirstYouTubeSearchAndStopsItForASong(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	bc := &beside{
		fakeSearcher: fakeSearcher{
			answers: map[string]SearchResult{matchKey("engorged in putrescence"): {Tracks: []SearchTrack{
				{Name: "Engorged in Putrescence", Band: "Decarnate", URL: "https://decarnate.bandcamp.com/track/e"}}}},
		},
		started: started,
	}
	yt := &blockingYT{
		fakeYT: radioYT(probeWorld, func(query string, n int) []YTHit {
			return []YTHit{{ID: "decarnate.03", Title: "Engorged in Putrescence"}}
		}),
		phrase: "engorged in putrescence", started: started, stopped: stopped,
	}
	q, _ := newRadioQueue("engorged in putrescence", yt, bc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := q.Next(ctx)
	if err != nil || s.Key != "yt:decarnate.03" {
		t.Fatalf("Next = %+v, %v; want the typed song", s, err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("the search for the phrase was left running")
	}
}

// beside is a Searcher that will not answer until the YouTube search has begun.
type beside struct {
	fakeSearcher
	started chan struct{}
}

func (b *beside) Search(ctx context.Context, text string) (SearchResult, error) {
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		return SearchResult{}, errors.New("the YouTube search never started: the lookups run one after the other")
	}
	return b.fakeSearcher.Search(ctx, text)
}

// When YouTube Music has no song by the band that carries the name, the
// Bandcamp page itself is played, through yt-dlp.
func TestSongFallsBackToTheBandcampPage(t *testing.T) {
	const page = "https://decarnate.bandcamp.com/track/engorged-in-putrescence"
	bc := &fakeSearcher{answers: map[string]SearchResult{
		matchKey("engorged in putrescence"): {Tracks: []SearchTrack{
			{Name: "Engorged in Putrescence", Band: "Decarnate", ID: 2222, URL: page},
		}},
	}}
	yt := radioYT(probeWorld, func(query string, n int) []YTHit {
		if strings.Contains(strings.ToLower(query), "decarnate") {
			// Only other songs: one by another band, one by the band but not the song.
			return []YTHit{{ID: "ingurgitating.00", Title: "Ingurgitating Putrescence"}, {ID: "decarnate.07", Title: "Another Song"}}
		}
		return styleSongs(query, n)
	})
	yt.resolve = func(ctx context.Context, p string) (YTMedia, error) {
		if p == page {
			return YTMedia{URL: "https://bc.example/stream/2222", Artist: "Decarnate", Track: "Engorged in Putrescence", Duration: 245}, nil
		}
		return radioMedia(p), nil
	}
	q, _ := newRadioQueue("engorged in putrescence", yt, bc)

	s, err := q.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if s.Key != "bc:2222" || s.Title != "Engorged in Putrescence" || s.Artist != "Decarnate" {
		t.Errorf("first song = %+v, want the Bandcamp page's song", s)
	}
	if s.URL != "https://bc.example/stream/2222" || s.Duration != 245*time.Second {
		t.Errorf("song URL %q, duration %v: want the stream yt-dlp found, not the page", s.URL, s.Duration)
	}
	_, _, resolved := yt.logs()
	if len(resolved) == 0 || resolved[len(resolved)-1] != page {
		t.Errorf("resolves = %v, want the page asked of yt-dlp last", resolved)
	}
}

// "swedish death metal" is a genre, and a genre is the station it always was:
// the same songs, in the same order, as without the classifying.
func TestClassifyGenre(t *testing.T) {
	feed := plentifulBC().items
	bc := &fakeSearcher{
		answers: map[string]SearchResult{matchKey("swedish death metal"): {
			Tags: []string{"swedish-death-metal"},
			Tracks: []SearchTrack{
				{Name: "Swedish Death Metal Compilation", Band: "Various", Album: true},
				{Name: "Swedish Death Metal Anthem", Band: "Someone"},
			},
		}},
		feed: feed,
	}
	q, clk := newRadioQueue("swedish death metal", plentifulYT(), bc)
	got := drain(t, q, clk, 40)

	lq, lclk := newTestQueue("swedish death metal", plentifulYT(), &fakeBC{items: feed}, nil)
	want := drain(t, lq, lclk, 40)
	for i := range want {
		if got[i].Key != want[i].Key {
			t.Fatalf("song #%d is %s, was %s before classifying", i+1, got[i].Key, want[i].Key)
		}
	}
	if calls := bc.feedCalls(); len(calls) == 0 || calls[0] != "swedish-death-metal|new" {
		t.Errorf("feed calls = %v, want the genre's own tag first", calls)
	}
	if n := len(bc.searches()); n != 1 {
		t.Errorf("Bandcamp was searched %d times, want once", n)
	}
	// Spacing is the genre's: never twice running, not eight apart.
	checkNoRepeats(t, got)
}

// A phrase nothing on Bandcamp matches is song radio from YouTube Music's top
// hit, and its style is the artist's, found by a second search.
func TestClassifyFallsBackToTopHit(t *testing.T) {
	bc := &fakeSearcher{
		answers: map[string]SearchResult{
			matchKey("Obscure Band"): {Bands: []SearchBand{{
				Name: "Obscure Band", Genre: "Metal", Tags: []string{"Metal", "Slam"},
			}}},
		},
		feed: func(tag, slice string) []BCItem { return bcItems("Slam "+tag+slice, 9000, 40) },
	}
	yt := radioYT(probeWorld, func(query string, n int) []YTHit {
		if query == "some song title" {
			return songsBy("obscure", 6)
		}
		return styleSongs(query, n)
	})
	q, clk := newRadioQueue("some song title", yt, bc)

	songs := drain(t, q, clk, 12)
	if first := songs[0]; first.Key != "yt:obscure.00" {
		t.Errorf("first song = %+v, want YouTube Music's top hit for the phrase", first)
	}
	checkNoRepeats(t, songs)
	if got := bc.searches(); len(got) != 2 || got[0] != "some song title" || got[1] != "Obscure Band" {
		t.Errorf("Bandcamp searches = %v, want the phrase and then the artist", got)
	}
	for tag := range bc.feedTags() {
		if tag != "slam" {
			t.Errorf("the feed was asked about %q, want the artist's tag", tag)
		}
	}
	songsLog, _, _ := yt.logs()
	for _, c := range songsLog[1:] {
		if strings.Contains(c, "some_song_title") || strings.Contains(c, "some song title") {
			t.Errorf("the phrase was searched again: %q", c)
		}
	}
}

// Bandcamp being out of reach is a phrase with no classification, and
// that is step 4: the station still starts, on YouTube Music's top hit.
func TestClassifyBandcampDown(t *testing.T) {
	downs := map[string]*fakeSearcher{
		"an error": {err: &StatusError{Code: 503}, tagErr: &StatusError{Code: 503}},
		"a slow answer": {
			hang: true,
		},
	}
	for name, bc := range downs {
		t.Run(name, func(t *testing.T) {
			yt := radioYT(radioWorld{
				others: map[string][]string{"chelsea": {"organectomy", "analepsy"}},
				own:    map[string]int{"chelsea": 10},
			}, func(query string, n int) []YTHit {
				if query == "chelsea wolfe" {
					return songsBy("chelsea", 6)
				}
				return styleSongs(query, n)
			})
			q, clk := newRadioQueue("chelsea wolfe", yt, bc)
			if q.bcFirst != 2*time.Second {
				t.Fatalf("the search is capped at %v, want 2s", q.bcFirst)
			}
			q.bcFirst = 20 * time.Millisecond

			start := time.Now()
			songs := drain(t, q, clk, 6)
			if first := songs[0]; first.Key != "yt:chelsea.00" {
				t.Errorf("first song = %+v, want the top hit for the phrase", first)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("a down Bandcamp held the first song for %v", d)
			}
			checkNoRepeats(t, songs)
		})
	}
}

// A classification made while the network was out is not kept: the phrase is
// asked of Bandcamp again once there is a network to ask it on.
func TestClassifyIsRedoneAfterAnOutage(t *testing.T) {
	bc := &fakeSearcher{
		err: fmt.Errorf("dial: %w", ErrNetwork),
		answers: map[string]SearchResult{matchKey("chelsea wolfe"): {Bands: []SearchBand{{
			Name: "Chelsea Wolfe", Tags: []string{"Folk"},
		}}}},
	}
	yt := radioYT(radioWorld{}, func(query string, n int) []YTHit { return songsBy("chelsea", 5) })
	yt.songsErr = fmt.Errorf("getaddrinfo: %w", ErrNetwork)
	q, clk := newRadioQueue("chelsea wolfe", yt, bc)

	_, err := q.Next(context.Background())
	if !isNetworkError(err) {
		t.Fatalf("Next while offline = %v, want a network error", err)
	}
	// Back online.
	bc.mu.Lock()
	bc.err = nil
	bc.mu.Unlock()
	yt.mu.Lock()
	yt.songsErr = nil
	yt.mu.Unlock()
	clk.Advance(time.Minute)

	s, err := q.Next(context.Background())
	if err != nil {
		t.Fatalf("Next once online: %v", err)
	}
	if s.Key != "yt:chelsea.00" {
		t.Errorf("first song = %+v, want the artist's top song", s)
	}
	if got := bc.searches(); len(got) != 2 {
		t.Errorf("Bandcamp searches = %v, want the outage's failed one and a second", got)
	}
}

// The seed's lookups that did succeed are not paid for twice when the song
// itself could not be reached: a retry resolves, and does not search again.
func TestSeedRetryKeepsTheClassification(t *testing.T) {
	bc := &fakeSearcher{answers: map[string]SearchResult{matchKey("chelsea wolfe"): {Bands: []SearchBand{{
		Name: "Chelsea Wolfe", Tags: []string{"Folk"},
	}}}}}
	yt := radioYT(radioWorld{}, func(query string, n int) []YTHit { return songsBy("chelsea", 5) })
	var resolves int
	yt.resolve = func(ctx context.Context, page string) (YTMedia, error) {
		resolves++
		if resolves == 1 {
			return YTMedia{}, fmt.Errorf("urlopen error: %w", ErrNetwork)
		}
		return radioMedia(page), nil
	}
	q, _ := newRadioQueue("chelsea wolfe", yt, bc)

	if _, err := q.Next(context.Background()); !isNetworkError(err) {
		t.Fatalf("first Next = %v, want the network error", err)
	}
	s, err := q.Next(context.Background())
	if err != nil || s.Key != "yt:chelsea.00" {
		t.Fatalf("second Next = %+v, %v; want the top song", s, err)
	}
	if got := bc.searches(); len(got) != 1 {
		t.Errorf("Bandcamp searches = %v, want one: the answer is kept", got)
	}
	songsLog, _, _ := yt.logs()
	if len(songsLog) != 1 {
		t.Errorf("YouTube searches = %v, want one: the answer is cached", songsLog)
	}
}

// --- what song radio searches for ---------------------------------------------------

// The typed phrase finds the seed and nothing else. It is not searched again,
// and its words are not tried one at a time ("putrescence") as a tag.
func TestSongRadioNeverSearchesTheTypedPhraseAgain(t *testing.T) {
	cases := []struct {
		name, phrase string
		bc           func() *fakeSearcher
		songs        func(query string, n int) []YTHit
		words        []string
	}{
		{
			name: "a song", phrase: "engorged in putrescence",
			bc: func() *fakeSearcher {
				return &fakeSearcher{
					answers: map[string]SearchResult{
						matchKey("engorged in putrescence"): {Tracks: []SearchTrack{
							{Name: "Engorged in Putrescence", Band: "Decarnate", URL: "https://decarnate.bandcamp.com/track/e"}}},
						matchKey("Decarnate"): {Bands: []SearchBand{{Name: "Decarnate", Genre: "Metal", Tags: []string{"Metal", "Death Metal", "Gore"}}}},
					},
					feed: func(tag, slice string) []BCItem { return bcItems("B "+tag+slice, 100, 40) },
				}
			},
			songs: func(query string, n int) []YTHit {
				if strings.Contains(strings.ToLower(query), "decarnate") {
					return []YTHit{{ID: "decarnate.03", Title: "Engorged in Putrescence"}}
				}
				return styleSongs(query, n)
			},
			words: []string{"engorged", "putrescence"},
		},
		{
			name: "a phrase no one matched", phrase: "putrid gore anthem",
			bc: func() *fakeSearcher {
				return &fakeSearcher{
					answers: map[string]SearchResult{
						matchKey("Decarnate"): {Bands: []SearchBand{{Name: "Decarnate", Tags: []string{"Death Metal", "Gore"}}}},
					},
					feed: func(tag, slice string) []BCItem { return bcItems("B "+tag+slice, 100, 40) },
				}
			},
			songs: func(query string, n int) []YTHit {
				if strings.Contains(query, "putrid") {
					return songsBy("decarnate", 3)
				}
				return styleSongs(query, n)
			},
			words: []string{"putrid", "gore anthem", "anthem"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bc := tc.bc()
			yt := radioYT(probeWorld, tc.songs)
			q, clk := newRadioQueue(tc.phrase, yt, bc)

			first, err := q.Next(context.Background())
			if err != nil || !strings.HasPrefix(first.Key, "yt:decarnate.") {
				t.Fatalf("first song = %+v, %v; want a Decarnate song", first, err)
			}
			clk.Advance(15 * time.Second)
			ytBefore, _, _ := yt.logs()
			bcBefore := len(bc.searches())

			drain(t, q, clk, 30)

			ytAfter, _, _ := yt.logs()
			for _, query := range ytAfter[len(ytBefore):] {
				if containsAny(strings.ToLower(query), tc.words...) {
					t.Errorf("YouTube Music was searched for %q after the first song", query)
				}
			}
			for _, s := range bc.searches()[bcBefore:] {
				if containsAny(strings.ToLower(s), tc.words...) {
					t.Errorf("Bandcamp was searched for %q after the first song", s)
				}
			}
			for _, c := range bc.feedCalls() {
				if containsAny(c, "engorged", "putrescence", "putrid", "anthem") {
					t.Errorf("the feed was asked %q: a word of the phrase became a tag", c)
				}
			}
		})
	}
}

// A Mix is mostly its own artist (45 of 50 for a Topic track). Spacing is what
// stops that from being a station of one band: no artist twice in any eight.
func TestSongRadioSpacesArtists(t *testing.T) {
	bc := &fakeSearcher{answers: map[string]SearchResult{matchKey("decarnate"): {Bands: []SearchBand{{
		Name: "Decarnate", Genre: "Metal", Tags: []string{"Metal", "Death Metal", "Brutal Death Metal"},
	}}}}}
	yt := radioYT(probeWorld, func(query string, n int) []YTHit {
		if matchKey(query) == matchKey("decarnate") {
			return songsBy("decarnate", 10)
		}
		return styleSongs(query, n)
	})
	for _, seed := range []int64{7, 8, 9} {
		clk := newClock()
		q := NewQueue("decarnate", yt, bc, nil, rand.New(rand.NewSource(seed)), clk.Now)
		songs := drain(t, q, clk, 40)
		t.Logf("seed %d: %v", seed, artistKeys(songs))

		checkSpaced(t, songs, 8)
		checkNoRepeats(t, songs)
		fromMix := 0
		for _, s := range songs {
			if strings.HasPrefix(s.Key, "yt:decarnate.") || containsAny(s.Key, "organectomy", "analepsy", "maximize", "release") {
				fromMix++
			}
		}
		if fromMix < 2 {
			t.Errorf("seed %d: only %d of 40 songs came from the Mix; the test no longer tests the Mix", seed, fromMix)
		}
	}
}

// Genre stations keep their own rule: never the same artist twice in a row.
// Eight apart would starve a small tag.
func TestGenreStationKeepsItsSpacing(t *testing.T) {
	var items []BCItem
	for i := int64(0); i < 12; i++ {
		items = append(items, BCItem{TrackID: 100 + i, Artist: fmt.Sprintf("Artist %d", i%3), Title: fmt.Sprintf("Song %d", i),
			StreamURL: fmt.Sprintf("https://t4.bcbits.com/stream/%d", i), Duration: 180})
	}
	bc := &fakeSearcher{
		answers: map[string]SearchResult{matchKey("dungeon synth"): {Tags: []string{"dungeon-synth"}}},
		feed:    func(tag, slice string) []BCItem { return items },
	}
	q, clk := newRadioQueue("dungeon synth", nil, bc)
	var got []Song
	for i := 0; i < 12; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			break
		}
		got = append(got, s)
		clk.Advance(15 * time.Second)
	}
	if len(got) < 8 {
		t.Fatalf("a genre station with three artists played %d songs", len(got))
	}
	checkNoRepeats(t, got)
}

// The station leaves the seed band by crawling: the Mix of a song by another
// artist found in the seed's Mix, one hop and no more.
func TestSongRadioHopsToOtherArtists(t *testing.T) {
	world := radioWorld{
		own: map[string]int{"decarnate": 6, "organectomy": 4, "analepsy": 4, "maximize": 4, "release": 4},
		others: map[string][]string{
			"decarnate":   {"organectomy", "analepsy", "maximize", "release"},
			"organectomy": {"devourment", "cenotaph", "gorgasm", "gutrot", "pyrexia"},
			"analepsy":    {"cenotaph", "gutrot"},
			"maximize":    {"gorgasm", "pyrexia"},
			"release":     {"devourment", "pyrexia"},
		},
	}
	bc := &fakeSearcher{answers: map[string]SearchResult{matchKey("decarnate"): {Bands: []SearchBand{{Name: "Decarnate"}}}}}
	yt := radioYT(world, func(query string, n int) []YTHit {
		if matchKey(query) == matchKey("decarnate") {
			return songsBy("decarnate", 5)
		}
		return nil
	})
	q, clk := newRadioQueue("decarnate", yt, bc)

	songs := drain(t, q, clk, 24)
	checkNoRepeats(t, songs)

	_, mixes, _ := yt.logs()
	t.Logf("songs %v\nmixes %v", keysOf(songs), mixes)
	seen := map[string]bool{}
	hopped := false
	for _, m := range mixes {
		if seen[m] {
			t.Errorf("Mix %s was asked for twice; it is cached", m)
		}
		seen[m] = true
		if !strings.HasPrefix(m, "decarnate.") && containsAny(m, "organectomy.", "analepsy.", "maximize.", "release.") {
			hopped = true
		}
		if containsAny(m, "devourment.", "cenotaph.", "gorgasm.", "gutrot.", "pyrexia.") {
			t.Errorf("Mix %s was crawled: that is a second hop", m)
		}
	}
	if len(mixes) == 0 || !strings.HasPrefix(mixes[0], "decarnate.") {
		t.Errorf("Mixes asked = %v, want the seed's first", mixes)
	}
	if !hopped {
		t.Errorf("Mixes asked = %v, want the Mix of another artist's song", mixes)
	}
	far := 0
	for _, s := range songs {
		if containsAny(s.Key, "devourment.", "cenotaph.", "gorgasm.", "gutrot.", "pyrexia.") {
			far++
		}
	}
	if far == 0 {
		t.Errorf("no song came from beyond the seed's Mix; keys: %v", keysOf(songs))
	}
}

func keysOf(songs []Song) []string {
	out := make([]string, len(songs))
	for i, s := range songs {
		out[i] = s.Key
	}
	return out
}

// A station never goes quiet for want of spacing: when every song left is by
// an artist who played lately, the rule gives way to "not twice running" and
// the songs play. It ends when the songs do.
func TestSongRadioRelaxesSpacingRatherThanEnding(t *testing.T) {
	world := radioWorld{
		own:    map[string]int{"decarnate": 3, "organectomy": 2},
		others: map[string][]string{"decarnate": {"organectomy"}},
	}
	bc := &fakeSearcher{answers: map[string]SearchResult{matchKey("decarnate"): {Bands: []SearchBand{{Name: "Decarnate"}}}}}
	yt := radioYT(world, func(query string, n int) []YTHit {
		if matchKey(query) == matchKey("decarnate") {
			return songsBy("decarnate", 3)
		}
		return nil
	})
	q, clk := newRadioQueue("decarnate", yt, bc)

	// Decarnate's three songs and Organectomy's two (one is in Decarnate's
	// Mix, and its own Mix has the other) are all there is.
	var songs []Song
	for i := 0; i < 5; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			t.Fatalf("song #%d: %v (played %v)", i+1, err, keysOf(songs))
		}
		songs = append(songs, s)
		clk.Advance(15 * time.Second)
	}
	checkNoRepeats(t, songs)
	if len(songs) != 5 {
		t.Errorf("played %v, want all five songs there are", keysOf(songs))
	}
	if _, err := q.Next(context.Background()); !errors.Is(err, ErrNoSources) {
		t.Errorf("after the last song: %v, want ErrNoSources", err)
	}
}

// --- style from elsewhere -----------------------------------------------------------

// A pasted link is song radio from that video. Its style is its artist's
// Bandcamp band, and failing that the tags the video carries itself, less the
// ones that are only the artist's name or a label for the kind of upload.
func TestSongRadioFromLink(t *testing.T) {
	const link = "https://www.youtube.com/watch?v=abcdefghijk&list=PL1"
	resolve := func(ctx context.Context, page string) (YTMedia, error) {
		if idOf(page) == "abcdefghijk" {
			return YTMedia{
				URL: "https://media.example/abcdefghijk", Artist: "Dismember", Track: "Dreaming In Red",
				Title: "Dreaming In Red", Duration: 300, Tags: []string{"Dismember", "swedish death metal", "music", "Swedish Metal"},
			}, nil
		}
		return defaultMedia(page), nil
	}
	feed := func(tag, slice string) []BCItem { return bcItems("L "+tag+slice, 300, 40) }

	t.Run("the band's tags", func(t *testing.T) {
		bc := &fakeSearcher{
			answers: map[string]SearchResult{matchKey("Dismember"): {Bands: []SearchBand{{
				Name: "Dismember", Genre: "Metal", Tags: []string{"Metal", "Death Metal", "Swedish"},
			}}}},
			feed: feed,
		}
		yt := radioYT(radioWorld{}, styleSongs)
		yt.resolve = resolve
		q, clk := newRadioQueue(link, yt, bc)

		songs := drain(t, q, clk, 14)
		if songs[0].Key != "yt:abcdefghijk" || songs[0].Artist != "Dismember" {
			t.Errorf("first song = %+v, want the linked one", songs[0])
		}
		checkNoRepeats(t, songs)
		if got := bc.searches(); len(got) != 1 || got[0] != "Dismember" {
			t.Errorf("Bandcamp searches = %v, want the artist", got)
		}
		for tag := range bc.feedTags() {
			if tag != "death-metal" && tag != "swedish" {
				t.Errorf("the feed was asked about %q, want the band's tags", tag)
			}
		}
		_, mixes, _ := yt.logs()
		if len(mixes) == 0 || mixes[0] != "abcdefghijk#50" {
			t.Errorf("Mixes = %v, want the linked video's", mixes)
		}
	})

	t.Run("the video's own tags", func(t *testing.T) {
		bc := &fakeSearcher{feed: feed} // no band under that name
		yt := radioYT(radioWorld{}, styleSongs)
		yt.resolve = resolve
		q, clk := newRadioQueue(link, yt, bc)

		drain(t, q, clk, 14)
		tags := bc.feedTags()
		if len(tags) == 0 {
			t.Fatal("the video's tags never reached the feed")
		}
		for tag := range tags {
			if tag != "swedish-death-metal" && tag != "swedish-metal" {
				t.Errorf("the feed was asked about %q; the artist's name and 'music' are not a style", tag)
			}
		}
	})
}

// An artist with no Bandcamp page takes the style of the artists found in
// its Mix: the three tags most of them share.
func TestSongRadioTagsFromTheMix(t *testing.T) {
	band := func(name string, tags ...string) SearchResult {
		return SearchResult{Bands: []SearchBand{{Name: name, Genre: "Metal", Tags: append([]string{"Metal"}, tags...)}}}
	}
	bc := &fakeSearcher{
		answers: map[string]SearchResult{
			matchKey("Organectomy"):         band("Organectomy", "Death Metal", "Slam"),
			matchKey("Analepsy"):            band("Analepsy", "Death Metal", "Brutal Death Metal"),
			matchKey("Maximize Bestiality"): band("Maximize Bestiality", "Slam", "Death Metal", "Grind"),
			// "Release" has no page, and "Obscure Band" has none either.
		},
		feed: func(tag, slice string) []BCItem { return bcItems("M "+tag+slice, 400, 40) },
	}
	world := radioWorld{
		own:    map[string]int{"obscure": 6, "organectomy": 6, "analepsy": 6, "maximize": 6, "release": 6},
		others: map[string][]string{"obscure": {"organectomy", "analepsy", "maximize", "release"}},
	}
	yt := radioYT(world, func(query string, n int) []YTHit {
		if query == "some song title" {
			return songsBy("obscure", 6)
		}
		return styleSongs(query, n)
	})
	q, clk := newRadioQueue("some song title", yt, bc)

	drain(t, q, clk, 30)
	tags := bc.feedTags()
	for _, want := range []string{"death-metal", "slam", "brutal-death-metal"} {
		if !tags[want] {
			t.Errorf("the feed never asked for %q; asked for %v", want, tags)
		}
	}
	for tag := range tags {
		if tag == "grind" || tag == "metal" {
			t.Errorf("the feed was asked about %q: only the three most common tags, without the genre", tag)
		}
	}
	seen := map[string]int{}
	for _, s := range bc.searches() {
		seen[s]++
		if seen[s] > 1 {
			t.Errorf("Bandcamp was searched for %q %d times", s, seen[s])
		}
	}
}

// With no yt-dlp there is no first song to play (the Bandcamp page needs it
// too), but an artist or a song is still a style, and Bandcamp's feed carries it.
func TestSongRadioWithoutYtdlp(t *testing.T) {
	feed := func(tag, slice string) []BCItem { return bcItems("N "+tag+slice, 600, 40) }

	t.Run("an artist", func(t *testing.T) {
		bc := &fakeSearcher{
			answers: map[string]SearchResult{matchKey("chelsea wolfe"): {
				Tags:  []string{"chelsea-wolfe"},
				Bands: []SearchBand{{Name: "Chelsea Wolfe", Genre: "Alternative", Tags: []string{"Alternative", "Folk"}}},
			}},
			feed: feed,
		}
		q, clk := newRadioQueue("chelsea wolfe", nil, bc)
		songs := drain(t, q, clk, 10)
		for _, s := range songs {
			if !strings.HasPrefix(s.Key, "bc:") {
				t.Errorf("song %+v is not from Bandcamp", s)
			}
		}
		for tag := range bc.feedTags() {
			if tag != "folk" {
				t.Errorf("the feed was asked about %q, want the band's tag", tag)
			}
		}
	})

	t.Run("a phrase that can only be a tag", func(t *testing.T) {
		bc := &fakeSearcher{feed: feed}
		q, clk := newRadioQueue("dungeon synth", nil, bc)
		drain(t, q, clk, 5)
		if calls := bc.feedCalls(); len(calls) == 0 || calls[0] != "dungeon-synth|new" {
			t.Errorf("feed calls = %v, want the phrase tried as a tag, as before", calls)
		}
	})
}

// --- the song itself ----------------------------------------------------------------

func TestSongDurationCarried(t *testing.T) {
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit { return hitsFor("d-", 60)[:n] },
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			m.Duration = 200.5
			return m, nil
		},
	}
	bc := &fakeBC{items: func(tag, slice string) []BCItem {
		items := bcItems("D", 800, 30)
		for i := range items {
			if i%2 == 1 {
				items[i].Duration = 0 // a feed that gave no length
			} else {
				items[i].Duration = 183.25
			}
		}
		return items
	}}
	q, clk := newTestQueue("dungeon synth", yt, bc, nil)

	var sawYT, sawBC, sawBCUnknown bool
	for _, s := range drain(t, q, clk, 40) {
		switch {
		case strings.HasPrefix(s.Key, "yt:"):
			sawYT = true
			if s.Duration != 200500*time.Millisecond {
				t.Errorf("%s duration = %v, want 200.5s", s.Key, s.Duration)
			}
		case s.Duration == 0:
			sawBCUnknown = true
		default:
			sawBC = true
			if s.Duration != 183250*time.Millisecond {
				t.Errorf("%s duration = %v, want 183.25s", s.Key, s.Duration)
			}
		}
	}
	if !sawYT || !sawBC || !sawBCUnknown {
		t.Errorf("saw YouTube %v, Bandcamp with a length %v, Bandcamp without %v; want all three", sawYT, sawBC, sawBCUnknown)
	}
}

// The seed song carries its length too: the player resumes it by that.
func TestSeedSongDuration(t *testing.T) {
	bc := &fakeSearcher{answers: map[string]SearchResult{matchKey("chelsea wolfe"): {Bands: []SearchBand{{Name: "Chelsea Wolfe"}}}}}
	yt := radioYT(radioWorld{}, func(query string, n int) []YTHit { return songsBy("chelsea", 3) })
	q, _ := newRadioQueue("chelsea wolfe", yt, bc)
	s, err := q.Next(context.Background())
	if err != nil || s.Duration != 200*time.Second {
		t.Errorf("seed = %+v, %v; want a 200s song", s, err)
	}
}
