package discover

import "testing"

func TestCleanTitle(t *testing.T) {
	cases := []struct {
		name                  string
		artist, title         string
		wantArtist, wantTitle string
	}{
		{"official music video", "Dismember", "Dreaming In Red (OFFICIAL MUSIC VIDEO)", "Dismember", "Dreaming In Red"},
		{"topic channel and hd tag", "Entombed - Topic", "Left Hand Path [HD]", "Entombed", "Left Hand Path"},
		{"trailing ft", "X", "Song ft. Y", "X", "Song"},
		{"remaster note stays", "X", "Song (Remastered 2019)", "X", "Song (Remastered 2019)"},
		{"live note stays", "X", "Song (Live at Wacken)", "X", "Song (Live at Wacken)"},

		{"official audio in brackets", "X", "Song [Official Audio]", "X", "Song"},
		{"official video", "X", "Song (Official Video)", "X", "Song"},
		{"official visualizer", "X", "Song (Official Visualizer)", "X", "Song"},
		{"official lyric video", "X", "Song (Official Lyric Video)", "X", "Song"},
		{"lyrics alone", "X", "Song (Lyrics)", "X", "Song"},
		{"bare audio and 4k", "X", "Song (Audio) [4K]", "X", "Song"},
		{"hq", "X", "Song [HQ]", "X", "Song"},
		{"packaging then a real note", "X", "Song (Remastered 2019) [Official Audio]", "X", "Song (Remastered 2019)"},
		{"a real note that merely mentions video", "X", "Song (Video Game Mix)", "X", "Song (Video Game Mix)"},

		{"feat with dot", "X", "Song feat. Y", "X", "Song"},
		{"featuring", "X", "Song featuring Y and Z", "X", "Song"},
		{"bracketed feature note", "X", "Song (feat. Y)", "X", "Song"},
		{"feature after packaging", "X", "Song ft. Y (Official Video)", "X", "Song"},
		{"credit before a remaster note keeps the note", "X", "Song ft. Y (Remastered 2019)", "X", "Song (Remastered 2019)"},
		{"credit before a live note keeps the note", "X", "Song feat. Y [Live at Wacken]", "X", "Song [Live at Wacken]"},
		{"credit between two notes", "X", "Song (Remastered 2019) ft. Y (Live at Wacken)", "X", "Song (Remastered 2019) (Live at Wacken)"},
		{"ft inside a word is not a credit", "X", "Soft Cell", "X", "Soft Cell"},
		{"ft as units is not a credit", "X", "10 ft Tall", "X", "10 ft Tall"},
		{"a title that starts with feat is not a credit", "X", "Feat of Strength", "X", "Feat of Strength"},

		{"feature on the artist", "X ft. Y", "Song", "X", "Song"},
		{"topic is case blind", "Entombed - topic", "Song", "Entombed", "Song"},
		{"topic only as a suffix", "Topic Maker", "Song", "Topic Maker", "Song"},

		{"separator left behind by packaging", "X", "Song - (Official Video)", "X", "Song"},
		{"only packaging keeps the original title", "X", "(Official Audio)", "X", "(Official Audio)"},
		{"escapes never reach the screen", "X", "Song \x1b[31mred\x1b[0m", "X", "Song red"},
		{"empty stays empty", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ti := CleanTitle(tc.artist, tc.title)
			if a != tc.wantArtist || ti != tc.wantTitle {
				t.Errorf("CleanTitle(%q, %q) = (%q, %q), want (%q, %q)",
					tc.artist, tc.title, a, ti, tc.wantArtist, tc.wantTitle)
			}
		})
	}
}

// yt-dlp prints NA for a field it has not got, a JSON null becomes null or
// None, and a feed with no featured artist leaves a comma behind: none of that
// is a name.
func TestCleanTitleNullAndCommas(t *testing.T) {
	cases := []struct {
		name                  string
		artist, title         string
		wantArtist, wantTitle string
	}{
		{"null artist is empty", "null", "Song", "", "Song"},
		{"NA artist is empty", "NA", "Song", "", "Song"},
		{"None artist is empty", "None", "Song", "", "Song"},
		{"null title is empty", "X", "null", "X", ""},
		{"NA with padding", "  NA ", "Song", "", "Song"},
		{"a name that merely contains one", "Nana", "Nonetheless", "Nana", "Nonetheless"},
		{"None inside a longer artist stays", "None More Black", "Song", "None More Black", "Song"},

		{"trailing comma", "Decarnate,", "Song", "Decarnate", "Song"},
		{"trailing comma and space", "Decarnate, ", "Song", "Decarnate", "Song"},
		{"several trailing commas", "Decarnate,,", "Song", "Decarnate", "Song"},
		{"comma left by a credit", "Decarnate, ft. Y", "Song", "Decarnate", "Song"},
		{"comma left by a bracketed credit", "Decarnate, (feat. Y)", "Song", "Decarnate", "Song"},
		{"comma inside a list stays", "Decarnate, Organectomy", "Song", "Decarnate, Organectomy", "Song"},
		{"separators trimmed", "Decarnate -", "Song", "Decarnate", "Song"},
		{"a title keeps its comma", "X", "Hello,", "X", "Hello,"},
		{"only commas keeps the original", ",", "Song", ",", "Song"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, ti := CleanTitle(tc.artist, tc.title)
			if a != tc.wantArtist || ti != tc.wantTitle {
				t.Errorf("CleanTitle(%q, %q) = (%q, %q), want (%q, %q)",
					tc.artist, tc.title, a, ti, tc.wantArtist, tc.wantTitle)
			}
		})
	}
}

// A null from yt-dlp must not hide the artist the title names, or the channel
// that stands in for one.
func TestMediaNamesSkipsNulls(t *testing.T) {
	m := YTMedia{Artist: "NA", Track: "null", Title: "Dismember - Dreaming In Red", Channel: "Dismember - Topic"}
	artist, title := mediaNames(m)
	artist, title = CleanTitle(artist, title)
	if artist != "Dismember" || title != "Dreaming In Red" {
		t.Errorf("got (%q, %q), want the names the title carries", artist, title)
	}

	m = YTMedia{Artist: "None", Track: "NA", Title: "A plain title", Channel: "Some Channel"}
	artist, title = mediaNames(m)
	if artist != "Some Channel" || title != "A plain title" {
		t.Errorf("got (%q, %q), want the channel and the title", artist, title)
	}
}
