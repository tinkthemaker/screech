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
// what YouTube relates to the song (its Mix, and the Mixes of the songs by
// other artists found in it), the songs of the tags Bandcamp lists the band and
// its neighbours under, and the occasional famous track of those tags. The Mix
// leads: YouTube Music searched for a tag is whatever it ranks for the words,
// playlist farms included, so that is where a station looks only once the Mixes
// have nothing it can play.
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

	// How many of those other artists are remembered by name, for the style
	// they lend the seed.
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
	// take more. It is bounded by time too: the player gives the lookup of the
	// next song 20 s, and a resolve is a few seconds of yt-dlp. Only the first
	// song of a tune is waited for more briefly, and that is the seed, found
	// by seedResolves.
	radioResolves = 8

	// A Mix that has twice in a row turned out to be an artist who just played
	// has said what it is; the hunt moves to the next source instead of
	// resolving the Mix's other 40 songs by the same band. Only when no source
	// has anything better does it come back and dig.
	radioStreak = 2

	// Looking through the Mixes leaves this many resolves for the style's
	// searches, and digging through them leaves the smaller reserve, so that a
	// Mix that is all one band does not use up the Next that would have found a
	// song elsewhere. (There can be a Mix for each hop, and each is allowed its
	// streak.)
	radioProbe   = 5
	radioReserve = 3

	// A band's Bandcamp page is looked up this many times before the station
	// stops asking, a try no sooner than the Bandcamp gap after the last.
	bandTries = 3

	// The style is the seed's tags and those of the other artists found in its
	// Mix (see refineStyle). It waits until this many are known, or one after
	// mixLate asks, and looks at no more than mixBands of them. Each is a
	// Bandcamp search, and none is made while the first song is waited for.
	mixFirst = 3
	mixLate  = 10
	mixBands = 3

	// The Bandcamp searches that settle the style take up to 2 s each. A Next
	// with a deadline closer than this leaves them for one with more time (the
	// player gives the song after the current one 20 s, and the first song of
	// a tune 8), since its song is what is being waited for.
	styleRoom = 15 * time.Second
)

// radio is what makes a queue song or artist radio: where it started, and what
// it sounds like.
type radio struct {
	seedID   string // the YouTube video of the first song; "" when it came from elsewhere
	seedName string // its artist, as shown
	seedKey  string // core.NormalizeArtist of seedName

	// Where the style comes from: the band Bandcamp matched to the phrase, or
	// the band of that name on Bandcamp, or the tags a linked video carries,
	// first; and then, to say what that band sounds like in company, the tags
	// of the other artists found in the seed's Mix.
	band      *SearchBand
	bandName  string // whose Bandcamp page is still to be looked up
	bandDone  bool   // that lookup has an answer, or is given up on
	bandTries int
	bandAt    time.Time
	ownTags   []string

	tags    []string // the style: Bandcamp tags, hyphenated, at most maxStyleTags
	styled  bool     // there is a style, if only the seed's own
	refined bool     // the neighbours have been asked, or there is nothing more to learn
	asks    int      // Nexts that have asked for the style

	mixTries int
	mixAt    time.Time

	// What the YouTube side knows. artistOf is who a video turned out to be by,
	// kept for the life of the station (the media itself goes stale in ten
	// minutes, who made it never does).
	artistOf map[string]string
	mixOf    map[string]string  // video -> the Mix (named by its video) that first listed it
	stat     map[string]mixStat // Mix -> what its resolved songs have turned out to be
	hops     []string           // videos by other artists found in the seed's Mix, whose Mixes are crawled
	heard    map[string]bool    // those artists
	others   []string           // and their names
}

// mixStat is what is known of how much of a Mix is the artist it is built
// around: of n songs resolved, own were by that artist.
type mixStat struct{ n, own int }

