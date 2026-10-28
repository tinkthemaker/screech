package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/tinkthemaker/screech/internal/config"
)

// runAdd writes link into config.toml as one of the listener's own
// stations. Nothing else is touched: the config file is the record, and
// every launch loads it into the database, so adding a link never means
// editing TOML or SQL by hand. It takes no lock and works while screech
// plays; that copy just doesn't see the link until it next starts.
func runAdd(link, name, tags string) int {
	return addStation(os.Stdout, os.Stderr, config.AddStation, link, name, tags)
}

func runRemove(key string) int {
	return removeStation(os.Stdout, os.Stderr, config.RemoveStation, key)
}

func addStation(stdout, stderr io.Writer, add func(config.Station) (string, config.Station, error), link, name, tags string) int {
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
