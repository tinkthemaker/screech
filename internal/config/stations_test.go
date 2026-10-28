package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/core"
)

func TestReadStations(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[[stations]]
name = "Late Night \u001b[31mJazz"
url = "https://jazz.example/stream"
tags = "Jazz,  late night ,jazz"
codec = "MP3"
bitrate = 128
country = "US"
homepage = "https://jazz.example/"

[[stations]]
name = "Lists Work Too"
url = "http://lists.example/a.aac"
tags = ["Ambient", "drone"]
`)
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", cfg.Warnings)
	}
	if len(cfg.Stations) != 2 {
		t.Fatalf("stations = %+v, want 2", cfg.Stations)
	}
	got := cfg.Stations[0]
	want := Station{Name: "Late Night Jazz", URL: "https://jazz.example/stream", Tags: "jazz,late night",
		Codec: "MP3", Bitrate: 128, Country: "US", Homepage: "https://jazz.example/"}
	if got != want {
		t.Errorf("station 1 = %+v\nwant %+v", got, want)
	}
	if cfg.Stations[1].Tags != "ambient,drone" {
		t.Errorf("list tags = %q, want ambient,drone", cfg.Stations[1].Tags)
	}
	if id := cfg.Stations[0].ID(); !core.IsCustom(id) || id != core.CustomUUID("https://jazz.example/stream") {
		t.Errorf("ID() = %q, want the custom: uuid of its link", id)
	}
}

// Every way a [[stations]] table can be wrong is a warning naming the
// table, never a failed load, and the good tables still come through.
func TestReadStationsWarns(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `accent = "#00FF00"

[[stations]]
name = "Good"
url = "https://good.example/live"
colour = "blue"
bitrate = "128"

[[stations]]
name = "FTP"
url = "ftp://files.example/song.mp3"

[[stations]]
name = "No Host"
url = "https:///stream"

[[stations]]
url = "https://nameless.example/"

[[stations]]
name = "No Link"

[[stations]]
name = "Shouty Duplicate"
url = "HTTPS://GOOD.example/live"

[[stations]]
name = 7
url = "https://typed.example/"
`)
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("a bad station must never fail the load: %v", err)
	}
	if cfg.Accent != "#00FF00" {
		t.Errorf("the rest of the file should still load; accent = %q", cfg.Accent)
	}
	if len(cfg.Stations) != 1 || cfg.Stations[0].Name != "Good" || cfg.Stations[0].Bitrate != 0 {
		t.Fatalf("stations = %+v, want only Good, with its bad bitrate dropped", cfg.Stations)
	}
	for _, want := range []string{
		`[[stations]] #1 "Good": unknown key "colour" (ignored)`,
		`[[stations]] #1 "Good": bitrate must be a whole number of kbps; ignored`,
		`[[stations]] #2 "FTP": url "ftp://files.example/song.mp3" is not an http(s) link; skipped`,
		`[[stations]] #3 "No Host": url "https:///stream" has no host; skipped`,
		`[[stations]] #4: needs a name; skipped`,
		`[[stations]] #5 "No Link": needs a url; skipped`,
		`[[stations]] #6 "Shouty Duplicate": same url as #1; skipped`,
		`[[stations]] #7: name must be a string; ignored`,
		`[[stations]] #7: needs a name; skipped`,
	} {
		if !contains(cfg.Warnings, want) {
			t.Errorf("missing warning %q\nhave %q", want, cfg.Warnings)
		}
	}
	if len(cfg.Warnings) != 9 {
		t.Errorf("got %d warnings, want 9: %q", len(cfg.Warnings), cfg.Warnings)
	}
	for _, w := range cfg.Warnings {
		if strings.Contains(w, `unknown key "stations`) {
			t.Errorf("stations keys must not also warn as unknown top-level keys: %q", w)
		}
	}
}

func TestReadStationsWrongShape(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `stations = "https://not.tables/"`+"\n")
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if len(cfg.Stations) != 0 || !contains(cfg.Warnings, badStationsShape) {
		t.Fatalf("stations=%v warnings=%q, want the shape warning", cfg.Stations, cfg.Warnings)
	}
}

