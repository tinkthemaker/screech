package core

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// A favourite with no tags to carry it, on a listener who never loved a
// track there, still makes the pool on its own listening history — the
// tag slice is full of other stations and only the heard slice can bring
// it back. Twenty seeds: the random slice alone would miss it nearly
// every time.
func TestHeardFavoriteReachesPoolWithoutTagsOrLoves(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	var sts []Station
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("filler-%03d", i)
		sts = append(sts, Station{UUID: id, Name: id, URL: "http://x/" + id, URLResolved: "http://x/" + id, Tags: "jazz"})
	}
	sts = append(sts, Station{UUID: "fav", Name: "Fav", URL: "http://x/fav", URLResolved: "http://x/fav"})
	if err := c.store.UpsertStations(sts, now); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	err := c.reload()
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c.tags["jazz"] = banditRow{Alpha: 4, UpdatedAt: now}
	c.bandit["fav"] = map[string]banditRow{
		DaypartAll:      {Alpha: 5, Beta: 1, UpdatedAt: now},
		DaypartFor(now): {Alpha: 5, Beta: 1, UpdatedAt: now},
	}
	for seed := int64(0); seed < 20; seed++ {
		found := false
		for _, cd := range c.buildPool(now, rand.New(rand.NewSource(seed))) {
			found = found || cd.st.UUID == "fav"
		}
		if !found {
			t.Fatalf("seed %d: a station with a 5:1 listening record is missing from the pool", seed)
		}
	}
}

// The pool's order is fixed, so a seeded rng picks the same station every
// time — the property the eval harness's comparisons stand on.
func TestSeededPickIsReproducible(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	first := ""
	for i := 0; i < 5; i++ {
		c.rng = rand.New(rand.NewSource(7))
		p, err := c.Tune(now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = p.Station.UUID
		} else if p.Station.UUID != first {
			t.Fatalf("run %d picked %s, run 0 picked %s", i, p.Station.UUID, first)
		}
	}
}

// A station the user keeps skipping must never read as a "favorite": skips
// are beta, not evidence of love.
func TestSkippedStationIsNeverFavorite(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	st := &c.stations[0]
	c.bandit[st.UUID] = map[string]banditRow{
		DaypartAll:      {Alpha: 1, Beta: 6, UpdatedAt: now},
		DaypartFor(now): {Alpha: 1, Beta: 6, UpdatedAt: now},
	}
	cd := candidate{st: st, tagScore: c.tagScore(st, now)}
	if r := c.reason(cd, DaypartFor(now), now); strings.Contains(r, "favorite") {
		t.Fatalf("heavily skipped station labeled %q", r)
	}
}

// A curation prior is a guess, not a history: an unheard station with a
// strong tag/overlap prior must not read as a favorite.
func TestUnheardStationIsNeverFavorite(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	st := &c.stations[0]
	// Hand-built candidate with a prior strong enough that the old
	// evidence check (a-1)+(b-1) >= 3 would have misfired.
	cd := candidate{st: st, tagScore: 0.9, overlap: 0.9}
	if r := c.reason(cd, DaypartFor(now), now); strings.Contains(r, "favorite") {
		t.Fatalf("unheard station labeled %q", r)
	}
}

// Real bandit rows with volume and direction earn the label.
func TestLovedListenedStationIsFavorite(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	st := c.stations[0]
	// Force the pick: every other station is struck out.
	for i := range c.stations {
		if c.stations[i].UUID != st.UUID {
			c.stations[i].FailCount = maxFailCount
		}
	}
	c.bandit[st.UUID] = map[string]banditRow{
		DaypartAll:      {Alpha: 6, Beta: 1, UpdatedAt: now},
		DaypartFor(now): {Alpha: 6, Beta: 1, UpdatedAt: now},
	}
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Station.UUID != st.UUID {
		t.Fatalf("picked %s, want the forced candidate", p.Station.UUID)
	}
	if want := "a " + DaypartFor(now) + " favorite"; p.Reason != want {
		t.Fatalf("reason %q, want %q", p.Reason, want)
	}
}

// The batch walk must agree with per-station scoring on a randomized
// fixture, including through live notes.
func TestLovedOverlapsMatchesPerStationScoring(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	sa := map[string]map[string]bool{}
	artists := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		artists = append(artists, "artist-"+padInt(i))
	}
	for s := 0; s < 60; s++ {
		st := "st-" + padInt(s)
		sa[st] = map[string]bool{}
		for _, a := range artists {
			if rng.Intn(6) == 0 {
				sa[st][a] = true
			}
		}
	}
	loved := map[string]bool{}
	for _, a := range artists {
		if rng.Intn(4) == 0 {
			loved[a] = true
		}
	}
	loved["never-heard-artist"] = true

	fp := newFingerprints(sa)
	fp.note("st-0001", "artist-0005") // live update keeps the index in step

	batch := fp.lovedOverlaps(loved)
	for st := range sa {
		wantScore, wantMatches := fp.lovedOverlap(st, loved)
		got := batch[st]
		if got.matches != wantMatches {
			t.Errorf("%s: matches %d, want %d", st, got.matches, wantMatches)
		}
		// Same formula, different summation order: allow last-bit slack.
		if math.Abs(got.score-wantScore) > 1e-12 {
			t.Errorf("%s: score %v, want %v", st, got.score, wantScore)
		}
	}
}

func TestForgiveFailure(t *testing.T) {
	c := openTestCore(t)
	st := &c.stations[0]
	c.MarkStationFailed(st.UUID)
	c.MarkStationFailed(st.UUID)
	if st.FailCount != 2 {
		t.Fatalf("FailCount=%d, want 2", st.FailCount)
	}
	c.ForgiveFailure(st.UUID)
	if st.FailCount != 1 {
		t.Fatalf("after forgive FailCount=%d, want 1", st.FailCount)
	}
	// Floor at zero: forgiving a clean station must not underflow.
	clean := &c.stations[1]
	c.ForgiveFailure(clean.UUID)
	if clean.FailCount != 0 {
		t.Fatalf("clean station FailCount=%d, want 0", clean.FailCount)
	}
	var n int
	if err := c.store.db.QueryRow(`SELECT fail_count FROM stations WHERE uuid=?`, st.UUID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("persisted fail_count=%d, want 1", n)
	}
}
