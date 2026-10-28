// Package streamurl mirrors mpv's media-title filename fallback. When a
// stream carries no ICY title, mpv reports the percent-decoded text after
// the last '/' of the whole URL — query included — which lands in the
// heard list as a bogus track ("rqqps6cbe3quv?rj-ttl=5&rj-tok=…") unless
// it is filtered out. It also knows which links are web pages rather than
// streams (page.go), the ones only yt-dlp can turn into something mpv plays.
package streamurl

import (
	"net/url"
	"path"
	"strings"
)

// MpvFilenames returns the distinct, non-empty candidates mpv can report
// as media-title for a stream URL before real metadata arrives.
func MpvFilenames(raw string) []string {
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}
	add(lastSeg(pctDecode(raw)))
	add(lastSeg(raw))
	if u, err := url.Parse(raw); err == nil && u.Path != "" && u.Path != "/" {
		add(path.Base(u.Path))
		add(path.Base(u.EscapedPath()))
	}
	return out
}

// pctDecode decodes the %XX escapes it can, byte-wise. Anything that isn't
// a valid escape — a stray '%' or a short pair — stays as written, and '+'
// is never a space: that's a form-decoding rule mpv doesn't apply.
func pctDecode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, ok := unhex(s[i+1], s[i+2]); ok {
				b.WriteByte(v)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhex(a, b byte) (byte, bool) {
	hi, ok1 := hexVal(a)
	lo, ok2 := hexVal(b)
	return hi<<4 | lo, ok1 && ok2
}

func hexVal(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// lastSeg is mpv's filename rule: the text after the last '/', and the
// whole string when nothing follows the slash.
func lastSeg(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}
