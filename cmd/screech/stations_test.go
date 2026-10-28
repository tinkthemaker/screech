package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
)

func TestParseArgsAddRemove(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		cmd            command
		link, nm, tags string
		err            string
	}{
		{"add link only", []string{"add", "https://x.example/s"}, cmdAdd, "https://x.example/s", "", "", ""},
		{"add with flags", []string{"add", "https://x.example/s", "--name", "My Station", "--tags", "jazz,blues"}, cmdAdd, "https://x.example/s", "My Station", "jazz,blues", ""},
		{"add flags first", []string{"add", "--tags=ambient", "--name=Drone", "https://x.example/s"}, cmdAdd, "https://x.example/s", "Drone", "ambient", ""},
		{"add reduced motion anywhere", []string{"add", "https://x.example/s", "--reduced-motion"}, cmdAdd, "https://x.example/s", "", "", ""},
		{"add no link", []string{"add", "--name", "N"}, cmdRun, "", "", "", "add needs a link"},
		{"add dangling flag", []string{"add", "https://x.example/s", "--name"}, cmdRun, "", "", "", "--name needs a value"},
		{"add unquoted name", []string{"add", "https://x.example/s", "My", "Station"}, cmdRun, "", "", "", "add takes one link"},
		{"add unknown flag", []string{"add", "https://x.example/s", "--genre", "x"}, cmdRun, "", "", "", `unknown flag "--genre"`},
		{"remove", []string{"remove", "custom:0123456789abcdef"}, cmdRemove, "custom:0123456789abcdef", "", "", ""},
		{"remove nothing", []string{"remove"}, cmdRun, "", "", "", "remove takes one station"},
		{"remove two", []string{"remove", "a", "b"}, cmdRun, "", "", "", "remove takes one station"},
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
			if inv.cmd != tc.cmd || inv.link != tc.link || inv.name != tc.nm || inv.tags != tc.tags {
				t.Fatalf("parseArgs(%q) = %+v", tc.args, inv)
			}
		})
	}
}

// screech add without --name names the station after its host, and both
// commands say plainly that a running Screech only sees the change on its
// next launch.
func TestAddRemoveCommands(t *testing.T) {
	var got config.Station
	add := func(st config.Station) (string, config.Station, error) {
		got = st
		return "/cfg/config.toml", st, nil
	}
	var out, errOut bytes.Buffer
	link := "https://www.stream.example/live.mp3"
	if code := addStation(&out, &errOut, add, link, "", "jazz", false); code != 0 {
		t.Fatalf("add exit %d: %s", code, errOut.String())
	}
	if got.Name != "stream.example" || got.URL != link || got.Tags != "jazz" {
		t.Errorf("add passed %+v", got)
	}
	text := out.String()
	for _, want := range []string{`added "stream.example" to /cfg/config.toml`, core.CustomUUID(link), "next time Screech starts"} {
		if !strings.Contains(text, want) {
			t.Errorf("add output lacks %q:\n%s", want, text)
		}
	}

	out.Reset()
	remove := func(key string) (string, config.Station, error) {
		if key != link {
			return "", config.Station{}, errors.New("no station matches")
		}
		return "/cfg/config.toml", got, nil
	}
	if code := removeStation(&out, &errOut, remove, link); code != 0 {
		t.Fatalf("remove exit %d", code)
	}
	if !strings.Contains(out.String(), `removed "stream.example"`) || !strings.Contains(out.String(), "until it next starts") {
		t.Errorf("remove output:\n%s", out.String())
	}
	errOut.Reset()
	if code := removeStation(&out, &errOut, remove, "https://other.example/"); code != 1 || !strings.Contains(errOut.String(), "no station matches") {
		t.Errorf("a failed remove should exit 1 with the reason, got %d %q", code, errOut.String())
	}
}

func TestDoctorCountsCustomStations(t *testing.T) {
	cfg := config.Config{Stations: []config.Station{
		{Name: "A", URL: "https://a.example/"}, {Name: "B", URL: "https://b.example/"},
	}, Warnings: []string{`[[stations]] #3: needs a url; skipped`}}
	var out bytes.Buffer
	if code := reportConfig(&out, "config.toml", cfg, nil); code != 0 {
		t.Fatalf("exit bits %d, want 0", code)
	}
	if !strings.Contains(out.String(), "-> 2 custom stations\n") || !strings.Contains(out.String(), "needs a url; skipped") {
		t.Fatalf("doctor config section:\n%s", out.String())
	}

	out.Reset()
	reportConfig(&out, "config.toml", config.Config{}, nil)
	if !strings.Contains(out.String(), "no custom stations") {
		t.Fatalf("doctor with none:\n%s", out.String())
	}
	out.Reset()
	if code := reportConfig(&out, "config.toml", config.Config{}, os.ErrNotExist); code != 0 || strings.Contains(out.String(), "custom") {
		t.Fatalf("a missing config is not a failure and has no count: %d\n%s", code, out.String())
	}
}

func TestAddPageLinkHints(t *testing.T) {
	add := func(st config.Station) (string, config.Station, error) { return "config.toml", st, nil }
	page := "https://soundcloud.com/artist/track"
	for _, tc := range []struct {
		on   bool
		want string
	}{
		{false, "plays only once ytdlp_path"},
		{true, "asks yt-dlp"},
	} {
		var out, errOut bytes.Buffer
		if code := addStation(&out, &errOut, add, page, "", "", tc.on); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("pagesOn=%v: output lacks %q:\n%s", tc.on, tc.want, out.String())
		}
	}
	var out, errOut bytes.Buffer
	addStation(&out, &errOut, add, "https://ice1.somafm.com/x", "", "", false)
	if strings.Contains(out.String(), "page link") {
		t.Errorf("a stream link needs no page hint:\n%s", out.String())
	}
}

func TestDoctorYtdlp(t *testing.T) {
	var out bytes.Buffer
	if code := reportYtdlp(&out, ""); code != 0 || !strings.Contains(out.String(), "[ -- ] yt-dlp   off") {
		t.Fatalf("off: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := reportYtdlp(&out, "no-such-yt-dlp-anywhere"); code != ecYtdlp || !strings.Contains(out.String(), "[FAIL] yt-dlp") {
		t.Fatalf("missing: exit %d\n%s", code, out.String())
	}
}
