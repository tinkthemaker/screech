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

func TestBandcampTags(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Swedish Death Metal", []string{"swedish-death-metal", "death-metal", "metal"}},
		{" dungeon synth ", []string{"dungeon-synth", "synth"}},
		{"Metal", []string{"metal"}},
		{"Dismember", []string{"dismember"}},
		{"Drum & Bass", []string{"drum-bass", "bass"}},
		{"", nil},
		{"   ", nil},
	}
	for _, tc := range cases {
		if got := BandcampTags(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("BandcampTags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// discoverResponse is the shape bandcamp.com really answers with, trimmed to
// what matters: a label release whose featured track names the band, a
// release with no featured track (merch comes back like this), and a plain
// album.
const discoverResponse = `{
  "results": [
    {
      "result_type": "a",
      "title": "Meat Your Death",
      "band_name": "Sheeple Records",
      "item_url": "https://sheeple13.bandcamp.com/album/meat-your-death?from=discover_page",
      "album_artist": "Various Artists",
      "featured_track": {
        "id": 897683859,
        "title": "Meat Your Death",
        "band_name": "Sheeple",
        "band_id": 96347213,
        "stream_url": "https://t4.bcbits.com/stream/abc/mp3-128/897683859?p=0&ts=1&t=2&token=3",
        "duration": 156.72
      },
      "release_date": "2026-10-30 00:00:00 UTC"
    },
    {
      "result_type": "s",
      "title": "A Shirt",
      "band_name": "Merchants",
      "album_artist": null,
      "featured_track": null
    },
    {
      "result_type": "a",
      "title": "No Stream Here",
      "band_name": "Mute",
      "featured_track": {"id": 5, "title": "Silent", "band_name": "Mute", "stream_url": "", "duration": 10}
    },
    {
      "result_type": "a",
      "title": "Fallback Band",
      "band_name": "Plain Band",
      "album_artist": null,
      "featured_track": {"id": 6, "title": "Plain Song", "stream_url": "https://t4.bcbits.com/stream/def/6", "duration": 200.5}
    }
  ],
  "result_count": 4,
  "batch_result_count": 4,
  "discover_spec_id": 1,
  "is_following_discover_spec": false,
  "cursor": "abc"
}`

func TestBandcampClientRequestAndParse(t *testing.T) {
	var gotMethod, gotUA, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotUA, gotType = r.Method, r.Header.Get("User-Agent"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(discoverResponse))
	}))
	defer srv.Close()

	c := BandcampClient{HTTP: srv.Client(), UserAgent: "screech/test", Endpoint: srv.URL}
	items, err := c.Tag(context.Background(), "death-metal", "new")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotUA != "screech/test" {
		t.Errorf("User-Agent = %q, want the client's", gotUA)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if got := gotBody["tag_norm_names"]; !reflect.DeepEqual(got, []any{"death-metal"}) {
		t.Errorf("tag_norm_names = %v", got)
	}
	if gotBody["slice"] != "new" {
		t.Errorf("slice = %v", gotBody["slice"])
	}
	if gotBody["size"] != float64(60) {
		t.Errorf("size = %v", gotBody["size"])
	}
	if got := gotBody["include_result_types"]; !reflect.DeepEqual(got, []any{"a"}) {
		t.Errorf("include_result_types = %v", got)
	}
	if gotBody["cursor"] != "*" || gotBody["category_id"] != float64(0) || gotBody["geoname_id"] != float64(0) {
		t.Errorf("fixed fields wrong: %v", gotBody)
	}

	want := []BCItem{
		// The featured track's band wins over the label that released it.
		{TrackID: 897683859, Artist: "Sheeple", Title: "Meat Your Death",
			StreamURL: "https://t4.bcbits.com/stream/abc/mp3-128/897683859?p=0&ts=1&t=2&token=3", Duration: 156.72},
		// With no band on the track, the release's band stands in. The
		// merch row and the row with no stream are dropped.
		{TrackID: 6, Artist: "Plain Band", Title: "Plain Song",
			StreamURL: "https://t4.bcbits.com/stream/def/6", Duration: 200.5},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v\nwant   %+v", items, want)
	}
}

func TestBandcampClientDefaultsAndText(t *testing.T) {
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		_, _ = w.Write([]byte(`{"results":[{"result_type":"a","title":"t","band_name":"b",
			"featured_track":{"id":1,"title":"Evil\u001b]52;c;aGk=\u0007 Song","band_name":"Bad\nBand","stream_url":"https://x/1","duration":100}}]}`))
	}))
	defer srv.Close()

	// An empty Endpoint means the real one; point the transport at the
	// test server so no byte leaves the machine.
	c := BandcampClient{HTTP: &http.Client{Transport: rewriteTo(srv.URL)}}
	items, err := c.Tag(context.Background(), "metal", "top")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if sawPath != "/api/discover/1/discover_web" {
		t.Errorf("default endpoint path = %q", sawPath)
	}
	if len(items) != 1 || items[0].Title != "Evil Song" || items[0].Artist != "Bad Band" {
		t.Errorf("network text must pass through CleanText, got %+v", items)
	}
}

