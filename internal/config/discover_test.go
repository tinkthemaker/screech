package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/core"
)

// discover = "<phrase or link>" is a third kind of station beside url:
// no link to check, an id of its own, and exactly one of the two.
func TestStationDiscoverParses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[[stations]]
name = "Dungeon \u001b[31mSynth"
discover = "  dungeon   synth "
tags = "Dungeon Synth, Ambient"

[[stations]]
name = "From a song"
discover = "https://youtu.be/abcdefghijk?t=3"

[[stations]]
name = "Both"
url = "https://both.example/live"
discover = "dungeon synth"

[[stations]]
name = "Same Phrase Again"
discover = "Dungeon Synth"

[[stations]]
discover = "no name"

[[stations]]
name = "Blank"
discover = "  "

[[stations]]
name = "Typed"
discover = 7
`)
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if len(cfg.Stations) != 2 {
		t.Fatalf("stations = %+v, want the two good ones", cfg.Stations)
	}
	want := Station{Name: "Dungeon Synth", Discover: "dungeon synth", Tags: "dungeon synth,ambient"}
	if cfg.Stations[0] != want {
		t.Errorf("station 1 = %+v\nwant %+v", cfg.Stations[0], want)
	}
	if got := cfg.Stations[0].ID(); got != core.DiscoveryUUID("dungeon synth") || !core.IsDiscovery(got) {
		t.Errorf("ID() = %q, want the disc: uuid of its phrase", got)
	}
	if got := cfg.Stations[1].ID(); got != core.DiscoveryUUID("https://www.youtube.com/watch?v=abcdefghijk") {
		t.Errorf("ID() of a link = %q, want the key of its video", got)
	}
	for _, w := range []string{
		`[[stations]] #3 "Both": url and discover are both set; use one; skipped`,
		`[[stations]] #4 "Same Phrase Again": same discover as #1; skipped`,
		`[[stations]] #5: needs a name; skipped`,
		`[[stations]] #6 "Blank": needs a url or discover; skipped`,
		`[[stations]] #7 "Typed": discover must be a string; ignored`,
		`[[stations]] #7 "Typed": needs a url or discover; skipped`,
	} {
		if !contains(cfg.Warnings, w) {
			t.Errorf("missing warning %q\nhave %q", w, cfg.Warnings)
		}
	}
	if len(cfg.Warnings) != 6 {
		t.Errorf("got %d warnings, want 6: %q", len(cfg.Warnings), cfg.Warnings)
	}
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "page link") {
			t.Errorf("a discovery is not a page link: %q", w)
		}
	}
}

// screech add --discover writes a discover table, and screech remove takes
// it out again by phrase, by link or by id, leaving the file as it was.
func TestAddDiscoverRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "screech")
	if _, _, err := loadFrom(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	mine := readFile(t, path) + "\n# my own note, keep it\n"
	writeConfig(t, dir, mine)

	_, added, err := addStationIn(dir, Station{Name: "Dungeon", Discover: "  Dungeon   Synth ", Tags: "Ambient"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Discover != "Dungeon Synth" || added.URL != "" {
		t.Errorf("added = %+v, want the phrase cleaned and no url", added)
	}
	text := readFile(t, path)
	if !strings.HasPrefix(text, mine) {
		t.Fatalf("add must only append; file now:\n%s", text)
	}
	if block := strings.TrimPrefix(text, mine); block != "\n[[stations]]\nname = \"Dungeon\"\ndiscover = \"Dungeon Synth\"\ntags = \"ambient\"\n" {
		t.Errorf("appended block = %q; a discovery must write discover and no url", block)
	}
	cfg, _, _, err := readFrom(dir)
	if err != nil || len(cfg.Stations) != 1 || cfg.Stations[0] != added || len(cfg.Warnings) != 0 {
		t.Fatalf("after add: stations=%+v warnings=%q err=%v", cfg.Stations, cfg.Warnings, err)
	}

	if _, _, err := addStationIn(dir, Station{Name: "Again", Discover: "dungeon synth"}); err == nil ||
		!strings.Contains(err.Error(), `as "Dungeon"`) {
		t.Errorf("adding the same phrase twice should be refused, got %v", err)
	}
	if _, _, err := addStationIn(dir, Station{Name: "Both", URL: "https://x.example/", Discover: "x"}); err == nil ||
		!strings.Contains(err.Error(), "both set") {
		t.Errorf("url and discover together should be refused, got %v", err)
	}
	if _, _, err := addStationIn(dir, Station{Name: "Neither"}); err == nil {
		t.Error("a station with neither url nor discover should be refused")
	}
	if got := readFile(t, path); got != text {
		t.Fatalf("refused adds must leave the file alone:\n%s", got)
	}

	// A url station and a discovery side by side; each is found by its own key.
	if _, _, err := addStationIn(dir, Station{Name: "Link", URL: "https://x.example/live"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{added.ID(), "dungeon synth", " DUNGEON  synth "} {
		_, removed, err := removeStationIn(dir, key)
		if err != nil {
			t.Fatalf("remove %q: %v", key, err)
		}
		if removed.Name != "Dungeon" || removed.Discover != "Dungeon Synth" {
			t.Errorf("removed %+v, want Dungeon", removed)
		}
		cfg, _, _, _ = readFrom(dir)
		if len(cfg.Stations) != 1 || cfg.Stations[0].Name != "Link" {
			t.Fatalf("after removing by %q: %+v, want only the link", key, cfg.Stations)
		}
		if _, _, err := addStationIn(dir, Station{Name: "Dungeon", Discover: "Dungeon Synth", Tags: "Ambient"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := removeStationIn(dir, "https://x.example/live"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := removeStationIn(dir, "disc:0000000000000000"); err == nil {
		t.Error("removing an id that isn't there should fail")
	}
	if _, _, err := removeStationIn(dir, added.ID()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != mine {
		t.Fatalf("add then remove should restore the file byte for byte\n got: %q\nwant: %q", got, mine)
	}
}

// A YouTube link given as a discovery is the same station whichever form
// of the link names it, so remove finds it by any of them.
func TestRemoveDiscoverByAnotherFormOfTheLink(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := addStationIn(dir, Station{Name: "Song", Discover: "https://www.youtube.com/watch?v=abcdefghijk&list=RDabcdefghijk"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := addStationIn(dir, Station{Name: "Same", Discover: "https://youtu.be/abcdefghijk"}); err == nil {
		t.Error("the same video under another link is the same station")
	}
	if _, removed, err := removeStationIn(dir, "https://youtu.be/abcdefghijk?t=9"); err != nil || removed.Name != "Song" {
		t.Fatalf("remove by another form of the link: %+v, %v", removed, err)
	}
}
