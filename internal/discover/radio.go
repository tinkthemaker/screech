package discover

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

// Song and artist radio.
//
// A phrase that is a genre ("swedish death metal") makes the station it always
// made: YouTube Music searched for the phrase, Bandcamp walked down the phrase's
// tags. A phrase that is a band or a song ("chelsea wolfe", "engorged in
// putrescence") is not a style to search for, it is a place to start from. The
// station plays that song or band first, and then songs that sound like it:
// what YouTube relates to the song (its Mix), the songs of the tags Bandcamp
// lists the band under, and the occasional famous track of those tags.
//
// The typed phrase is used for the first song and nothing else. Searching it
// again would find more songs with its words in the title, which is the fault
// song radio exists to fix, and dropping its words one at a time to make a tag
// ("putrescence") would be worse.

const (
	// A radio's style is at most this many tags: each one is a request to
	// Bandcamp and a search on YouTube Music, and more would only thin out
	// what the first ones say.
	maxStyleTags = 3

	// How far a radio follows YouTube's Mixes outward: the Mix of at most this
	// many songs by other artists, each found in the seed's own Mix.
	maxHops = 6

	// How many of those other artists are remembered by name, for the style a
	// band with no Bandcamp page borrows from them.
	maxOthers = 12

	// Finding the first song may resolve this many videos, which is a yt-dlp
	// process each, and ask a search for this many results. A song search puts
	// the song itself within the first few; the rest are in case one of them is
	// by someone else, or will not play.
	seedResolves = 4
	seedHits     = 10

	// A radio may spend this many resolves on a song, where a genre station
	// spends maxResolves. Telling who the songs of a Mix are by costs a resolve
	// each, and a Mix is mostly one artist, so finding one who may play can
	// take many. Only the first song of a tune is waited for, and that is the
	// seed, found by seedResolves; the rest are prefetched behind the
	// listener's own lookups.
	radioResolves = 10

	// A Mix that has twice in a row turned out to be an artist who just played
	// has said what it is; the hunt moves to the next source instead of
	// resolving the Mix's other 40 songs by the same band. Only when no source
	// has anything better does it come back and dig.
	radioStreak = 2

	// A band's Bandcamp page is looked up this many times before the station
	// stops asking, a try no sooner than the Bandcamp gap after the last.
	bandTries = 3

	// A band with no Bandcamp page takes its style from the other artists in
	// its Mix once this many are known (or one, after mixLate asks), looking at
	// no more than mixBands of them.
	mixFirst = 3
	mixLate  = 6
	mixBands = 4
)

// radio is what makes a queue song or artist radio: where it started, and what
// it sounds like.
type radio struct {
	seedID   string // the YouTube video of the first song; "" when it came from elsewhere
	seedName string // its artist, as shown
	seedKey  string // core.NormalizeArtist of seedName

	// Where the style comes from, in order: the band Bandcamp matched to the
	// phrase, the band of that name on Bandcamp, the tags a linked video
	// carries, and the other artists found in the seed's Mix.
	band      *SearchBand
	bandName  string // whose Bandcamp page is still to be looked up
	bandDone  bool   // that lookup has an answer, or is given up on
	bandTries int
	bandAt    time.Time
	ownTags   []string

	tags     []string // the style: Bandcamp tags, hyphenated, at most maxStyleTags
	styled   bool     // the style is settled, even if it is empty
	asks     int      // Nexts that have asked for the style
	mixTries int

	// What the YouTube side knows. artistOf is who a video turned out to be by,
	// kept for the life of the station (the media itself goes stale in ten
	// minutes, who made it never does).
	artistOf map[string]string
	mixIDs   map[string]bool // videos listed in the seed's own Mix
	hops     []string        // videos by other artists found there, whose Mixes are crawled
	heard    map[string]bool // those artists
	others   []string        // and their names
}

func newRadio() *radio {
	return &radio{artistOf: map[string]string{}, mixIDs: map[string]bool{}, heard: map[string]bool{}}
}

