package core

import (
	"strings"

	"screech/internal/mathx"
)

// Commercial simulcast tells: big-network CDN hostnames.
var adHosts = []string{
	"iheart.com", "ihrhls.com", "iheartradio",
	"audacy", "entercom", "cumulus", "townsquare",
	"radio.com", "tritondigital", "adswizz", "amperwave",
}

var adTags = []string{
	"top 40", "top40", "hits", "adult contemporary", "classic hits", "hot ac",
}

var goodTags = []string{
	"college", "community", "freeform", "eclectic", "listener-supported",
	"non-commercial", "noncommercial", "public radio", "independent", "underground",
}

var goodNameWords = []string{"college", "university", "community"}

// ComputeAdRisk scores 0 (hobbyist/community, ad-free likely) to 1
// (commercial simulcast, ad breaks near-certain). Heuristic curation layer;
// it kills most ads before runtime detection is ever needed.
func ComputeAdRisk(st *Station) float64 {
	risk := 0.25 // neutral prior
	streamURL := strings.ToLower(st.URL + " " + st.URLResolved)
	home := strings.ToLower(st.Homepage)
	name := strings.ToLower(st.Name)
	tags := strings.ToLower(st.Tags)

	for _, h := range adHosts {
		if strings.Contains(streamURL, h) || strings.Contains(home, h) {
			risk += 0.5
			break
		}
	}
	for _, t := range adTags {
		if strings.Contains(tags, t) {
			risk += 0.15
			if st.ClickCount > 3000 { // generic-hits tags plus big traffic = network station
				risk += 0.15
			}
			break
		}
	}

	if strings.Contains(home, ".edu") {
		risk -= 0.3
	}
	for _, t := range goodTags {
		if strings.Contains(tags, t) {
			risk -= 0.2
			break
		}
	}
	for _, w := range goodNameWords {
		if strings.Contains(name, w) {
			risk -= 0.1
			break
		}
	}

	return mathx.Clamp(risk, 0, 1)
}
