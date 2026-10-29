package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// errNoTestNetwork is what any directory call out of a plain test core
// hits. Tests that exercise the directory use pointCoreAt / openTestCoreAt
// to aim at an httptest server instead.
var errNoTestNetwork = errors.New("test core has no network; use pointCoreAt")

type deadTransport struct{}

func (deadTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errNoTestNetwork
}

// openTestCore builds a Core whose directory client is dead on arrival: the
// httptest server is closed before the core returns, and the transport
// refuses outright, so neither a stray call nor the pickServer fallback
// (which fires after dropServer wipes the dead base) can reach the real
// radio-browser.
func openTestCore(t *testing.T) *Core {
	t.Helper()
	srv := httptest.NewServer(http.NewServeMux())
	c, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	pointCoreAt(c, srv)
	srv.Close()
	c.rb.client = &http.Client{Transport: deadTransport{}}
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

	loved, hadTrack, lovedNow, _ := c.Love(now.Add(2 * time.Minute))
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

	for dp, r := range c.bandit[p.Station.UUID] {
		if r.Beta > 1.0 {
			t.Fatalf("tune-dead applied a skip beta on %s: %+v", dp, r)
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
	rows := c.bandit[next.Station.UUID]
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
	if _, _, lovedNow, _ := c.Love(now.Add(2 * time.Minute)); !lovedNow {
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
	_, hadTrack, lovedNow, _ := c.Love(now.Add(3 * time.Minute))
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
	if _, hadTrack, lovedNow, _ := c.Love(now.Add(time.Minute)); hadTrack || !lovedNow {
		t.Fatalf("trackless love: hadTrack=%v lovedNow=%v", hadTrack, lovedNow)
	}
	if _, _, lovedNow, _ := c.Love(now.Add(2 * time.Minute)); lovedNow {
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

// A superseding Play at the same stream must not split a session into two
// listen rows, and a different station must bank the first before opening
// its own — the late-lock-while-offline fix depends on both.
func TestStartListenIsIdempotent(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()

	c.StartListen("s1", now)
	c.StartListen("s1", now.Add(5*time.Second)) // same stream: keep the row
	var n int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("double StartListen on one station left %d rows, want 1", n)
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens WHERE ended_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the kept listen should still be open, %d open rows", n)
	}

	c.StartListen("s2", now.Add(10*time.Second)) // different station: bank the first
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("switching stations should leave 2 rows, got %d", n)
	}
	var ended int64
	if err := c.store.db.QueryRow(`SELECT ended_at FROM listens WHERE station_uuid='s1'`).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if ended <= now.Unix() {
		t.Fatalf("the superseded listen should be banked, ended_at=%d", ended)
	}
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens WHERE ended_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("only the current listen may be open, %d open rows", n)
	}
}

func listenRow(t *testing.T, c *Core, n int) (skipFast int, open bool) {
	t.Helper()
	var ended any
	row := c.store.db.QueryRow(`SELECT skip_fast, ended_at FROM listens ORDER BY id LIMIT 1 OFFSET ?`, n-1)
	if err := row.Scan(&skipFast, &ended); err != nil {
		t.Fatal(err)
	}
	return skipFast, ended == nil
}

// Pause/resume splits a listen into segments; the fast-skip call on resume
// is judged on the session's whole dwell.
func TestResumeListenJudgesSessionDwell(t *testing.T) {
	t.Run("long dwell is no fast skip", func(t *testing.T) {
		c := openTestCore(t)
		now := time.Now()
		p, err := c.Tune(now)
		if err != nil {
			t.Fatal(err)
		}
		c.StartListen(p.Station.UUID, now)
		c.EndListen(now.Add(2 * time.Minute))
		c.ResumeListen(p.Station.UUID, now.Add(3*time.Minute))

		if _, err := c.Tune(now.Add(3*time.Minute + 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("pause+resume should leave 2 listen rows, got %d", n)
		}
		skip, open := listenRow(t, c, 2)
		if skip != 0 || open {
			t.Fatalf("second segment: skip_fast=%d open=%v, want 0 false", skip, open)
		}
		if r := c.bandit[p.Station.UUID]; r != nil && r[DaypartAll].Beta > 1.0 {
			t.Fatalf("beta rose to %v on a long-session close", r[DaypartAll].Beta)
		}
	})

	t.Run("short dwell is a fast skip", func(t *testing.T) {
		c := openTestCore(t)
		now := time.Now()
		p, err := c.Tune(now)
		if err != nil {
			t.Fatal(err)
		}
		c.StartListen(p.Station.UUID, now)
		c.EndListen(now.Add(30 * time.Second))
		c.ResumeListen(p.Station.UUID, now.Add(time.Minute))

		if _, err := c.Tune(now.Add(time.Minute + 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		skip, open := listenRow(t, c, 2)
		if skip != 1 || open {
			t.Fatalf("second segment: skip_fast=%d open=%v, want 1 false", skip, open)
		}
		if r := c.bandit[p.Station.UUID]; r == nil || r[DaypartAll].Beta <= 1.0 {
			t.Fatalf("session-dwell fast skip should charge beta, got %v", r)
		}
	})
}

func TestResumeListenKeepsTrack(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)
	if _, ok, _, _ := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(time.Minute)); !ok {
		t.Fatal("NoteTitle did not take")
	}
	c.EndListen(now.Add(2 * time.Minute))
	c.ResumeListen(p.Station.UUID, now.Add(3*time.Minute))

	// Love still targets the track on screen from before the pause.
	tr, hadTrack, lovedNow, err := c.Love(now.Add(4 * time.Minute))
	if err != nil || !hadTrack || !lovedNow || tr.ArtistKey != "stellardrone" {
		t.Fatalf("Love after resume: %v hadTrack=%v loved=%v key=%q", err, hadTrack, lovedNow, tr.ArtistKey)
	}

	// The stream re-sending the same ICY title must not log the track twice.
	if _, ok, _, _ := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(5*time.Minute)); !ok {
		t.Fatal("re-sent title should still report the current track")
	}
	var n int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM tracks_heard`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("same title across a resume logged %d rows, want 1", n)
	}
}

func TestResumeListenDifferentStationStartsFresh(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)
	if _, ok, _, _ := c.NoteTitle("Stellardrone - Billions and Billions", now.Add(time.Minute)); !ok {
		t.Fatal("NoteTitle did not take")
	}
	c.EndListen(now.Add(2 * time.Minute))
	c.ResumeListen("other-station", now.Add(3*time.Minute))

	if c.currentUUID != "other-station" || c.previousUUID != p.Station.UUID {
		t.Fatalf("resume to a different station should move like StartListen: cur=%q prev=%q",
			c.currentUUID, c.previousUUID)
	}
	if c.hasTrack {
		t.Fatal("a different station must reset the track on screen")
	}
}

func TestResumeListenWhileOpenIsNoop(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)
	c.ResumeListen(p.Station.UUID, now.Add(10*time.Second))
	var n int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ResumeListen over an open listen added a row, got %d", n)
	}
}

// The station sheet reads the same rows the picker and readouts use:
// finished segments of a minute or more count as visits, an open listen
// contributes nothing, and loves split into named tracks and the bare
// station heart.
func TestStationInfo(t *testing.T) {
	c := openTestCore(t)
	now := time.Now().Truncate(time.Second)
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	uuid := p.Station.UUID

	seed := func(start, end time.Time) {
		id, err := c.store.InsertListen(uuid, start, DaypartFor(start))
		if err != nil {
			t.Fatal(err)
		}
		if !end.IsZero() {
			if err := c.store.FinishListen(id, end, false, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(now.Add(-48*time.Hour), now.Add(-46*time.Hour))
	seed(now.Add(-24*time.Hour), now.Add(-24*time.Hour+90*time.Second))
	seed(now.Add(-2*time.Hour), now.Add(-2*time.Hour+30*time.Second)) // a visit too short to count
	seed(now.Add(-time.Minute), time.Time{})                          // still open: contributes nothing

	// Trackless love first — a heart for the station itself — then two
	// named tracks.
	c.StartListen(uuid, now)
	if _, _, loved, err := c.Love(now); err != nil || !loved {
		t.Fatalf("trackless love: loved=%v err=%v", loved, err)
	}
	if _, ok, _, _ := c.NoteTitle("Artist One - Song One", now.Add(time.Second)); !ok {
		t.Fatal("first NoteTitle did not take")
	}
	if _, _, loved, err := c.Love(now.Add(time.Second)); err != nil || !loved {
		t.Fatalf("track love one: loved=%v err=%v", loved, err)
	}
	if _, ok, _, _ := c.NoteTitle("Artist Two - Song Two", now.Add(2*time.Second)); !ok {
		t.Fatal("second NoteTitle did not take")
	}
	if _, _, loved, err := c.Love(now.Add(2 * time.Second)); err != nil || !loved {
		t.Fatalf("track love two: loved=%v err=%v", loved, err)
	}
	c.EndListen(now.Add(3 * time.Second))

	info, err := c.StationInfo(uuid, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wantListened := 2*time.Hour + 90*time.Second + 30*time.Second + 3*time.Second
	if info.Listened != wantListened {
		t.Fatalf("Listened = %v, want %v", info.Listened, wantListened)
	}
	if info.Visits != 2 {
		// The sub-minute and open rows count toward time, not visits.
		t.Fatalf("Visits = %d, want 2", info.Visits)
	}
	if !info.FirstHeard.Equal(now.Add(-48 * time.Hour)) {
		t.Fatalf("FirstHeard = %v", info.FirstHeard)
	}
	if !info.LastHeard.Equal(now) {
		t.Fatalf("LastHeard = %v, want the live listen start %v", info.LastHeard, now)
	}
	if info.LovedTracks != 2 {
		t.Fatalf("LovedTracks = %d, want 2 named tracks", info.LovedTracks)
	}
	if !info.Heard {
		t.Fatal("a loved, listened-to station should count as heard")
	}
	daypart := DaypartFor(now.Add(4 * time.Second))
	if info.Daypart != daypart {
		t.Fatalf("Daypart = %q, want %q", info.Daypart, daypart)
	}
	wantMatch := c.posteriorMean(
		candidate{st: &p.Station, tagScore: c.tagScore(&p.Station, now.Add(4*time.Second))},
		daypart, now.Add(4*time.Second))
	if info.Match != wantMatch {
		t.Fatalf("Match = %v, want posteriorMean %v", info.Match, wantMatch)
	}
}

func TestStationInfoUnheard(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.StationInfo(p.Station.UUID, now)
	if err != nil {
		t.Fatal(err)
	}
	if info.Heard || info.Match != 0 {
		t.Fatalf("never-heard station: Heard=%v Match=%v", info.Heard, info.Match)
	}
	if !info.FirstHeard.IsZero() || info.Visits != 0 || info.Listened != 0 {
		t.Fatalf("never-heard station has history: %+v", info)
	}
	if _, err := c.StationInfo("no-such-station", now); err == nil {
		t.Fatal("unknown uuid should error")
	}
}