// setSeed names the seed's artist from the media it resolved to.
func (rd *radio) setSeed(m YTMedia) {
	artist, title := mediaNames(m)
	artist, _ = CleanTitle(artist, title)
	rd.seedName, rd.seedKey = artist, core.NormalizeArtist(artist)
}

// foundSeed is the song that plays first, and what it came from.
type foundSeed struct {
	song  Song
	media YTMedia
	id    string // its YouTube video; "" for a Bandcamp page
}

// --- classifying a typed phrase -------------------------------------------------------

// prepareTyped decides what a typed phrase is and finds the song to start with.
//
// The first Next of a tune has 8 s. Bandcamp's search runs beside the first
// YouTube Music search, capped at 2 s, so that a phrase which is a band or a
// plain song title costs no more than the YouTube lookup; the song itself is
// then at most a few resolves away.
//
// Each stage keeps what it learned, so a Next that runs out of time or finds
// the network down is retried from where it stopped and does not ask the same
// question twice.
func (q *Queue) prepareTyped(ctx context.Context, seed string) error {
	phrase := core.CleanText(seed)

	var cl classification
	if q.cls != nil {
		cl = *q.cls
	} else {
		var keep bool
		var err error
		if cl, keep, err = q.classifyPhrase(ctx, phrase); err != nil {
			return err
		}
		if keep {
			q.cls = &cl
		}
	}
	if cl.step == stepGenre {
		q.genrePhrase(seed)
		return nil
	}

	var found *foundSeed
	if q.yt != nil {
		var err error
		if found, err = q.findSeed(ctx, phrase, cl); err != nil {
			if ctx.Err() != nil || isNetworkError(err) {
				return err
			}
			// The first failure may be a blip, so the call fails and the next
			// tries again. The second means the first song cannot be had; the
			// station can still start from what the phrase is.
			q.seedFail++
			if q.seedFail < 2 {
				return fmt.Errorf("discover: finding the first song: %w", err)
			}
			found = nil
		}
	}
	if found == nil && cl.step == stepTopHit {
		// Nothing on Bandcamp and nothing to play: the phrase may still be a
		// genre Bandcamp has no tag for, which is how a station treats a
		// phrase it can make nothing else of.
		q.genrePhrase(seed)
		return nil
	}
	q.beginTypedRadio(cl, found)
	return nil
}

// classifyPhrase asks Bandcamp what the phrase is. keep reports whether the
// answer is worth keeping for a retry: one made while the network was down is
// not, it says nothing about the phrase.
func (q *Queue) classifyPhrase(ctx context.Context, phrase string) (cl classification, keep bool, err error) {
	type lookup struct {
		hits []YTHit
		err  error
	}
	var ahead chan lookup
	yctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if q.yt != nil {
		// Whatever the phrase turns out to be, a search of YouTube Music for
		// it is the next thing needed unless it names a song, and it takes
		// seconds. Nothing but the answer comes back across the channel; the
		// queue's own state is touched only after the join.
		ahead = make(chan lookup, 1)
		go func() {
			hits, err := q.yt.Songs(yctx, phrase, searchSize)
			ahead <- lookup{hits, err}
		}()
	}
	join := func() (lookup, bool) {
		if ahead == nil {
			return lookup{}, false
		}
		return <-ahead, true
	}

	sctx, stop := q.bcContext(ctx, true, false)
	res, serr := q.searcher.Search(sctx, phrase)
	stop()
	if serr != nil && ctx.Err() != nil {
		// The caller gave up; that is no verdict on the phrase.
		cancel()
		join()
		return classification{}, false, ctx.Err()
	}

	cl = classify(phrase, res, serr == nil)
	if cl.step == stepSong {
		// A song is found by its band's name, not by its own: the search for
		// the phrase has nothing to give, and is stopped so it does not hold
		// the yt-dlp turn the band's search needs.
		cancel()
	}
	if l, ok := join(); ok && l.err == nil && cl.step != stepSong {
		q.ytCache[phrase] = ytEntry{hits: l.hits, at: q.now(), asked: searchSize}
	}
	return cl, serr == nil || !isNetworkError(serr), nil
}

