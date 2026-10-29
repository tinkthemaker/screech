package discover

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

// --- fakes ------------------------------------------------------------------
//
// Nothing here sleeps, listens or dials: the queue's clock is a value the test
// moves, and both sources are in-memory.

var t0 = time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: t0} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const watch = "https://www.youtube.com/watch?v="

type fakeYT struct {
	mu      sync.Mutex
	songs   func(query string, n int) []YTHit
	mix     func(id string, n int) []YTHit
	resolve func(ctx context.Context, page string) (YTMedia, error)

	songsErr error

	songsLog, mixLog, resolveLog []string
}

func (f *fakeYT) Songs(ctx context.Context, query string, n int) ([]YTHit, error) {
	f.mu.Lock()
	f.songsLog = append(f.songsLog, fmt.Sprintf("%s#%d", query, n))
	fn, err := f.songs, f.songsErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, nil
	}
	return fn(query, n), nil
}

func (f *fakeYT) Mix(ctx context.Context, id string, n int) ([]YTHit, error) {
	f.mu.Lock()
	f.mixLog = append(f.mixLog, fmt.Sprintf("%s#%d", id, n))
	fn := f.mix
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(id, n), nil
}

func (f *fakeYT) Resolve(ctx context.Context, page string) (YTMedia, error) {
	f.mu.Lock()
	f.resolveLog = append(f.resolveLog, page)
	fn := f.resolve
	f.mu.Unlock()
	if fn == nil {
		return defaultMedia(page), nil
	}
	return fn(ctx, page)
}

func (f *fakeYT) logs() (songs, mix, resolve []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.songsLog...), append([]string(nil), f.mixLog...), append([]string(nil), f.resolveLog...)
}

// hitsFor makes count hits whose ids start with prefix.
func hitsFor(prefix string, count int) []YTHit {
	out := make([]YTHit, count)
	for i := range out {
		out[i] = YTHit{ID: fmt.Sprintf("%s%02d", prefix, i), Title: fmt.Sprintf("Song %s%02d", prefix, i)}
	}
	return out
}

func idOf(page string) string { return strings.TrimPrefix(page, watch) }

// defaultMedia is a well-behaved song by one of twelve artists chosen by the id.
func defaultMedia(page string) YTMedia {
	id := idOf(page)
	sum := 0
	for _, b := range []byte(id) {
		sum += int(b)
	}
	return YTMedia{
		URL:      "https://media.example/" + id,
		Artist:   fmt.Sprintf("YT Band %d", sum%12),
		Track:    "Track " + id,
		Title:    "Track " + id,
		Duration: 200,
		Views:    5_000,
	}
}

// plentifulYT answers every query with sixty fresh hits, as the real search
// does, and resolves them all to ordinary songs.
func plentifulYT() *fakeYT {
	return &fakeYT{
		songs: func(query string, n int) []YTHit {
			hits := hitsFor(strings.ReplaceAll(query, " ", "_")+"-", 60)
			if n < len(hits) {
				hits = hits[:n]
			}
			return hits
		},
		mix: func(id string, n int) []YTHit { return hitsFor("mix-"+id+"-", 60) },
	}
}

type fakeBC struct {
	mu    sync.Mutex
	items func(tag, slice string) []BCItem
	err   error
	calls []string
}

func (f *fakeBC) Tag(ctx context.Context, tag, slice string) ([]BCItem, error) {
	f.mu.Lock()
	f.calls = append(f.calls, tag+"|"+slice)
	fn, err := f.items, f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, nil
	}
	return fn(tag, slice), nil
}

func (f *fakeBC) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// bcItems makes count items whose track ids start at first, spread over
// twelve artists named after prefix.
func bcItems(prefix string, first int64, count int) []BCItem {
	out := make([]BCItem, count)
	for i := range out {
		id := first + int64(i)
		out[i] = BCItem{
			TrackID:   id,
			Artist:    fmt.Sprintf("%s %d", prefix, id%12),
			Title:     fmt.Sprintf("Cut %d", id),
			StreamURL: fmt.Sprintf("https://t4.bcbits.com/stream/%d", id),
			Duration:  180,
		}
	}
	return out
}

// plentifulBC has sixty items in each slice, none shared.
func plentifulBC() *fakeBC {
	return &fakeBC{items: func(tag, slice string) []BCItem {
		switch slice {
		case "top":
			return bcItems("BC Band", 2000, 60)
		case "rand":
			return bcItems("BC Band", 3000, 60)
		}
		return bcItems("BC Band", 1000, 60)
	}}
}

func newTestQueue(seed string, yt YouTube, bc Bandcamp, known Known) (*Queue, *clock) {
	clk := newClock()
	return NewQueue(seed, yt, bc, known, rand.New(rand.NewSource(7)), clk.Now), clk
}

// drain asks for n songs, letting fifteen seconds pass between asks the way a
// listener's songs would, and fails the test on any error.
func drain(t *testing.T, q *Queue, clk *clock, n int) []Song {
	t.Helper()
	var out []Song
	for i := 0; i < n; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			t.Fatalf("Next #%d: %v", i+1, err)
		}
		out = append(out, s)
		clk.Advance(15 * time.Second)
	}
	return out
}

