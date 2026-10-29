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
