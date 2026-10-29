package streamurl

import (
	"slices"
	"testing"
)

func TestMpvFilenames(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want []string
	}{
		{"https://h/x/aac-128", []string{"aac-128"}},
		{"https://h/k%20exp.mp3", []string{"k exp.mp3", "k%20exp.mp3"}},
		{"https://h/rq?rj-ttl=5&rj-tok=AA", []string{"rq?rj-ttl=5&rj-tok=AA"}},
		{"https://h/s?tok=AA%2FBB%20cc", []string{"BB cc", "s?tok=AA%2FBB%20cc"}},
		{"https://h/s?a=1/2", []string{"2"}},
		{"https://h/a%zz", []string{"a%zz"}},
	} {
		got := MpvFilenames(tc.url)
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("MpvFilenames(%q) = %v, missing %q", tc.url, got, w)
			}
		}
		for i, g := range got {
			if g == "" {
				t.Errorf("MpvFilenames(%q) has empty entry at %d", tc.url, i)
			}
			for _, o := range got[i+1:] {
				if o == g {
					t.Errorf("MpvFilenames(%q) has duplicate %q", tc.url, g)
				}
			}
		}
	}
}

func TestIsPage(t *testing.T) {
	for link, want := range map[string]bool{
		"https://www.youtube.com/watch?v=jfKfPfyJRdk":        true,
		"https://youtu.be/jfKfPfyJRdk":                       true,
		"https://music.YouTube.com/watch?v=x":                true,
		"https://soundcloud.com/artist/track":                true,
		"https://artist.bandcamp.com/track/song":             true,
		"https://vimeo.com/123":                              true,
		"https://rr3---sn-abc.googlevideo.com/videoplayback": false,
		"https://ice1.somafm.com/groovesalad-128-mp3":        false,
		"https://notyoutube.com/live":                        false,
		"://broken":                                          false,
	} {
		if got := IsPage(link); got != want {
			t.Errorf("IsPage(%q) = %v, want %v", link, got, want)
		}
	}
}