// checkNoRepeats holds songs to the queue's two promises.
func checkNoRepeats(t *testing.T, songs []Song) {
	t.Helper()
	seen := map[string]int{}
	for i, s := range songs {
		if j, dup := seen[s.Key]; dup {
			t.Errorf("%s came at #%d and again at #%d", s.Key, j+1, i+1)
		}
		seen[s.Key] = i
		if i > 0 && core.NormalizeArtist(s.Artist) == core.NormalizeArtist(songs[i-1].Artist) {
			t.Errorf("%q played twice running at #%d", s.Artist, i+1)
		}
	}
}

// --- the mix ------------------------------------------------------------------

func TestQueueMixesAnchorsAndDiscoveries(t *testing.T) {
	// Several seeds, because a share is a probability: any one fixed seed
	// could pass by luck.
	for _, seed := range []int64{1, 2, 3, 4, 5} {
		clk := newClock()
		q := NewQueue("swedish death metal", plentifulYT(), plentifulBC(), nil, rand.New(rand.NewSource(seed)), clk.Now)
		songs := drain(t, q, clk, 100)

		anchors, yt, bc := 0, 0, 0
		for _, s := range songs {
			if s.Anchor {
				anchors++
			}
			switch {
			case strings.HasPrefix(s.Key, "yt:"):
				yt++
			case strings.HasPrefix(s.Key, "bc:"):
				bc++
			default:
				t.Fatalf("key %q is neither yt: nor bc:", s.Key)
			}
			if s.URL == "" || s.Artist == "" || s.Title == "" || s.Resolved.IsZero() {
				t.Fatalf("incomplete song %+v", s)
			}
		}
		t.Logf("seed %d: anchor share %.2f, yt=%d bc=%d", seed, float64(anchors)/100, yt, bc)
		if share := float64(anchors) / 100; share < 0.12 || share > 0.30 {
			t.Errorf("seed %d: anchor share %.2f outside [0.12, 0.30]", seed, share)
		}
		if yt == 0 || bc == 0 {
			t.Errorf("seed %d: both sources must appear, got yt=%d bc=%d", seed, yt, bc)
		}
		checkNoRepeats(t, songs)
	}
}

func TestQueueNeverRepeatsOrDoublesArtist(t *testing.T) {
	// Thirty songs, six artists, every slice the same list.
	var all []BCItem
	for i := int64(0); i < 30; i++ {
		all = append(all, BCItem{
			TrackID: 100 + i, Artist: fmt.Sprintf("Artist %d", i%6), Title: fmt.Sprintf("Song %d", i),
			StreamURL: fmt.Sprintf("https://t4.bcbits.com/stream/%d", 100+i), Duration: 180,
		})
	}
	bc := &fakeBC{items: func(tag, slice string) []BCItem { return all }}
	q, clk := newTestQueue("dungeon synth", nil, bc, nil)

	var got []Song
	var last error
	for i := 0; i < 60; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			last = err
			break
		}
		got = append(got, s)
		clk.Advance(15 * time.Second)
	}
	checkNoRepeats(t, got)
	t.Logf("%d of 30 songs played before the queue ran out", len(got))
	if len(got) < 20 {
		t.Errorf("only %d of 30 songs came out", len(got))
	}
	// Past the last fresh song the queue says so; it never serves a repeat.
	if !errors.Is(last, ErrNoSources) {
		t.Errorf("after the songs ran out got %v, want ErrNoSources", last)
	}
}

func TestQueueNewToYouFirst(t *testing.T) {
	var items []BCItem
	id := int64(1)
	for _, artist := range []string{"Known One", "Fresh One", "Known Two", "Fresh Two"} {
		for i := 0; i < 3; i++ {
			items = append(items, BCItem{TrackID: id, Artist: artist, Title: fmt.Sprintf("%s %d", artist, i),
				StreamURL: fmt.Sprintf("https://t4.bcbits.com/stream/%d", id), Duration: 180})
			id++
		}
	}
	bc := &fakeBC{items: func(tag, slice string) []BCItem {
		if slice == "new" {
			return items
		}
		return nil
	}}
	known := func(key string) bool { return key == "known one" || key == "known two" }
	q, clk := newTestQueue("dungeon synth", nil, bc, known)

	songs := drain(t, q, clk, 12)
	for i, s := range songs {
		wantNew := i < 6
		if s.NewToYou != wantNew {
			t.Errorf("song #%d (%s): NewToYou = %v, want %v", i+1, s.Artist, s.NewToYou, wantNew)
		}
		if s.Anchor {
			t.Errorf("song #%d came back as an anchor from an empty top slice", i+1)
		}
	}
	checkNoRepeats(t, songs)
}

func TestQueueNewToYouLooksPastKnownYouTube(t *testing.T) {
	// A search result carries no artist, so the queue can only tell after it
	// resolves. It must not settle for the first known band it meets.
	media := map[string]YTMedia{}
	for _, id := range []string{"aaa", "bbb"} {
		m := defaultMedia(watch + id)
		m.Artist = "Known " + id
		media[id] = m
	}
	fresh := defaultMedia(watch + "zzz")
	fresh.Artist = "Fresh Band"
	media["zzz"] = fresh

	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			if n == 5 {
				return nil // no anchors: every song is a discovery
			}
			hits := hitsFor("pad", 10)
			return append(hits, YTHit{ID: "aaa"}, YTHit{ID: "bbb"}, YTHit{ID: "zzz"})
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) { return media[idOf(page)], nil },
	}
	known := func(key string) bool { return strings.HasPrefix(key, "known ") }
	q, _ := newTestQueue("dungeon synth", yt, nil, known)

	s, err := q.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if s.Key != "yt:zzz" || !s.NewToYou {
		t.Errorf("got %s NewToYou=%v, want the fresh band first", s.Key, s.NewToYou)
	}
}