// findSeed finds the song that plays first. A band's is its top song on
// YouTube Music; a song's is that song, by its band, and failing that the
// Bandcamp page; a top hit's is the top hit. Every candidate is resolved before
// it is believed: a search row says nothing about who it is by.
func (q *Queue) findSeed(ctx context.Context, phrase string, cl classification) (*foundSeed, error) {
	r := &run{left: seedResolves}
	switch cl.step {
	case stepArtist:
		hits, err := q.search(ctx, phrase, searchSize)
		if err != nil {
			return nil, err
		}
		return q.seedFrom(ctx, r, hits, cl.band.Name, "")
	case stepSong:
		t := cl.track
		hits, err := q.search(ctx, t.Band+" "+t.Name, seedHits)
		if err != nil {
			return nil, err
		}
		// The hit must be the song and be by the band: "Ingurgitating
		// Putrescence" is not "Engorged in Putrescence", and the band's other
		// songs are not the song typed.
		if f, err := q.seedFrom(ctx, r, hits, t.Band, t.Name); f != nil || err != nil {
			return f, err
		}
		return q.seedFromPage(ctx, t)
	}
	hits, err := q.search(ctx, phrase, searchSize)
	if err != nil {
		return nil, err
	}
	return q.seedFrom(ctx, r, hits, "", "")
}

// seedFrom resolves hits in order until one is a song that plays, by artist
// when one is named and with title in its name when one is. nil, nil means none
// was. The error is the context's or the network's as soon as it happens, or,
// when every resolve failed and not one succeeded, the last of them.
func (q *Queue) seedFrom(ctx context.Context, r *run, hits []YTHit, artist, title string) (*foundSeed, error) {
	titleKey := matchKey(title)
	if title == "" {
		titleKey = ""
	}
	var lastErr error
	resolved := false
	for _, h := range hits {
		if r.left <= 0 {
			break
		}
		key := "yt:" + h.ID
		if h.ID == "" || q.ruledOut(key) {
			continue
		}
		if titleKey != "" && h.Title != "" && !strings.Contains(matchKey(h.Title), titleKey) {
			continue
		}
		m, at, err := q.resolve(ctx, cand{key: key, id: h.ID}, r)
		if err != nil {
			if ctx.Err() != nil || isNetworkError(err) {
				return nil, err
			}
			q.failed(key)
			lastErr = err
			continue
		}
		resolved = true
		s, ok := q.songFrom(key, m, true, at)
		if !ok {
			q.rule(key)
			continue
		}
		if artist != "" && !sameArtist(m, artist) {
			continue
		}
		return &foundSeed{song: s, media: m, id: h.ID}, nil
	}
	if !resolved && lastErr != nil {
		return nil, lastErr
	}
	return nil, nil
}

// seedFromPage plays a track from its Bandcamp page, through yt-dlp, which
// resolves any page it has an extractor for. It is where a song YouTube Music
// does not have comes from. A page that will not play is no failure of the
// station.
func (q *Queue) seedFromPage(ctx context.Context, t *SearchTrack) (*foundSeed, error) {
	if t.URL == "" {
		return nil, nil
	}
	m, err := q.yt.Resolve(ctx, t.URL)
	if err != nil {
		if ctx.Err() != nil || isNetworkError(err) {
			return nil, err
		}
		return nil, nil
	}
	// Bandcamp has just said whose song this is; yt-dlp's fields are the
	// fallback, not the other way around.
	m.Artist, m.Track = t.Band, t.Name
	key := pageKey(t)
	s, ok := q.songFrom(key, m, true, q.now())
	if !ok {
		return nil, nil
	}
	return &foundSeed{song: s, media: m}, nil
}

