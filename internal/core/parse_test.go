package core

import "testing"

func TestParseTitle(t *testing.T) {
	cases := []struct {
		raw, station          string
		ok                    bool
		artistKey, wantArtist string
		wantTitle             string
	}{
		{"Stellardrone - Billions and Billions", "SomaFM", true, "stellardrone", "Stellardrone", "Billions and Billions"},
		{"The Orb - Little Fluffy Clouds", "X", true, "orb", "The Orb", "Little Fluffy Clouds"},
		{"Boards of Canada feat. Someone - Roygbiv", "X", true, "boards of canada", "Boards of Canada feat. Someone", "Roygbiv"},
		{"Just A Title No Separator", "X", true, "", "", "Just A Title No Separator"},
		{"", "X", false, "", "", ""},
		{"   ", "X", false, "", "", ""},
		{"WFMU", "WFMU", false, "", "", ""},                      // station name as title
		{"wfmu", "WFMU", false, "", "", ""},                      // case-insensitive
		{"Advertisement", "X", false, "", "", ""},                // ad marker
		{"Commercial Break - Back Soon", "X", false, "", "", ""}, // ad marker inside
		{"Visit www.example.com now", "X", false, "", "", ""},    // promo junk
		{"https://somewhere.com/stream", "X", false, "", "", ""}, // URL
		{"DJ Night - ", "X", true, "", "", "DJ Night"},           // empty title side collapses
	}
	for _, tc := range cases {
		tr, ok := ParseTitle(tc.raw, tc.station)
		if ok != tc.ok {
			t.Errorf("ParseTitle(%q): ok=%v want %v", tc.raw, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if tr.ArtistKey != tc.artistKey || tr.Artist != tc.wantArtist || tr.Title != tc.wantTitle {
			t.Errorf("ParseTitle(%q) = key %q artist %q title %q; want %q %q %q",
				tc.raw, tr.ArtistKey, tr.Artist, tr.Title, tc.artistKey, tc.wantArtist, tc.wantTitle)
		}
	}
}

func TestNormalizeArtist(t *testing.T) {
	cases := map[string]string{
		"The Beatles":                  "beatles",
		"  BONOBO  ":                   "bonobo",
		"Massive Attack ft. Tracey":    "massive attack",
		"A Winged Victory featuring X": "a winged victory",
		"":                             "",
	}
	for in, want := range cases {
		if got := NormalizeArtist(in); got != want {
			t.Errorf("NormalizeArtist(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuspectAd(t *testing.T) {
	if !SuspectAd("", "X") {
		t.Error("empty title should be suspect")
	}
	if !SuspectAd("KIIS FM", "KIIS FM") {
		t.Error("station-name title should be suspect")
	}
	if SuspectAd("Aphex Twin - Rhubarb", "X") {
		t.Error("normal track should not be suspect")
	}
}
