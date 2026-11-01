package discover

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/tinkthemaker/screech/internal/core"
)

// What a typed phrase is. Bandcamp's own search knows the difference between a
// band, a song and a tag better than a list of words does, so the queue asks
// it before deciding what a station is for; see classify.

// SearchResult is what Bandcamp's search made of a phrase.
type SearchResult struct {
	Tags   []string      // norm_names of exact-or-near tag matches, best first
	Bands  []SearchBand  // results of type "b", best first
	Tracks []SearchTrack // results of type "t" and "a", best first
}

// SearchBand is a band Bandcamp knows.
type SearchBand struct {
	Name, Genre, URL string
	Location         string   // "Christchurch, New Zealand": a place is not a style
	Tags             []string // as the band lists them: "Death Metal", "Slam"
}

// SearchTrack is a track, or an album, Bandcamp knows.
type SearchTrack struct {
	Name, Band, URL string // URL is its Bandcamp page, "" when the answer gave none worth following
	Album           bool
	ID              int64 // Bandcamp's id; 0 when the answer gave none or it is not a number
}

// Searcher is Bandcamp's search as the queue uses it. BandcampClient is one; a
// Bandcamp that is not (a test's fake) means the queue has nothing to classify
// a phrase with, and a phrase is a genre as it was before song radio.
type Searcher interface {
	Search(ctx context.Context, text string) (SearchResult, error)
}

// defaultSearchEndpoint is the autocomplete behind bandcamp.com's search box.
// Like the discover feed it is undocumented, and a failure of it costs the
// station its classification, never the station.
const defaultSearchEndpoint = "https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic"

const (
	// A search answers with a couple of dozen rows; the queue looks at the top
	// of them. The caps bound what a hostile answer can make us keep.
	maxSearchRows = 30
	maxRowTags    = 12
)

type bcSearchRequest struct {
	SearchText string `json:"search_text"`
	// SearchFilter is always empty: "b" returned nothing for a band that ""
	// found, so the queue asks for everything and picks by each row's type.
	SearchFilter string `json:"search_filter"`
	FullPage     bool   `json:"full_page"`
	FanID        *int64 `json:"fan_id"` // always null: the search is not personal
}

type bcSearchRow struct {
	Type        string          `json:"type"`
	ID          json.RawMessage `json:"id"`
	Name        string          `json:"name"`
	BandName    string          `json:"band_name"`
	GenreName   string          `json:"genre_name"`
	Location    string          `json:"location"`
	TagNames    []string        `json:"tag_names"`
	ItemURLRoot string          `json:"item_url_root"`
	ItemURLPath string          `json:"item_url_path"`
}

type bcSearchResponse struct {
	Auto struct {
		Results []bcSearchRow `json:"results"`
	} `json:"auto"`
	Tag struct {
		Matches []struct {
			NormName string `json:"norm_name"`
		} `json:"matches"`
	} `json:"tag"`
}

// Search asks Bandcamp what text is: bands, songs and albums by that name, and
// the tags that look like it. Everything in the answer is network text and
// passes through core.CleanText, and a link is kept only if it is a page on
// bandcamp.com, since yt-dlp may be asked to open it.
func (c BandcampClient) Search(ctx context.Context, text string) (SearchResult, error) {
	body, err := json.Marshal(bcSearchRequest{SearchText: text})
	if err != nil {
		return SearchResult{}, err
	}
	endpoint := c.SearchEndpoint
	if endpoint == "" {
		endpoint = defaultSearchEndpoint
	}
	var out bcSearchResponse
	if err := c.post(ctx, endpoint, body, &out); err != nil {
		return SearchResult{}, err
	}
	return out.result(), nil
}