// pageKey is the no-repeat key of a track played from its page: Bandcamp's id
// for it when the feed would use the same one, and otherwise a hash of its
// address.
func pageKey(t *SearchTrack) string {
	if t.ID != 0 && !t.Album {
		return fmt.Sprintf("bc:%d", t.ID)
	}
	h := fnv.New64a()
	if u, err := url.Parse(t.URL); err == nil {
		_, _ = h.Write([]byte(u.Host + u.Path))
	}
	return fmt.Sprintf("bc:p%x", h.Sum64())
}

// beginTypedRadio makes the queue song or artist radio for a classified
// phrase, with found as the song that plays first (nil if there is none).
func (q *Queue) beginTypedRadio(cl classification, f *foundSeed) {
	rd := newRadio()
	switch cl.step {
	case stepArtist:
		rd.band, rd.bandName, rd.seedName = cl.band, cl.band.Name, cl.band.Name
	case stepSong:
		rd.bandName = cl.track.Band
	}
	if f != nil {
		rd.seedID = f.id
		rd.setSeed(f.media)
		if cl.step == stepTopHit {
			// The artist is whoever the top hit turned out to be by.
			rd.bandName = rd.seedName
		}
		s := f.song
		q.seedSong = &s
	}
	if rd.seedKey == "" {
		rd.seedKey = core.NormalizeArtist(rd.seedName)
	}
	if rd.seedID != "" && rd.seedKey != "" {
		rd.artistOf[rd.seedID] = rd.seedKey
	}
	q.radio = rd
}

// beginLinkRadio makes the queue radio for a pasted video. m is what the video
// resolved to, nil if it would not.
func (q *Queue) beginLinkRadio(id string, m *YTMedia) {
	rd := newRadio()
	rd.seedID = id
	if m != nil {
		rd.setSeed(*m)
		rd.bandName = rd.seedName
		rd.ownTags = linkTags(m.Tags, rd.seedName)
	}
	if rd.seedKey != "" {
		rd.artistOf[id] = rd.seedKey
	}
	q.radio = rd
}

// notStyles are the tags an uploader puts on a video that say what kind of
// upload it is and nothing about how it sounds.
var notStyles = map[string]bool{
	"music": true, "song": true, "songs": true, "audio": true, "video": true,
	"official": true, "lyrics": true, "lyric": true, "hd": true, "new": true,
}

// linkTags are a video's own tags as a style: what it says about the sound,
// less its own artist's name and the labels of its upload.
func linkTags(tags []string, artist string) []string {
	if len(tags) > 8 {
		tags = tags[:8]
	}
	own := normTag(artist)
	var out []string
	have := map[string]bool{}
	for _, t := range tags {
		n := normTag(core.CleanText(t))
		if n == "" || n == own || notStyles[n] || have[n] {
			continue
		}
		have[n] = true
		out = append(out, n)
		if len(out) == maxStyleTags {
			break
		}
	}
	return out
}

// sameArtist reports whether a resolved video is by band. YouTube lists
// several artists in one field ("Decarnate, Organectomy"), a Topic channel
// stands in for an artist the metadata does not name, and a name is compared
// as matchKey compares them.
func sameArtist(m YTMedia, band string) bool {
	want := matchKey(band)
	artist, title := mediaNames(m)
	artist, _ = CleanTitle(artist, title)
	if matchKey(artist) == want {
		return true
	}
	for _, part := range artistSplit.Split(artist, -1) {
		if matchKey(part) == want {
			return true
		}
	}
	return false
}

// artistSplit divides a list of artists: commas, ampersands, "and", "x" and
// the ways of crediting a guest.
var artistSplit = regexp.MustCompile(`(?i)\s*(?:,|;|&|/|\s+and\s+|\s+x\s+|\s+(?:feat\.?|ft\.?|featuring|with)\s+)\s*`)

// --- a radio's style ---------------------------------------------------------------------

