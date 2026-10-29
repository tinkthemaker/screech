package core

import (
	"testing"
	"time"
)

func loved(artist, title, station string, at time.Time) LovedTrack {
	return LovedTrack{Artist: artist, Title: title, StationName: station, ArtistKey: artist, LovedAt: at}
}

func TestFilterLovedTracksEmptyQuery(t *testing.T) {
	in := []LovedTrack{loved("A", "T1", "S", time.Now())}
	if got := FilterLovedTracks(in, ""); len(got) != 1 || got[0] != in[0] {
		t.Fatalf("empty query should pass entries through: %+v", got)
	}
	if got := FilterLovedTracks(in, "   "); len(got) != 1 {
		t.Fatalf("space-only query should pass entries through: %+v", got)
	}
}

func TestFilterLovedTracksRanksPrefixAboveContains(t *testing.T) {
	now := time.Now()
	entries := []LovedTrack{
		loved("Broadcast", "Come On Let's Go", "WFMU", now),  // artist prefix 70
		loved("Someone", "Broadcast News", "NTS", now),       // title prefix 80
		loved("The Others", "A Broadcast Story", "BBC", now), // title contains 50
		loved("Joe Broadcasts", "Other", "Radio", now),       // artist contains 40
		loved("Nobody", "Nothing", "Broadcast FM", now),      // station only 10
	}
	got := FilterLovedTracks(entries, "broadcast")
	if len(got) != 5 {
		t.Fatalf("expected 5 matches: %+v", got)
	}
	if got[0].Title != "Broadcast News" {
		t.Fatalf("title prefix should outrank artist prefix: %v", got[0].Title)
	}
	if got[4].StationName != "Broadcast FM" {
		t.Fatalf("station-only match should rank last: %v", got[4].StationName)
	}
}

func TestFilterLovedTracksAllTermsMustMatch(t *testing.T) {
	entries := []LovedTrack{
		loved("Broadcast", "Come On Let's Go", "WFMU", time.Now()),
		loved("Broadcast", "Other Song", "WFMU", time.Now()),
	}
	got := FilterLovedTracks(entries, "broadcast come")
	if len(got) != 1 || got[0].Title != "Come On Let's Go" {
		t.Fatalf("every term must match somewhere: %+v", got)
	}
	if got := FilterLovedTracks(entries, "broadcast zzz"); len(got) != 0 {
		t.Fatalf("unmatched term should drop everything: %+v", got)
	}
}

func TestFilterLovedTracksTiesNewestFirst(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	recent := time.Now()
	entries := []LovedTrack{
		loved("Broadcast", "Older", "S", old),
		loved("Broadcast", "Newer", "S", recent),
	}
	got := FilterLovedTracks(entries, "broadcast")
	if len(got) != 2 || got[0].Title != "Newer" {
		t.Fatalf("tie should order newest first: %+v", got)
	}
}

func TestFilterSavedStations(t *testing.T) {
	entries := []SavedStation{
		{UUID: "a", Name: "WFMU Freeform"},
		{UUID: "b", Name: "Space Station Soma"},
		{UUID: "c", Name: "Night Bus Radio"},
	}
	if got := FilterSavedStations(entries, ""); len(got) != 3 {
		t.Fatalf("empty query passes through: %+v", got)
	}
	got := FilterSavedStations(entries, "soma space")
	if len(got) != 1 || got[0].UUID != "b" {
		t.Fatalf("all terms must appear in the name: %+v", got)
	}
	if got := FilterSavedStations(entries, "zzz"); len(got) != 0 {
		t.Fatalf("expected no matches: %+v", got)
	}
}
