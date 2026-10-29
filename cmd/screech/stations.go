package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/streamurl"
	"github.com/tinkthemaker/screech/internal/ytdlp"
)

// runAdd writes link (or, with --discover, a discovery phrase) into
// config.toml as one of the listener's own stations. Nothing else is
// touched: the config file is the record, and every launch loads it into
// the database, so adding a station never means editing TOML or SQL by
// hand. It takes no lock and works while screech plays; that copy just
// doesn't see the station until it next starts.
func runAdd(link, discover, name, tags string) int {
	if discover != "" {
		return addDiscovery(os.Stdout, os.Stderr, config.AddStation, discover, name, tags)
	}
	// pagesOn is "would a page link resolve": ytdlp_path "" finds yt-dlp and
	// "off" doesn't, so the setting alone no longer says.
	cfg, _, dataDir, _ := config.Read()
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	_, pagesOn := ytdlp.Find(cfg.YtdlpPath, exeDir, dataDir)
	return addStation(os.Stdout, os.Stderr, config.AddStation, link, name, tags, pagesOn)
}

func runRemove(key string) int {
	return removeStation(os.Stdout, os.Stderr, config.RemoveStation, key)
}

// addDiscovery adds a discovery station: songs found from seed, a phrase
// or a YouTube link, instead of one stream. Without --name it is named for
// the phrase or the link itself. A link is a poor name, and that is the
// point: core.RenameDiscovery swaps a name that is still the link for the
// title of the first song it plays, and leaves any name the listener chose.
// The host ("youtu.be") would say nothing and never be replaced.
func addDiscovery(stdout, stderr io.Writer, add func(config.Station) (string, config.Station, error), seed, name, tags string) int {
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSpace(seed)
	}
	path, st, err := add(config.Station{Name: name, Discover: seed, Tags: tags})
	if err != nil {
		fmt.Fprintf(stderr, "screech: add: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "added %q to %s\n", st.Name, path)
	fmt.Fprintf(stdout, "  from  %s\n", st.Discover)
	if st.Tags != "" {
		fmt.Fprintf(stdout, "  tags  %s\n", st.Tags)
	}
	fmt.Fprintf(stdout, "  id    %s\n", st.ID())
	fmt.Fprintln(stdout, "It joins the rotation the next time Screech starts; one that's already running keeps playing without it until then.")
	return 0
}

// addStation does the work behind runAdd. pagesOn says whether yt-dlp is
// there to resolve a page link, which decides what one needs to hear about. A YouTube
// video link is not a stream mpv can open: it seeds a discovery station.
func addStation(stdout, stderr io.Writer, add func(config.Station) (string, config.Station, error), link, name, tags string, pagesOn bool) int {
	if _, isVideo := streamurl.YouTubeID(link); isVideo {
		return addDiscovery(stdout, stderr, add, link, name, tags)
	}
	if strings.TrimSpace(name) == "" {
		name = nameFromLink(link)
	}
	path, st, err := add(config.Station{Name: name, URL: link, Tags: tags})
	if err != nil {
		fmt.Fprintf(stderr, "screech: add: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "added %q to %s\n", st.Name, path)
	fmt.Fprintf(stdout, "  link  %s\n", st.URL)
	if st.Tags != "" {
		fmt.Fprintf(stdout, "  tags  %s\n", st.Tags)
	}
	fmt.Fprintf(stdout, "  id    %s\n", st.ID())
	fmt.Fprintln(stdout, "It joins the rotation the next time Screech starts; one that's already running keeps playing without it until then.")
	if streamurl.IsPage(st.URL) {
		if pagesOn {
			fmt.Fprintln(stdout, "It's a page link, so each tune asks yt-dlp for the stream first.")
		} else {
			fmt.Fprintln(stdout, "It's a page link, not a stream: it plays only with yt-dlp, which wasn't found (put it on PATH, beside screech or in the data folder, or set ytdlp_path; \"off\" turns it off).")
		}
	}
	return 0
}

func removeStation(stdout, stderr io.Writer, remove func(string) (string, config.Station, error), key string) int {
	path, st, err := remove(key)
	if err != nil {
		fmt.Fprintf(stderr, "screech: remove: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %q from %s\n", st.Name, path)
	fmt.Fprintln(stdout, "A Screech that's already running keeps it until it next starts. Its listening history stays; adding the same link again brings it back.")
	return 0
}

// nameFromLink names a station after its host when --name is left off:
// "stream.example.org" says more in the station line than "live.mp3".
func nameFromLink(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Hostname() == "" {
		return link
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
