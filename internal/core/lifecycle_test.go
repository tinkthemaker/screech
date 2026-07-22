package core

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestPruneStaleKeepsHistoryBearers(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)

	// Four directory stations fetched long ago.
	mk := func(id string) Station {
		return Station{UUID: id, Name: id, URL: "http://x/" + id, URLResolved: "http://x/" + id, LastCheckOK: true}
	}
	stale := []Station{mk("gone-plain"), mk("kept-bandit"), mk("kept-preset"), mk("kept-listen")}
	if err := c.store.UpsertStations(stale, old); err != nil {
		t.Fatal(err)
	}

	// History: bandit row, preset, listen.
	if err := c.store.PutBandit("kept-bandit", DaypartAll, 2, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := c.store.SetPreset(1, "kept-preset", now); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.InsertListen("kept-listen", now, DaypartAll); err != nil {
		t.Fatal(err)
	}

	pruned, err := c.store.PruneStale(now.Add(-time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("expected exactly the plain station pruned, got %d", pruned)
	}

	left, err := c.store.LoadStations()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, st := range left {
		have[st.UUID] = true
	}
	if have["gone-plain"] {
		t.Error("history-free stale station should be pruned")
	}
	for _, keep := range []string{"kept-bandit", "kept-preset", "kept-listen", "seed:wfmu"} {
		if !have[keep] {
			t.Errorf("%s should survive pruning", keep)
		}
	}
}

func TestPruneStaleSparesResumeStation(t *testing.T) {
	c := openTestCore(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := c.store.UpsertStations([]Station{{UUID: "resume-me", Name: "R", URL: "http://x/r", URLResolved: "http://x/r"}}, old); err != nil {
		t.Fatal(err)
	}
	pruned, err := c.store.PruneStale(time.Now(), "resume-me")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Fatalf("resume station must survive, pruned %d", pruned)
	}
}

func TestSyncStale(t *testing.T) {
	c := openTestCore(t)
	// Seeds only: the blocking first-run sync owns it, so not "stale".
	if c.SyncStale(20000) {
		t.Fatal("seeds-only cache should route to first-run sync, not background refresh")
	}

	// Grow past the seed threshold so NeedsSync is false.
	var extra []Station
	for i := 0; i < 30; i++ {
		id := string(rune('a'+i%26)) + "-extra"
		extra = append(extra, Station{UUID: id + string(rune('0'+i/26)), Name: id, URL: "http://x/" + id, URLResolved: "http://x/" + id})
	}
	if err := c.store.UpsertStations(extra, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_ = c.reload()
	c.mu.Unlock()

	// No last_sync recorded: stale.
	if !c.SyncStale(40) {
		t.Fatal("missing last_sync should be stale")
	}

	// Fresh sync stamp, cache >= half the limit: not stale.
	_ = c.store.SetMeta("last_sync", fmtUnix(time.Now()))
	if c.SyncStale(40) {
		t.Fatal("fresh stamp with full-enough cache should not be stale")
	}

	// Same stamp but the limit grew: cache is under half, stale again.
	if !c.SyncStale(200) {
		t.Fatal("cache under half the limit should be stale")
	}

	// Old stamp: stale regardless of size.
	_ = c.store.SetMeta("last_sync", fmtUnix(time.Now().Add(-8*24*time.Hour)))
	if !c.SyncStale(40) {
		t.Fatal("week-old stamp should be stale")
	}
}

func TestUpsertHealsFailCount(t *testing.T) {
	c := openTestCore(t)
	st := Station{UUID: "healer", Name: "H", URL: "http://x/h", URLResolved: "http://x/h"}
	if err := c.store.UpsertStations([]Station{st}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := c.store.BumpFailCount("healer", 1); err != nil {
			t.Fatal(err)
		}
	}
	// A re-sync that vouches for the station heals one strike.
	if err := c.store.UpsertStations([]Station{st}, time.Now()); err != nil {
		t.Fatal(err)
	}
	sts, _ := c.store.LoadStations()
	for _, s := range sts {
		if s.UUID == "healer" && s.FailCount != 2 {
			t.Fatalf("expected fail_count healed 3 -> 2, got %d", s.FailCount)
		}
	}
}

// A listen left open by a dead previous process is reaped at startup: it
// closes at its start time and banks no bandit credit. The alternative —
// leaving ended_at NULL forever — poisons history (open listens count as
// zero) and pruning (they count as history).
func TestStartupReaperClosesZombieListens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reap.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed session: a listen started long ago, never ended.
	old := time.Now().Add(-3 * time.Hour)
	if _, err := c.store.InsertListen("seed:wfmu", old, DaypartFor(old)); err != nil {
		t.Fatal(err)
	}
	c.Close()

	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The zombie is now closed at zero duration, so it neither shows in the
	// listen-time leaderboard nor banks bandit alpha for the station.
	top, err := c.TopListened(9)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		if e.UUID == "seed:wfmu" {
			t.Fatalf("reaped zombie listen should not earn leaderboard time: %+v", e)
		}
	}
	if rows := c.bandit["seed:wfmu"]; rows != nil {
		t.Fatalf("reaped zombie listen should bank no bandit credit: %+v", rows)
	}
	// But the row exists, so the station still counts as "has history" for
	// pruning purposes — an old habit the directory dropped isn't forgotten.
	var open int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens WHERE ended_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("reaper left %d open listens", open)
	}
}

// A clean Close ends the active listen, banking the dwell-time credit.
func TestCloseEndsActiveListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close.db")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-20 * time.Minute)
	c.StartListen("seed:wfmu", now)
	c.listenStart = now // pretend we've been playing a while
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var open int
	if err := c.store.db.QueryRow(`SELECT COUNT(*) FROM listens WHERE ended_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("Close should end the active listen, %d still open", open)
	}
	rows := c.bandit["seed:wfmu"]
	if rows == nil || rows[DaypartAll].Alpha <= 1.0 {
		t.Fatalf("a 20-minute clean listen should bank alpha: %+v", rows)
	}
}

func fmtUnix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}
