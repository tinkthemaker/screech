package streamurl

import (
	"net/url"
	"strings"
)

// pageHosts are sites whose links open a web page, not a stream: mpv runs
// with --ytdl=no, so it can't play them, and screech hands them to yt-dlp
// only when ytdlp_path is set. Subdomains count (music.youtube.com,
// artist.bandcamp.com); the media hosts these pages resolve to
// (googlevideo.com, sndcdn.com) are deliberately absent.
var pageHosts = []string{
	"youtube.com", "youtu.be", "soundcloud.com", "vimeo.com",
	"mixcloud.com", "bandcamp.com", "twitch.tv",
}

// IsPage reports whether link points at one of those pages: a link that
// needs an extractor to become something mpv can open.
func IsPage(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range pageHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}