// rewriteTo sends every request to base, whatever host it names.
type rewriteTo string

func (b rewriteTo) RoundTrip(r *http.Request) (*http.Response, error) {
	u := string(b) + r.URL.Path
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, r.Body)
	if err != nil {
		return nil, err
	}
	req.Header = r.Header
	return http.DefaultTransport.RoundTrip(req)
}

func TestBandcampClientFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantCode int // the StatusError code; 0 when the answer was a 200
	}{
		{"server error", http.StatusInternalServerError, "boom", http.StatusInternalServerError},
		{"throttled", http.StatusTooManyRequests, "slow down", http.StatusTooManyRequests},
		{"not json", http.StatusOK, "<html>nope</html>", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := BandcampClient{HTTP: srv.Client(), Endpoint: srv.URL}
			items, err := c.Tag(context.Background(), "metal", "new")
			if err == nil {
				t.Fatalf("want an error, got %+v", items)
			}
			// A caller can tell a throttle from an outage from a changed feed.
			var se *StatusError
			switch gotStatus := errors.As(err, &se); {
			case tc.wantCode != 0 && (!gotStatus || se.Code != tc.wantCode):
				t.Errorf("error %v: want a *StatusError with code %d", err, tc.wantCode)
			case tc.wantCode == 0 && gotStatus:
				t.Errorf("error %v: a 200 with a bad body is not a StatusError", err)
			}
		})
	}

	// A cancelled context stops the request before it leaves.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a cancelled lookup must not reach the server")
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := BandcampClient{HTTP: srv.Client(), Endpoint: srv.URL}
	if _, err := c.Tag(ctx, "metal", "new"); err == nil {
		t.Error("want an error from a cancelled context")
	}
}

// A host that never answers is the network's doing when it is the client's
// own timeout that ends the wait, and the lookup's own clock when it is the
// caller's deadline: only the first is marked as an outage.
func TestBandcampClientTimeoutIsAnOutageButTheCallersDeadlineIsNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	own := BandcampClient{HTTP: &http.Client{Timeout: 50 * time.Millisecond}, Endpoint: srv.URL}
	_, err := own.Tag(context.Background(), "metal", "new")
	if !errors.Is(err, ErrNetwork) {
		t.Errorf("the client's own timeout = %v, want it marked ErrNetwork", err)
	}
	if !isNetworkError(err) {
		t.Errorf("isNetworkError(%v) = false for a host that never answered", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	callers := BandcampClient{HTTP: srv.Client(), Endpoint: srv.URL}
	_, err = callers.Tag(ctx, "metal", "new")
	if err == nil || errors.Is(err, ErrNetwork) {
		t.Errorf("the caller's deadline = %v, want a plain failure that is not marked ErrNetwork", err)
	}
	if isNetworkError(err) {
		t.Errorf("isNetworkError(%v) = true for the caller's own deadline", err)
	}
}
