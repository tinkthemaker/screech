// Package discover is the song queue behind a discovery station: given a
// phrase or a YouTube link, it keeps finding the next song in that sound, from
// YouTube Music (through yt-dlp) and from Bandcamp's genre tags.
//
// It mixes discoveries (bands the listener has likely never heard, smaller
// and newer) with the occasional anchor (the genre's well-known tracks). It
// never repeats a song within the last 200, and never plays one band twice
// running. It knows nothing of mpv or the session: the player asks it for a
// song with Next and tells it how the listener took the last one.
//
// Both sources are interfaces defined here, so the queue imports neither
// yt-dlp nor an HTTP client and its tests run with no process and no network.
package discover

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

// ErrNoSources means no source has songs left for this seed. It is the
// answer, not a failure of a lookup: when a lookup itself fails (the network
// is down, yt-dlp crashed) Next returns that error instead, so the caller can
// tell "nothing to play" from "couldn't ask".
var ErrNoSources = errors.New("discover: no source has songs for this seed")

// Song is one playable song.
type Song struct {
	Key           string // "yt:<id>" or "bc:<trackid>": the no-repeat key
	Artist, Title string // cleaned for display
	URL           string // direct media URL; signed, so it is never stored or logged
	Anchor        bool   // a well-known track, not a find
	NewToYou      bool   // a discovery by a band the listener has never heard here
	Resolved      time.Time
}

// Known reports whether the listener has heard an artist, or loved them.
// It takes core.NormalizeArtist keys (core.KnownArtist is the real one).
type Known func(artistKey string) bool

const (
	// How far back a song may not repeat.
	recentSize = 200

	// The anchor share starts at one in five and stays between one in eight
	// and one in three.
	anchorStart = 0.2
	anchorMin   = 1.0 / 8
	anchorMax   = 1.0 / 3

	// Three quick skips in a row add boostBy to the share for the next
	// boostSongs songs; a love takes lovedBy off for good.
	quickRun   = 3
	boostBy    = 0.1
	boostSongs = 10
	lovedBy    = 0.03

	searchTTL = 6 * time.Hour
)

type source int

const (
	srcBC source = iota
	srcYT
)

// cand is a song that might be next. A YouTube candidate is only an id until
// it has been resolved; a Bandcamp one arrives as a complete Song.
type cand struct {
	key    string
	artist string // NormalizeArtist key; "" when not known before resolving
	id     string // YouTube id
	song   Song   // Bandcamp
}

// run is the working state of one Next.
type run struct {
	left int     // resolves still allowed
	errs []error // lookups that failed
	down [2]bool // sources that failed this time and aren't asked again
}

// Queue is the endless song queue of one discovery station.
type Queue struct {
	seed  string
	yt    YouTube
	bc    Bandcamp
	known Known
	rng   *rand.Rand
	now   func() time.Time

	// The queue is safe for concurrent use, and its two halves are guarded
	// separately so that no lock is ever held across a lookup.
	//
	// The adaptive share lives behind mu, which Skipped, Loved, AnchorShare
	// and Next's start take for a few instructions and never across a
	// network or yt-dlp call. A NEXT or a love arriving while the following
	// song is still resolving therefore never waits for it.
	mu      sync.Mutex
	base    float64
	boost   int  // songs left on the raised share
	quick   int  // quick skips in the current run
	skipped bool // a Skipped call has come since the last Next

	// Everything below belongs to a running Next, and slot lets one run at a
	// time: no-repeat, the last artist, the caches and the Bandcamp gap are
	// all "what happened just before", so two lookups at once would pick the
	// same song and break each promise. It is a channel, not a mutex, so a
	// Next that has to wait gives up when its own context does.
	slot     chan struct{}
	prepared bool
	phrase   string // the YouTube search phrase; "" if the seed gave none
	linkID   string // the video id, when the seed is a YouTube link
	seedFail int
	seedSong *Song // the linked song, played first

	bcProbes []bcProbe
	bcNext   int
	bcTag    string
	bcOut    bool
	bcLast   time.Time
	bcErr    error
	bcCache  map[string]bcEntry
	ytCache  map[string]ytEntry

	recent     []string
	seen       map[string]struct{}
	bad        map[string]struct{} // out of range or unusable: never offered again
	fails      map[string]int
	lastArtist string
	ytFirst    [2]bool // per kind (discovery, anchor): YouTube goes first this time
	memo       map[string]bool
}

