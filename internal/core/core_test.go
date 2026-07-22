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

	tr, ok, suspect, _ := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(time.Minute))
	if !ok || suspect {
		t.Fatalf("NoteTitle: ok=%v suspect=%v", ok, suspect)
	}
	if tr.ArtistKey != "stellardrone" {
		t.Fatalf("artist key: %q", tr.ArtistKey)
	}

	loved, hadTrack, lovedNow := c.Love(now.Add(2 * time.Minute))
	if !hadTrack || !lovedNow || loved.ArtistKey != "stellardrone" {
		t.Fatalf("Love: hadTrack=%v lovedNow=%v key=%q", hadTrack, lovedNow, loved.ArtistKey)
	}
	if !c.loved["stellardrone"] {
		t.Fatal("loved set not updated")
	}

	// A fresh NoteTitle on the loved track must report it as loved so the
	// UI lights the heart on tracks loved in earlier sessions.
	_, _, _, alreadyLoved := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(3*time.Minute))
	if !alreadyLoved {
		t.Fatal("NoteTitle should report a loved track as loved")
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

// TuneDead closes the failed listen without skip semantics: no beta bump,
// no skip_fast flag. A stream that never played teaches nothing about
// taste — the fail_count strike is the whole penalty.
func TestTuneDeadAppliesNoSkipPenalty(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)

	// The stream dies 10 seconds in; screech moves on. User pressed nothing.
	next, err := c.TuneDead(now.Add(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.Station.UUID == p.Station.UUID {
		t.Fatal("tune-dead must still move to a different station")
	}

	rows := c.bandit[p.Station.UUID]
	if rows != nil {
		for dp, r := range rows {
			if r.Beta > 1.0 {
				t.Fatalf("tune-dead applied a skip beta on %s: %+v", dp, r)
			}
		}
	}
	var skipFast int
	if err := c.store.db.QueryRow(`SELECT COALESCE(skip_fast, -1) FROM listens WHERE station_uuid=? ORDER BY id DESC LIMIT 1`,
		p.Station.UUID).Scan(&skipFast); err != nil {
		t.Fatal(err)
	}
	if skipFast != 0 {
		t.Fatalf("dead listen must not be recorded as a fast skip: skip_fast=%d", skipFast)
	}

	// Contrast: a user skip at the same dwell time does bump beta.
	c.StartListen(next.Station.UUID, now.Add(time.Minute))
	if _, err := c.Tune(now.Add(time.Minute + 10*time.Second)); err != nil {
		t.Fatal(err)
	}
	rows = c.bandit[next.Station.UUID]
	if rows == nil || rows[DaypartAll].Beta <= 1.0 {
		t.Fatalf("user fast skip should bump beta: %+v", rows)
	}
}

// Unlove returns exactly what love gave: station alpha and tag boosts are
// reversed (clamped at the prior), the track leaves the library, and the
// artist leaves the loved set when no other loved track by them remains.
func TestUnloveReturnsBoosts(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	st := p.Station
	c.StartListen(st.UUID, now)
	c.NoteTitle("Some Artist - Some Track", now.Add(time.Minute))

	alphaOf := func() float64 {
		rows := c.bandit[st.UUID]
		if rows == nil {
			return 1.0
		}
		r, ok := rows[DaypartAll]
		if !ok {
			return 1.0
		}
		a, _ := r.decayed(now)
		return a
	}
	tagWeightOf := func(tag string) float64 {
		if r, ok := c.tags[tag]; ok {
			return decayToward(r.Alpha, r.UpdatedAt, now)
		}
		return 1.0
	}

	baseAlpha := alphaOf()
	if _, _, lovedNow := c.Love(now.Add(2 * time.Minute)); !lovedNow {
		t.Fatal("love should apply")
	}
	lovedAlpha := alphaOf()
	if lovedAlpha <= baseAlpha {
		t.Fatalf("love should raise alpha: base %v loved %v", baseAlpha, lovedAlpha)
	}
	tags := st.TagList()
	if len(tags) == 0 {
		t.Skip("seed station without tags")
	}
	lovedTagW := tagWeightOf(tags[0])
	if lovedTagW <= 1.0 {
		t.Fatalf("love should raise the tag weight: %v", lovedTagW)
	}

	// Second press on the same track unloves.
	_, hadTrack, lovedNow := c.Love(now.Add(3 * time.Minute))
	if !hadTrack || lovedNow {
		t.Fatalf("second press should unlove the same track: hadTrack=%v lovedNow=%v", hadTrack, lovedNow)
	}
	if got := alphaOf(); got > baseAlpha+0.01 {
		t.Fatalf("unlove should return the alpha boost: base %v after %v", baseAlpha, got)
	}
	if got := tagWeightOf(tags[0]); got > 1.01 {
		t.Fatalf("unlove should return the tag boost toward the prior: %v", got)
	}
	if c.loved["some artist"] {
		t.Fatal("artist should leave the loved set when their last loved track goes")
	}
	if got, _ := c.LovedTracks(100); len(got) != 0 {
		t.Fatalf("unloved track should leave the library: %+v", got)
	}
}

// A trackless love (station has no parsed title) is also a toggle: the
// second press removes the station-credited loved row.
func TestTracklessLoveToggles(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)
	// No NoteTitle: the stream never sent one, so hasTrack is false.
	if _, hadTrack, lovedNow := c.Love(now.Add(time.Minute)); hadTrack || !lovedNow {
		t.Fatalf("trackless love: hadTrack=%v lovedNow=%v", hadTrack, lovedNow)
	}
	if _, _, lovedNow := c.Love(now.Add(2 * time.Minute)); lovedNow {
		t.Fatal("second trackless press should unlove the station")
	}
	var n int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM loved`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("station love rows should be gone: %d", n)
	}
}

func TestVolumePersistsAndClamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "volume.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Volume(); got != 100 {
		t.Fatalf("default volume=%d, want 100", got)
	}
	if err := c.SetVolume(35); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.Volume(); got != 35 {
		t.Fatalf("persisted volume=%d, want 35", got)
	}
	if err := c.SetVolume(140); err != nil {
		t.Fatal(err)
	}
	if got := c.Volume(); got != 100 {
		t.Fatalf("high clamp=%d, want 100", got)
	}
	if err := c.SetVolume(-20); err != nil {
		t.Fatal(err)
	}
	if got := c.Volume(); got != 0 {
		t.Fatalf("low clamp=%d, want 0", got)
	}
}
