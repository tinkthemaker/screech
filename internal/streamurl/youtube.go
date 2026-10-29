package streamurl

import (
	"net/url"
	"strings"
)

// YouTubeID returns the 11-character video id of a YouTube link, so every
// way of naming one song (a watch page with a playlist riding along, a
// youtu.be share link with a start time, the music site, a short) finds
// the same station. It reads youtube.com/watch?v=, youtu.be/<id>,
// music.youtube.com/watch?v= and youtube.com/shorts/<id>; the rest of the
// link (&list=…, ?t=…) is ignored. A link with the scheme left off, as
// pasted from an address bar, is accepted. Anything else, a playlist or a
// channel page included, is not a video link.
func YouTubeID(link string) (string, bool) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", false
	}
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "youtu.be":
		return videoID(strings.Trim(u.Path, "/"))
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		if rest, ok := strings.CutPrefix(u.Path, "/shorts/"); ok {
			return videoID(strings.TrimSuffix(rest, "/"))
		}
		if strings.TrimSuffix(u.Path, "/") == "/watch" {
			return videoID(u.Query().Get("v"))
		}
	}
	return "", false
}

// videoID accepts exactly what YouTube issues: eleven characters of
// letters, digits, '-' and '_'.
func videoID(s string) (string, bool) {
	if len(s) != 11 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return s, true
}