// NewQueue makes the queue for one station. seed is what the listener typed:
// a phrase, or a YouTube link. yt may be nil (no yt-dlp), and so may bc and
// known. rng and now are injected so tests are deterministic.
func NewQueue(seed string, yt YouTube, bc Bandcamp, known Known, rng *rand.Rand, now func() time.Time) *Queue {
	if now == nil {
		now = time.Now
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(now().UnixNano()))
	}
	if known == nil {
		known = func(string) bool { return false }
	}
	return &Queue{
		seed:    seed,
		yt:      yt,
		bc:      bc,
		known:   known,
		rng:     rng,
		now:     now,
		base:    anchorStart,
		slot:    make(chan struct{}, 1),
		bcCache: map[string]bcEntry{},
		ytCache: map[string]ytEntry{},
		seen:    map[string]struct{}{},
		bad:     map[string]struct{}{},
		fails:   map[string]int{},
	}
}

// Next returns the next playable song: media already resolved, not played in
// the last 200, and by a different band than the one before.
//
// A source that fails does not fail the station while another can answer. If
// nothing can, the error is the lookup's own (wrapped, so errors.Is sees
// through it) when a lookup failed, and ErrNoSources when every source simply
// had nothing.
//
// Next may be called from any goroutine, alongside Skipped, Loved and
// AnchorShare. Two Next calls run one after the other; the second waits for
// the first unless its own context ends first.
func (q *Queue) Next(ctx context.Context) (Song, error) {
	if err := ctx.Err(); err != nil {
		return Song{}, err
	}
	select {
	case q.slot <- struct{}{}:
		defer func() { <-q.slot }()
	case <-ctx.Done():
		return Song{}, ctx.Err()
	}

	share := q.tick()
	if q.yt == nil && q.bc == nil {
		return Song{}, ErrNoSources
	}
	if err := q.prepare(ctx); err != nil {
		return Song{}, err
	}
	if q.seedSong != nil {
		s := *q.seedSong
		q.seedSong = nil
		return q.serve(s), nil
	}

	q.memo = map[string]bool{}
	r := &run{left: maxResolves}
	anchor := q.rng.Float64() < share
	for _, kind := range [2]bool{anchor, !anchor} {
		if s, ok := q.pick(ctx, kind, r); ok {
			return q.serve(s), nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return Song{}, fmt.Errorf("discover: %w", errors.Join(append(r.errs, err)...))
	}
	if len(r.errs) > 0 {
		return Song{}, fmt.Errorf("discover: %w", errors.Join(r.errs...))
	}
	return Song{}, ErrNoSources
}

// pick tries the sources in turn for one kind of song. The two take turns
// first, so a station is about half and half; Bandcamp goes first on the very
// first ask because it needs no yt-dlp and so answers fastest.
func (q *Queue) pick(ctx context.Context, anchor bool, r *run) (Song, bool) {
	k := 0
	if anchor {
		k = 1
	}
	order := [2]source{srcBC, srcYT}
	if q.ytFirst[k] {
		order = [2]source{srcYT, srcBC}
	}
	for _, src := range order {
		if r.down[src] {
			continue
		}
		var (
			s   Song
			ok  bool
			err error
		)
		if src == srcBC {
			s, ok, err = q.pickBC(ctx, anchor)
		} else {
			s, ok, err = q.pickYT(ctx, anchor, r)
		}
		if err != nil {
			if !errors.Is(err, errPaced) {
				r.errs = append(r.errs, err)
				r.down[src] = true
			}
			continue
		}
		if ok {
			// Whoever just answered waits its turn next time.
			q.ytFirst[k] = src == srcBC
			return s, true
		}
	}
	return Song{}, false
}

// serve records a song as played and hands it back.
func (q *Queue) serve(s Song) Song {
	q.recent = append(q.recent, s.Key)
	q.seen[s.Key] = struct{}{}
	if len(q.recent) > recentSize {
		delete(q.seen, q.recent[0])
		q.recent = q.recent[1:]
	}
	q.lastArtist = core.NormalizeArtist(s.Artist)
	return s
}

// ruledOut reports a key that has played within the window or been judged
// unusable.
func (q *Queue) ruledOut(key string) bool {
	if _, ok := q.seen[key]; ok {
		return true
	}
	_, ok := q.bad[key]
	return ok
}

func (q *Queue) rule(key string) { q.bad[key] = struct{}{} }

// failed counts a lookup that errored. A video that fails twice is dropped:
// the first failure may be a blip, the second is the video.
func (q *Queue) failed(key string) {
	q.fails[key]++
	if q.fails[key] >= 2 {
		q.rule(key)
	}
}

// isNew reports whether a discovery's band is unknown to the listener.
func (q *Queue) isNew(artistKey string) bool {
	return artistKey != "" && !q.isKnown(artistKey)
}

// isKnown asks the Known function at most once per artist per Next: it is a
// database lookup in the real app and choose asks about every candidate.
func (q *Queue) isKnown(artistKey string) bool {
	if q.memo == nil {
		return q.known(artistKey)
	}
	v, ok := q.memo[artistKey]
	if !ok {
		v = q.known(artistKey)
		q.memo[artistKey] = v
	}
	return v
}

// choose picks one candidate at random. For a discovery it picks only from
// the best group present: bands known to be new to the listener, then those
// whose band can't be told yet (a YouTube search row seldom names it), then
// bands already known.
func (q *Queue) choose(cs []cand, discovery bool) int {
	if !discovery {
		return q.rng.Intn(len(cs))
	}
	tier := func(c cand) int {
		switch {
		case c.artist == "":
			return 1
		case q.isKnown(c.artist):
			return 2
		}
		return 0
	}
	best, idx := 3, []int(nil)
	for i, c := range cs {
		switch t := tier(c); {
		case t < best:
			best, idx = t, []int{i}
		case t == best:
			idx = append(idx, i)
		}
	}
	return idx[q.rng.Intn(len(idx))]
}

// prepare does the one-time work a seed needs. A phrase needs none beyond
// being remembered. A YouTube link is resolved once: its tags say which
// Bandcamp tags to try, and the song itself is the first one played.
func (q *Queue) prepare(ctx context.Context) error {
	if q.prepared {
		return nil
	}
	seed := strings.TrimSpace(q.seed)
	id, isLink := VideoID(seed)
	switch {
	case isLink:
		if q.yt == nil {
			// A link can't be started without yt-dlp.
			return ErrNoSources
		}
		q.linkID = id
		// The linked video is played first and then never offered again as
		// one of its own relatives.
		q.rule("yt:" + id)
		m, err := q.yt.Resolve(ctx, watchURL(id))
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			// The first failure may be a blip, so the call fails and the
			// next tries again. The second means the video won't resolve;
			// its Mix can still carry the station, without its tags.
			q.seedFail++
			if q.seedFail < 2 {
				return fmt.Errorf("discover: resolving the linked video: %w", err)
			}
			break
		}
		if s, ok := q.ytSong(id, m, true); ok {
			q.seedSong = &s
		}
		q.probesFromTags(m.Tags)
	case !strings.Contains(seed, "://"):
		// Some other link is not a phrase to search for.
		q.phrase = core.CleanText(seed)
		for _, t := range BandcampTags(seed) {
			q.bcProbes = append(q.bcProbes, bcProbe{tag: t})
		}
	}
	q.prepared = true
	return nil
}

