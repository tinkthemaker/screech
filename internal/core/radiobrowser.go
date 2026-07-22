package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const rbUserAgent = "screech/0.1 (+terminal radio; enthusiast build)"

// Fallback pool if the all.api server list is unreachable.
var rbFallbackServers = []string{
	"de1.api.radio-browser.info",
	"de2.api.radio-browser.info",
	"fi1.api.radio-browser.info",
}

type RadioBrowser struct {
	client *http.Client
	base   string // chosen server, e.g. https://de1.api.radio-browser.info
	rng    *rand.Rand
}

func NewRadioBrowser() *RadioBrowser {
	return &RadioBrowser{
		client: &http.Client{Timeout: 20 * time.Second},
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (rb *RadioBrowser) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", rbUserAgent)
	resp, err := rb.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("radio-browser: %s -> %s", u, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// pickServer resolves a server per radio-browser etiquette: ask the pool,
// pick one at random, fall back to a hardcoded list.
func (rb *RadioBrowser) pickServer(ctx context.Context) string {
	if rb.base != "" {
		return rb.base
	}
	var servers []struct {
		Name string `json:"name"`
	}
	err := rb.get(ctx, "https://all.api.radio-browser.info/json/servers", &servers)
	names := []string{}
	if err == nil {
		seen := map[string]bool{}
		for _, s := range servers {
			if s.Name != "" && !seen[s.Name] {
				seen[s.Name] = true
				names = append(names, s.Name)
			}
		}
	}
	if len(names) == 0 {
		names = rbFallbackServers
	}
	rb.base = "https://" + names[rb.rng.Intn(len(names))]
	return rb.base
}

type rbStation struct {
	UUID        string  `json:"stationuuid"`
	Name        string  `json:"name"`
	URL         string  `json:"url"`
	URLResolved string  `json:"url_resolved"`
	Homepage    string  `json:"homepage"`
	Tags        string  `json:"tags"`
	Country     string  `json:"countrycode"`
	Codec       string  `json:"codec"`
	Bitrate     int     `json:"bitrate"`
	Votes       int     `json:"votes"`
	ClickCount  int     `json:"clickcount"`
	LastCheckOK float64 `json:"lastcheckok"` // API is loose with number types
}

// FetchTop pulls the top `limit` working stations by votes, paging through
// the directory 5000 at a time (gentler on their volunteer-run servers than
// one giant request). Votes shift between pages, so results are deduped;
// a partial slice beats no slice if a later page fails.
func (rb *RadioBrowser) FetchTop(ctx context.Context, limit int) ([]Station, error) {
	const page = 5000
	base := rb.pickServer(ctx)
	seen := map[string]bool{}
	var out []Station
	for offset := 0; offset < limit; offset += page {
		n := page
		if limit-offset < page {
			n = limit - offset
		}
		q := url.Values{}
		q.Set("hidebroken", "true")
		q.Set("order", "votes")
		q.Set("reverse", "true")
		q.Set("limit", fmt.Sprint(n))
		q.Set("offset", fmt.Sprint(offset))
		var raw []rbStation
		if err := rb.get(ctx, base+"/json/stations/search?"+q.Encode(), &raw); err != nil {
			rb.base = "" // let the next attempt pick a different server
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		for _, r := range raw {
			if r.UUID == "" || seen[r.UUID] || (r.URL == "" && r.URLResolved == "") {
				continue
			}
			seen[r.UUID] = true
			st := Station{
				UUID: r.UUID, Name: r.Name, URL: r.URL, URLResolved: r.URLResolved,
				Homepage: r.Homepage, Tags: r.Tags, Country: r.Country, Codec: r.Codec,
				Bitrate: r.Bitrate, Votes: r.Votes, ClickCount: r.ClickCount,
				LastCheckOK: r.LastCheckOK >= 1,
			}
			st.AdRisk = ComputeAdRisk(&st)
			out = append(out, st)
		}
		if len(raw) < n {
			break // directory exhausted before the limit
		}
	}
	return out, nil
}

// SearchByName finds working stations whose name matches (niche internet
// radio is full of "<artist> Radio" stations, which makes this a decent
// artist-seeding source).
func (rb *RadioBrowser) SearchByName(ctx context.Context, name string, limit int) ([]Station, error) {
	q := url.Values{}
	q.Set("name", name)
	return rb.search(ctx, q, limit)
}

// SearchByTag finds working stations carrying a genre tag. This is where
// artist seeds land when no station is named for the artist: genre-tagged
// stations are far more common than eponymous ones.
func (rb *RadioBrowser) SearchByTag(ctx context.Context, tag string, limit int) ([]Station, error) {
	q := url.Values{}
	q.Set("tag", tag)
	return rb.search(ctx, q, limit)
}

// search runs a station query against the directory, mapping results into
// the cache's Station shape with ad-risk precomputed.
func (rb *RadioBrowser) search(ctx context.Context, q url.Values, limit int) ([]Station, error) {
	base := rb.pickServer(ctx)
	q.Set("hidebroken", "true")
	q.Set("order", "votes")
	q.Set("reverse", "true")
	q.Set("limit", fmt.Sprint(limit))
	var raw []rbStation
	if err := rb.get(ctx, base+"/json/stations/search?"+q.Encode(), &raw); err != nil {
		rb.base = ""
		return nil, err
	}
	out := make([]Station, 0, len(raw))
	for _, r := range raw {
		if r.URL == "" && r.URLResolved == "" {
			continue
		}
		st := Station{
			UUID: r.UUID, Name: r.Name, URL: r.URL, URLResolved: r.URLResolved,
			Homepage: r.Homepage, Tags: r.Tags, Country: r.Country, Codec: r.Codec,
			Bitrate: r.Bitrate, Votes: r.Votes, ClickCount: r.ClickCount,
			LastCheckOK: r.LastCheckOK >= 1,
		}
		st.AdRisk = ComputeAdRisk(&st)
		out = append(out, st)
	}
	return out, nil
}

// Click reports a tune-in to radio-browser (their etiquette; improves their
// popularity data). Fire and forget; errors are irrelevant. Seed stations
// (seed: prefix) are screech-local and never reported.
func (rb *RadioBrowser) Click(stationUUID string) {
	if stationUUID == "" || len(stationUUID) > 64 || strings.HasPrefix(stationUUID, "seed:") {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		base := rb.pickServer(ctx)
		_ = rb.get(ctx, base+"/json/url/"+url.PathEscape(stationUUID), nil)
	}()
}