// --- YouTube specifics -----------------------------------------------------------

func TestQueueSkipsOutOfRangeDurations(t *testing.T) {
	media := map[string]float64{"short": 45, "album": 2 * 3600, "good": 200}
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			return []YTHit{{ID: "short"}, {ID: "album"}, {ID: "good"}}
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			m.Duration = media[idOf(page)]
			return m, nil
		},
	}
	q, _ := newTestQueue("dungeon synth", yt, nil, nil)

	s, err := q.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if s.Key != "yt:good" {
		t.Errorf("got %s, want the only song in range", s.Key)
	}
	// The two out of range are never offered again.
	if _, err := q.Next(context.Background()); !errors.Is(err, ErrNoSources) {
		t.Errorf("second Next = %v, want ErrNoSources", err)
	}
	_, _, resolved := yt.logs()
	if len(resolved) != 3 {
		t.Errorf("resolved %v: each candidate should be looked at exactly once", resolved)
	}
}

func TestQueueRelabelsPopularDiscovery(t *testing.T) {
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			if n == 5 {
				return nil // no anchors, so everything below is a discovery
			}
			if query != "dungeon synth" {
				return nil // variants add nothing here
			}
			return append(hitsFor("pad", 10), YTHit{ID: "pop"}, YTHit{ID: "small"})
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			if idOf(page) == "pop" {
				m.Views = 2_000_000
			}
			return m, nil
		},
	}
	q, clk := newTestQueue("dungeon synth", yt, nil, func(string) bool { return false })

	got := map[string]Song{}
	for _, s := range drain(t, q, clk, 2) {
		got[s.Key] = s
	}
	if pop := got["yt:pop"]; !pop.Anchor || pop.NewToYou {
		t.Errorf("a million-view discovery should come back as an anchor, not new: %+v", pop)
	}
	if small := got["yt:small"]; small.Anchor || !small.NewToYou {
		t.Errorf("a small discovery should stay one, and be new: %+v", small)
	}
}

func TestQueueYouTubeDiscoveriesSkipTheAnchorsAndTryVariants(t *testing.T) {
	// Results 1-5 are anchors, 6-10 are neither, 11 on are discoveries; when
	// those run dry the phrase is searched again with a word added.
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			if n == 5 {
				return hitsFor("top", 5)
			}
			if query == "dungeon synth" {
				return hitsFor("base", 12) // two past the cut
			}
			return hitsFor(strings.ReplaceAll(query, " ", "_")+"-", 3)
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			m.Artist = "Band " + idOf(page) // one band per song: nothing is held up by the same-artist rule
			return m, nil
		},
	}
	q, clk := newTestQueue("dungeon synth", yt, nil, nil)

	var songs []Song
	var last error
	for i := 0; i < 40; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			last = err
			break
		}
		songs = append(songs, s)
		clk.Advance(15 * time.Second)
	}
	checkNoRepeats(t, songs)
	if !errors.Is(last, ErrNoSources) {
		t.Errorf("after everything was played got %v, want ErrNoSources", last)
	}
	// 5 anchors, the 2 base results past the cut, and 3 for each of 3 variants.
	if len(songs) != 5+2+3*3 {
		t.Errorf("%d songs played, want 16", len(songs))
	}
	for _, s := range songs {
		if strings.HasPrefix(s.Key, "yt:base") {
			if n := strings.TrimPrefix(s.Key, "yt:base"); n != "10" && n != "11" {
				t.Errorf("%s is inside the first ten results, which are not discoveries", s.Key)
			}
		}
	}
	songsLog, _, _ := yt.logs()
	asked := strings.Join(songsLog, ",")
	for _, v := range []string{"dungeon synth underground#50", "dungeon synth demo#50", "dungeon synth new#50"} {
		if !strings.Contains(asked, v) {
			t.Errorf("variant search %q never ran; searches were %s", v, asked)
		}
	}
}

func TestQueueYouTubeTitleGuessOrdersCandidates(t *testing.T) {
	// A search row sometimes does name its band ("Band - Song"). When it does,
	// a band the listener hasn't heard goes first, a row that names no one
	// next, and a band they know last.
	titles := map[string]string{
		"new1": "Unseen One - Song", "new2": "Unseen Two - Song",
		"k1": "Known Band - Song One", "k2": "Known Band - Song Two",
		"p1": "Plain", "p2": "Plain", "p3": "Plain", "p4": "Plain",
	}
	order := []string{"k1", "p1", "k2", "p2", "p3", "new1", "p4", "new2"}
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			if n == 5 || query != "dungeon synth" {
				return nil
			}
			hits := hitsFor("pad", 10)
			for _, id := range order {
				hits = append(hits, YTHit{ID: id, Title: titles[id]})
			}
			return hits
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			m.Artist = "Band " + idOf(page)
			if strings.HasPrefix(idOf(page), "k") {
				m.Artist = "Known Band"
			}
			return m, nil
		},
	}
	known := func(key string) bool { return key == "known band" }
	q, clk := newTestQueue("dungeon synth", yt, nil, known)

	var got []string
	for _, s := range drain(t, q, clk, 6) {
		got = append(got, strings.TrimPrefix(s.Key, "yt:"))
	}
	if first := map[string]bool{got[0]: true, got[1]: true}; !first["new1"] || !first["new2"] {
		t.Errorf("order = %v: the two unheard bands should come first", got)
	}
	for _, id := range got[2:] {
		if strings.HasPrefix(id, "k") {
			t.Errorf("order = %v: a known band came before the unnamed rows ran out", got)
		}
	}
}

