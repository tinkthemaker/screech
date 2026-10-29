package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
)

func TestParseArgsAddDiscover(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want invocation
		err  string
	}{
		{"phrase and name", []string{"add", "--discover", "dungeon synth", "--name", "Dungeon"},
			invocation{cmd: cmdAdd, discover: "dungeon synth", name: "Dungeon"}, ""},
		{"equals form", []string{"add", "--discover=swedish death metal", "--tags=metal"},
			invocation{cmd: cmdAdd, discover: "swedish death metal", tags: "metal"}, ""},
		{"a link as the seed", []string{"add", "--discover", "https://youtu.be/abcdefghijk"},
			invocation{cmd: cmdAdd, discover: "https://youtu.be/abcdefghijk"}, ""},
		{"flag last", []string{"add", "--name", "N", "--discover", "x y"},
			invocation{cmd: cmdAdd, discover: "x y", name: "N"}, ""},
		{"no value", []string{"add", "--discover"}, invocation{}, "--discover needs a value"},
		{"empty phrase", []string{"add", "--discover", "  "}, invocation{}, "--discover needs a phrase"},
		{"both a link and a phrase", []string{"add", "https://x.example/s", "--discover", "x"}, invocation{}, "not both"},
		{"phrase then link", []string{"add", "--discover", "x", "https://x.example/s"}, invocation{}, "not both"},
		{"unquoted phrase", []string{"add", "--discover", "dungeon", "synth"}, invocation{}, "quote a phrase"},
		{"plain add still needs a link", []string{"add", "--name", "N"}, invocation{}, "add needs a link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseArgs(tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseArgs(%q) error %v, want %q", tc.args, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%q): %v", tc.args, err)
			}
			if inv != tc.want {
				t.Fatalf("parseArgs(%q) = %+v\nwant %+v", tc.args, inv, tc.want)
			}
		})
	}
}

// screech add --discover writes a discovery station: the phrase, no url,
// and a name that defaults to the phrase.
func TestAddDiscoverCommand(t *testing.T) {
	var got config.Station
	add := func(st config.Station) (string, config.Station, error) {
		got = st
		return "/cfg/config.toml", st, nil
	}
	var out, errOut bytes.Buffer
	if code := addDiscovery(&out, &errOut, add, "dungeon synth", "Dungeon", "ambient"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Discover != "dungeon synth" || got.URL != "" || got.Name != "Dungeon" || got.Tags != "ambient" {
		t.Errorf("add passed %+v", got)
	}
	text := out.String()
	for _, want := range []string{`added "Dungeon" to /cfg/config.toml`, "dungeon synth", core.DiscoveryUUID("dungeon synth"), "next time Screech starts"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}

	out.Reset()
	if code := addDiscovery(&out, &errOut, add, "swedish death metal", "", ""); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if got.Name != "swedish death metal" {
		t.Errorf("a discovery without --name is named for its phrase, got %q", got.Name)
	}
}

// A YouTube link given to plain add is not a stream of its own: it seeds a
// discovery station, so the config gets discover = and no url.
func TestAddYouTubeLinkWritesDiscover(t *testing.T) {
	var got config.Station
	add := func(st config.Station) (string, config.Station, error) {
		got = st
		return "config.toml", st, nil
	}
	for _, link := range []string{
		"https://youtu.be/abcdefghijk",
		"https://www.youtube.com/watch?v=abcdefghijk&list=RDabcdefghijk",
		"https://music.youtube.com/watch?v=abcdefghijk",
	} {
		got = config.Station{}
		var out, errOut bytes.Buffer
		if code := addStation(&out, &errOut, add, link, "", "", false); code != 0 {
			t.Fatalf("%s: exit %d: %s", link, code, errOut.String())
		}
		if got.Discover != link || got.URL != "" || got.Name == "" {
			t.Errorf("%s: add passed %+v, want discover = the link, no url, and a name", link, got)
		}
		if strings.Contains(out.String(), "page link") {
			t.Errorf("%s: a discovery needs no page-link hint:\n%s", link, out.String())
		}
	}

	// Other page links, and streams, stay what they were.
	got = config.Station{}
	var out, errOut bytes.Buffer
	addStation(&out, &errOut, add, "https://soundcloud.com/artist/track", "", "", true)
	if got.URL == "" || got.Discover != "" {
		t.Errorf("a SoundCloud link is still a url station, got %+v", got)
	}
	got = config.Station{}
	addStation(&out, &errOut, add, "https://www.youtube.com/playlist?list=PL1", "", "", true)
	if got.URL == "" || got.Discover != "" {
		t.Errorf("a playlist isn't a video; it stays a url station, got %+v", got)
	}
}

func TestRemoveHelpNamesDiscoveries(t *testing.T) {
	if _, err := parseArgs([]string{"remove"}); err == nil || !strings.Contains(err.Error(), "remove takes one station") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(usage, "--discover") {
		t.Error("usage should document add --discover")
	}
}