// ensureStyle settles what a radio sounds like, as far as it can be told now.
// It runs at the start of every Next until it has, and a Next that cannot
// settle it plays on from the Mix, which needs no style. A failure of Bandcamp
// here costs the style (retried a few times, the Bandcamp gap apart), never a
// song.
func (q *Queue) ensureStyle(ctx context.Context) {
	rd := q.radio
	if rd == nil || rd.styled || ctx.Err() != nil {
		return
	}
	rd.asks++

	if rd.band != nil {
		if tags := styleTags(*rd.band); len(tags) > 0 {
			rd.settle(tags)
			return
		}
		rd.bandDone = true
	}
	if rd.bandName != "" && !rd.bandDone {
		if rd.bandTries > 0 && q.now().Sub(rd.bandAt) < bcGap {
			return
		}
		band, err := q.bandFor(ctx, rd.bandName)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			rd.bandTries++
			rd.bandAt = q.now()
			if len(rd.ownTags) == 0 && rd.bandTries < bandTries {
				return
			}
			rd.bandDone = true
		default:
			rd.bandDone = true
			if band != nil {
				rd.band = band
				if tags := styleTags(*band); len(tags) > 0 {
					rd.settle(tags)
					return
				}
			}
		}
	}
	if len(rd.ownTags) > 0 {
		rd.settle(rd.ownTags)
		return
	}
	q.styleFromMix(ctx)
}

func (rd *radio) settle(tags []string) {
	if len(tags) > maxStyleTags {
		tags = tags[:maxStyleTags]
	}
	rd.tags, rd.styled = tags, true
}

// bandFor looks for the Bandcamp band of that name. nil, nil means there is
// none, and an error that Bandcamp could not say.
func (q *Queue) bandFor(ctx context.Context, name string) (*SearchBand, error) {
	sctx, stop := q.bcContext(ctx, true, false)
	defer stop()
	res, err := q.searcher.Search(sctx, name)
	if err != nil {
		return nil, err
	}
	key := matchKey(name)
	for i := range res.Bands {
		if matchKey(res.Bands[i].Name) == key {
			return &res.Bands[i], nil
		}
	}
	return nil, nil
}

// styleFromMix is the style of an artist with no Bandcamp page: the three tags
// most common among the other artists found in the seed's Mix. It waits until
// there are enough of them to say something, and it tries twice at most.
func (q *Queue) styleFromMix(ctx context.Context) {
	rd := q.radio
	if rd.mixTries >= 2 || len(rd.others) < mixFirst && !(len(rd.others) > 0 && rd.asks >= mixLate) {
		return
	}
	counts := map[string]int{}
	var order []string
	for _, name := range rd.others[:min(len(rd.others), mixBands)] {
		band, err := q.bandFor(ctx, name)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			break
		}
		if band == nil {
			continue
		}
		for _, t := range styleTags(*band) {
			if counts[t] == 0 {
				order = append(order, t)
			}
			counts[t]++
		}
	}
	rd.mixTries++
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	if len(order) > 0 || rd.mixTries >= 2 {
		rd.settle(order)
	}
}

// --- the YouTube side ---------------------------------------------------------------------

// noteResolved learns from a video that was just resolved. Whose it is is
// remembered; and if it is by an artist other than the seed's and was listed in
// the seed's Mix, its own Mix is worth crawling, because that is how a station
// leaves the seed's band.
func (q *Queue) noteResolved(id string, m YTMedia) {
	rd := q.radio
	if rd == nil {
		return
	}
	artist, title := mediaNames(m)
	artist, _ = CleanTitle(artist, title)
	key := core.NormalizeArtist(artist)
	if key == "" {
		return
	}
	rd.artistOf[id] = key
	if !rd.mixIDs[id] || key == rd.seedKey || rd.heard[key] {
		return
	}
	rd.heard[key] = true
	if len(rd.others) < maxOthers {
		rd.others = append(rd.others, artist)
	}
	if len(rd.hops) < maxHops {
		rd.hops = append(rd.hops, id)
	}
}

