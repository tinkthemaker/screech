package discover

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// searchResponse is bandcamp.com's own autocomplete answer, cut down to the
// shapes the live probes showed: an album and a track of the same name, a band
// with its genre, tags and place, a fan (which is none of our business), and
// a track whose strings and link are hostile.
const searchResponse = `{
  "auto": {
    "results": [
      {"type": "a", "id": 1111, "name": "Engorged in Putrescence", "band_name": "Decarnate",
       "item_url_root": "https://decarnate.bandcamp.com",
       "item_url_path": "https://decarnate.bandcamp.com/album/engorged-in-putrescence"},
      {"type": "t", "id": 2222, "name": "Engorged in Putrescence", "band_name": "Decarnate",
       "item_url_root": "https://decarnate.bandcamp.com",
       "item_url_path": "https://decarnate.bandcamp.com/track/engorged-in-putrescence"},
      {"type": "b", "id": 3333, "name": "CHELSEA WOLFE", "genre_name": "Alternative",
       "tag_names": ["Alternative", "Folk", "rock-acoustic-alternative-rock"],
       "location": "Los Angeles, California",
       "item_url_root": "https://chelseawolfe.bandcamp.com"},
      {"type": "f", "name": "A Fan", "item_url_root": "https://bandcamp.com/afan"},
      {"type": "t", "id": "4444", "name": "Evil\u001b]52;c;aGk=\u0007 Song", "band_name": "Bad\nBand",
       "item_url_root": "https://evil.example", "item_url_path": "https://evil.example/track/x"},
      {"type": "t", "name": "Relative", "band_name": "Decarnate",
       "item_url_root": "https://decarnate.bandcamp.com/", "item_url_path": "/track/relative"}
    ]
  },
  "tag": {"matches": [{"norm_name": "chelsea-wolfe", "count": 99}, {"norm_name": "Chelsea-Wolfe-Fans", "count": 3}], "count": 2},
  "genre": {}
}`

func TestBandcampClientSearchRequestAndParse(t *testing.T) {
	var gotMethod, gotPath, gotUA, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotUA, gotType = r.Method, r.URL.Path, r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchResponse))
	}))
	defer srv.Close()

	// The default endpoint is the real one; the transport sends it to the test
	// server so no byte leaves the machine.
	c := BandcampClient{HTTP: &http.Client{Transport: rewriteTo(srv.URL)}, UserAgent: "screech/test"}
	got, err := c.Search(context.Background(), "engorged in putrescence")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/api/bcsearch_public_api/1/autocomplete_elastic" {
		t.Errorf("request = %s %s, want POST to the autocomplete endpoint", gotMethod, gotPath)
	}
	if gotUA != "screech/test" || gotType != "application/json" {
		t.Errorf("headers: User-Agent %q, Content-Type %q", gotUA, gotType)
	}
	// search_filter stays empty: "b" found nothing for a band that "" found.
	want := map[string]any{"search_text": "engorged in putrescence", "search_filter": "", "full_page": false, "fan_id": nil}
	if !reflect.DeepEqual(gotBody, want) {
		t.Errorf("body = %v, want %v", gotBody, want)
	}

	wantRes := SearchResult{
		Tags: []string{"chelsea-wolfe", "chelsea-wolfe-fans"},
		Bands: []SearchBand{{
			Name: "CHELSEA WOLFE", Genre: "Alternative", Location: "Los Angeles, California",
			URL:  "https://chelseawolfe.bandcamp.com",
			Tags: []string{"Alternative", "Folk", "rock-acoustic-alternative-rock"},
		}},
		Tracks: []SearchTrack{
			{Name: "Engorged in Putrescence", Band: "Decarnate", Album: true, ID: 1111,
				URL: "https://decarnate.bandcamp.com/album/engorged-in-putrescence"},
			{Name: "Engorged in Putrescence", Band: "Decarnate", ID: 2222,
				URL: "https://decarnate.bandcamp.com/track/engorged-in-putrescence"},
			// Network text is cleaned, and a link off Bandcamp is not followed.
			{Name: "Evil Song", Band: "Bad Band", ID: 4444, URL: ""},
			// A path relative to the band's root is joined to it.
			{Name: "Relative", Band: "Decarnate", URL: "https://decarnate.bandcamp.com/track/relative"},
		},
	}
	if !reflect.DeepEqual(got, wantRes) {
		t.Errorf("result =\n%+v\nwant\n%+v", got, wantRes)
	}
}