func newRadio() *radio {
	return &radio{
		artistOf: map[string]string{}, mixOf: map[string]string{}, stat: map[string]mixStat{},
		heard: map[string]bool{},
	}
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
		if titleKey != "" && h.Title != "" && !containsName(h.Title, title) {
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
//
// The style is settled twice. First comes the seed's own: its band's tags from
// the answer that classified it or a search for it, or failing both a linked
// video's tags. That is a thin description, though (Chelsea Wolfe lists
// Alternative, Folk and one long odd tag), so once the Next calls that follow
// have turned up some of the other artists in the seed's Mix, the style is
// worked out again from the seed and up to mixBands of them.
func (q *Queue) ensureStyle(ctx context.Context) {
	rd := q.radio
	if rd == nil || ctx.Err() != nil || rd.styled && rd.refined {
		return
	}
	rd.asks++
	if !rd.styled {
		q.seedStyle(ctx)
	}
	q.refineStyle(ctx)
}

// hasRoom reports whether ctx leaves time for the Bandcamp searches that settle
// a style: it has no deadline, or one at least styleRoom away.
func hasRoom(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) >= styleRoom
}

// seedStyle is the style of the seed alone.
func (q *Queue) seedStyle(ctx context.Context) {
	rd := q.radio
	if rd.band != nil {
		// Bandcamp has already said whose band this is: nothing to look up.
		rd.bandDone = true
		if tags := styleTags(*rd.band); len(tags) > 0 {
			rd.settle(tags)
			return
		}
	}
	if rd.bandName != "" && !rd.bandDone {
		if !hasRoom(ctx) || rd.bandTries > 0 && q.now().Sub(rd.bandAt) < bcGap {
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
		rd.settle(styleFrom([]SearchBand{{Tags: rd.ownTags}}))
	}
}

// refineStyle works the style out again from the seed and the other artists
// found in its Mix: a Bandcamp search for each (the first mixBands), and the
// tags ranked by how many of the bands carry them. It waits until there are
// enough of them to say something, and tries twice at most. A seed with no
// style of its own (an artist with no Bandcamp page) has only them.
func (q *Queue) refineStyle(ctx context.Context) {
	rd := q.radio
	if rd.refined || rd.bandName != "" && !rd.bandDone {
		return
	}
	n := len(rd.others)
	if n < mixFirst && !(n > 0 && rd.asks >= mixLate) || !hasRoom(ctx) {
		return
	}
	if rd.mixTries > 0 && q.now().Sub(rd.mixAt) < bcGap {
		return
	}

	var bands []SearchBand
	switch {
	case rd.band != nil:
		bands = append(bands, *rd.band)
	case len(rd.ownTags) > 0:
		bands = append(bands, SearchBand{Tags: rd.ownTags})
	}
	failed := false
	for _, name := range rd.others[:min(n, mixBands)] {
		band, err := q.bandFor(ctx, name)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failed = true
			break
		}
		if band != nil {
			bands = append(bands, *band)
		}
	}
	rd.mixTries++
	rd.mixAt = q.now()
	if failed && rd.mixTries < 2 {
		return
	}
	rd.refined, rd.styled = true, true
	if tags := styleFrom(bands); len(tags) > 0 && !failed {
		rd.settle(tags)
	}
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
	mix, listed := rd.mixOf[id]
	if listed {
		st := rd.stat[mix]
		st.n++
		if key == rd.artistOf[mix] {
			st.own++
		}
		rd.stat[mix] = st
	}
	if !listed || mix != rd.seedID || key == rd.seedKey || rd.heard[key] {
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

// mixGroups lists the Mixes to look in: the seed's, and those of the songs by
// other artists found in it. A Mix is mostly the artist it is built around (45
// of 50 for a Topic track), so one whose artist has played too lately for a
// song of theirs to play is a poor place to look, and the more of what has been
// resolved from it was that artist's, the poorer. Those come last; the rest
// are in a random order, since each is as likely to be good as another.
func (q *Queue) mixGroups() []ytGroup {
	rd := q.radio
	var gs []ytGroup
	if rd.seedID != "" {
		gs = append(gs, ytGroup{mix: true, id: rd.seedID})
	}
	for _, id := range rd.hops {
		gs = append(gs, ytGroup{mix: true, id: id})
	}
	q.rng.Shuffle(len(gs), func(i, j int) { gs[i], gs[j] = gs[j], gs[i] })
	// Cost: 0 when the artist the Mix is built around may play, and otherwise
	// 1 plus the share of what has been resolved from it that was theirs (an
	// unknown share is taken for half).
	cost := func(g ytGroup) float64 {
		owner := rd.artistOf[g.id]
		if !q.blocked(owner) {
			return 0
		}
		st := rd.stat[g.id]
		if st.n < 2 {
			return 1.5
		}
		return 1 + float64(st.own)/float64(st.n)
	}
	sort.SliceStable(gs, func(i, j int) bool { return cost(gs[i]) < cost(gs[j]) })
	return gs
}

// tagGroups lists the style's tags as YouTube Music searches, in a random
// order. A generic genre is left out: a search for "folk" returns whatever
// playlist farm has the word in its name, which is the one thing a station
// that knows better should not play.
func (q *Queue) tagGroups() []ytGroup {
	var gs []ytGroup
	for _, t := range q.radio.tags {
		if !genericTag(t) {
			gs = append(gs, ytGroup{words: strings.ReplaceAll(t, "-", " ")})
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
		for _, h := range hits {
			if _, ok := q.radio.mixOf[h.ID]; !ok {
				q.radio.mixOf[h.ID] = g.id
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

// mixHasSongs reports whether any of the Mixes still has a song that could be
// resolved and tried: not played, not ruled out, and not known to be by an
// artist the spacing rule holds back. A Mix that cannot be fetched has none.
func (q *Queue) mixHasSongs(ctx context.Context) bool {
	rd := q.radio
	ids := append([]string(nil), rd.hops...)
	if rd.seedID != "" {
		ids = append([]string{rd.seedID}, ids...)
	}
	for _, id := range ids {
		cands, err := q.groupCands(ctx, ytGroup{mix: true, id: id}, false)
		if err == nil && len(cands) > 0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

// rung is one step of the hunt for a radio's next YouTube song: which places to
// look in, how many blocked songs in a row end the look in one place (0 never),
// and how many of the run's resolves it leaves for the steps after it.
type rung struct {
	mix           bool
	streak, floor int
}

// discoveryLadder is the order a discovery is hunted in. The Mixes lead. The
// searches of the style come only when the Mixes have given nothing: twice over
// a Mix said it is all one artist, and then the Mixes were dug through as far
// as the reserve allows. What is left of the budget goes back to the Mixes,
// then to the searches without the streak rule.
var discoveryLadder = []rung{
	{mix: true, streak: radioStreak, floor: radioProbe},
	{mix: true, floor: radioReserve},
	{streak: radioStreak},
	{mix: true},
	{},
}

// anchorLadder is an anchor's: the famous songs of the style, which are
// searches and only searches.
var anchorLadder = []rung{{streak: radioStreak}, {}}

// pickRadioYT is pickYT for a radio. It works down a ladder of rungs, each a
// walk over the places to look until one has a song. A place that fails does
// not fail the rest, only the song if no other has one.
func (q *Queue) pickRadioYT(ctx context.Context, anchor bool, r *run) (Song, bool, error) {
	var firstErr error
	ladder := discoveryLadder
	if anchor {
		// The famous songs of the style are a search, and a search waits until
		// the Mixes have nothing to play. Until then an anchor is Bandcamp's top
		// sellers, or a discovery that turns out to be well known.
		if q.mixHasSongs(ctx) {
			return Song{}, false, nil
		}
		ladder = anchorLadder
	}
	for _, rg := range ladder {
		groups := q.tagGroups()
		if rg.mix {
			groups = q.mixGroups()
		}
		for _, g := range groups {
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
			if s, ok := q.pickFrom(ctx, cands, anchor, r, rg.streak, rg.floor); ok {
				return s, true, nil
			}
			if r.left <= rg.floor {
				break
			}
		}
		if r.left <= 0 {
			break
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
