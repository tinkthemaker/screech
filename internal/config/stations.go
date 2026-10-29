package config

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/tinkthemaker/screech/internal/core"
)

// Station is one of the listener's own streams: a [[stations]] table in
// config.toml, written by `screech add` or by hand. The config file is the
// record of these; the database only caches them, so it stays disposable.
// name and exactly one of url and discover are required; the rest only fill
// in the station sheet and give tag affinity something to match. A url is
// one stream (or page link) to play. A discover is a phrase or a YouTube
// link to find songs from: a discovery station, which has no stream of its
// own to name.
type Station struct {
	Name     string `toml:"name"`
	URL      string `toml:"url,omitempty"`
	Discover string `toml:"discover,omitempty"`
	Tags     string `toml:"tags,omitempty"`
	Codec    string `toml:"codec,omitempty"`
	Bitrate  int    `toml:"bitrate,omitzero"`
	Country  string `toml:"country,omitempty"`
	Homepage string `toml:"homepage,omitempty"`
}

// ID is the station's custom: (or, for a discovery, disc:) uuid, the
// identity the database and `screech remove` know it by.
func (s Station) ID() string {
	if s.Discover != "" {
		return core.DiscoveryUUID(s.Discover)
	}
	return core.CustomUUID(s.URL)
}

// source is what the station was written as, for messages.
func (s Station) source() string {
	if s.Discover != "" {
		return s.Discover
	}
	return s.URL
}

const badStationsShape = "stations must be [[stations]] tables; ignored"

// parseStations turns the decoded `stations` value into stations, one
// warning per thing it had to ignore. Nothing here fails the load: a bad
// entry is skipped, a bad field dropped, and the rest still play.
func parseStations(raw any) ([]Station, []string) {
	var tables []map[string]any
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []map[string]any: // [[stations]] tables
		tables = v
	case []any: // stations = [{...}, ...]
		for _, e := range v {
			t, ok := e.(map[string]any)
			if !ok {
				return nil, []string{badStationsShape}
			}
			tables = append(tables, t)
		}
	default:
		return nil, []string{badStationsShape}
	}
	var out []Station
	var warns []string
	seen := map[string]int{} // station id -> the table that claimed it
	for i, t := range tables {
		st, fieldWarns := parseStation(t)
		label := fmt.Sprintf("[[stations]] #%d", i+1)
		if name := core.CleanText(st.Name); name != "" {
			label += fmt.Sprintf(" %q", name)
		}
		for _, w := range fieldWarns {
			warns = append(warns, label+": "+w)
		}
		st, err := st.clean()
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v; skipped", label, err))
			continue
		}
		if first, dup := seen[st.ID()]; dup {
			what := "url"
			if st.Discover != "" {
				what = "discover"
			}
			warns = append(warns, fmt.Sprintf("%s: same %s as #%d; skipped", label, what, first))
			continue
		}
		seen[st.ID()] = i + 1
		out = append(out, st)
	}
	return out, warns
}

// parseStation reads one table field by field, so a wrong type costs that
// field, not the station.
func parseStation(t map[string]any) (Station, []string) {
	var st Station
	text := map[string]*string{
		"name": &st.Name, "url": &st.URL, "discover": &st.Discover, "codec": &st.Codec,
		"country": &st.Country, "homepage": &st.Homepage,
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var warns []string
	for _, k := range keys {
		v := t[k]
		if dst, ok := text[k]; ok {
			s, ok := v.(string)
			if !ok {
				warns = append(warns, fmt.Sprintf("%s must be a string; ignored", k))
				continue
			}
			*dst = s
			continue
		}
		switch k {
		case "tags":
			tags, ok := tagsValue(v)
			if !ok {
				warns = append(warns, "tags must be a string or a list of strings; ignored")
				continue
			}
			st.Tags = tags
		case "bitrate":
			n, ok := v.(int64)
			if !ok || n < 0 || n > 100_000 {
				warns = append(warns, "bitrate must be a whole number of kbps; ignored")
				continue
			}
			st.Bitrate = int(n)
		default:
			if topLevelKeys[k] {
				// The TOML trap: a setting written below a [[stations]]
				// header belongs to that table, not to the file.
				warns = append(warns, fmt.Sprintf("%s here belongs to this station, not the file (ignored); move it above the first [[stations]]", k))
				continue
			}
			warns = append(warns, fmt.Sprintf("unknown key %q (ignored)", k))
		}
	}
	return st, warns
}

// topLevelKeys are Config's own TOML keys, read off its struct tags so a
// new setting is covered without touching this list.
var topLevelKeys = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}()

// tagsValue accepts tags written either way TOML makes natural:
// "jazz, blues" or ["jazz", "blues"].
func tagsValue(v any) (string, bool) {
	switch tv := v.(type) {
	case string:
		return tv, true
	case []any:
		parts := make([]string, 0, len(tv))
		for _, e := range tv {
			s, ok := e.(string)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

// clean puts a station into the form the rest of Screech may trust: every
// text field through core.CleanText (the config is typed by hand and may
// be pasted from anywhere), tags lowercased and comma-joined, and a link
// the player will actually open. An error means the station can't be used.
// A discovery has no link to check: its phrase is whatever the listener
// typed, and only has to say something.
func (s Station) clean() (Station, error) {
	s.Name = core.CleanText(s.Name)
	s.URL = core.CleanText(s.URL)
	s.Discover = core.CleanText(s.Discover)
	s.Tags = cleanTags(s.Tags)
	s.Codec = core.CleanText(s.Codec)
	s.Country = core.CleanText(s.Country)
	s.Homepage = core.CleanText(s.Homepage)
	if s.Bitrate < 0 {
		s.Bitrate = 0
	}
	switch {
	case s.URL != "" && s.Discover != "":
		return s, errors.New("url and discover are both set; use one")
	case s.Discover != "":
		// Nothing to check: any phrase will do.
	case s.URL == "":
		return s, errors.New("needs a url")
	default:
		if err := checkLink(s.URL); err != nil {
			return s, err
		}
	}
	if s.Name == "" {
		return s, errors.New("needs a name")
	}
	return s, nil
}

// checkLink holds a link to the player's own rule (player.MPV.Play): http
// or https, with a host. Anything else would fail at tune time as a strike
// against a station the listener chose, so it is refused here instead.
func checkLink(link string) error {
	u, err := url.Parse(link)
	if err != nil {
		return fmt.Errorf("url %q is not a valid link", link)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q is not an http(s) link", link)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", link)
	}
	return nil
}

// cleanTags normalizes a comma-separated tag list the way directory tags
// arrive: lowercase, trimmed, no empties, no repeats.
func cleanTags(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		t = strings.ToLower(core.CleanText(t))
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}