func TestQueueSearchCache(t *testing.T) {
	yt := plentifulYT()
	q, clk := newTestQueue("dungeon synth", yt, nil, nil)

	drain(t, q, clk, 12)
	songsLog, _, _ := yt.logs()
	before := len(songsLog)
	if before == 0 || before > 2 {
		t.Fatalf("12 songs took %d searches (%v), want the anchors' and the discoveries' one each", before, songsLog)
	}

	clk.Advance(7 * time.Hour)
	drain(t, q, clk, 4)
	songsLog, _, _ = yt.logs()
	if len(songsLog) <= before {
		t.Errorf("searches stayed at %d seven hours on: the cache should have expired", len(songsLog))
	}
}

func TestQueueLinkSeedUsesMix(t *testing.T) {
	const link = "https://www.youtube.com/watch?v=abcdefghijk&list=PL1"
	yt := plentifulYT()
	yt.resolve = func(ctx context.Context, page string) (YTMedia, error) {
		m := defaultMedia(page)
		if idOf(page) == "abcdefghijk" {
			m.Artist, m.Track, m.Tags = "Dismember", "Dreaming In Red", []string{"death metal"}
		}
		return m, nil
	}
	q, clk := newTestQueue(link, yt, nil, nil)

	songs := drain(t, q, clk, 6)
	checkNoRepeats(t, songs)

	// The song that was pasted plays first: it is already resolved.
	if first := songs[0]; first.Key != "yt:abcdefghijk" || first.Artist != "Dismember" || first.Title != "Dreaming In Red" {
		t.Errorf("first song = %+v, want the linked one", first)
	}
	songsLog, mixLog, _ := yt.logs()
	if len(mixLog) == 0 || mixLog[0] != "abcdefghijk#50" {
		t.Errorf("Mix calls = %v, want one for the video id", mixLog)
	}
	for _, query := range songsLog {
		if strings.Contains(query, "http") || strings.Contains(query, "youtube.com") || strings.Contains(query, "abcdefghijk") {
			t.Errorf("Songs was asked for the link text: %q", query)
		}
	}
	// Related songs come from the Mix.
	related := 0
	for _, s := range songs[1:] {
		if strings.HasPrefix(s.Key, "yt:mix-abcdefghijk-") {
			related++
		}
	}
	if related == 0 {
		t.Errorf("no song came from the Mix: %+v", songs)
	}
}

func TestQueueLinkSeedTagsFindBandcamp(t *testing.T) {
	const link = "https://youtu.be/abcdefghijk?t=3"
	yt := plentifulYT()
	yt.resolve = func(ctx context.Context, page string) (YTMedia, error) {
		m := defaultMedia(page)
		if idOf(page) == "abcdefghijk" {
			// An artist tag Bandcamp has never heard of comes first.
			m.Tags = []string{"Dismember", "swedish death metal", "music"}
		}
		return m, nil
	}
	bc := &fakeBC{items: func(tag, slice string) []BCItem {
		if tag == "swedish-death-metal" {
			return bcItems("Swede", 500, 60)
		}
		return nil
	}}
	q, clk := newTestQueue(link, yt, bc, nil)

	songs := drain(t, q, clk, 20)
	calls := bc.log()
	if len(calls) < 2 || calls[0] != "dismember|new" || calls[1] != "swedish-death-metal|new" {
		t.Fatalf("Bandcamp probes = %v, want dismember then swedish-death-metal", calls)
	}
	for _, c := range calls {
		if c == "music|new" || c == "metal|new" || c == "death-metal|new" {
			t.Errorf("kept probing after a tag answered: %v", calls)
		}
	}
	bcSongs := 0
	for _, s := range songs {
		if strings.HasPrefix(s.Key, "bc:") {
			bcSongs++
		}
	}
	if bcSongs == 0 {
		t.Errorf("no song came from Bandcamp: %+v", songs)
	}
}

func TestQueueLinkSeedNeedsYouTube(t *testing.T) {
	q, _ := newTestQueue("https://youtu.be/abcdefghijk", nil, plentifulBC(), nil)
	if _, err := q.Next(context.Background()); !errors.Is(err, ErrNoSources) {
		t.Errorf("a link with no yt-dlp = %v, want ErrNoSources", err)
	}
}

