package core

import (
	"fmt"
	"testing"
	"time"
)

func TestCustomUUIDIsTheLink(t *testing.T) {
	a := CustomUUID("https://Radio.Example/live?x=1")
	if !IsCustom(a) || len(a) > 64 {
		t.Fatalf("CustomUUID = %q, want a short custom: id", a)
	}
	if b := CustomUUID("HTTPS://radio.example/live?x=1"); b != a {
		t.Errorf("scheme and host case must not change the id: %q vs %q", a, b)
	}
	if b := CustomUUID("https://radio.example/LIVE?x=1"); b == a {
		t.Error("a different path is a different station")
	}
}

// The weekly directory sync prunes what the directory no longer lists, and
// the directory never lists the listener's own stations.
func TestCustomStationsSurvivePruneStale(t *testing.T) {
	c := openTestCore(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	mine := []Station{
		{Name: "Mine", URL: "https://mine.example/stream"},
		{Name: "Also Mine", URL: "http://also.example/a.aac", Tags: "jazz"},
	}
	if err := c.SetCustomStations(mine, old); err != nil {
		t.Fatal(err)
	}
	if err := c.store.UpsertStations([]Station{{UUID: "gone-plain", Name: "G", URL: "http://x/g", URLResolved: "http://x/g"}}, old); err != nil {
		t.Fatal(err)
	}
	pruned, err := c.store.PruneStale(time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d, want only the plain directory station", pruned)
	}
	for _, st := range mine {
		if _, ok := c.StationByUUID(CustomUUID(st.URL)); !ok {
			t.Errorf("%s missing from the cache", st.Name)
		}
	}
	left, err := c.store.LoadStations()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, st := range left {
		if IsCustom(st.UUID) {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d custom rows after PruneStale, want 2", n)
	}
}

// The config file is the record: a link taken out of it leaves the cache
// (and its preset slot) on the next launch, its history stays, and putting
// the same link back finds that history again.
func TestSetCustomStationsFollowsConfig(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	a := Station{Name: "A \x1b]0;pwned\x07Station", URL: " https://a.example/live ", Tags: "ambient"}
	b := Station{Name: "B", URL: "https://b.example/live"}
	if err := c.SetCustomStations([]Station{a, b, b}, now); err != nil {
		t.Fatal(err)
	}
	ua := CustomUUID("https://a.example/live")
	st, ok := c.StationByUUID(ua)
	if !ok {
		t.Fatal("A should be cached under the hash of its link")
	}
	if st.Name != "A Station" || st.StreamURL() != "https://a.example/live" || !st.LastCheckOK {
		t.Errorf("cached A = %+v, want cleaned text and the link as its stream", st)
	}
	if c.StationCount() != len(seedStations)+2 {
		t.Errorf("StationCount = %d, want seeds plus 2 (the repeat of B collapses)", c.StationCount())
	}
	c.StartListen(ua, now.Add(-time.Hour))
	c.EndListen(now)
	if err := c.store.SetPreset(4, ua, now); err != nil {
		t.Fatal(err)
	}

	if err := c.SetCustomStations([]Station{b}, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.StationByUUID(ua); ok {
		t.Fatal("A was taken out of the config; it must leave the cache")
	}
	if presets, _ := c.store.Presets(); presets[4] != "" {
		t.Errorf("A's preset slot should go with it, have %v", presets)
	}

	if err := c.SetCustomStations([]Station{a, b}, now); err != nil {
		t.Fatal(err)
	}
	top, err := c.TopListened(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) == 0 || top[0].UUID != ua || top[0].Name != "A Station" || top[0].Total < 59*time.Minute {
		t.Fatalf("TopListened = %+v, want A's hour back under its name", top)
	}
}

// First-run detection asks whether the directory was ever fetched; links
// from config.toml must not make an empty cache look synced.
func TestNeedsSyncIgnoresCustomStations(t *testing.T) {
	c := openTestCore(t)
	var mine []Station
	for i := 0; i < 2*len(seedStations); i++ {
		mine = append(mine, Station{Name: fmt.Sprint("Link ", i), URL: fmt.Sprintf("https://link%d.example/", i)})
	}
	if err := c.SetCustomStations(mine, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !c.NeedsSync() {
		t.Fatal("seeds plus custom stations is still a cache that has never synced")
	}
	if c.SyncStale(20000) {
		t.Fatal("the first-run sync owns this case, not the background refresh")
	}
}

// Untagged links can't win tag affinity or artist overlap, so the pool
// seats them directly: a few per pick, rotating when there are more.
func TestPoolSeatsCustomStations(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	var dir []Station
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("dir-%04d", i)
		dir = append(dir, Station{UUID: id, Name: id, URL: "http://x/" + id, URLResolved: "http://x/" + id, Tags: "rock", LastCheckOK: true})
	}
	if err := c.store.UpsertStations(dir, now); err != nil {
		t.Fatal(err)
	}
	if err := c.store.PutTagAffinity("rock", 6, now); err != nil {
		t.Fatal(err)
	}
	links := []Station{{Name: "One", URL: "https://one.example/"}, {Name: "Two", URL: "https://two.example/"}}
	if err := c.SetCustomStations(links, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		seated := 0
		for _, cd := range c.buildPool(now, c.rng) {
			if IsCustom(cd.st.UUID) {
				seated++
				if r := c.reason(cd, DaypartFor(now), now); r != "added by you" {
					t.Errorf("reason for a link = %q, want added by you", r)
				}
			}
		}
		if seated != 2 {
			t.Fatalf("pool %d seated %d links, want both", i, seated)
		}
	}

	var more []Station
	for i := 0; i < 3*poolCustomSlice; i++ {
		more = append(more, Station{Name: fmt.Sprint("Link ", i), URL: fmt.Sprintf("https://many%d.example/", i)})
	}
	if err := c.SetCustomStations(more, now); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		seated := 0
		for _, cd := range c.buildPool(now, c.rng) {
			if IsCustom(cd.st.UUID) {
				seated++
				seen[cd.st.UUID] = true
			}
		}
		if seated < poolCustomSlice {
			t.Fatalf("pool %d seated %d links, want at least %d", i, seated, poolCustomSlice)
		}
	}
	if len(seen) != len(more) {
		t.Errorf("%d of %d links ever seated; more links than seats should rotate", len(seen), len(more))
	}
}
