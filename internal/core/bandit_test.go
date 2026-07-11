package core

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestSampleBetaMean(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	a, b := 8.0, 2.0
	sum := 0.0
	n := 20000
	for i := 0; i < n; i++ {
		s := sampleBeta(rng, a, b)
		if s < 0 || s > 1 {
			t.Fatalf("sample out of range: %v", s)
		}
		sum += s
	}
	mean := sum / float64(n)
	want := a / (a + b)
	if math.Abs(mean-want) > 0.01 {
		t.Errorf("beta(%v,%v) empirical mean %v, want ~%v", a, b, mean, want)
	}
}

func TestDecayHalfLife(t *testing.T) {
	now := time.Now()
	then := now.Add(-decayHalfLife)
	// Excess above the prior of 1 should halve after one half-life.
	got := decayToward(5, then, now)
	want := 1 + (5-1)*0.5
	if math.Abs(got-want) > 0.01 {
		t.Errorf("decayToward(5, -halflife) = %v, want %v", got, want)
	}
	// No time elapsed: unchanged.
	if got := decayToward(5, now, now); got != 5 {
		t.Errorf("no-elapsed decay changed value: %v", got)
	}
}

func TestFastSkipLowersSampleMean(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	now := time.Now()
	// Station A: two good listens. Station B: two fast skips.
	aA, bA := 1.0, 1.0
	aA += listenAlpha(20 * time.Minute)
	aA += listenAlpha(20 * time.Minute)
	aB, bB := 1.0, 1.0
	bB += skipBeta
	bB += skipBeta

	winsA := 0
	n := 5000
	for i := 0; i < n; i++ {
		if sampleBeta(rng, aA, bA) > sampleBeta(rng, aB, bB) {
			winsA++
		}
	}
	if float64(winsA)/float64(n) < 0.8 {
		t.Errorf("listened-to station should dominate skipped one; won only %d/%d", winsA, n)
	}
	_ = now
}

func TestListenAlphaBounds(t *testing.T) {
	if got := listenAlpha(8 * time.Hour); got != listenAlphaMax {
		t.Errorf("sleep-listening must be capped: got %v want %v", got, listenAlphaMax)
	}
	if got := listenAlpha(2 * time.Minute); got != listenAlphaMin {
		t.Errorf("short listen floor: got %v want %v", got, listenAlphaMin)
	}
}

func TestBlendBacksOffWhenThin(t *testing.T) {
	// Thin daypart evidence: all-time counts should bleed through strongly.
	a1, b1 := blend(1, 1, 9, 1)
	// Rich daypart evidence: all-time influence shrinks.
	a2, b2 := blend(9, 1, 9, 1)
	thinLift := a1 - 1
	richLift := (a2 - 9)
	if thinLift <= richLift {
		t.Errorf("thin daypart should borrow more from all-time: thin lift %v, rich lift %v", thinLift, richLift)
	}
	_, _ = b1, b2
}

func TestPriorPenalizesAdRisk(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	aClean, bClean := prior(0.5, 0, 0.0)
	aRisky, bRisky := prior(0.5, 0, 1.0)
	meanClean := aClean / (aClean + bClean)
	meanRisky := aRisky / (aRisky + bRisky)
	if meanRisky >= meanClean {
		t.Errorf("ad risk must lower prior mean: clean %v risky %v", meanClean, meanRisky)
	}
	_ = rng
}
