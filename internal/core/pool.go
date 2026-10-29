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
	poolHeardSlice   = 12
	poolCustomSlice  = 4
	poolRandomSlice  = 12
	maxFailCount     = MaxFailCount
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
		if c.seedPicks > 0 && t == c.seedTag {
			sum += 6
		}
	}
	return sum / (sum + 2.0)
}

// buildPool assembles tune candidates: top tag-affinity stations, loved-artist
// overlap neighbors, heard favourites, a few of the listener's own stations,
// and a pure-random serendipity slice. Never the current or previous
// station; never stations that keep failing.
func (c *Core) buildPool(now time.Time, rng *rand.Rand) []candidate {
	var eligible []*Station
	for i := range c.stations {
		st := &c.stations[i]
		if st.UUID == c.currentUUID || st.UUID == c.previousUUID {
			continue
		}
		if st.FailCount >= maxFailCount {
			continue
		}
		if st.StreamURL() == "" {
			continue
		}
		eligible = append(eligible, st)
	}
	if len(eligible) == 0 {
		return nil
	}

	cands := make([]candidate, len(eligible))
	// One walk of the loved set through the inverted index scores every
	// station; the alternative — probing loved artists per candidate —
	// costs stations × loved on every skip under the core lock.
	overlaps := c.fp.lovedOverlaps(c.loved)
	for i, st := range eligible {
		o := overlaps[st.UUID]
		cands[i] = candidate{st: st, tagScore: c.tagScore(st, now), overlap: o.score, matches: o.matches}
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

	// Heard favourites: stations whose own listening says yes. Without
	// this slice a station re-enters the pool only through its tags, a
	// loved artist, or the random draw — so a tagless favourite, for a
	// listener who never presses love, was all but lost after one great
	// hour. In the simulated-listener harness (eval_test.go) that listener
	// went from 0.30 to 0.45 E[like] on late picks; listeners who do love
	// tracks were unchanged. The bar is "more evidence for than against".
	daypart := DaypartFor(now)
	means := map[string]float64{}
	var heard []candidate
	for _, cd := range cands {
		if len(c.bandit[cd.st.UUID]) > 0 {
			means[cd.st.UUID] = c.posteriorMean(cd, daypart, now)
			heard = append(heard, cd)
		}
	}
	sort.Slice(heard, func(i, j int) bool { return means[heard[i].st.UUID] > means[heard[j].st.UUID] })
	take(heard, poolHeardSlice, func(cd candidate) bool { return means[cd.st.UUID] > 0.5 })

	// The listener's own stations: links pasted into config.toml, and the
	// discovery stations they started or kept. They share the seats. Until
	// they're heard they carry no directory tags and no fingerprint, so
	// neither slice above can find them, and the random slice draws a
	// dozen from twenty thousand. A few seats per pick let Thompson
	// sampling try them now and then; the bandit keeps a liked one coming
	// back and lets a dull one fade, as with any station. In the
	// simulated-listener harness (genre+links), three liked links went
	// from 0% of picks to 7% for a listener who never presses love, a
	// dull fourth was gone by the late picks, and late E[like] held
	// (0.604 -> 0.609). More links than seats rotate through at random.
	var custom []candidate
	for _, cd := range cands {
		if IsCustom(cd.st.UUID) || IsDiscovery(cd.st.UUID) {
			custom = append(custom, cd)
		}
	}
	if len(custom) > poolCustomSlice {
		rng.Shuffle(len(custom), func(i, j int) { custom[i], custom[j] = custom[j], custom[i] })
	}
	take(custom, poolCustomSlice, func(candidate) bool { return true })

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
	// Map order would tie every pick to the hash seed. A fixed order
	// leaves the sampling odds alone and makes a seeded rng reproducible,
	// which the eval harness depends on.
	sort.Slice(out, func(i, j int) bool { return out[i].st.UUID < out[j].st.UUID })
	return out
}

// candidatesFor scores every eligible station passing keep. Eligibility is
// the shared hygiene: never the current or previous station, never repeat
// failures, never URL-less entries.
func (c *Core) candidatesFor(now time.Time, keep func(*Station) bool) []candidate {
	var kept []*Station
	for i := range c.stations {
		st := &c.stations[i]
		if st.UUID == c.currentUUID || st.UUID == c.previousUUID {
			continue
		}
		if st.FailCount >= maxFailCount || st.StreamURL() == "" {
			continue
		}
		if keep != nil && !keep(st) {
			continue
		}
		kept = append(kept, st)
	}
	overlaps := c.fp.lovedOverlaps(c.loved)
	out := make([]candidate, 0, len(kept))
	for _, st := range kept {
		o := overlaps[st.UUID]
		out = append(out, candidate{st: st, tagScore: c.tagScore(st, now), overlap: o.score, matches: o.matches})
	}
	return out
}

// pickFrom runs Thompson sampling over candidates: sample each Beta, take the
// argmax. Uncertainty does the explore/exploit balancing; there is
// deliberately no exploration knob to tune.
func (c *Core) pickFrom(cands []candidate, now time.Time, rng *rand.Rand) (candidate, error) {
	if len(cands) == 0 {
		return candidate{}, fmt.Errorf("no eligible stations")
	}
	daypart := DaypartFor(now)
	best := -1.0
	var bestCd candidate
	for _, cd := range cands {
		a, b := c.effectiveCounts(cd, daypart, now)
		sample := sampleBeta(rng, a, b)
		if sample > best {
			best = sample
			bestCd = cd
		}
	}
	return bestCd, nil
}

func (c *Core) pick(now time.Time, rng *rand.Rand) (Pick, error) {
	pool := c.buildPool(now, rng)
	if c.seedPicks > 0 {
		seeded := c.candidatesFor(now, func(st *Station) bool {
			for _, tag := range st.TagList() {
				if tag == c.seedTag {
					return true
				}
			}
			return false
		})
		if len(seeded) > 0 {
			pool = seeded
		}
	}
	cd, err := c.pickFrom(pool, now, rng)
	if err != nil {
		return Pick{}, fmt.Errorf("no eligible stations (station cache empty?)")
	}
	reason := c.reason(cd, DaypartFor(now), now)
	if c.seedPicks > 0 {
		for _, tag := range cd.st.TagList() {
			if tag == c.seedTag {
				reason = "seed echo: " + c.seedTag
				break
			}
		}
		c.seedPicks--
		if c.seedPicks == 0 {
			c.seedTag = ""
		}
	}
	return Pick{Station: *cd.st, Reason: reason}, nil
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

// posteriorMean is the stay probability the picker currently expects from
// a candidate: the mean of the Beta it samples from.
func (c *Core) posteriorMean(cd candidate, daypart string, now time.Time) float64 {
	a, b := c.effectiveCounts(cd, daypart, now)
	return a / (a + b)
}

func (c *Core) reason(cd candidate, daypart string, now time.Time) string {
	switch {
	case IsDiscovery(cd.st.UUID):
		// The listener's phrase says more than any statistic would.
		return discoveringReason(cd.st.Name)
	case cd.matches == 1:
		return "plays an artist you love"
	case cd.matches > 1:
		return fmt.Sprintf("plays %d artists you love", cd.matches)
	case c.isFavorite(cd, daypart, now):
		return fmt.Sprintf("a %s favorite", daypart)
	case IsCustom(cd.st.UUID):
		// "wildcard" would be wrong: the listener chose this one.
		return "added by you"
	case cd.tagScore > 0.1:
		if t := c.bestTag(cd.st); t != "" {
			return "tag: " + t
		}
		return "matches your tags"
	default:
		return "wildcard"
	}
}

// isFavorite decides whether "a <daypart> favorite" is an honest label for
// the pick. Skips count as evidence neither of love nor of habit, and a
// strong curation prior on a never-heard station is a guess, not a
// favorite — so the label needs real bandit rows whose effective (decayed,
// blended) counts show both volume (a-1 >= 3) and direction (a >= 2b).
func (c *Core) isFavorite(cd candidate, daypart string, now time.Time) bool {
	if len(c.bandit[cd.st.UUID]) == 0 {
		return false
	}
	a, b := c.effectiveCounts(cd, daypart, now)
	return a-1 >= 3 && a >= 2*b
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
