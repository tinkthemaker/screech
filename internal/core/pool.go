package core

import (
	"fmt"
	"math/rand"
	"sort"
	"time"
)

const (
	poolTagSlice     = 40
	poolOverlapSlice = 20
	poolRandomSlice  = 12
	maxFailCount     = 3
)

// Pick is a tune decision plus the reason it was made (explainability is a
// feature, not a debug aid).
type Pick struct {
	Station Station
	Reason  string
}

type candidate struct {
	st       *Station
	tagScore float64
	overlap  float64
	matches  int
}

// tagScore sums decayed tag-affinity weights over the station's tags,
// saturating into [0,1).
func (c *Core) tagScore(st *Station, now time.Time) float64 {
	sum := 0.0
	for _, t := range st.TagList() {
		if r, ok := c.tags[t]; ok {
			w := decayToward(r.Alpha, r.UpdatedAt, now) - 1 // weight rides above the 1-prior
			if w > 0 {
				sum += w
			}
		}
	}
	return sum / (sum + 2.0)
}

// buildPool assembles tune candidates: top tag-affinity stations, loved-artist
// overlap neighbors, and a pure-random serendipity slice. Never the current or
// previous station; never stations that keep failing.
func (c *Core) buildPool(now time.Time, rng *rand.Rand) []candidate {
	var eligible []*Station
	for i := range c.stations {
		st := &c.stations[i]
		if !c.eligibleLocked(st) {
			continue
		}
		eligible = append(eligible, st)
	}
	if len(eligible) == 0 {
		return nil
	}

	cands := make([]candidate, len(eligible))
	for i, st := range eligible {
		score, matches := c.fp.lovedOverlap(st.UUID, c.loved)
		cands[i] = candidate{st: st, tagScore: c.tagScore(st, now), overlap: score, matches: matches}
	}

	selected := map[string]candidate{}
	take := func(sorted []candidate, n int, min func(candidate) bool) {
		for _, cd := range sorted {
			if n == 0 {
				return
			}
			if _, dup := selected[cd.st.UUID]; dup || !min(cd) {
				continue
			}
			selected[cd.st.UUID] = cd
			n--
		}
	}

	byTag := append([]candidate(nil), cands...)
	sort.Slice(byTag, func(i, j int) bool { return byTag[i].tagScore > byTag[j].tagScore })
	take(byTag, poolTagSlice, func(cd candidate) bool { return cd.tagScore > 0.01 })

	byOverlap := append([]candidate(nil), cands...)
	sort.Slice(byOverlap, func(i, j int) bool { return byOverlap[i].overlap > byOverlap[j].overlap })
	take(byOverlap, poolOverlapSlice, func(cd candidate) bool { return cd.matches > 0 })

	// Serendipity slice: pure random, weighted nothing. The bandit's prior
	// (which folds in ad-risk) still gets a say at sampling time.
	perm := rng.Perm(len(cands))
	n := poolRandomSlice
	for _, i := range perm {
		if n == 0 {
			break
		}
		cd := cands[i]
		if _, dup := selected[cd.st.UUID]; dup {
			continue
		}
		selected[cd.st.UUID] = cd
		n--
	}

	// Cold start: nothing has affinity or overlap yet — fill from the general
	// population so Thompson sampling over priors can do the exploring.
	if len(selected) < 8 {
		for _, i := range perm {
			if len(selected) >= 24 {
				break
			}
			cd := cands[i]
			if _, dup := selected[cd.st.UUID]; dup {
				continue
			}
			selected[cd.st.UUID] = cd
		}
	}

	out := make([]candidate, 0, len(selected))
	for _, cd := range selected {
		out = append(out, cd)
	}
	return out
}

// eligibleLocked reports whether a station may enter a candidate pool: never
// the current or previous station, never a repeat failure, never a URL-less
// entry. Callers must hold c.mu.
func (c *Core) eligibleLocked(st *Station) bool {
	if st.UUID == c.currentUUID || st.UUID == c.previousUUID {
		return false
	}
	if st.FailCount >= maxFailCount {
		return false
	}
	return st.StreamURL() != ""
}

// candidatesFor scores every eligible station passing keep.
func (c *Core) candidatesFor(now time.Time, keep func(*Station) bool) []candidate {
	var out []candidate
	for i := range c.stations {
		st := &c.stations[i]
		if !c.eligibleLocked(st) {
			continue
		}
		if keep != nil && !keep(st) {
			continue
		}
		score, matches := c.fp.lovedOverlap(st.UUID, c.loved)
		out = append(out, candidate{st: st, tagScore: c.tagScore(st, now), overlap: score, matches: matches})
	}
	return out
}

// pickFrom runs Thompson sampling over candidates: sample each Beta, take the
// argmax. Uncertainty does the explore/exploit balancing; there is
// deliberately no exploration knob to tune.
func (c *Core) pickFrom(cands []candidate, now time.Time, rng *rand.Rand) (candidate, float64, error) {
	if len(cands) == 0 {
		return candidate{}, 0, fmt.Errorf("no eligible stations")
	}
	daypart := DaypartFor(now)
	best := -1.0
	var bestCd candidate
	var bestEvidence float64
	for _, cd := range cands {
		a, b := c.effectiveCounts(cd, daypart, now)
		sample := sampleBeta(rng, a, b)
		if sample > best {
			best = sample
			bestCd = cd
			bestEvidence = (a - 1) + (b - 1)
		}
	}
	return bestCd, bestEvidence, nil
}

func (c *Core) pick(now time.Time, rng *rand.Rand) (Pick, error) {
	pool := c.buildPool(now, rng)
	cd, evidence, err := c.pickFrom(pool, now, rng)
	if err != nil {
		return Pick{}, fmt.Errorf("no eligible stations (station cache empty?)")
	}
	return Pick{Station: *cd.st, Reason: c.reason(cd, DaypartFor(now), evidence)}, nil
}

// effectiveCounts resolves a candidate's Beta parameters: decayed daypart row
// blended with the decayed all-time row for heard stations, curation-seeded
// priors for never-heard ones.
func (c *Core) effectiveCounts(cd candidate, daypart string, now time.Time) (float64, float64) {
	rows := c.bandit[cd.st.UUID]
	if len(rows) == 0 {
		return prior(cd.tagScore, cd.overlap, cd.st.AdRisk)
	}
	allA, allB := 1.0, 1.0
	if r, ok := rows[DaypartAll]; ok {
		allA, allB = r.decayed(now)
	}
	dpA, dpB := 1.0, 1.0
	if r, ok := rows[daypart]; ok {
		dpA, dpB = r.decayed(now)
	}
	return blend(dpA, dpB, allA, allB)
}

func (c *Core) reason(cd candidate, daypart string, evidence float64) string {
	switch {
	case cd.matches == 1:
		return "plays an artist you love"
	case cd.matches > 1:
		return fmt.Sprintf("plays %d artists you love", cd.matches)
	case evidence >= 3:
		return fmt.Sprintf("a %s favorite", daypart)
	case cd.tagScore > 0.1:
		if t := c.bestTag(cd.st); t != "" {
			return "tag: " + t
		}
		return "matches your tags"
	default:
		return "wildcard"
	}
}

func (c *Core) bestTag(st *Station) string {
	bestW := 0.0
	best := ""
	for _, t := range st.TagList() {
		if r, ok := c.tags[t]; ok && r.Alpha > bestW {
			bestW = r.Alpha
			best = t
		}
	}
	return best
}
