package core

import (
	"testing"
	"time"
)

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
		{"WFMU", "WFMU", false, "", "", ""},                                        // station name as title
		{"wfmu", "WFMU", false, "", "", ""},                                        // case-insensitive
		{"Advertisement", "X", false, "", "", ""},                                  // ad marker
		{"Commercial Break - Back Soon", "X", false, "", "", ""},                   // ad marker inside
		{"Visit www.example.com now", "X", false, "", "", ""},                      // promo junk
		{"https://somewhere.com/stream", "X", false, "", "", ""},                   // URL
		{"DJ Night - ", "X", true, "", "", "DJ Night"},                             // empty title side collapses
		{" - Om Unit - Patterning", "X", true, "om unit", "Om Unit", "Patterning"}, // empty leading field
		{"- - Bare Title", "X", true, "", "", "Bare Title"},
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
		"Sleeping With Sirens":         "sleeping with sirens",
		"Dance With The Dead":          "dance with the dead",
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

func TestAdMarker(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"ADVERTISEMENT", true},
		{"Commercial Break - back soon", true},
		{"Werbung", true},
		{"", false},
		{"Artist - Title", false},
	}
	for _, tc := range cases {
		if got := AdMarker(tc.raw); got != tc.want {
			t.Errorf("AdMarker(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	// A station-name title is suspicion, not a marker — DJs say those too.
	if AdMarker("Some Station") {
		t.Error("station-name title is not an ad marker")
	}
}

func TestAdBreak(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()

	// No listen open yet: a marker-looking title with no current station
	// must not arm anything.
	c.NoteTitle("Advertisement", now)
	if c.AdBreak() {
		t.Fatal("title before any listen should not arm AdBreak")
	}

	c.StartListen("s1", now)
	c.NoteTitle("Advertisement", now.Add(time.Second))
	if !c.AdBreak() {
		t.Fatal("marker title should arm AdBreak")
	}
	c.NoteTitle("Artist - Title", now.Add(2*time.Second))
	if c.AdBreak() {
		t.Fatal("normal title should clear AdBreak")
	}

	// DJ talk and station IDs: empty and station-name titles can trip
	// SuspectedAd but must never arm AdBreak.
	if _, _, _, _ = c.NoteTitle("", now.Add(3*time.Second)); c.AdBreak() {
		t.Fatal("empty title must not arm AdBreak")
	}
	c.StartListen("s2", now.Add(4*time.Second))
	if c.AdBreak() {
		t.Fatal("a new station's listen starts with AdBreak clear")
	}
}
