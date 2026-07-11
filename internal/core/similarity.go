package core

import "math"

// fingerprints holds the in-memory empirical station fingerprints:
// which artist keys each station has been observed playing, and the inverse
// document frequency of each artist across heard stations. Sharing a chart
// artist means nothing; sharing an obscure one means a lot.
type fingerprints struct {
	stationArtists map[string]map[string]bool // station uuid -> artist keys
	artistDF       map[string]int             // artist key -> #stations observed playing it
	heardStations  int
}

func newFingerprints(stationArtists map[string]map[string]bool) *fingerprints {
	fp := &fingerprints{
		stationArtists: stationArtists,
		artistDF:       map[string]int{},
		heardStations:  len(stationArtists),
	}
	for _, artists := range stationArtists {
		for a := range artists {
			fp.artistDF[a]++
		}
	}
	return fp
}

// note records a newly heard (station, artist) pair.
func (fp *fingerprints) note(station, artistKey string) {
	if artistKey == "" {
		return
	}
	set := fp.stationArtists[station]
	if set == nil {
		set = map[string]bool{}
		fp.stationArtists[station] = set
		fp.heardStations++
	}
	if !set[artistKey] {
		set[artistKey] = true
		fp.artistDF[artistKey]++
	}
}

// idf weights an artist by how few stations play them.
func (fp *fingerprints) idf(artistKey string) float64 {
	df := fp.artistDF[artistKey]
	if df == 0 {
		return 0
	}
	return math.Log(1 + float64(fp.heardStations)/float64(df))
}

// lovedOverlap scores how much of the loved-artist set a station is known to
// play. Returns a saturating score in [0,1) and the raw matched-artist count.
// Only heard stations can score above zero — that's the honest cold-start
// limit of empirical fingerprints.
func (fp *fingerprints) lovedOverlap(station string, loved map[string]bool) (score float64, matches int) {
	artists := fp.stationArtists[station]
	if len(artists) == 0 || len(loved) == 0 {
		return 0, 0
	}
	sum := 0.0
	for a := range loved {
		if artists[a] {
			sum += fp.idf(a)
			matches++
		}
	}
	return sum / (sum + 3.0), matches
}