func TestQueueLinkSeedGivesUpOnTagsAfterTwoFailures(t *testing.T) {
	boom := errors.New("sign in to confirm you are not a bot")
	yt := plentifulYT()
	yt.resolve = func(ctx context.Context, page string) (YTMedia, error) {
		if idOf(page) == "abcdefghijk" {
			return YTMedia{}, boom
		}
		return defaultMedia(page), nil
	}
	q, _ := newTestQueue("https://youtu.be/abcdefghijk", yt, nil, nil)

	// The first failure may be a blip: the error comes out and the next
	// call tries again. The second means the video itself won't resolve, so
	// the Mix carries on without its tags.
	if _, err := q.Next(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("first Next = %v, want the resolve error", err)
	}
	if _, err := q.Next(context.Background()); err != nil {
		t.Fatalf("second Next: %v", err)
	}
}

// --- Bandcamp specifics ----------------------------------------------------------

func TestQueueBandcampOnlyWhenYouTubeNil(t *testing.T) {
	q, clk := newTestQueue("dungeon synth", nil, plentifulBC(), nil)
	songs := drain(t, q, clk, 10)
	for _, s := range songs {
		if !strings.HasPrefix(s.Key, "bc:") {
			t.Errorf("song %+v is not from Bandcamp", s)
		}
	}
	checkNoRepeats(t, songs)
}

func TestQueueBandcampTagFallback(t *testing.T) {
	bc := &fakeBC{items: func(tag, slice string) []BCItem {
		if tag == "death-metal" {
			return bcItems("DM", 700, 60)
		}
		return nil
	}}
	q, clk := newTestQueue("Swedish Death Metal", nil, bc, nil)

	drain(t, q, clk, 10)
	calls := bc.log()
	if len(calls) < 2 || calls[0] != "swedish-death-metal|new" || calls[1] != "death-metal|new" {
		t.Fatalf("probes = %v, want the full phrase, then with a leading word dropped", calls)
	}
	for _, c := range calls[2:] {
		if !strings.HasPrefix(c, "death-metal|") {
			t.Errorf("after the tag answered, asked for %q", c)
		}
	}
}

func TestQueueBandcampSitsOutWhenNoTagMatches(t *testing.T) {
	bc := &fakeBC{}
	yt := plentifulYT()
	q, clk := newTestQueue("Dismember", yt, bc, nil)

	songs := drain(t, q, clk, 30)
	for _, s := range songs {
		if !strings.HasPrefix(s.Key, "yt:") {
			t.Errorf("song %+v is not from YouTube", s)
		}
	}
	if n := len(bc.log()); n != 1 {
		t.Errorf("Bandcamp was asked %d times about a tag that never matches: %v", n, bc.log())
	}
}

func TestQueueBandcampPacing(t *testing.T) {
	bc := plentifulBC()
	q, clk := newTestQueue("death metal", nil, bc, nil)
	ctx := context.Background()

	if _, _, err := q.bcFetch(ctx, "death-metal", "new", false); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	clk.Advance(5 * time.Second)
	if _, _, err := q.bcFetch(ctx, "death-metal", "new", false); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if n := len(bc.log()); n != 1 {
		t.Fatalf("same tag and slice within 10 s made %d requests, want the cache to answer", n)
	}

	// A different slice inside the gap waits; it is not sent.
	if _, _, err := q.bcFetch(ctx, "death-metal", "top", false); err == nil {
		t.Error("a new request 5 s after the last should be held back")
	}
	if n := len(bc.log()); n != 1 {
		t.Fatalf("a held-back request still went out: %v", bc.log())
	}
	clk.Advance(6 * time.Second)
	if _, _, err := q.bcFetch(ctx, "death-metal", "top", false); err != nil {
		t.Fatalf("after the gap: %v", err)
	}
	if n := len(bc.log()); n != 2 {
		t.Fatalf("after the gap the request should go out, calls = %v", bc.log())
	}

	// The cache keeps for half an hour and no longer.
	clk.Advance(29 * time.Minute)
	if _, _, err := q.bcFetch(ctx, "death-metal", "new", false); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if n := len(bc.log()); n != 2 {
		t.Errorf("a 29-minute-old answer should still serve, calls = %v", bc.log())
	}
	clk.Advance(2 * time.Minute)
	if _, _, err := q.bcFetch(ctx, "death-metal", "new", false); err != nil {
		t.Fatalf("expired fetch: %v", err)
	}
	if n := len(bc.log()); n != 3 {
		t.Errorf("an expired answer should be asked for again, calls = %v", bc.log())
	}
}

func TestQueueBandcampFailureWaitsOutTheGap(t *testing.T) {
	down := errors.New("bandcamp is down")
	bc := &fakeBC{err: down}
	q, clk := newTestQueue("death metal", nil, bc, nil)

	_, err := q.Next(context.Background())
	if !errors.Is(err, down) {
		t.Fatalf("first Next = %v, want the lookup error", err)
	}
	clk.Advance(3 * time.Second)
	// Still inside the gap: no second request, and the same failure comes
	// back so the caller's offline handling still sees it.
	_, err = q.Next(context.Background())
	if !errors.Is(err, down) {
		t.Fatalf("second Next = %v, want the same lookup error", err)
	}
	if n := len(bc.log()); n != 1 {
		t.Errorf("a failing endpoint was asked %d times in 3 s", n)
	}
	clk.Advance(10 * time.Second)
	bc.mu.Lock()
	bc.err = nil
	bc.items = func(tag, slice string) []BCItem { return bcItems("Back", 10, 20) }
	bc.mu.Unlock()
	if _, err := q.Next(context.Background()); err != nil {
		t.Errorf("after the gap and a recovery: %v", err)
	}
}

