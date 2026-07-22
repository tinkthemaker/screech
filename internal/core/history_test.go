package core

import (
	"testing"
	"time"
)

func TestTopListened(t *testing.T) {
	c := openTestCore(t)
	base := time.Now().Add(-24 * time.Hour)

	listen := func(uuid string, start time.Time, dur time.Duration) {
		id, err := c.store.InsertListen(uuid, start, DaypartFor(start))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.store.FinishListen(id, start.Add(dur), false, false); err != nil {
			t.Fatal(err)
		}
	}

	// WFMU: two listens totalling 90 minutes. Space Station: one 60-minute
	// listen. Groove Salad: 5 minutes. Plus one open listen (no end) that
	// must count as zero and not crash the query.
	listen("seed:wfmu", base, 60*time.Minute)
	listen("seed:wfmu", base.Add(2*time.Hour), 30*time.Minute)
	listen("seed:somafm-spacestation", base.Add(4*time.Hour), 60*time.Minute)
	listen("seed:somafm-groovesalad", base.Add(6*time.Hour), 5*time.Minute)
	if _, err := c.store.InsertListen("seed:somafm-dronezone", base.Add(7*time.Hour), DaypartNight); err != nil {
		t.Fatal(err)
	}

	top, err := c.TopListened(9)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 {
		t.Fatalf("expected 3 ranked stations (open listen excluded), got %d: %+v", len(top), top)
	}
	if top[0].UUID != "seed:wfmu" || top[0].Total != 90*time.Minute {
		t.Fatalf("rank 1 wrong: %+v", top[0])
	}
	if top[1].UUID != "seed:somafm-spacestation" || top[1].Total != 60*time.Minute {
		t.Fatalf("rank 2 wrong: %+v", top[1])
	}
	if top[0].Name != "WFMU" {
		t.Fatalf("name join failed: %q", top[0].Name)
	}

	// Limit respected.
	top2, err := c.TopListened(2)
	if err != nil || len(top2) != 2 {
		t.Fatalf("limit: %v %v", top2, err)
	}
}

func TestRecentlyHeardIncludesLoveAndNewestFirst(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	c.StartListen("seed:wfmu", now)
	c.NoteTitle("Artist One - First", now)
	c.NoteTitle("Artist Two - Second", now.Add(time.Minute))
	c.Love(now.Add(time.Minute))

	got, err := c.RecentlyHeard(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "Second" || got[1].Title != "First" {
		t.Fatalf("recent order: %+v", got)
	}
	if !got[0].Loved || got[1].Loved {
		t.Fatalf("love state: %+v", got)
	}
	if got[0].StationName != "WFMU" {
		t.Fatalf("station join: %+v", got[0])
	}
}

func TestLovedTracksDeduplicatesAndCanForget(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	c.StartListen("seed:wfmu", now)
	c.NoteTitle("Artist One - First", now)
	c.Love(now.Add(time.Minute))
	c.NoteTitle("Artist Two - Second", now.Add(3*time.Minute))
	c.Love(now.Add(4*time.Minute))

	got, err := c.LovedTracks(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two tracks, got %d: %+v", len(got), got)
	}
	if got[0].Title != "Second" || got[1].Title != "First" {
		t.Fatalf("loved order: %+v", got)
	}

	removed, err := c.ForgetLovedTrack(got[1].ArtistKey, got[1].Title)
	if err != nil || !removed {
		t.Fatalf("forget: removed=%v err=%v", removed, err)
	}
	got, err = c.LovedTracks(100)
	if err != nil || len(got) != 1 || got[0].Title != "Second" {
		t.Fatalf("library after forget: %+v err=%v", got, err)
	}
}

// Loving the same track twice is now an unlove, not a duplicate row. To
// stack LoveCount the user loves, hears the track again later, and re-loves.
func TestLoveToggleDedupesAndRestacks(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	c.StartListen("seed:wfmu", now)
	c.NoteTitle("Artist One - First", now)
	if _, _, lovedNow := c.Love(now.Add(time.Minute)); !lovedNow {
		t.Fatal("first love should apply")
	}
	if _, _, lovedNow := c.Love(now.Add(2 * time.Minute)); lovedNow {
		t.Fatal("second press on the same track should unlove")
	}
	if got, _ := c.LovedTracks(100); len(got) != 0 {
		t.Fatalf("unloved track should leave the library: %+v", got)
	}

	// Hear it again, love it again: a fresh row.
	c.NoteTitle("Artist One - First", now.Add(3*time.Minute))
	if _, _, lovedNow := c.Love(now.Add(4 * time.Minute)); !lovedNow {
		t.Fatal("re-love after re-hearing should apply")
	}
	got, _ := c.LovedTracks(100)
	if len(got) != 1 || got[0].LoveCount != 1 {
		t.Fatalf("re-loved track: %+v", got)
	}
}
