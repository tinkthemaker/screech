package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDirectory stands up a local radio-browser: a /json/servers endpoint
// (so pickServer resolves) and /json/stations/search that filters a fixed
// station set by the tag/name params the client sends.
func fakeDirectory(t *testing.T, stations []rbStation) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var self *httptest.Server
	mux.HandleFunc("/json/servers", func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimPrefix(r.Host, "http://")
		json.NewEncoder(w).Encode([]map[string]string{{"name": host}})
	})
	mux.HandleFunc("/json/stations/search", func(w http.ResponseWriter, r *http.Request) {
		name := strings.ToLower(r.URL.Query().Get("name"))
		tag := strings.ToLower(r.URL.Query().Get("tag"))
		var out []rbStation
		for _, s := range stations {
			if name != "" && !strings.Contains(strings.ToLower(s.Name), name) {
				continue
			}
			if tag != "" && !strings.Contains(strings.ToLower(s.Tags), tag) {
				continue
			}
			out = append(out, s)
		}
		json.NewEncoder(w).Encode(out)
	})
	self = httptest.NewServer(mux)
	t.Cleanup(self.Close)
	return self
}

// pointCoreAt replaces the core's radio-browser base URL with a test server.
func pointCoreAt(c *Core, server *httptest.Server) {
	c.rb.base = server.URL
	c.rb.client = server.Client()
}

func rbCorrido(uuid, name string) rbStation {
	return rbStation{
		UUID: uuid, Name: name,
		URL: "http://stream/" + uuid, URLResolved: "http://stream/" + uuid,
		Tags: "corridos,regional mexican,banda", Country: "MX", Codec: "MP3",
		Bitrate: 128, Votes: 500, LastCheckOK: 1,
	}
}

func openTestCoreAt(t *testing.T, server *httptest.Server) *Core {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	pointCoreAt(c, server)
	t.Cleanup(func() { c.Close() })
	return c
}

// A genre query the local cache doesn't carry must seed from the directory
// and tune a station with the tag.
func TestSeedGenreFromDirectory(t *testing.T) {
	srv := fakeDirectory(t, []rbStation{rbCorrido("mx-1", "La Corrida FM")})
	c := openTestCoreAt(t, srv)
	res, ok := c.Seed(context.Background(), "corridos", time.Now())
	if !ok {
		t.Fatal("corridos should seed from the directory")
	}
	if res.Kind != "tag" {
		t.Errorf("kind=%q, want tag", res.Kind)
	}
	if !strings.Contains(res.Pick.Station.Name, "Corrida") {
		t.Errorf("tuned station %q, want the corridos one", res.Pick.Station.Name)
	}
	if !strings.HasPrefix(res.Pick.Reason, "seeded: corridos") {
		t.Errorf("reason %q should name the tag", res.Pick.Reason)
	}
}

// An artist with no eponymous station but a curated genre mapping must
// resolve through the genre family, and the reason must say so honestly.
func TestSeedArtistFallsBackToGenre(t *testing.T) {
	srv := fakeDirectory(t, []rbStation{rbCorrido("mx-1", "Puros Corridos Radio")})
	c := openTestCoreAt(t, srv)
	res, ok := c.Seed(context.Background(), "peso pluma", time.Now())
	if !ok {
		t.Fatal("peso pluma should resolve via the corridos genre")
	}
	if res.Kind != "genre" {
		t.Errorf("kind=%q, want genre (honest about the fallback)", res.Kind)
	}
	if !strings.Contains(res.Pick.Reason, "seeded genre: corridos") {
		t.Errorf("reason %q should disclose the genre guess", res.Pick.Reason)
	}
	if !strings.Contains(res.Label, "corridos") {
		t.Errorf("label %q should name the genre used", res.Label)
	}
	// The pseudo-love is still banked for fingerprints.
	if !c.loved["peso pluma"] {
		t.Error("artist pseudo-love should be recorded for fingerprints")
	}
}

// An artist with a genuinely named station tunes it directly (kind=artist),
// never touching the genre map.
func TestSeedArtistByNameBeatsGenre(t *testing.T) {
	stations := []rbStation{{
		UUID: "pp-1", Name: "Peso Pluma Radio 24/7",
		URL: "http://stream/pp-1", URLResolved: "http://stream/pp-1",
		Tags: "corridos", Country: "MX", Codec: "MP3", Bitrate: 128, Votes: 900, LastCheckOK: 1,
	}}
	srv := fakeDirectory(t, stations)
	c := openTestCoreAt(t, srv)
	res, ok := c.Seed(context.Background(), "peso pluma", time.Now())
	if !ok {
		t.Fatal("a named station should tune directly")
	}
	if res.Kind != "artist" {
		t.Errorf("kind=%q, want artist (name match beats genre guess)", res.Kind)
	}
	if !strings.Contains(res.Pick.Station.Name, "Peso Pluma") {
		t.Errorf("tuned %q, want the artist-named station", res.Pick.Station.Name)
	}
}

// A query that matches nothing anywhere reports failure honestly.
func TestSeedNoMatchIsHonest(t *testing.T) {
	srv := fakeDirectory(t, nil)
	c := openTestCoreAt(t, srv)
	res, ok := c.Seed(context.Background(), "zzz-not-a-real-thing", time.Now())
	if ok {
		t.Errorf("a query with no matches must fail, got %+v", res)
	}
}

// The genre map only carries unambiguous artists, and every tag in it must
// be a plausible radio-browser tag (lowercase, no empties).
func TestGenreMapIsCurated(t *testing.T) {
	if len(artistGenres) == 0 {
		t.Fatal("genre map is empty")
	}
	for artist, tags := range artistGenres {
		if artist != strings.ToLower(artist) {
			t.Errorf("artist key %q must be normalized lowercase", artist)
		}
		if len(tags) == 0 {
			t.Errorf("artist %q has no genre candidates", artist)
		}
		for _, tag := range tags {
			if tag == "" || tag != strings.ToLower(tag) {
				t.Errorf("artist %q has bad tag %q", artist, tag)
			}
		}
	}
	// The artists that prompted this work must be mapped.
	for _, a := range []string{"peso pluma", "tito double p"} {
		if genreCandidates(a) == nil {
			t.Errorf("%q must be in the genre map", a)
		}
	}
}
