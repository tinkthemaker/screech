package core

// artistGenres maps a normalized artist key to the directory genre tags most
// likely to carry their music, most specific first. It exists because
// internet radio rarely names stations for individual artists, but it does
// tag them by genre — and for genre-defining artists the genre *is* the
// artist's fingerprint at directory resolution.
//
// This is deliberately small and curated: a wrong genre guess is worse than
// an honest "no matches", so an artist only gets an entry when the mapping
// is unambiguous. The tags are radio-browser vocabulary, not musicological
// labels (see the tag census: "regional mexican" 673 stations, "corridos"
// family in the hundreds, "banda" 382).
var artistGenres = map[string][]string{
	// Corridos tumbados / regional mexican urban wave.
	"peso pluma":         {"corridos", "regional mexican", "corridos tumbados"},
	"tito double p":      {"corridos", "regional mexican", "corridos tumbados"},
	"natanael cano":      {"corridos", "corridos tumbados", "regional mexican"},
	"junior h":           {"corridos", "corridos tumbados", "regional mexican"},
	"ivan cornejo":       {"corridos", "regional mexican", "sad sierreno"},
	"danny lux":          {"corridos", "regional mexican", "sierreno"},
	"erekelown":          {"corridos", "regional mexican"},
	"gabito ballesteros": {"corridos", "regional mexican"},
	"oscar maydon":       {"corridos", "regional mexican"},
	"fuerza regida":      {"corridos", "regional mexican", "banda"},
	"grupo frontera":     {"corridos", "regional mexican", "nortena"},
	"calibre 50":         {"regional mexican", "nortena", "banda norteña"},
	"la adictiva":        {"banda", "regional mexican", "banda norteña"},
	"banda ms":           {"banda", "regional mexican", "banda norteña"},
	"el fantasma":        {"regional mexican", "corridos", "banda"},
	"christian nodal":    {"regional mexican", "mariachi", "ranchera"},
	"carin leon":         {"regional mexican", "corridos", "nortena"},
	"luis r conriquez":   {"corridos", "regional mexican"},
	"tony loya":          {"corridos", "regional mexican"},
	"xavi":               {"corridos tumbados", "corridos", "regional mexican"},

	// Older regional mexican anchors.
	"vicente fernandez":    {"mariachi", "ranchera", "regional mexican"},
	"selena":               {"tejano", "cumbia", "latin pop"},
	"intocable":            {"nortena", "tejano", "regional mexican"},
	"los tigres del norte": {"nortena", "corridos", "regional mexican"},
	"bronco":               {"nortena", "grupera", "regional mexican"},
	"grupo firme":          {"regional mexican", "banda", "nortena"},

	// A few non-Mexican anchors where the genre mapping is unambiguous.
	"bad bunny":      {"reggaeton", "latin urban", "latin pop"},
	"j balvin":       {"reggaeton", "latin urban", "latin pop"},
	"karol g":        {"reggaeton", "latin urban", "latin pop"},
	"shakira":        {"latin pop", "pop latino"},
	"daddy yankee":   {"reggaeton", "latin urban"},
	"don omar":       {"reggaeton", "latin urban"},
	"romeo santos":   {"bachata", "latin pop"},
	"aventura":       {"bachata", "latin pop"},
	"ozuna":          {"reggaeton", "latin urban"},
	"anuel aa":       {"reggaeton", "latin trap", "latin urban"},
	"rauw alejandro": {"reggaeton", "latin urban", "latin pop"},
}

// genreCandidates returns the directory tag candidates for an artist key,
// or nil if the artist isn't in the curated map.
func genreCandidates(artistKey string) []string {
	return artistGenres[artistKey]
}