func TestBandcampClientSearchEmptyAndFailures(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	// The shape the probe returned for a phrase nobody has: not an error.
	empty := serve(http.StatusOK, `{"auto":{"results":[]},"tag":{"matches":[],"count":0},"genre":{}}`)
	defer empty.Close()
	res, err := BandcampClient{HTTP: empty.Client(), SearchEndpoint: empty.URL}.Search(context.Background(), "zzz")
	if err != nil || len(res.Tags)+len(res.Bands)+len(res.Tracks) != 0 {
		t.Errorf("empty answer = %+v, %v; want nothing and no error", res, err)
	}

	throttled := serve(http.StatusTooManyRequests, "slow down")
	defer throttled.Close()
	_, err = BandcampClient{HTTP: throttled.Client(), SearchEndpoint: throttled.URL}.Search(context.Background(), "x")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Errorf("a 429 = %v, want a *StatusError", err)
	}

	junk := serve(http.StatusOK, "<html>nope</html>")
	defer junk.Close()
	if _, err = (BandcampClient{HTTP: junk.Client(), SearchEndpoint: junk.URL}).Search(context.Background(), "x"); err == nil {
		t.Error("an unreadable answer must be an error")
	}

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer hang.Close()
	own := BandcampClient{HTTP: &http.Client{Timeout: 50 * time.Millisecond}, SearchEndpoint: hang.URL}
	if _, err = own.Search(context.Background(), "x"); !errors.Is(err, ErrNetwork) {
		t.Errorf("the client's own timeout = %v, want it marked ErrNetwork", err)
	}
}

// --- names -----------------------------------------------------------------------

func TestMatchKey(t *testing.T) {
	same := [][2]string{
		{"chelsea wolfe", "CHELSEA WOLFE"},
		{"Chelsea  Wolfe", "chelsea-wolfe"},
		{"Engorged in Putrescence", "engorged in putrescence!"},
		{"Mr. Bungle", "mr bungle"},
		{"Sigur Rós", "sigur rós"},
		{"Death/Metal", "death metal"},
		// An apostrophe joins a word to its ending; it does not divide.
		{"Don't Stop", "dont stop"},
		{"Don’t Stop", "dont stop"},
	}
	for _, p := range same {
		if matchKey(p[0]) != matchKey(p[1]) {
			t.Errorf("matchKey(%q) = %q, matchKey(%q) = %q; want equal", p[0], matchKey(p[0]), p[1], matchKey(p[1]))
		}
	}
	if matchKey("death metal") == matchKey("death") {
		t.Error("a prefix is not a match")
	}
	// An accent typed as a letter and a combining mark is one word, not two.
	if got := matchKey("Sigur Rós"); got != "sigur rós" {
		t.Errorf("matchKey with a combining mark = %q", got)
	}
	// Words keep their boundaries: a track called "SwedishDeathMetal" is not
	// the phrase "swedish death metal", and a station that took it for one
	// played downtempo.
	for _, p := range [][2]string{
		{"SwedishDeathMetal", "swedish death metal"},
		{"deathmetal", "death metal"},
		{"Chelsea Wolfe", "ChelseaWolfe"},
	} {
		if matchKey(p[0]) == matchKey(p[1]) {
			t.Errorf("matchKey(%q) = matchKey(%q) = %q: word boundaries were lost", p[0], p[1], matchKey(p[0]))
		}
	}
	// A band named only in punctuation still has a key, and it is not empty:
	// an empty key would equal every other empty key.
	if matchKey("!!!") == "" || matchKey("!!!") == matchKey("???") {
		t.Errorf("symbol-only names: %q, %q", matchKey("!!!"), matchKey("???"))
	}
}