// --- errors -------------------------------------------------------------------

func TestQueueNoSourcesErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Bandcamp answers empty for every tag and there is no yt-dlp.
	q, _ := newTestQueue("a name nobody tagged", nil, &fakeBC{}, nil)
	if _, err := q.Next(ctx); !errors.Is(err, ErrNoSources) {
		t.Errorf("empty Bandcamp, no YouTube: %v, want ErrNoSources", err)
	}
	if ctx.Err() != nil {
		t.Errorf("the answer should come before the deadline, ctx = %v", ctx.Err())
	}

	// Neither source exists.
	q, _ = newTestQueue("anything", nil, nil, nil)
	if _, err := q.Next(ctx); !errors.Is(err, ErrNoSources) {
		t.Errorf("no sources at all: %v, want ErrNoSources", err)
	}

	// Both exist and have nothing.
	q, _ = newTestQueue("anything", &fakeYT{}, &fakeBC{}, nil)
	if _, err := q.Next(ctx); !errors.Is(err, ErrNoSources) {
		t.Errorf("two empty sources: %v, want ErrNoSources", err)
	}
}

func TestQueueSourceFailureKeepsItsChain(t *testing.T) {
	// A dead network is not "no songs": the caller must be able to tell, so
	// it can go offline instead of striking the station.
	netDown := errors.New("dial tcp: no route to host")
	yt := &fakeYT{songsErr: netDown}
	q, _ := newTestQueue("dungeon synth", yt, nil, nil)
	_, err := q.Next(context.Background())
	if !errors.Is(err, netDown) {
		t.Errorf("Next = %v, want the original error in the chain", err)
	}
	if errors.Is(err, ErrNoSources) {
		t.Errorf("a failed lookup must not read as an empty one: %v", err)
	}
}

func TestQueueOneSourceDownDoesNotStopTheOther(t *testing.T) {
	bc := &fakeBC{err: errors.New("bandcamp is down")}
	q, clk := newTestQueue("dungeon synth", plentifulYT(), bc, nil)
	for _, s := range drain(t, q, clk, 8) {
		if !strings.HasPrefix(s.Key, "yt:") {
			t.Errorf("song %+v is not from YouTube", s)
		}
	}

	yt := &fakeYT{songsErr: errors.New("yt-dlp crashed")}
	q, clk = newTestQueue("dungeon synth", yt, plentifulBC(), nil)
	for _, s := range drain(t, q, clk, 8) {
		if !strings.HasPrefix(s.Key, "bc:") {
			t.Errorf("song %+v is not from Bandcamp", s)
		}
	}
}

func TestQueueResolveFailuresMoveOnThenSurface(t *testing.T) {
	gone := errors.New("video unavailable")
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit { return hitsFor("v", 12) },
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			if idOf(page) == "v00" || idOf(page) == "v01" {
				return YTMedia{}, gone
			}
			return defaultMedia(page), nil
		},
	}
	q, clk := newTestQueue("dungeon synth", yt, nil, nil)
	// Whatever the roll picks first, a dead video never stops the station for
	// good: within a few asks every live song is reachable.
	var served int
	for i := 0; i < 8; i++ {
		if _, err := q.Next(context.Background()); err == nil {
			served++
		} else if !errors.Is(err, gone) {
			t.Fatalf("unexpected error %v", err)
		}
		clk.Advance(15 * time.Second)
	}
	if served < 5 {
		t.Errorf("only %d songs in 8 asks with two dead videos", served)
	}

	// With nothing but dead videos the error reaches the caller.
	yt2 := &fakeYT{
		songs:   func(query string, n int) []YTHit { return hitsFor("d", 12) },
		resolve: func(ctx context.Context, page string) (YTMedia, error) { return YTMedia{}, gone },
	}
	q2, _ := newTestQueue("dungeon synth", yt2, nil, nil)
	if _, err := q2.Next(context.Background()); !errors.Is(err, gone) {
		t.Errorf("all-dead Next = %v, want the resolve error", err)
	}
}

func TestQueueDropsAVideoThatFailsTwice(t *testing.T) {
	gone := errors.New("video unavailable")
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit {
			if n == 5 {
				return []YTHit{{ID: "dead"}}
			}
			return nil
		},
		resolve: func(ctx context.Context, page string) (YTMedia, error) { return YTMedia{}, gone },
	}
	q, clk := newTestQueue("dungeon synth", yt, nil, nil)

	for i := 1; i <= 2; i++ {
		if _, err := q.Next(context.Background()); !errors.Is(err, gone) {
			t.Fatalf("Next #%d = %v, want the resolve error", i, err)
		}
		clk.Advance(15 * time.Second)
	}
	// The first failure might have been a blip. The second was the video.
	if _, err := q.Next(context.Background()); !errors.Is(err, ErrNoSources) {
		t.Errorf("Next #3 = %v, want ErrNoSources: the video should be dropped", err)
	}
	if _, _, resolved := yt.logs(); len(resolved) != 2 {
		t.Errorf("the dead video was resolved %d times, want 2", len(resolved))
	}
}

