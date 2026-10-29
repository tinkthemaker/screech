package discover

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/tinkthemaker/screech/internal/core"
)

// packagingWords are the only words a bracketed note may be made of and still
// count as packaging. A note with any other word in it ("Remastered 2019",
// "Live at Wacken", "Video Game Mix") says something about the recording, so
// it stays; the rule is "all of it is packaging", not "some of it".
var packagingWords = map[string]bool{
	"official": true, "music": true, "video": true, "audio": true,
	"lyric": true, "lyrics": true, "visualizer": true, "visualiser": true,
	"hd": true, "hq": true, "4k": true,
}

var (
	bracketed = regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]`)

	// Credits are cut only where they are plainly credits. "ft." and "feat."
	// need their dot: "10 ft Tall" and "Feat of Strength" are titles, and a
	// false cut loses part of a name, where a missed credit costs nothing.
	featBracket = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:ft\.|feat\.|featuring)\s[^()\[\]]*[)\]]`)
	featTrail   = regexp.MustCompile(`(?i)\s+(?:ft\.|feat\.|featuring)\s.*$`)

	// YouTube names an artist's auto-generated channel "<Artist> - Topic".
	topicSuffix = regexp.MustCompile(`(?i)\s+-\s+topic$`)
)

// CleanTitle strips the packaging off a song's artist and title for display:
// "(Official Music Video)", "[HD]" and their kin from the title, trailing
// "ft. …" credits from both, and " - Topic" from a channel name standing in
// as the artist. Remaster and live notes are left alone. Both come back
// through core.CleanText, because both came off the network.
//
// It only ever shortens: if cleaning would leave a title or an artist empty,
// the original (just made safe) is kept, so a song called "(Official Audio)"
// still shows something.
func CleanTitle(artist, title string) (string, string) {
	return cleanName(artist, false), cleanName(title, true)
}

func cleanName(s string, isTitle bool) string {
	orig := core.CleanText(s)
	out := orig
	if isTitle {
		out = bracketed.ReplaceAllStringFunc(out, func(m string) string {
			if isPackaging(bracketed.FindStringSubmatch(m)[1]) {
				return ""
			}
			return m
		})
	} else {
		out = topicSuffix.ReplaceAllString(out, "")
	}
	out = featBracket.ReplaceAllString(out, "")
	out = featTrail.ReplaceAllString(out, "")
	out = strings.Join(strings.Fields(out), " ")
	if out != orig {
		// A separator the packaging hung from ("Song - (Official Video)").
		out = strings.TrimRight(out, " -–—|:~")
	}
	if out == "" {
		return orig
	}
	return out
}

func isPackaging(note string) bool {
	words := strings.FieldsFunc(strings.ToLower(note), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if !packagingWords[w] {
			return false
		}
	}
	return true
}