// --- classifying a phrase ----------------------------------------------------------

func TestClassify(t *testing.T) {
	chelsea := SearchBand{Name: "CHELSEA WOLFE", Tags: []string{"Folk"}}
	track := func(name, band string, album bool) SearchTrack {
		return SearchTrack{Name: name, Band: band, Album: album, URL: "https://x.bandcamp.com/track/y"}
	}
	cases := []struct {
		name     string
		phrase   string
		res      SearchResult
		answered bool
		want     int
	}{
		{"a band beats the tag of the same name", "chelsea wolfe",
			SearchResult{Tags: []string{"chelsea-wolfe"}, Bands: []SearchBand{chelsea}}, true, stepArtist},
		{"a band is matched however it is typed", "Chelsea  Wolfe!",
			SearchResult{Bands: []SearchBand{chelsea}}, true, stepArtist},
		{"a band beats a track of the same name", "chelsea wolfe",
			SearchResult{Bands: []SearchBand{chelsea}, Tracks: []SearchTrack{track("Chelsea Wolfe", "Someone", false)}}, true, stepArtist},
		{"a track", "engorged in putrescence",
			SearchResult{Tracks: []SearchTrack{track("Engorged in Putrescence", "Decarnate", false)}}, true, stepSong},
		{"an album alone is a song", "engorged in putrescence",
			SearchResult{Tracks: []SearchTrack{track("Engorged in Putrescence", "Decarnate", true)}}, true, stepSong},
		// A genre beats a song that happens to share its name: live, the tag
		// swedish-death-metal came with a downtempo track "SwedishDeathMetal".
		{"a tag beats a track of the same name", "death metal",
			SearchResult{Tags: []string{"death-metal"}, Tracks: []SearchTrack{track("Death Metal", "Someone", false)}}, true, stepGenre},
		{"a tag beats an album of the same name", "death metal",
			SearchResult{Tags: []string{"death-metal"}, Tracks: []SearchTrack{track("Death Metal", "Someone", true)}}, true, stepGenre},
		{"a run-together title is not the phrase", "swedish death metal",
			SearchResult{Tags: []string{"swedish-death-metal"}, Tracks: []SearchTrack{track("SwedishDeathMetal", "Magmatik", false)}}, true, stepGenre},
		{"a run-together title is not a song either", "swedish death metal",
			SearchResult{Tracks: []SearchTrack{track("SwedishDeathMetal", "Magmatik", false)}}, true, stepTopHit},
		{"a band beats a tag and a track", "chelsea wolfe",
			SearchResult{Tags: []string{"chelsea-wolfe"}, Bands: []SearchBand{chelsea},
				Tracks: []SearchTrack{track("Chelsea Wolfe", "Someone", false)}}, true, stepArtist},
		{"results that merely contain the phrase are not matches", "swedish death metal",
			SearchResult{Tags: []string{"swedish-death-metal"}, Tracks: []SearchTrack{
				track("Swedish Death Metal Compilation", "Various", true),
				track("Swedish Death Metal Anthem", "Someone", false)}}, true, stepGenre},
		{"a tag alone is a genre", "swedish death metal",
			SearchResult{Tags: []string{"swedish-death-metal"}}, true, stepGenre},
		{"a tag is found in a list of near matches", "Swedish Death Metal",
			SearchResult{Tags: []string{"death-metal", "swedish-death-metal"}}, true, stepGenre},
		{"a near tag is not the tag", "swedish death metal",
			SearchResult{Tags: []string{"swedish-death-metal-band"}}, true, stepTopHit},
		{"nothing matched", "a phrase nobody wrote", SearchResult{}, true, stepTopHit},
		{"Bandcamp did not answer", "chelsea wolfe", SearchResult{Bands: []SearchBand{chelsea}}, false, stepTopHit},
		{"an unreadable phrase matches nothing", "!!!",
			SearchResult{Bands: []SearchBand{{Name: "???"}}}, true, stepTopHit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.phrase, tc.res, tc.answered)
			if got.step != tc.want {
				t.Errorf("classify(%q) = step %d, want %d", tc.phrase, got.step, tc.want)
			}
			switch got.step {
			case stepArtist:
				if got.band == nil || matchKey(got.band.Name) != matchKey(tc.phrase) {
					t.Errorf("artist step carries band %+v", got.band)
				}
			case stepSong:
				if got.track == nil || matchKey(got.track.Name) != matchKey(tc.phrase) {
					t.Errorf("song step carries track %+v", got.track)
				}
			}
		})
	}

	// When a track and an album share the name, the track is the song.
	res := SearchResult{Tracks: []SearchTrack{
		track("Engorged in Putrescence", "Decarnate", true),
		{Name: "Engorged in Putrescence", Band: "Decarnate", URL: "https://decarnate.bandcamp.com/track/e"},
	}}
	if got := classify("engorged in putrescence", res, true); got.track == nil || got.track.Album {
		t.Errorf("album listed before the track: chose %+v, want the track", got.track)
	}
}

