package core

import (
	"math"
	"math/rand"
	"time"

	"screech/internal/mathx"
)

// Tuning constants. All rewards are bounded so no single session can swamp
// the counts, and everything decays toward the (1,1) prior with a ~3 week
// half-life so taste drift self-corrects.
const (
	decayHalfLife = 21 * 24 * time.Hour

	fastSkipWindow = 90 * time.Second

	listenAlphaPer10Min = 1.0 // α += minutes/10 ...
	listenAlphaMin      = 0.2 // ... clamped to [0.2, 3.0]
	listenAlphaMax      = 3.0
	skipBeta            = 1.0
	skipBetaDuringAd    = 0.25 // ad-break skips are discounted, not ignored
	loveAlpha           = 2.0
)

// Dayparts partition the bandit counts by local time of day.
const (
	DaypartAll     = "all"
	DaypartMorning = "morning"
	DaypartDay     = "day"
	DaypartEvening = "evening"
	DaypartNight   = "night"
)

func DaypartFor(t time.Time) string {
	h := t.Hour()
	switch {
	case h >= 5 && h < 11:
		return DaypartMorning
	case h >= 11 && h < 17:
		return DaypartDay
	case h >= 17 && h < 22:
		return DaypartEvening
	default:
		return DaypartNight
	}
}

// decayToward pulls a pseudo-count toward its prior of 1 based on elapsed time.
// Lazy decay: applied whenever a row is read or written, no background jobs.
func decayToward(x float64, since time.Time, now time.Time) float64 {
	dt := now.Sub(since)
	if dt <= 0 {
		return x
	}
	f := math.Exp(-math.Ln2 * dt.Seconds() / decayHalfLife.Seconds())
	return 1 + (x-1)*f
}

func (r banditRow) decayed(now time.Time) (alpha, beta float64) {
	return decayToward(r.Alpha, r.UpdatedAt, now), decayToward(r.Beta, r.UpdatedAt, now)
}

// blend combines a daypart row with the all-time row. Thin daypart evidence
// leans on all-time; rich daypart evidence mostly ignores it.
func blend(dpA, dpB, allA, allB float64) (a, b float64) {
	evidence := (dpA - 1) + (dpB - 1)
	w := mathx.Clamp(1-evidence/6.0, 0.15, 0.7)
	return dpA + w*(allA-1), dpB + w*(allB-1)
}

// prior builds pseudo-counts for a never-heard station from curation signals.
// tagScore, overlapScore in [0,1]; adRisk in [0,1].
func prior(tagScore, overlapScore, adRisk float64) (a, b float64) {
	a = 1 + 2.5*tagScore + 3.0*overlapScore
	a *= 1 - 0.55*adRisk
	b = 1 + 1.6*adRisk
	return mathx.Clamp(a, 0.6, 6), mathx.Clamp(b, 1, 4)
}

// sampleBeta draws from Beta(a, b) via two Gamma draws (Marsaglia–Tsang).
func sampleBeta(rng *rand.Rand, a, b float64) float64 {
	x := sampleGamma(rng, math.Max(a, 1e-3))
	y := sampleGamma(rng, math.Max(b, 1e-3))
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

func sampleGamma(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		// Boost: G(a) = G(a+1) * U^(1/a)
		u := rng.Float64()
		for u == 0 {
			u = rng.Float64()
		}
		return sampleGamma(rng, shape+1) * math.Pow(u, 1/shape)
	}
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// listenAlpha converts a listen duration into bounded α credit.
func listenAlpha(dur time.Duration) float64 {
	return mathx.Clamp(dur.Minutes()/10*listenAlphaPer10Min, listenAlphaMin, listenAlphaMax)
}