func TestQueueCancelledContext(t *testing.T) {
	q, _ := newTestQueue("dungeon synth", plentifulYT(), plentifulBC(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Next with a cancelled context = %v, want context.Canceled", err)
	}
}

// --- titles on the way out ------------------------------------------------------------

func TestQueueSongTitlesAreClean(t *testing.T) {
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit { return []YTHit{{ID: "one"}, {ID: "two"}, {ID: "three"}} },
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			m := defaultMedia(page)
			switch idOf(page) {
			case "one": // yt-dlp's own fields win
				m.Artist, m.Track, m.Title = "Dismember", "Dreaming In Red", "Dismember - Dreaming In Red (OFFICIAL MUSIC VIDEO)"
			case "two": // no artist field: the title's "Artist - Title" is split
				m.Artist, m.Track, m.Title, m.Channel = "", "", "Entombed - Left Hand Path [HD]", "Entombed - Topic"
			case "three": // no split either: the channel stands in
				m.Artist, m.Track, m.Title, m.Channel = "", "", "Crawl (Official Audio)", "Carnage - Topic"
			}
			return m, nil
		},
	}
	q, clk := newTestQueue("dungeon synth", yt, nil, nil)
	got := map[string][2]string{}
	for _, s := range drain(t, q, clk, 3) {
		got[s.Key] = [2]string{s.Artist, s.Title}
	}
	want := map[string][2]string{
		"yt:one":   {"Dismember", "Dreaming In Red"},
		"yt:two":   {"Entombed", "Left Hand Path"},
		"yt:three": {"Carnage", "Crawl"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

// --- the adaptive share ----------------------------------------------------------------

func TestQueueAdaptsAnchorShare(t *testing.T) {
	q, _ := newTestQueue("dungeon synth", nil, nil, nil)
	start := q.AnchorShare()
	if start != 0.2 {
		t.Fatalf("starting share = %v, want 0.2", start)
	}

	q.Skipped(true)
	q.Skipped(true)
	if got := q.AnchorShare(); got != start {
		t.Errorf("two quick skips moved the share to %v", got)
	}
	q.Skipped(true)
	raised := q.AnchorShare()
	if raised <= start {
		t.Fatalf("three quick skips left the share at %v", raised)
	}

	q.Loved()
	if got := q.AnchorShare(); got >= raised {
		t.Errorf("a love left the share at %v, was %v", got, raised)
	}
	// A love says the finds are working, so it ends the raised share outright
	// and goes on to lower the base: back below where the station began.
	if got := q.AnchorShare(); got >= start {
		t.Errorf("after a love the share is %v, want below the starting %v", got, start)
	}

	// However hard it is pushed, the share stays between 1/8 and 1/3.
	for i := 0; i < 60; i++ {
		q.Skipped(true)
	}
	if got := q.AnchorShare(); got > 1.0/3+1e-9 {
		t.Errorf("share %v above 1/3", got)
	}
	for i := 0; i < 60; i++ {
		q.Loved()
	}
	if got := q.AnchorShare(); got < 1.0/8-1e-9 {
		t.Errorf("share %v below 1/8", got)
	}
}

func TestQueueSlowSkipBreaksTheRun(t *testing.T) {
	q, _ := newTestQueue("dungeon synth", nil, nil, nil)
	q.Skipped(true)
	q.Skipped(true)
	q.Skipped(false) // a skip after a minute of listening is not impatience
	q.Skipped(true)
	if got := q.AnchorShare(); got != 0.2 {
		t.Errorf("share = %v, want 0.2: the run was broken", got)
	}
}

func TestQueueListeningToASongBreaksTheRun(t *testing.T) {
	q, clk := newTestQueue("dungeon synth", nil, plentifulBC(), nil)
	q.Skipped(true)
	q.Skipped(true)
	drain(t, q, clk, 2) // the second Next follows a song that played out: no Skipped before it
	q.Skipped(true)
	if got := q.AnchorShare(); got != 0.2 {
		t.Errorf("share = %v, want 0.2: two skips either side of a full listen are not three", got)
	}
}

func TestQueueAnchorBoostLastsTenSongs(t *testing.T) {
	q, clk := newTestQueue("dungeon synth", nil, plentifulBC(), nil)
	for i := 0; i < 3; i++ {
		q.Skipped(true)
	}
	raised := q.AnchorShare()
	if raised <= 0.2 {
		t.Fatalf("share = %v after three quick skips", raised)
	}
	drain(t, q, clk, 9)
	if got := q.AnchorShare(); got != raised {
		t.Errorf("share after nine songs = %v, want %v still", got, raised)
	}
	drain(t, q, clk, 1)
	if got := q.AnchorShare(); got != 0.2 {
		t.Errorf("share after ten songs = %v, want back to 0.2", got)
	}
}

// A NEXT arrives while the next song is still being looked up. The share's
// state must answer at once rather than queue behind the lookup.
func TestQueueSkippedDuringNext(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit { return hitsFor("slow", 12) },
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return defaultMedia(page), nil
		},
	}
	q, _ := newTestQueue("dungeon synth", yt, nil, nil)

	result := make(chan error, 1)
	go func() {
		_, err := q.Next(context.Background())
		result <- err
	}()
	<-started

	done := make(chan struct{})
	go func() {
		q.Skipped(true)
		q.Loved()
		_ = q.AnchorShare()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Skipped, Loved and AnchorShare blocked behind a lookup in flight")
	}
	close(release)
	if err := <-result; err != nil {
		t.Errorf("Next: %v", err)
	}
}