// --- style tags --------------------------------------------------------------------

func TestStyleTagFiltering(t *testing.T) {
	cases := []struct {
		name string
		band SearchBand
		want []string
	}{
		{"genre and place dropped, as for Organectomy",
			SearchBand{Name: "Organectomy", Genre: "Metal", Location: "Christchurch, New Zealand",
				Tags: []string{"Metal", "Death Metal", "Brutal Death Metal", "Slam"}},
			[]string{"death-metal", "brutal-death-metal", "slam"}},
		{"at most three",
			SearchBand{Genre: "Metal", Tags: []string{"Metal", "Death Metal", "Brutal Death Metal", "Slam", "Grind", "Gore"}},
			[]string{"death-metal", "brutal-death-metal", "slam"}},
		{"a tag that names its place is dropped",
			SearchBand{Genre: "Metal", Location: "Christchurch, New Zealand",
				Tags: []string{"Christchurch", "Death Metal", "New Zealand", "new-zealand"}},
			[]string{"death-metal"}},
		{"the whole location counts too",
			SearchBand{Location: "Stockholm", Tags: []string{"stockholm", "Doom"}},
			[]string{"doom"}},
		{"the genre stays when it is all there is",
			SearchBand{Genre: "Metal", Tags: []string{"Metal"}},
			[]string{"metal"}},
		{"a genre tag stays when only places are left besides it",
			SearchBand{Genre: "Rock", Location: "Oslo, Norway", Tags: []string{"Rock", "Oslo"}},
			[]string{"rock"}},
		{"Bandcamp's own spelling is kept hyphenated, and a generic genre is not a style",
			SearchBand{Genre: "Alternative", Tags: []string{"Alternative", "Folk", "rock-acoustic-alternative-rock"}},
			[]string{"rock-acoustic-alternative-rock"}},
		{"generic genres go whenever something more particular is left",
			SearchBand{Genre: "Rock", Tags: []string{"Rock", "Indie", "Pop", "Shoegaze", "Dream Pop"}},
			[]string{"shoegaze", "dream-pop"}},
		{"generic genres stay when they are all there is",
			SearchBand{Tags: []string{"Folk", "Pop"}},
			[]string{"folk", "pop"}},
		{"a generic word inside a longer tag is not a generic tag",
			SearchBand{Tags: []string{"Folk", "Folk Punk", "Hip-Hop"}},
			[]string{"folk-punk"}},
		{"punctuation becomes a single hyphen",
			SearchBand{Tags: []string{"Drum & Bass", "R&B/Soul"}},
			[]string{"drum-bass", "r-b-soul"}},
		{"repeats collapse", SearchBand{Tags: []string{"Death Metal", "death-metal", "DEATH  METAL"}}, []string{"death-metal"}},
		{"blank tags are skipped", SearchBand{Tags: []string{"", "  ", "Doom"}}, []string{"doom"}},
		{"nothing", SearchBand{Genre: "Metal"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := styleTags(tc.band); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("styleTags = %q, want %q", got, tc.want)
			}
		})
	}
}