// ytGroup is a place the YouTube side of a radio gets songs: the Mix of a
// video, or a search of YouTube Music for one of the style's tags.
type ytGroup struct {
	mix   bool
	id    string // a Mix: the video it is built around
	words string // a search: what is searched for
}

// radioGroups lists the places to look, in the order to look. Anchors are the
// style's famous songs, so only the tag searches; discoveries come from those
// and from the Mixes. Each group is as likely to be first as another: a Mix of
// fifty songs holds no more of the station than a search of fifty, and a
// Mix is mostly one artist.
func (q *Queue) radioGroups(anchor bool) []ytGroup {
	rd := q.radio
	var gs []ytGroup
	for _, t := range rd.tags {
		gs = append(gs, ytGroup{words: strings.ReplaceAll(t, "-", " ")})
	}
	if !anchor {
		if rd.seedID != "" {
			gs = append(gs, ytGroup{mix: true, id: rd.seedID})
		}
		for _, id := range rd.hops {
			gs = append(gs, ytGroup{mix: true, id: id})
		}
	}
	q.rng.Shuffle(len(gs), func(i, j int) { gs[i], gs[j] = gs[j], gs[i] })
	return gs
}

// groupCands lists what a group has to play now.
func (q *Queue) groupCands(ctx context.Context, g ytGroup, anchor bool) ([]cand, error) {
	switch {
	case g.mix:
		hits, err := q.mix(ctx, g.id)
		if err != nil {
			return nil, err
		}
		if g.id == q.radio.seedID {
			for _, h := range hits {
				q.radio.mixIDs[h.ID] = true
			}
		}
		return q.ytFilter(hits), nil
	case anchor:
		hits, err := q.search(ctx, g.words, anchorHits)
		if err != nil {
			return nil, err
		}
		return q.ytFilter(hits), nil
	}
	return q.searchStages(ctx, g.words)
}

// pickRadioYT is pickYT for a radio: it tries each group in turn until one has a
// song. A group that fails does not fail the rest, only the song if no other
// group has one.
func (q *Queue) pickRadioYT(ctx context.Context, anchor bool, r *run) (Song, bool, error) {
	var firstErr error
	// The second pass has no streak limit: if the groups tried one after the
	// other have nothing, the song is in whichever of them has the most still
	// to resolve.
	for _, streak := range [2]int{radioStreak, 0} {
		for _, g := range q.radioGroups(anchor) {
			if ctx.Err() != nil {
				return Song{}, false, firstErr
			}
			cands, err := q.groupCands(ctx, g, anchor)
			if err != nil {
				firstErr = errors.Join(firstErr, err)
				continue
			}
			if len(cands) == 0 {
				continue
			}
			if s, ok := q.pickFrom(ctx, cands, anchor, r, streak); ok {
				return s, true, nil
			}
			if r.left <= 0 {
				return Song{}, false, firstErr
			}
		}
	}
	return Song{}, false, firstErr
}

// resolveBudget is how many videos one Next may resolve.
func (q *Queue) resolveBudget() int {
	if q.radio != nil {
		return radioResolves
	}
	return maxResolves
}

// pickBCRadio is pickBC for a radio: the songs of one of the style's tags,
// taking the tags in turn from a random place so that each is heard.
func (q *Queue) pickBCRadio(ctx context.Context, anchor bool) (Song, bool, error) {
	tags := q.radio.tags
	if len(tags) == 0 {
		return Song{}, false, nil
	}
	slices := []string{"new", "rand"}
	if anchor {
		slices = []string{"top"}
	}
	start := q.rng.Intn(len(tags))
	var firstErr error
	for i := range tags {
		cands, err := q.bcCandidates(ctx, tags[(start+i)%len(tags)], slices)
		if len(cands) > 0 {
			return q.bcChoose(cands, anchor), true, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return Song{}, false, firstErr
}