// The player prefetches from one goroutine while the session's goroutine
// reports skips and loves. Run under -race, this is the check that the two
// halves of the queue never touch the same state unguarded.
func TestQueueNextRunsAlongsideSkippedAndLoved(t *testing.T) {
	clk := newClock()
	q := NewQueue("swedish death metal", plentifulYT(), plentifulBC(), nil, rand.New(rand.NewSource(3)), clk.Now)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				switch (i + g) % 4 {
				case 0, 1:
					q.Skipped(true)
				case 2:
					q.Skipped(false)
				case 3:
					q.Loved()
				}
				if share := q.AnchorShare(); share < 1.0/8-1e-9 || share > 1.0/3+1e-9 {
					t.Errorf("share %v left [1/8, 1/3]", share)
					return
				}
				runtime.Gosched()
			}
		}(g)
	}

	var songs []Song
	for i := 0; i < 60; i++ {
		s, err := q.Next(context.Background())
		if err != nil {
			t.Errorf("Next #%d: %v", i+1, err)
			break
		}
		songs = append(songs, s)
		clk.Advance(15 * time.Second)
	}
	close(stop)
	wg.Wait()
	checkNoRepeats(t, songs)
}

// Two goroutines asking at once is not the plan, but it must not break the
// promises: the second Next waits its turn, so nothing is handed out twice.
func TestQueueConcurrentNextsNeverRepeat(t *testing.T) {
	clk := newClock()
	q := NewQueue("swedish death metal", plentifulYT(), plentifulBC(), nil, rand.New(rand.NewSource(5)), clk.Now)

	var (
		mu   sync.Mutex
		keys = map[string]int{}
		wg   sync.WaitGroup
	)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				s, err := q.Next(context.Background())
				if err != nil {
					t.Errorf("Next: %v", err)
					return
				}
				mu.Lock()
				keys[s.Key]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for k, n := range keys {
		if n > 1 {
			t.Errorf("%s was handed out %d times", k, n)
		}
	}
	if len(keys) != 60 {
		t.Errorf("%d distinct songs, want 60", len(keys))
	}
}

// errProbe is a context that announces the first time anyone asks whether it
// has ended, which is how a test knows Next has begun without sleeping.
type errProbe struct {
	context.Context
	once sync.Once
	seen chan struct{}
}

func (p *errProbe) Err() error {
	p.once.Do(func() { close(p.seen) })
	return p.Context.Err()
}

// A Next that has to wait for another gives up when its own context ends; it
// does not sit behind a slow lookup it never asked for.
func TestQueueWaitingNextHonoursItsContext(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	yt := &fakeYT{
		songs: func(query string, n int) []YTHit { return hitsFor("slow", 12) },
		resolve: func(ctx context.Context, page string) (YTMedia, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return defaultMedia(page), nil
		},
	}
	q, _ := newTestQueue("dungeon synth", yt, nil, nil)

	first := make(chan error, 1)
	go func() {
		_, err := q.Next(context.Background())
		first <- err
	}()
	<-started

	// The second Next is cancelled only once it has started, so it really is
	// waiting its turn when its context ends.
	base, cancel := context.WithCancel(context.Background())
	ctx := &errProbe{Context: base, seen: make(chan struct{})}
	second := make(chan error, 1)
	go func() {
		_, err := q.Next(ctx)
		second <- err
	}()
	<-ctx.seen
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiting Next = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting Next ignored its cancelled context")
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("first Next: %v", err)
	}
}

// --- VideoID ---------------------------------------------------------------------------

func TestVideoID(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://www.youtube.com/watch?v=abcdefghijk&list=PL1", "abcdefghijk", true},
		{"https://youtu.be/abcdefghijk?t=3", "abcdefghijk", true},
		{"https://music.youtube.com/watch?v=abcdefghijk", "abcdefghijk", true},
		{"https://www.youtube.com/shorts/abcdefghijk", "abcdefghijk", true},
		{"https://m.youtube.com/watch?feature=share&v=abcdefghijk", "abcdefghijk", true},
		{"  https://youtu.be/abcdefghijk  ", "abcdefghijk", true},
		{"youtu.be/abcdefghijk", "abcdefghijk", true},
		{"https://www.youtube.com/watch?v=A-b_1234567", "A-b_1234567", true},

		{"https://example.com", "", false},
		{"https://example.com/watch?v=abcdefghijk", "", false},
		{"https://www.youtube.com/playlist?list=PL1", "", false},
		// An embed page is not a link the rest of Screech reads as a video
		// (config, `screech add` and the station's identity all go by
		// streamurl.YouTubeID), so it is not a seed either: one parser, not two.
		{"https://www.youtube.com/embed/abcdefghijk", "", false},
		{"https://www.youtube.com/watch?v=tooshort", "", false},
		{"https://www.youtube.com/watch?v=abcdefghijklmnop", "", false},
		{"https://notyoutube.com/watch?v=abcdefghijk", "", false},
		{"swedish death metal", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := VideoID(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("VideoID(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
