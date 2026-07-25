package core

import (
	"testing"
	"time"
)

func twoStations(t *testing.T, c *Core) (string, string) {
	t.Helper()
	if len(c.stations) < 2 {
		t.Fatalf("need two seed stations, have %d", len(c.stations))
	}
	return c.stations[0].UUID, c.stations[1].UUID
}

// A trackless love is a statement about one stream. Loving a second silent
// stream must love it, not silently un-love the first.
func TestTracklessLoveIsScopedToItsStation(t *testing.T) {
	c := openTestCore(t)
	a, b := twoStations(t, c)
	now := time.Now()

	c.StartListen(a, now)
	if _, _, loved := c.Love(now); !loved {
		t.Fatal("first trackless love did not take")
	}

	c.StartListen(b, now)
	if _, _, loved := c.Love(now); !loved {
		t.Fatal("loving station B read as un-loving station A")
	}

	// A's row must have survived B's press.
	exists, err := c.store.TracklessStationLoveExists(a)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("station A's trackless love was deleted by a press on station B")
	}
}

// Returning a love must leave the station exactly as untouched as one that
// was never loved, never worse.
func TestUnloveNeverPushesCountsBelowThePrior(t *testing.T) {
	c := openTestCore(t)
	a, _ := twoStations(t, c)
	loved := time.Now()

	c.StartListen(a, loved)
	c.Love(loved)

	// Decay eats part of the boost before the user changes their mind.
	returned := loved.Add(30 * 24 * time.Hour)
	if _, _, still := c.Love(returned); still {
		t.Fatal("second press did not un-love")
	}

	for _, dp := range []string{DaypartAll, DaypartFor(loved)} {
		row, ok := c.bandit[a][dp]
		if !ok {
			t.Fatalf("no bandit row for daypart %q", dp)
		}
		if row.Alpha < countFloor {
			t.Errorf("daypart %q: alpha %v fell below the %v prior", dp, row.Alpha, countFloor)
		}
		if row.Beta < countFloor {
			t.Errorf("daypart %q: beta %v fell below the %v prior", dp, row.Beta, countFloor)
		}
	}
}

func TestUnloveDoesNotStrandTagAffinityBelowThePrior(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	c.mu.Lock()
	c.bumpTagLocked("ambient", -5.0, now)
	weight := c.tags["ambient"].Alpha
	c.mu.Unlock()
	if weight < countFloor {
		t.Errorf("tag weight %v fell below the %v prior", weight, countFloor)
	}
}

// A stream that sends a bare title with no " - " separator parses to a
// track with an empty artist key. That is still a track, so the toggle has
// to work on it in both directions.
func TestLoveTogglesOnATrackWithNoArtist(t *testing.T) {
	c := openTestCore(t)
	a, _ := twoStations(t, c)
	now := time.Now()

	c.StartListen(a, now)
	_, ok, _, _ := c.NoteTitle("Untitled Broadcast", now)
	if !ok {
		t.Fatal("bare title did not parse as a track")
	}
	if c.curTrack.ArtistKey != "" {
		t.Fatalf("expected an empty artist key, got %q", c.curTrack.ArtistKey)
	}

	if _, hadTrack, loved := c.Love(now); !loved || !hadTrack {
		t.Fatalf("love on a bare title: loved=%v hadTrack=%v, want true/true", loved, hadTrack)
	}
	c.mu.Lock()
	shown := c.trackLovedLocked()
	c.mu.Unlock()
	if !shown {
		t.Error("a loved bare-title track does not report as loved, so its heart never lights")
	}
	if _, _, loved := c.Love(now); loved {
		t.Error("second press did not un-love a bare-title track")
	}
}