func (r bcSearchResponse) result() SearchResult {
	var res SearchResult
	for _, m := range r.Tag.Matches {
		if n := strings.ToLower(strings.TrimSpace(core.CleanText(m.NormName))); n != "" {
			res.Tags = append(res.Tags, n)
		}
	}
	for i, row := range r.Auto.Results {
		if i >= maxSearchRows {
			break
		}
		switch row.Type {
		case "b":
			res.Bands = append(res.Bands, SearchBand{
				Name:     core.CleanText(row.Name),
				Genre:    core.CleanText(row.GenreName),
				Location: core.CleanText(row.Location),
				URL:      bandcampPage(row.ItemURLRoot),
				Tags:     cleanAll(row.TagNames),
			})
		case "t", "a":
			album := row.Type == "a"
			t := SearchTrack{
				Name:  core.CleanText(row.Name),
				Band:  core.CleanText(row.BandName),
				URL:   trackPage(row.ItemURLRoot, row.ItemURLPath),
				Album: album,
			}
			if n, err := strconv.ParseInt(strings.Trim(string(row.ID), `" `), 10, 64); err == nil {
				t.ID = n
			}
			res.Tracks = append(res.Tracks, t)
		}
	}
	return res
}

func cleanAll(in []string) []string {
	if len(in) > maxRowTags {
		in = in[:maxRowTags]
	}
	var out []string
	for _, s := range in {
		if s = core.CleanText(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// bandcampPage returns u if it is an https page on bandcamp.com, and "" if it
// is anything else. Custom domains lose the Bandcamp-page fallback by this; the
// alternative is handing yt-dlp whatever address a response names.
func bandcampPage(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || p.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(p.Hostname())
	if host != "bandcamp.com" && !strings.HasSuffix(host, ".bandcamp.com") {
		return ""
	}
	return p.String()
}

// trackPage is the page of a track or album: the row's path, which Bandcamp
// gives whole or relative to the band's root. A bare root is a band's front
// page, not a song.
func trackPage(root, path string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return ""
	case strings.Contains(path, "://"):
		return bandcampPage(path)
	}
	return bandcampPage(strings.TrimRight(strings.TrimSpace(root), "/") + "/" + strings.TrimLeft(path, "/"))
}

// --- names and classes -----------------------------------------------------------------

// matchKey is a name as it is compared: lower case, every run of anything but
// letters and digits one space, so that words keep their boundaries.
// "Chelsea  Wolfe!" and "CHELSEA WOLFE" are one name; "SwedishDeathMetal" and
// "swedish death metal" are not, and live, a downtempo track of the first name
// was taken for the second. An apostrophe joins and does not divide ("Don't"
// is "dont"), and a combining accent belongs to its letter.
func matchKey(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case unicode.IsMark(r):
			b.WriteRune(r)
		case r == '\'' || r == '’' || r == '`':
			// An apostrophe joins; see above.
		default:
			space = true
		}
	}
	if b.Len() == 0 {
		// A name made only of symbols ("!!!" is a band) still names something.
		return strings.ToLower(strings.Join(strings.Fields(s), ""))
	}
	return b.String()
}

// containsName reports whether name appears in s as whole words: "engorged in
// putrescence (official audio)" has "engorged in putrescence" in it, "reengorged
// in putrescence" does not.
func containsName(s, name string) bool {
	return strings.Contains(" "+matchKey(s)+" ", " "+matchKey(name)+" ")
}

// normTag is Bandcamp's spelling of a tag: lower case, every run of anything
// but letters and digits one hyphen. "Drum & Bass" is drum-bass.
func normTag(s string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sep && b.Len() > 0 {
				b.WriteByte('-')
			}
			sep = false
			b.WriteRune(r)
		} else {
			sep = true
		}
	}
	return b.String()
}

// The four things a typed phrase can be, in the order they are tried.
const (
	stepArtist = iota + 1 // a band by that name: artist radio
	stepGenre             // a tag by that name: a genre station, as it always was
	stepSong              // a song or album by that name: song radio
	stepTopHit            // none of them: song radio from YouTube Music's top hit
)

// classification is what a phrase turned out to be.
type classification struct {
	step  int
	band  *SearchBand  // stepArtist: the band
	track *SearchTrack // stepSong: the song, or the album when that is all there is
}

