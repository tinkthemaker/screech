package core

import (
	"sort"
	"strings"
)

// FilterLovedTracks ranks library entries against a lower-cased query: every
// term must match somewhere, title prefixes beat artist prefixes, contains
// beats a station-name hit, and ties fall to the newest LovedAt. An empty or
// space-only query returns the entries unchanged.
func FilterLovedTracks(entries []LovedTrack, query string) []LovedTrack {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return entries
	}
	type match struct {
		entry LovedTrack
		score int
	}
	var matches []match
	for _, entry := range entries {
		score, ok := lovedMatchScore(entry, query)
		if ok {
			matches = append(matches, match{entry: entry, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].entry.LovedAt.After(matches[j].entry.LovedAt)
		}
		return matches[i].score > matches[j].score
	})
	out := make([]LovedTrack, len(matches))
	for i := range matches {
		out[i] = matches[i].entry
	}
	return out
}

func lovedMatchScore(entry LovedTrack, query string) (int, bool) {
	artist := strings.ToLower(entry.Artist)
	title := strings.ToLower(entry.Title)
	station := strings.ToLower(entry.StationName)
	all := artist + " " + title + " " + station
	score := 0
	for _, term := range strings.Fields(query) {
		if !strings.Contains(all, term) {
			return 0, false
		}
		switch {
		case strings.HasPrefix(title, term):
			score += 80
		case strings.HasPrefix(artist, term):
			score += 70
		case strings.Contains(title, term):
			score += 50
		case strings.Contains(artist, term):
			score += 40
		default:
			score += 10
		}
	}
	return score, true
}

// FilterSavedStations keeps the stations whose lower-cased name contains
// every term of the query. An empty or space-only query returns the entries
// unchanged.
func FilterSavedStations(entries []SavedStation, query string) []SavedStation {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return entries
	}
	var out []SavedStation
	for _, e := range entries {
		name := strings.ToLower(e.Name)
		ok := true
		for _, term := range strings.Fields(query) {
			if !strings.Contains(name, term) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}
