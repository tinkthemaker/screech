package player

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateStreamURL guards the loadfile boundary. Station URLs come from the
// radio-browser directory, a public database anyone can add entries to, so
// they are untrusted input. mpv's loadfile accepts far more than network
// streams (file:// paths, local playlists, device inputs), so an unvetted URL
// could make mpv read local files or open local devices. Only ordinary web
// stream schemes are allowed through.
func ValidateStreamURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return fmt.Errorf("empty stream URL")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid stream URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("refusing non-web stream URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("stream URL missing host")
	}
	return nil
}
