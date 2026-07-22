package core

import "testing"

// The inverted index must agree with the brute-force computation it
// replaced: score every station both ways, including through live notes.
func TestInvertedIndexMatchesBruteForce(t *testing.T) {
	sa := map[string]map[string]bool{
		"st-a": {"alpha": true, "beta": true, "gamma": true},
		"st-b": {"beta": true, "delta": true},
		"st-c": {"gamma": true, "delta": true, "epsilon": true},
	}
	fp := newFingerprints(sa)
	loved := map[string]bool{"alpha": true, "delta": true, "zeta-unheard": true}

	brute := func(station string) (float64, int) {
		artists := sa[station]
		sum, matches := 0.0, 0
		for a := range loved {
			if artists[a] {
				sum += fp.idf(a)
				matches++
			}
		}
		return sum / (sum + 3.0), matches
	}
	for station := range sa {
		bs, bm := brute(station)
		is, im := fp.lovedOverlap(station, loved)
		if is != bs || im != bm {
			t.Errorf("%s: indexed (%v,%d) != brute (%v,%d)", station, is, im, bs, bm)
		}
	}

	// Live notes keep the index in step.
	fp.note("st-b", "alpha")
	if _, m := fp.lovedOverlap("st-b", loved); m != 2 {
		t.Errorf("after noting alpha on st-b, matches=%d, want 2", m)
	}
	// Noting the same pair twice must not double-count DF.
	fp.note("st-b", "alpha")
	if df := fp.artistDF["alpha"]; df != 2 {
		t.Errorf("alpha DF after duplicate note = %d, want 2", df)
	}
}
