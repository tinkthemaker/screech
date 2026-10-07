package core

import (
	"strings"
	"testing"
	"time"
)

func TestCleanText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"osc52 clipboard write", "evil\x1b]52;c;aGVsbG8=\x07 station", "evil station"},
		{"csi color", "\x1b[31mRED\x1b[0m FM", "RED FM"},
		{"osc title set", "\x1b]0;pwned\x07radio", "radio"},
		{"bel dropped", "ding\x07dong", "dingdong"},
		{"newline flattened", "line one\nline two", "line one line two"},
		{"crlf and tab", "a\r\nb\tc", "a b c"},
		{"c1 csi", "bad\u009b31mgood", "badgood"},
		{"lone c1", "bad\u009bgood", "badood"}, // 0x9b + one final byte
		{"c1 osc", "x\u009dpayload\u009cy", "xy"},
		{"bidi override", "fm\u202ereverser\u202c", "fmreverser"},
		{"bidi isolate", "a\u2066b\u2069c", "abc"},
		{"del dropped", "ab\x7fcd", "abcd"},
		{"invalid utf8", "ok\xff\xfeend", "okend"},
		{"unicode line sep", "a\u2028b", "a b"},
		{"unicode para sep", "a\u2029b", "a b"},
		{"emoji and zwj kept", "Radio 📻 Family👨‍👩‍👧", "Radio 📻 Family👨‍👩‍👧"},
		{"cjk kept", "ラジオ日本", "ラジオ日本"},
		{"whitespace collapse", "  lots   of    space  ", "lots of space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.in); got != tc.want {
				t.Errorf("CleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// An ICY title carrying escapes must never reach the stored track or the
// dedupe state: cleaned before either one sees it.
func TestNoteTitleSanitizesRaw(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)

	tr, ok, _, _ := c.NoteTitle("\x1b]52;c;AAAA\x07Artist\x1b[31m - Ti\u009btle", now.Add(time.Minute))
	if !ok {
		t.Fatal("cleaned title should still parse")
	}
	if strings.ContainsAny(tr.Artist+tr.Title+tr.Raw, "\x1b\u009b") {
		t.Fatalf("escapes survived into the track: %+v", tr)
	}
	var stored string
	if err := c.store.db.QueryRow(`SELECT raw FROM tracks_heard ORDER BY id DESC LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stored, "\x1b\u009b") {
		t.Fatalf("escapes stored in tracks_heard: %q", stored)
	}
}

// Stations cached by an older version (before ingest cleaning) must read
// clean too.
func TestLoadStationsCleansStoredRows(t *testing.T) {
	c := openTestCore(t)
	dirty := []Station{{
		UUID: "dirty-1", Name: "Dirty\x1b[31mStation\nName",
		URL: "http://s/dirty", URLResolved: "http://s/dirty",
		Tags: "tag\x07one", Homepage: "http://h/\x1b", Country: "U\u009bS", Codec: "MP\x073",
	}}
	if err := c.store.UpsertStations(dirty, time.Now()); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.store.LoadStations()
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range loaded {
		if st.UUID != "dirty-1" {
			continue
		}
		for _, field := range []string{st.Name, st.Tags, st.Homepage, st.Country, st.Codec} {
			if strings.ContainsAny(field, "\x1b\u009b\x07\n") {
				t.Fatalf("stored row still carries escapes: %+v", st)
			}
		}
		if st.Name != "DirtyStation Name" {
			t.Errorf("name = %q, want %q", st.Name, "DirtyStation Name")
		}
		return
	}
	t.Fatal("dirty-1 not in loaded stations")
}
