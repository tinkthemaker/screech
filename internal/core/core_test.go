package core

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestCore(t *testing.T) *Core {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestColdStartTune(t *testing.T) {
	c := openTestCore(t)
	if c.StationCount() == 0 {
		t.Fatal("seeds should be present on open")
	}
	pick, err := c.Tune(time.Now())
	if err != nil {
		t.Fatalf("cold-start Tune: %v", err)
	}
	if pick.Station.StreamURL() == "" {
		t.Fatal("picked station has no stream URL")
	}
	if pick.Reason == "" {
		t.Fatal("every pick must carry a reason")
	}
}

func TestNeverRepeatPressure(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p1, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p1.Station.UUID, now)
	for i := 0; i < 12; i++ {
		now = now.Add(10 * time.Minute)
		p2, err := c.Tune(now)
		if err != nil {
			t.Fatal(err)
		}
		if p2.Station.UUID == c.currentUUID {
			t.Fatalf("tune %d landed on the current station", i)
		}
		c.StartListen(p2.Station.UUID, now)
	}
}

func TestListenLoveRoundtrip(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)

	tr, ok, suspect := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(time.Minute))
	if !ok || suspect {
		t.Fatalf("NoteTitle: ok=%v suspect=%v", ok, suspect)
	}
	if tr.ArtistKey != "stellardrone" {
		t.Fatalf("artist key: %q", tr.ArtistKey)
	}

	loved, hadTrack := c.Love(now.Add(2 * time.Minute))
	if !hadTrack || loved.ArtistKey != "stellardrone" {
		t.Fatalf("Love: hadTrack=%v key=%q", hadTrack, loved.ArtistKey)
	}
	if !c.loved["stellardrone"] {
		t.Fatal("loved set not updated")
	}

	// End with a long listen: alpha should rise above prior on both rows.
	c.EndListen(now.Add(25 * time.Minute))
	rows := c.bandit[p.Station.UUID]
	if rows == nil {
		t.Fatal("no bandit rows written")
	}
	all := rows[DaypartAll]
	if all.Alpha <= 1.0 {
		t.Fatalf("all-time alpha should exceed prior after love+listen: %v", all.Alpha)
	}

	// Reopen: state must persist.
	dbPath := "" // core doesn't expose it; verify via fresh queries instead
	_ = dbPath
	lovedKeys, err := c.store.LovedArtistKeys()
	if err != nil || !lovedKeys["stellardrone"] {
		t.Fatalf("loved not persisted: %v %v", lovedKeys, err)
	}
}

func TestFingerprintOverlapFavorsSharedObscureArtists(t *testing.T) {
	sa := map[string]map[string]bool{
		"st-obscure": {"rare artist": true},
		"st-pop-1":   {"chart artist": true},
		"st-pop-2":   {"chart artist": true},
		"st-pop-3":   {"chart artist": true},
		"st-pop-4":   {"chart artist": true},
	}
	fp := newFingerprints(sa)
	loved := map[string]bool{"rare artist": true, "chart artist": true}

	rareScore, _ := fp.lovedOverlap("st-obscure", loved)
	popScore, _ := fp.lovedOverlap("st-pop-1", loved)
	if rareScore <= popScore {
		t.Errorf("IDF weighting should favor the shared obscure artist: rare %v pop %v", rareScore, popScore)
	}
}

func TestFailedStationsLeavePool(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	// Fail every station except one three times; tune must land on the survivor.
	survivor := c.stations[0].UUID
	for i := range c.stations {
		if c.stations[i].UUID == survivor {
			continue
		}
		for j := 0; j < maxFailCount; j++ {
			c.MarkStationFailed(c.stations[i].UUID)
		}
	}
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Station.UUID != survivor {
		t.Fatalf("expected survivor %s, got %s", survivor, p.Station.UUID)
	}
}