// probesFromTags makes the Bandcamp probes for a linked video from its tags,
// in the order YouTube lists them. The first tag also stands in as the
// YouTube search phrase until Bandcamp says which tag is the real genre.
func (q *Queue) probesFromTags(tags []string) {
	if len(tags) > 8 {
		tags = tags[:8]
	}
	have := map[string]bool{}
	for _, tag := range tags {
		tag = core.CleanText(tag)
		if tag == "" {
			continue
		}
		if q.phrase == "" {
			q.phrase = tag
		}
		for _, t := range BandcampTags(tag) {
			if !have[t] {
				have[t] = true
				q.bcProbes = append(q.bcProbes, bcProbe{tag: t, phrase: tag})
			}
		}
	}
}

// --- the adaptive share ---------------------------------------------------------

// tick settles the share before a roll and returns it. A Next that comes with
// no skip since the last one means the listener let a song play out, which
// ends any run of quick skips; and the raised share ages by one song.
func (q *Queue) tick() float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.skipped {
		q.quick = 0
	}
	q.skipped = false
	share := q.shareLocked()
	if q.boost > 0 {
		q.boost--
	}
	return share
}

func (q *Queue) shareLocked() float64 {
	s := q.base
	if q.boost > 0 {
		s += boostBy
	}
	return min(max(s, anchorMin), anchorMax)
}

// Skipped tells the queue the listener moved on. quick means within a minute
// of the song starting. Three quick skips in a row read as "not this kind of
// thing": the anchor share rises for the next ten songs.
func (q *Queue) Skipped(quick bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.skipped = true
	if !quick {
		q.quick = 0
		return
	}
	q.quick++
	if q.quick >= quickRun {
		q.quick = 0
		q.boost = boostSongs
	}
}

// Loved tells the queue the listener loved a discovery: what it is finding
// is working, so any raised share ends and the base share drops a notch. The
// caller should not call it for an anchor, which says nothing about the
// finds.
func (q *Queue) Loved() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.boost = 0
	q.quick = 0
	q.base = max(q.base-lovedBy, anchorMin)
}

// AnchorShare is the chance the next song is an anchor.
func (q *Queue) AnchorShare() float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.shareLocked()
}
