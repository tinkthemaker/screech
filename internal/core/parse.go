package core

import (
	"strings"
)

// Track is a parsed StreamTitle.
type Track struct {
	ArtistKey string // normalized for matching
	Artist    string // display form
	Title     string
	Raw       string
}

var adMarkers = []string{
	"advertisement", "advert break", "commercial break", "ad break",
	"publicité", "werbung", "reklama",
}

// SuspectAd reports whether a raw StreamTitle looks like an ad break.
// Deliberately conservative: empty titles and station-name titles also occur
// during DJ talk and station IDs, so callers must treat this as *suspicion*,
// never certainty.
func SuspectAd(raw, stationName string) bool {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return true
	}
	if stationName != "" && t == strings.ToLower(strings.TrimSpace(stationName)) {
		return true
	}
	for _, m := range adMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// ParseTitle turns a raw ICY StreamTitle into a Track. ok=false means the
// title carries no usable track info (empty, station slogan, ad marker, junk).
// "Artist - Title" is a convention, not a standard; a missing artist is normal
// and the Track is still returned with ok=true when a bare title exists.
func ParseTitle(raw, stationName string) (Track, bool) {
	tr := Track{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 250 {
		return tr, false
	}
	if SuspectAd(s, stationName) {
		return tr, false
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "http://") || strings.Contains(low, "https://") || strings.Contains(low, "www.") {
		return tr, false
	}

	artist, title := "", s
	if i := strings.Index(s, " - "); i > 0 {
		artist = strings.TrimSpace(s[:i])
		title = strings.TrimSpace(s[i+3:])
	}
	title = strings.TrimSpace(strings.TrimRight(title, "- "))
	if title == "" {
		title = artist
		artist = ""
	}
	if title == "" {
		return tr, false
	}
	// Artists that are obviously promo/junk get dropped; the title may still count.
	if artist != "" {
		la := strings.ToLower(artist)
		if strings.Contains(la, "www.") || strings.Contains(la, "@") || len(artist) > 100 {
			artist = ""
		}
	}
	tr.Artist = artist
	tr.Title = title
	tr.ArtistKey = NormalizeArtist(artist)
	return tr, true
}

// NormalizeArtist produces the matching key for an artist name.
// Case-folded, "the " stripped, feat./ft. suffixes cut, whitespace collapsed.
func NormalizeArtist(a string) string {
	s := strings.ToLower(strings.TrimSpace(a))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "the ")
	for _, sep := range []string{" feat.", " feat ", " featuring ", " ft.", " ft ", " with "} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