// classify reads Bandcamp's answer for a phrase. answered is false when
// Bandcamp could not be asked, which reads like an answer that matched nothing.
//
// Names must match whole, word for word (see matchKey): "swedish death metal"
// is not the album "Swedish Death Metal Compilation", and a station that took
// it for one would play a record the listener never named. A band beats a tag
// ("chelsea wolfe" is a tag too, with 99 releases under it), and a tag beats a
// song: someone who types a genre wants the genre, and a track that happens to
// share its name (there is one called "Death Metal") is rarely the point.
func classify(phrase string, res SearchResult, answered bool) classification {
	if !answered {
		return classification{step: stepTopHit}
	}
	key := matchKey(phrase)
	for i := range res.Bands {
		if matchKey(res.Bands[i].Name) == key {
			return classification{step: stepArtist, band: &res.Bands[i]}
		}
	}
	want := map[string]bool{normTag(phrase): true}
	if tags := BandcampTags(phrase); len(tags) > 0 {
		want[tags[0]] = true
	}
	for _, t := range res.Tags {
		if want[t] {
			return classification{step: stepGenre}
		}
	}
	// A track is the song; an album of that name is the song only when no
	// track says so.
	var album *SearchTrack
	for i := range res.Tracks {
		t := &res.Tracks[i]
		if matchKey(t.Name) != key {
			continue
		}
		if !t.Album {
			return classification{step: stepSong, track: t}
		}
		if album == nil {
			album = t
		}
	}
	if album != nil {
		return classification{step: stepSong, track: album}
	}
	return classification{step: stepTopHit}
}

// genericGenres are Bandcamp's top-level genres, which name half of Bandcamp
// and say little about a sound: the feed under "folk" is country, "rock" is
// everything. They are not a style when anything more particular is known,
// and they are never what YouTube Music is searched for.
var genericGenres = map[string]bool{
	"acoustic": true, "alternative": true, "ambient": true, "blues": true,
	"classical": true, "country": true, "dance": true, "electronic": true,
	"experimental": true, "folk": true, "funk": true, "hip-hop": true,
	"hip-hop-rap": true, "indie": true, "instrumental": true, "jazz": true,
	"latin": true, "metal": true, "pop": true, "punk": true, "rap": true,
	"reggae": true, "rock": true, "soul": true, "soundtrack": true, "world": true,
}

// genericTag reports a tag (in Bandcamp's spelling) that is one of those.
func genericTag(tag string) bool { return genericGenres[tag] }

// bandTags are one band's tags as styles: in Bandcamp's spelling, less the
// band's top-level genre (its genre_name) when it has anything more particular,
// and less any tag that is only its place ("Christchurch", "New Zealand"), since
// a town is not a sound.
func bandTags(b SearchBand) []string {
	places := map[string]bool{}
	if k := normTag(b.Location); k != "" {
		places[k] = true
	}
	for _, part := range strings.Split(b.Location, ",") {
		if k := normTag(part); k != "" {
			places[k] = true
		}
	}
	genre := normTag(b.Genre)

	var all, specific []string
	have := map[string]bool{}
	for _, name := range b.Tags {
		tag := normTag(name)
		if tag == "" || have[tag] || places[tag] {
			continue
		}
		have[tag] = true
		all = append(all, tag)
		if tag != genre {
			specific = append(specific, tag)
		}
	}
	if len(specific) > 0 {
		return specific
	}
	return all
}

// styleFrom is what a group of bands sounds like, as at most maxStyleTags
// Bandcamp tags: the tags ranked by how many of the bands carry them, the
// earlier band's order breaking a tie, with the generic genres dropped as long
// as anything more particular is left. A band alone is a thin description (the
// seed of a station lists three tags, one of them "Folk"); the bands around it
// agree on what it is.
func styleFrom(bands []SearchBand) []string {
	counts := map[string]int{}
	var order []string
	for _, b := range bands {
		for _, t := range bandTags(b) {
			if counts[t] == 0 {
				order = append(order, t)
			}
			counts[t]++
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	var specific []string
	for _, t := range order {
		if !genericTag(t) {
			specific = append(specific, t)
		}
	}
	if len(specific) > 0 {
		order = specific
	}
	if len(order) > maxStyleTags {
		order = order[:maxStyleTags]
	}
	return order
}

// styleTags is what one band sounds like, as styleFrom says it.
func styleTags(b SearchBand) []string { return styleFrom([]SearchBand{b}) }