// screech add followed by screech remove leaves the listener's file
// exactly as it was, comments and all.
func TestAddRemoveRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "screech")
	if _, _, err := loadFrom(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	mine := strings.Replace(readFile(t, path), `accent = "#FFB000"`, `accent = "#33CCFF"`, 1) + "\n# my own note, keep it\n"
	writeConfig(t, dir, mine)

	_, added, err := addStationIn(dir, Station{Name: "KEXP", URL: "https://kexp.example/kexp128.mp3", Tags: "Indie, Eclectic"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added.Tags != "indie,eclectic" {
		t.Errorf("added tags = %q, want them cleaned", added.Tags)
	}
	text := readFile(t, path)
	if !strings.HasPrefix(text, mine) {
		t.Fatalf("add must only append; file now:\n%s", text)
	}
	if block := strings.TrimPrefix(text, mine); block != "\n[[stations]]\nname = \"KEXP\"\nurl = \"https://kexp.example/kexp128.mp3\"\ntags = \"indie,eclectic\"\n" {
		t.Errorf("appended block = %q; unset fields must be left out", block)
	}
	cfg, _, _, err := readFrom(dir)
	if err != nil || len(cfg.Stations) != 1 || cfg.Stations[0] != added || len(cfg.Warnings) != 0 {
		t.Fatalf("after add: stations=%+v warnings=%q err=%v", cfg.Stations, cfg.Warnings, err)
	}
	if cfg.Accent != "#33CCFF" {
		t.Errorf("accent = %q; the rest of the file must be untouched", cfg.Accent)
	}

	if _, _, err := addStationIn(dir, Station{Name: "Again", URL: "https://KEXP.example/kexp128.mp3"}); err == nil ||
		!strings.Contains(err.Error(), `as "KEXP"`) {
		t.Errorf("adding the same link twice should be refused, got %v", err)
	}

	_, removed, err := removeStationIn(dir, added.ID())
	if err != nil {
		t.Fatalf("remove by id: %v", err)
	}
	if removed.Name != "KEXP" {
		t.Errorf("removed %+v, want KEXP", removed)
	}
	if got := readFile(t, path); got != mine {
		t.Fatalf("add then remove should restore the file byte for byte\n got: %q\nwant: %q", got, mine)
	}
}

// Removing one table keeps its neighbours, including the comment that
// describes the next one, and works by link as well as by id.
func TestRemoveKeepsNeighbours(t *testing.T) {
	dir := t.TempDir()
	body := "accent = \"#FFB000\"\r\n" +
		"\r\n" +
		"[[stations]]\r\n" +
		"name = \"One\"\r\n" +
		"url = \"https://one.example/\"\r\n" +
		"\r\n" +
		"# the second one is the good one\r\n" +
		"[[stations]] # really\r\n" +
		"name = \"Two\"\r\n" +
		"url = \"https://two.example/\"\r\n"
	writeConfig(t, dir, body)

	if _, _, err := removeStationIn(dir, "https://ONE.example/"); err != nil {
		t.Fatalf("remove by link: %v", err)
	}
	want := "accent = \"#FFB000\"\r\n" +
		"\r\n" +
		"# the second one is the good one\r\n" +
		"[[stations]] # really\r\n" +
		"name = \"Two\"\r\n" +
		"url = \"https://two.example/\"\r\n"
	if got := readFile(t, filepath.Join(dir, "config.toml")); got != want {
		t.Fatalf("after remove:\n got %q\nwant %q", got, want)
	}

	if _, _, err := addStationIn(dir, Station{Name: "Three", URL: "https://three.example/"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "config.toml")); strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("a CRLF file must stay CRLF after add: %q", got)
	}
	if _, _, err := removeStationIn(dir, "https://nowhere.example/"); err == nil {
		t.Error("removing a link that isn't there should fail")
	}
}

func TestAddCreatesConfigAndChecksLinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	if _, _, err := addStationIn(dir, Station{Name: "Pipe", URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("a non-http(s) link must be refused")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("a refused add must not create the file: %v", err)
	}
	path, _, err := addStationIn(dir, Station{Name: "First", URL: "https://first.example/"})
	if err != nil {
		t.Fatalf("add into a missing config: %v", err)
	}
	if text := readFile(t, path); !strings.HasPrefix(text, defaultFile) {
		t.Fatalf("a new config should start from the commented defaults:\n%s", text)
	}
	cfg, _, _, err := readFrom(dir)
	if err != nil || len(cfg.Stations) != 1 {
		t.Fatalf("stations=%+v err=%v", cfg.Stations, err)
	}
}

// A station written inline can't be cut out as text; remove says so
// instead of guessing, and the file is left alone.
func TestRemoveInlineStationRefuses(t *testing.T) {
	dir := t.TempDir()
	body := `stations = [{name = "Inline", url = "https://inline.example/"}]` + "\n"
	writeConfig(t, dir, body)
	if _, _, err := removeStationIn(dir, "https://inline.example/"); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("err = %v, want a remove-it-by-hand error", err)
	}
	if got := readFile(t, filepath.Join(dir, "config.toml")); got != body {
		t.Fatalf("file changed: %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
