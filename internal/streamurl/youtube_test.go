package streamurl

import "testing"

func TestYouTubeID(t *testing.T) {
	const id = "abcdefghijk"
	for _, tc := range []struct {
		link string
		want string
		ok   bool
	}{
		{"https://www.youtube.com/watch?v=abcdefghijk", id, true},
		{"https://www.youtube.com/watch?v=abcdefghijk&list=PL1&index=3", id, true},
		{"https://www.youtube.com/watch?list=RDabcdefghijk&v=abcdefghijk", id, true},
		{"https://youtu.be/abcdefghijk", id, true},
		{"https://youtu.be/abcdefghijk?t=3", id, true},
		{"https://music.youtube.com/watch?v=abcdefghijk", id, true},
		{"https://m.youtube.com/watch?v=abcdefghijk", id, true},
		{"https://www.youtube.com/shorts/abcdefghijk", id, true},
		{"https://www.youtube.com/shorts/abcdefghijk?feature=share", id, true},
		{"HTTPS://WWW.YOUTUBE.COM/watch?v=abcdefghijk", id, true},
		{"  https://youtu.be/abcdefghijk  ", id, true},
		{"youtu.be/abcdefghijk", id, true}, // pasted without the scheme
		{"https://youtu.be/a_c-e1gh2jK", "a_c-e1gh2jK", true},

		{"https://example.com/watch?v=abcdefghijk", "", false},
		{"https://notyoutube.com/watch?v=abcdefghijk", "", false},
		{"https://youtube.com.evil.example/watch?v=abcdefghijk", "", false},
		{"https://www.youtube.com/watch?v=short", "", false},
		{"https://www.youtube.com/watch?v=abcdefghijkl", "", false},
		{"https://www.youtube.com/watch?v=abc!efghijk", "", false},
		{"https://www.youtube.com/watch", "", false},
		{"https://www.youtube.com/playlist?list=PL1", "", false},
		{"https://www.youtube.com/@somechannel", "", false},
		{"https://youtu.be/", "", false},
		{"https://youtu.be/abcdefghijk/extra", "", false},
		{"swedish death metal", "", false},
		{"", "", false},
	} {
		got, ok := YouTubeID(tc.link)
		if got != tc.want || ok != tc.ok {
			t.Errorf("YouTubeID(%q) = %q, %v; want %q, %v", tc.link, got, ok, tc.want, tc.ok)
		}
	}
}