// A band's own tags are a poor description of a station (Chelsea Wolfe lists
// Alternative, Folk and one odd long tag, and Bandcamp's folk feed is
// country). The bands around it say more: the tags are ranked by how many of
// the bands carry them.
func TestStyleFromSeveralBands(t *testing.T) {
	seed := SearchBand{Name: "Chelsea Wolfe", Genre: "Alternative", Tags: []string{"Alternative", "Folk", "rock-acoustic-alternative-rock"}}
	zola := SearchBand{Name: "Zola Jesus", Genre: "Electronic", Tags: []string{"Darkwave", "Gothic Rock", "Folk"}}
	ruth := SearchBand{Name: "Emma Ruth Rundle", Genre: "Rock", Tags: []string{"Doom", "Darkwave"}}
	wolves := SearchBand{Name: "Wolves", Genre: "Metal", Tags: []string{"Gothic Rock", "Doom", "Rock"}}

	cases := []struct {
		name  string
		bands []SearchBand
		want  []string
	}{
		{"the neighbours decide, and folk is gone",
			[]SearchBand{seed, zola, ruth, wolves}, []string{"darkwave", "gothic-rock", "doom"}},
		{"alone, the seed's particular tag is all that is left",
			[]SearchBand{seed}, []string{"rock-acoustic-alternative-rock"}},
		{"a tag two bands carry comes before ones a single band has",
			[]SearchBand{seed, zola, ruth}, []string{"darkwave", "rock-acoustic-alternative-rock", "gothic-rock"}},
		{"counted by bands, not by mentions",
			[]SearchBand{{Tags: []string{"Slam", "Slam", "slam"}}, {Tags: []string{"Death Metal"}}, {Tags: []string{"Death Metal"}}},
			[]string{"death-metal", "slam"}},
		{"ties go to the earlier band's order",
			[]SearchBand{{Tags: []string{"Doom"}}, {Tags: []string{"Drone"}}, {Tags: []string{"Sludge"}}, {Tags: []string{"Funeral Doom"}}},
			[]string{"doom", "drone", "sludge"}},
		{"bands with nothing to say", []SearchBand{{}, {Genre: "Metal"}}, nil},
		{"nothing", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := styleFrom(tc.bands); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("styleFrom = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGenericTag(t *testing.T) {
	for _, g := range []string{"rock", "metal", "folk", "alternative", "pop", "electronic", "experimental", "indie",
		"ambient", "punk", "jazz", "hip-hop", "country", "classical"} {
		if !genericTag(g) {
			t.Errorf("%q should be generic", g)
		}
	}
	for _, s := range []string{"darkwave", "doom", "gothic-rock", "folk-punk", "death-metal", "slam", "shoegaze", "dream-pop", ""} {
		if genericTag(s) {
			t.Errorf("%q is a style, not a generic genre", s)
		}
	}
}

func TestSameArtist(t *testing.T) {
	cases := []struct {
		media YTMedia
		band  string
		want  bool
	}{
		{YTMedia{Artist: "Decarnate", Track: "x"}, "Decarnate", true},
		{YTMedia{Artist: "DECARNATE", Track: "x"}, "decarnate", true},
		{YTMedia{Artist: "Decarnate, Organectomy", Track: "x"}, "Organectomy", true},
		{YTMedia{Artist: "Decarnate & Friends", Track: "x"}, "Decarnate", true},
		{YTMedia{Title: "Decarnate - Engorged", Channel: "Decarnate - Topic"}, "Decarnate", true},
		{YTMedia{Title: "A song", Channel: "Decarnate - Topic"}, "Decarnate", true},
		{YTMedia{Artist: "Wolfe Parade", Track: "x"}, "Chelsea Wolfe", false},
		{YTMedia{Artist: "Decarnate Remixes", Track: "x"}, "Decarnate", false},
		{YTMedia{Artist: "NA", Title: "A song"}, "Decarnate", false},
	}
	for _, tc := range cases {
		if got := sameArtist(tc.media, tc.band); got != tc.want {
			t.Errorf("sameArtist(%+v, %q) = %v, want %v", tc.media, tc.band, got, tc.want)
		}
	}
}
