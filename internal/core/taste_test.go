package core

import (
	"testing"
	"time"
)

func TestTasteTotalsAndDayparts(t *testing.T) {
	c := openTestCore(t)
	now := time.Date(2026, 2, 10, 20, 0, 0, 0, time.Local)

	// A 2h morning listen today (6:00), a 1h day listen ten days ago
	// (13:00, outside the week window), a 30m day listen two days ago.
	c.StartListen("s1", now.Add(-14*time.Hour))
	c.EndListen(now.Add(-14*time.Hour + 2*time.Hour))
	c.StartListen("s2", now.Add(-10*24*time.Hour-7*time.Hour))
	c.EndListen(now.Add(-10*24*time.Hour - 7*time.Hour + time.Hour))
	c.StartListen("s3", now.Add(-2*24*time.Hour-7*time.Hour))
	c.EndListen(now.Add(-2*24*time.Hour - 7*time.Hour + 30*time.Minute))

	taste, err := c.Taste(now)
	if err != nil {
		t.Fatalf("Taste: %v", err)
	}
	if taste.Total != 3*time.Hour+30*time.Minute {
		t.Fatalf("Total = %v, want 3h30m", taste.Total)
	}
	if taste.Week != 2*time.Hour+30*time.Minute {
		t.Fatalf("Week = %v, want 2h30m", taste.Week)
	}
	if taste.ByDaypart[DaypartMorning] != 2*time.Hour {
		t.Fatalf("morning = %v, want 2h", taste.ByDaypart[DaypartMorning])
	}
	if got := taste.ByDaypart[DaypartDay]; got != time.Hour+30*time.Minute {
		t.Fatalf("day = %v, want 1h30m", got)
	}
}

func TestTasteArtists(t *testing.T) {
	c := openTestCore(t)
	now := time.Date(2026, 2, 10, 20, 0, 0, 0, time.Local)

	// A real listen: Alpha heard twice, so it outranks a single-play artist.
	c.StartListen("s1", now.Add(-24*time.Hour))
	c.NoteTitle("Alpha Artist - First", now.Add(-24*time.Hour+time.Minute))
	c.NoteTitle("Alpha Artist - Second", now.Add(-24*time.Hour+2*time.Minute))
	c.EndListen(now.Add(-22 * time.Hour))

	// A 30-second skip: its title is not taste.
	c.StartListen("s2", now.Add(-3*24*time.Hour))
	c.NoteTitle("Skip Artist - Gone", now.Add(-3*24*time.Hour+10*time.Second))
	c.EndListen(now.Add(-3*24*time.Hour + 30*time.Second))

	// An old listen: real, but its titles are outside the 90-day window.
	c.StartListen("s3", now.Add(-100*24*time.Hour))
	c.NoteTitle("Old Artist - Aged", now.Add(-100*24*time.Hour+time.Minute))
	c.EndListen(now.Add(-100*24*time.Hour + 2*time.Hour))

	// The currently open listen counts — what is on the radio right now is
	// the freshest taste signal there is.
	c.StartListen("s4", now.Add(-5*time.Minute))
	c.NoteTitle("Live Artist - Now", now.Add(-2*time.Minute))

	taste, err := c.Taste(now)
	if err != nil {
		t.Fatalf("Taste: %v", err)
	}
	plays := map[string]int{}
	for _, a := range taste.TopArtists {
		plays[a.Artist] = a.Plays
	}
	if plays["Alpha Artist"] != 2 {
		t.Fatalf("Alpha plays = %d, want 2 (artists %v)", plays["Alpha Artist"], taste.TopArtists)
	}
	if plays["Live Artist"] != 1 {
		t.Fatalf("open-listen artist missing: %v", taste.TopArtists)
	}
	if plays["Skip Artist"] != 0 {
		t.Fatal("skipped-listen artist should not count")
	}
	if plays["Old Artist"] != 0 {
		t.Fatal("90-day-old artist should not count")
	}
	if len(taste.TopArtists) != 2 || taste.TopArtists[0].Artist != "Alpha Artist" {
		t.Fatalf("artists = %v, want Alpha Artist leading", taste.TopArtists)
	}
}

func TestTasteTags(t *testing.T) {
	c := openTestCore(t)
	now := time.Date(2026, 2, 10, 20, 0, 0, 0, time.Local)
	c.StartListen("s1", now.Add(-14*time.Hour))
	c.EndListen(now.Add(-12 * time.Hour))

	// Fresh rows keep their weight; an ancient row has decayed almost all
	// the way back to the prior, and a prior-strength row never had any.
	c.tags["jazz"] = banditRow{Alpha: 5, UpdatedAt: now}
	c.tags["ambient"] = banditRow{Alpha: 2, UpdatedAt: now}
	c.tags["old"] = banditRow{Alpha: 3, UpdatedAt: now.Add(-365 * 24 * time.Hour)}
	c.tags["prior"] = banditRow{Alpha: 1, UpdatedAt: now}

	taste, err := c.Taste(now)
	if err != nil {
		t.Fatalf("Taste: %v", err)
	}
	// Decay only approaches the prior, so the year-old row still carries a
	// sliver of weight; it is history, not taste, and stays off the list.
	if len(taste.TopTags) != 2 || taste.TopTags[0].Tag != "jazz" || taste.TopTags[1].Tag != "ambient" {
		t.Fatalf("TopTags = %v, want jazz then ambient", taste.TopTags)
	}
}

func TestTasteStationsAndLoved(t *testing.T) {
	c := openTestCore(t)
	now := time.Date(2026, 2, 10, 20, 0, 0, 0, time.Local)

	c.StartListen("s1", now.Add(-24*time.Hour))
	c.EndListen(now.Add(-24*time.Hour + 2*time.Hour))
	c.StartListen("s2", now.Add(-2*24*time.Hour))
	c.EndListen(now.Add(-2*24*time.Hour + 30*time.Minute))
	// Started more than 30 days ago: outside the window even though the
	// listen itself ran long.
	c.StartListen("s3", now.Add(-40*24*time.Hour))
	c.EndListen(now.Add(-40*24*time.Hour + 5*time.Hour))

	// One loved track via the real path.
	c.StartListen("s1", now.Add(-time.Hour))
	c.NoteTitle("Loved Artist - Loved Track", now.Add(-59*time.Minute))
	c.Love(now.Add(-59 * time.Minute))
	c.EndListen(now.Add(-30 * time.Minute))

	taste, err := c.Taste(now)
	if err != nil {
		t.Fatalf("Taste: %v", err)
	}
	if len(taste.TopStations) != 2 {
		t.Fatalf("TopStations = %v, want s1 and s2 only", taste.TopStations)
	}
	if taste.TopStations[0].UUID != "s1" || taste.TopStations[0].Total != 2*time.Hour+30*time.Minute {
		t.Fatalf("leader = %+v", taste.TopStations[0])
	}
	if taste.LovedTracks != 1 {
		t.Fatalf("LovedTracks = %d, want 1", taste.LovedTracks)
	}
}

func TestTasteEmpty(t *testing.T) {
	c := openTestCore(t)
	taste, err := c.Taste(time.Now())
	if err != nil {
		t.Fatalf("Taste: %v", err)
	}
	if taste.Total != 0 || len(taste.TopArtists) != 0 || len(taste.TopTags) != 0 {
		t.Fatalf("fresh core should have an empty taste: %+v", taste)
	}
}
