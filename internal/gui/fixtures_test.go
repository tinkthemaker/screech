//go:build windows && layout

package gui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/session"
)

// frontendFS is the embedded page, for the fixture server.
func frontendFS() (fs.FS, error) { return fs.Sub(Assets, "frontend") }

// fixtureNowMs is the one clock the fixtures and the page share: the page
// renders "LIVE 0:42", "08-47" and a deterministic meter frame off it.
var fixtureNowMs = time.Date(2026, 10, 19, 8, 47, 0, 0, time.UTC).UnixMilli()

// fixture is everything the page needs to render one state, built from
// the bridge's real types so it can't drift from them. Expect pins exact
// collapsed textContent per data-id and ExpectStyle computed styles;
// ExpectUnfolded overrides Expect entries when the window is unfolded.
type fixture struct {
	Name     string           `json:"name"`
	Snapshot session.Snapshot `json:"snapshot"`
	// SnapExtra carries the snapshot fields Task 5's session adds
	// (newToYou, discovery) until session.Snapshot has them; fixture.js
	// lays it over the snapshot. Replace with the real fields once merged.
	SnapExtra   map[string]any `json:"snapExtra,omitempty"`
	Unfolded    bool           `json:"unfolded"`
	Tab         string         `json:"tab"`
	Filter      string         `json:"filter,omitempty"`
	Find        string         `json:"find,omitempty"` // text already in the FIND bar
	RemoveArmed bool           `json:"removeArmed,omitempty"`
	Reduced     bool           `json:"reduced,omitempty"`
	Recent      []RecentRow    `json:"recent,omitempty"`
	Loved       []LovedRow     `json:"loved,omitempty"`
	Stations    []StationRow   `json:"stations,omitempty"`
	Discoveries []DiscoveryRow `json:"discoveries,omitempty"` // FIND's rows
	RadioTop    []StationRow   `json:"radioTop,omitempty"`    // RADIO's rows
	Taste       TasteView      `json:"taste"`
	Info        InfoView       `json:"info"`
	Selected    int            `json:"selected"`
	Top         int            `json:"top,omitempty"` // list scroll offset
	Level       *[3]float64    `json:"level,omitempty"`
	// Expect pins collapsed textContent per data-id (an input's value; a
	// "id@attr" key pins an attribute instead). ExpectRect pins a box to
	// [left, top, width, height] in page px — the brief's geometry, copied
	// exactly. Absent lists data-ids that must not exist.
	Expect         map[string]string            `json:"expect,omitempty"`
	ExpectUnfolded map[string]string            `json:"expectUnfolded,omitempty"`
	ExpectStyle    map[string]map[string]string `json:"expectStyle,omitempty"`
	ExpectRect     map[string][4]float64        `json:"expectRect,omitempty"`
	Absent         []string                     `json:"absent,omitempty"`

	// BothModes also renders unfolded, for the folded-vs-unfolded strip
	// comparison.
	BothModes bool `json:"-"`
}

var somaCard = session.StationCard{
	UUID: "seed:somafm-groovesalad", Name: "SomaFM: Groove Salad",
	Country: "US", Codec: "MP3", Bitrate: 128,
}
var dsoCard = session.StationCard{
	UUID: "seed:somafm-deepspaceone", Name: "Deep Space One",
	Country: "US", Codec: "AAC", Bitrate: 128,
}

func playingSnapshot() session.Snapshot {
	return session.Snapshot{
		Phase:       "playing",
		HaveStation: true,
		Station:     somaCard,
		Reason:      "tag: ambient",
		Artist:      "Boards of Canada",
		Title:       "Roygbiv",
		HaveTrack:   true,
		PlayStartMs: fixtureNowMs - 42_000,
		Volume:      70,
		Slots:       [9]bool{true, true, true, true},
		CurrentSlot: 3,
	}
}

func ptrLevel(l, r, p float64) *[3]float64 { return &[3]float64{l, r, p} }

// exp builds a fixture's Expect map: the button labels every strip render
// carries (the ✕ cap stands where EXIT was; guard.js fails if btn-exit
// ever comes back), plus per-fixture entries.
func exp(extra map[string]string) map[string]string {
	m := map[string]string{
		"btn-next": "NEXT", "btn-love": "LOVE",
		"btn-pause": "PAUSE", "btn-more": "MORE", "btn-close": "✕",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// expC is exp for unfolded console fixtures, where MORE becomes LESS.
func expC(extra map[string]string) map[string]string {
	m := exp(extra)
	m["btn-more"] = "LESS"
	return m
}

// fixtures returns every fixture the plan names: the strip states plus
// the console views, each with its exact content expectations.
func fixtures() []fixture {
	play := playingSnapshot()
	out := []fixture{
		{Name: "playing", Snapshot: play, Level: ptrLevel(0.6, 0.5, 0.8), BothModes: true,
			Expect: exp(map[string]string{
				"station": "SomaFM: Groove Salad", "track": "Boards of Canada — Roygbiv",
				"status-word": "● LIVE 00:42", "status-detail": "· tag: ambient",
			}),
			ExpectUnfolded: map[string]string{"btn-more": "LESS"},
			// Wails drags the frame, never a control — the ✕ cap included.
			ExpectStyle: map[string]map[string]string{
				"left-block": {"--wails-draggable": "drag"},
				"btn-next":   {"--wails-draggable": ""},
				"btn-close": {
					"--wails-draggable": "", "background-color": "rgb(255, 85, 85)",
					"border-radius": "0px 10px 10px 0px", "font-size": "13px", "font-weight": "700",
				},
				"btn-more": {"border-radius": "0px 14px 14px 0px"},
			},
			// The top bar's tan segment ends at 428, the ✕ cap takes
			// 432–480 × 0–20, and MORE/LESS spans 376–480.
			ExpectRect: map[string][4]float64{
				"bar-b":     {332, 0, 96, 12},
				"btn-close": {432, 0, 48, 20},
				"btn-more":  {376, 100, 104, 28},
			}},
		{Name: "tuning", Snapshot: session.Snapshot{
			Phase: "tuning", HaveStation: true, Station: dsoCard,
			Reason: "plays 3 artists you love", Volume: 70,
			Slots: [9]bool{true, true, true, true},
		}, Expect: exp(map[string]string{
			"station": "Deep Space One", "track": "plays 3 artists you love",
			"status-word": "◌ TUNING",
		})},
		{Name: "reconnecting", Snapshot: withFlags(play, "tuning", func(s *session.Snapshot) {
			s.Reconnecting = true
			s.Note = "signal lost · reconnecting"
		}), Expect: exp(map[string]string{
			"station": "SomaFM: Groove Salad", "track": "Boards of Canada — Roygbiv",
			"status-word": "◌ RECONNECTING", "status-detail": "· signal lost · reconnecting",
		})},
		{Name: "resuming", Snapshot: withFlags(play, "tuning", func(s *session.Snapshot) {
			s.Resuming = true
		}), Expect: exp(map[string]string{
			"station": "SomaFM: Groove Salad", "track": "Boards of Canada — Roygbiv",
			"status-word": "◌ RESUMING",
		})},
		{Name: "seeking", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Seeking = true
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"track":       "Boards of Canada — Roygbiv",
			"status-word": "◌ SEEKING",
		})},
		{Name: "buffering", Snapshot: withFlags(play, "buffering", func(s *session.Snapshot) {}), Level: ptrLevel(0.3, 0.3, 0.5),
			Expect: exp(map[string]string{"status-word": "BUFFERING"})},
		{Name: "paused", Snapshot: withFlags(play, "paused", func(s *session.Snapshot) {
			s.PausedAtMs = fixtureNowMs - 65_000
		}), Expect: exp(map[string]string{
			"status-word": "❚❚ PAUSED 01:05", "btn-pause": "RESUME",
		})},
		{Name: "paused-sleep", Snapshot: withFlags(play, "paused", func(s *session.Snapshot) {
			s.PausedAtMs = fixtureNowMs - 65_000
			s.SleepAtMs = fixtureNowMs + 30*time.Minute.Milliseconds()
		}), Expect: exp(map[string]string{
			"status-word": "❚❚ PAUSED 01:05", "status-detail": "· SLEEP 30 MIN",
			"btn-pause": "RESUME",
		})},
		{Name: "offline", Snapshot: withFlags(play, "offline", func(s *session.Snapshot) {
			s.RetryAtMs = fixtureNowMs + 20_000
		}), Expect: exp(map[string]string{
			"station":     "SomaFM: Groove Salad",
			"track":       "No connection. Stations aren't being blamed for it.",
			"status-word": "■ OFFLINE", "status-detail": "· RETRYING IN 20 S",
			"btn-next": "RETRY NOW",
		}), ExpectStyle: map[string]map[string]string{
			"left-block": {"background-color": "rgb(204, 51, 51)"},
			// Red alert: the ✕ cap goes dark red with a white cross, as
			// EXIT did.
			"btn-close": {"background-color": "rgb(204, 51, 51)", "color": "rgb(255, 255, 255)"},
		}},
		{Name: "stopped", Snapshot: withFlags(play, "stopped", func(s *session.Snapshot) {
			s.Fatal = "mpv exited · restart screech"
		}), Expect: exp(map[string]string{
			"status-word": "■ STOPPED",
			"track":       "mpv exited · restart screech · run screech doctor",
		}), ExpectStyle: map[string]map[string]string{
			"left-block": {"background-color": "rgb(255, 153, 0)"},
		}},
		{Name: "suspect", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Suspect = true
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"status-word": "● LIVE 00:42", "chip": "BREAK",
		})},
		// A band never heard on Screech before: the strip says so where the
		// pick reason would sit.
		{Name: "new-to-you", Snapshot: play, SnapExtra: map[string]any{"newToYou": true, "discovery": true},
			Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
				"status-word": "● LIVE 00:42", "status-detail": "· NEW TO YOU",
			})},
		{Name: "syncing", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Syncing = true
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"status-word": "● LIVE 00:42", "status-detail": "· SYNCING DIRECTORY…",
		})},
		{Name: "virgin", Snapshot: session.Snapshot{
			Phase: "idle", Virgin: true, Volume: 70,
		}, Expect: exp(map[string]string{
			"station": "SCREECH", "track": "Finding a station…",
			"status-word": "◌ TUNING",
		})},
		{Name: "feedback", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Feedback = "LEARNED  Fast skip noted"
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"status-word": "LEARNED", "status-detail": "· Fast skip noted",
		})},
		{Name: "notice", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Notice = "database recovered · moved aside"
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"track": "database recovered · moved aside", "status-word": "● LIVE 00:42",
		})},
		{Name: "loved-track", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Loved = true
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"btn-love": "LOVED",
		})},
		{Name: "long-names", Snapshot: session.Snapshot{
			Phase:       "playing",
			HaveStation: true,
			Station: session.StationCard{
				UUID: "seed:long", Country: "US", Codec: "MP3", Bitrate: 128,
				Name: "A Very Long Station Name From A Very Long Station Directory That Goes On And On Past The Column Edge",
			},
			Reason:      "the reason for this pick runs to eighty characters, rambling on and on",
			Artist:      "An Artist Whose Name Is Absurdly Long And Keeps Going",
			Title:       "And A Title That Will Not Fit Inside The Track Row Either Because Some Streams Send Whole Sentences As Titles",
			HaveTrack:   true,
			PlayStartMs: fixtureNowMs - 42_000,
			Volume:      70,
		}, Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"station":     "A Very Long Station Name From A Very Long Station Directory That Goes On And On Past The Column Edge",
			"track":       "An Artist Whose Name Is Absurdly Long And Keeps Going — And A Title That Will Not Fit Inside The Track Row Either Because Some Streams Send Whole Sentences As Titles",
			"status-word": "● LIVE 00:42",
		})},
		// A stream that names no artist must not gain a dangling "— ".
		{Name: "artistless-track", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Artist = ""
			s.Title = "Hi-Fi Internet Stream"
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"track": "Hi-Fi Internet Stream",
		})},
		// Ten seconds locked with no title: say why the row is empty.
		{Name: "no-track-info", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Artist, s.Title = "", ""
			s.HaveTrack = false
			s.PlayStartMs = fixtureNowMs - 15_000
		}), Expect: exp(map[string]string{
			"track":       "No track info sent",
			"status-word": "● LIVE 00:15",
		}), ExpectStyle: map[string]map[string]string{
			"track": {"color": "rgb(122, 153, 179)"},
		}},
		{Name: "volume-0", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Volume = 0
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"vol-value": "0",
		})},
		{Name: "volume-100", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Volume = 100
		}), Level: ptrLevel(0.6, 0.5, 0.8), Expect: exp(map[string]string{
			"vol-value": "100",
		})},

		// Console fixtures: every one renders the strip plus its panel.
		{Name: "console-recent", Snapshot: play, Unfolded: true, Tab: "recent",
			Recent: sampleRecent(), Expect: expC(map[string]string{
				"hdr-name": "Recent", "hdr-count": "6 tracks",
				"row-0":       "Boards of Canada — Roygbiv",
				"row-0-right": "SomaFM: Groove Salad",
				"row-0-heart": "♥",
			})},
		{Name: "console-recent-long", Snapshot: play, Unfolded: true, Tab: "recent",
			Recent: append([]RecentRow{{
				StationUUID: somaCard.UUID,
				StationName: "A Station Name That Runs To Sixty Characters Altogether Now",
				Artist:      "An Artist Whose Name Is Absurdly Long And Keeps Going",
				Title:       "And A Title That Will Not Fit Inside The Track Row Either Because Some Streams Send Whole Sentences As Titles",
				HeardAtMs:   fixtureNowMs - 40*time.Minute.Milliseconds(),
			}}, sampleRecent()...), Expect: expC(map[string]string{
				"hdr-name": "Recent", "hdr-count": "7 tracks",
				"row-0": "An Artist Whose Name Is Absurdly Long And Keeps Going — " +
					"And A Title That Will Not Fit Inside The Track Row Either Because Some Streams Send Whole Sentences As Titles",
				"row-0-right": "A Station Name That Runs To Sixty Characters Altogether Now",
			}), ExpectStyle: map[string]map[string]string{
				// The station column takes min(content, 160 px).
				"row-0-right": {"width": "160px"},
			}},
		{Name: "console-loved", Snapshot: play, Unfolded: true, Tab: "loved",
			Loved: sampleLoved(6), Selected: 0, RemoveArmed: true,
			Expect: expC(map[string]string{
				"hdr-name": "Loved", "hdr-count": "6 tracks",
				"row-0":       "Boards of Canada — Roygbiv",
				"act-remove":  "CONFIRM",
				"filter-pill": "/ FILTER",
				"preset-3":    "3",
				"preset-save": "SAVED",
			}), ExpectStyle: map[string]map[string]string{
				"tab-loved":  {"background-color": "rgb(153, 204, 255)"},
				"tab-recent": {"background-color": "rgb(153, 153, 255)"},
			}},
		{Name: "console-loved-filter", Snapshot: play, Unfolded: true, Tab: "loved",
			Loved: sampleLoved(2), Selected: 0, Filter: "orb",
			Expect: expC(map[string]string{
				"hdr-name": "Loved", "hdr-count": "2 tracks",
				"row-0": "Boards of Canada — Roygbiv",
			})},
		{Name: "console-stations", Snapshot: play, Unfolded: true, Tab: "stations",
			Stations: sampleStations(), Selected: 0, Expect: expC(map[string]string{
				"hdr-name": "Stations", "hdr-count": "5 stations",
				"row-0":       "SomaFM: Groove Salad",
				"row-0-right": "1d 15h · P3 · ♥ 7",
				"act-tune":    "TUNE",
			})},
		{Name: "console-taste", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Loved = true
		}), Unfolded: true, Tab: "taste",
			Taste: sampleTaste(), Expect: expC(map[string]string{
				"btn-love": "LOVED",
				"hdr-name": "Taste", "hdr-count": "88 h all time · 14 h this week · 12 loved",
				"dp-0": "morning", "artist-0": "Boards of Canada", "tag-0": "ambient",
			})},
		// FIND, the front door: one bar, up to four discovery stations to
		// pick up, one faint hint. The geometry is front-door-v6's, page px.
		{Name: "console-find", Snapshot: play, Unfolded: true, Tab: "find",
			Discoveries: sampleDiscoveries(), Expect: expC(map[string]string{
				"bar-cap": "FIND", "bar-go": "GO ▸",
				"bar-input@placeholder": "Artist, song, genre or YouTube link",
				"find-label":            "Pick up where you left off",
				"row-0":                 "swedish death metal", "row-0-right": "2h ago",
				"find-hint": "ESC TO CLOSE · TAB FOR RADIO",
			}), ExpectStyle: map[string]map[string]string{
				"tab-find":  {"background-color": "rgb(153, 204, 255)"},
				"tab-radio": {"background-color": "rgb(153, 153, 255)"},
				"tab-more":  {"background-color": "rgb(255, 204, 153)"},
				// The two caps are one colour, the bar's.
				"bar-cap": {"background-color": "rgb(255, 153, 0)"},
				"bar-go":  {"background-color": "rgb(255, 153, 0)"},
				"bar-input": {
					"font-size": "16px", "color": "rgb(153, 204, 255)",
					"text-overflow": "ellipsis", "--wails-draggable": "",
				},
				"find-label": {"color": "rgb(255, 153, 102)", "font-size": "11px"},
				"find-hint":  {"color": "rgb(119, 119, 119)", "font-size": "11px", "text-align": "center"},
			}, ExpectRect: map[string][4]float64{
				"tab-find": {0, 156, 96, 28}, "tab-radio": {0, 188, 96, 28},
				"tab-recent": {0, 220, 96, 28}, "tab-loved": {0, 252, 96, 28},
				"tab-stations": {0, 284, 96, 28}, "tab-taste": {0, 316, 96, 28},
				"tab-more": {0, 348, 96, 28},
				"find-bar": {136, 148, 328, 36}, "bar-cap": {136, 148, 52, 36},
				"bar-go": {412, 148, 52, 36},
				// The label heads rows at 28-px pitch from 220.
				"find-label": {136, 204, 328, 16}, "row-top": {124, 220, 340, 28},
				"find-hint": {124, 348, 340, 16},
			}, Absent: []string{"tab-search", "tab-info", "tab-sleep"}},
		// Nothing discovered yet: no label, no rows, the bar and the hint.
		{Name: "console-find-empty", Snapshot: play, Unfolded: true, Tab: "find",
			Expect: expC(map[string]string{
				"bar-cap": "FIND", "find-hint": "ESC TO CLOSE · TAB FOR RADIO",
			}), Absent: []string{"find-label", "row-top"}},
		// A long link scrolls inside the field instead of growing it.
		{Name: "console-find-typed", Snapshot: play, Unfolded: true, Tab: "find",
			Find: longLink, Discoveries: sampleDiscoveries(), Expect: expC(map[string]string{
				"bar-cap": "FIND", "bar-input": longLink,
			}), ExpectStyle: map[string]map[string]string{
				"bar-input": {"text-overflow": "ellipsis", "font-size": "16px"},
			}},
		// RADIO: the same bar in blue; below it the most-listened radio
		// stations, as STATIONS lays them out, six at most.
		{Name: "console-radio", Snapshot: play, Unfolded: true, Tab: "radio",
			RadioTop: append(sampleStations(), StationRow{
				UUID: "seed:long", Name: "A Station Whose Name Runs On Past The Edge Of Its Row Entirely",
				TotalSec: 3_600, LoveCount: 1,
			}), Expect: expC(map[string]string{
				"bar-cap": "RADIO", "bar-go": "GO ▸",
				"bar-input@placeholder": "Station, genre or country",
				"row-0":                 "SomaFM: Groove Salad", "row-0-right": "1d 15h · P3 · ♥ 7",
			}), ExpectStyle: map[string]map[string]string{
				"tab-radio": {"background-color": "rgb(153, 204, 255)"},
				"tab-find":  {"background-color": "rgb(153, 153, 255)"},
				"bar-cap":   {"background-color": "rgb(153, 153, 255)"},
				"bar-go":    {"background-color": "rgb(153, 153, 255)"},
			}, ExpectRect: map[string][4]float64{
				"find-bar": {136, 148, 328, 36}, "row-top": {124, 200, 340, 28},
			}, Absent: []string{"find-hint", "find-label"}},
		{Name: "console-radio-empty", Snapshot: play, Unfolded: true, Tab: "radio",
			Expect: expC(map[string]string{"bar-cap": "RADIO"}), Absent: []string{"row-top"}},
		// MORE holds what INFO and SLEEP held: the info rows, and the sleep
		// pill that carries the countdown.
		{Name: "console-more", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.SleepAtMs = fixtureNowMs + 28*time.Minute.Milliseconds()
		}), Unfolded: true, Tab: "more", Info: sampleInfo(), Expect: expC(map[string]string{
			"hdr-name": "More", "sleep-pill": "SLEEP 28",
			"info-0": "FROM", "info-0-value": "US · MP3 128K",
		}), ExpectStyle: map[string]map[string]string{
			"tab-more": {"background-color": "rgb(153, 204, 255)"},
		}, Absent: []string{"tab-info", "tab-sleep"}},
		{Name: "console-more-idle", Snapshot: play, Unfolded: true, Tab: "more", Info: sampleInfo(),
			Expect: expC(map[string]string{"hdr-name": "More", "sleep-pill": "SLEEP", "info-0": "FROM"})},
		{Name: "console-more-nothing-tuned", Snapshot: session.Snapshot{Phase: "idle", Virgin: true, Volume: 70},
			Unfolded: true, Tab: "more", Expect: expC(map[string]string{
				"hdr-name": "More", "sleep-pill": "SLEEP", "empty": "nothing tuned yet",
			})},
		{Name: "empty-recent", Snapshot: play, Unfolded: true, Tab: "recent",
			Expect: expC(map[string]string{
				"hdr-name": "Recent", "empty": "nothing heard yet",
			})},
		{Name: "empty-loved", Snapshot: play, Unfolded: true, Tab: "loved",
			Expect: expC(map[string]string{
				"hdr-name": "Loved", "empty": "love a track and it will appear here",
			})},
		{Name: "empty-stations", Snapshot: play, Unfolded: true, Tab: "stations",
			Expect: expC(map[string]string{
				"hdr-name": "Stations", "empty": "save a station with f, or love a track",
			})},
		{Name: "empty-taste", Snapshot: play, Unfolded: true, Tab: "taste",
			Expect: expC(map[string]string{
				"hdr-name": "Taste", "empty": "Nothing yet. Listen for a while and this fills in.",
			})},
		{Name: "many-rows", Snapshot: play, Unfolded: true, Tab: "loved",
			Loved: sampleLoved(150), Selected: 0, Expect: expC(map[string]string{
				"hdr-name": "Loved", "hdr-count": "150 tracks",
				"row-0": "Boards of Canada — Roygbiv",
			})},
		// Scrolled: the view starts at row 20 with row 22 selected; the
		// first mounted row carries row-top. sampleLoved cycles the six
		// sample tracks, so row 20 is src[2].
		{Name: "many-rows-scrolled", Snapshot: play, Unfolded: true, Tab: "loved",
			Loved: sampleLoved(150), Top: 20, Selected: 22, Expect: expC(map[string]string{
				"hdr-name": "Loved", "hdr-count": "150 tracks",
				// row-top is the row element: its textContent is the
				// track and side spans concatenated.
				"row-top": "Om Unit — PatterningNEU Radio",
			})},
		{Name: "presets-full", Snapshot: withFlags(play, "playing", func(s *session.Snapshot) {
			s.Slots = [9]bool{true, true, true, true, true, true, true, true, true}
		}), Unfolded: true, Tab: "loved", Loved: sampleLoved(6), Expect: expC(map[string]string{
			"hdr-name": "Loved", "preset-9": "9", "preset-save": "SAVED",
		})},
		{Name: "console-offline", Snapshot: withFlags(play, "offline", func(s *session.Snapshot) {
			s.RetryAtMs = fixtureNowMs + 20_000
		}), Unfolded: true, Tab: "recent", Recent: sampleRecent(), Expect: expC(map[string]string{
			"hdr-name": "Recent", "row-0": "Boards of Canada — Roygbiv",
			"btn-next": "RETRY NOW",
		}), ExpectStyle: map[string]map[string]string{
			"left-block":  {"background-color": "rgb(204, 51, 51)"},
			"sidebar-top": {"background-color": "rgb(204, 51, 51)"},
			"elbow":       {"background-color": "rgb(255, 170, 170)"},
			// Red alert turns the live tab palest.
			"tab-recent": {"background-color": "rgb(255, 214, 214)"},
			"tab-find":   {"background-color": "rgb(255, 102, 102)"},
			"tab-more":   {"background-color": "rgb(255, 170, 170)"},
		}},

		// keys drives TestKeyboardMap: a virgin snapshot (so RADIO's space
		// wildcard is live) unfolded onto LOVED with eight rows — enough
		// for the ±5/End moves and one wheel scroll — and the lists FIND
		// and RADIO draw their rows from.
		{Name: "keys", Snapshot: session.Snapshot{
			Phase: "idle", Virgin: true, Volume: 70,
			Slots: [9]bool{true, false, true},
		}, Unfolded: true, Tab: "loved", Loved: sampleLoved(8),
			Discoveries: sampleDiscoveries(), RadioTop: sampleStations()},
		// keys-folded carries loved rows but stays folded, on LOVED: every
		// row key must be a no-op — nothing may act on a list that isn't
		// drawn — while MORE and / open the console on FIND.
		{Name: "keys-folded", Snapshot: play, Tab: "loved", Loved: sampleLoved(8)},
		// Reduced motion through the bridge preference: the buffering
		// pulse flattens but the meter keeps running.
		{Name: "reduced", Snapshot: withFlags(play, "buffering", func(s *session.Snapshot) {}),
			Reduced: true, Expect: exp(map[string]string{
				"status-word": "BUFFERING",
			}), ExpectStyle: map[string]map[string]string{
				"status": {"animation-name": "none"},
			}},
	}
	return out
}

// The console fixtures all draw on the same small library, built from the
// bridge's real row types.
func sampleRecent() []RecentRow {
	base := fixtureNowMs - 40*time.Minute.Milliseconds()
	rows := []RecentRow{
		{StationUUID: somaCard.UUID, StationName: somaCard.Name, Artist: "Boards of Canada", Title: "Roygbiv", HeardAtMs: base, Loved: true},
		{StationUUID: dsoCard.UUID, StationName: dsoCard.Name, Artist: "Stellardrone", Title: "Billions and Billions", HeardAtMs: base - 240_000},
		{StationUUID: "seed:neuradio", StationName: "NEU Radio", Artist: "Om Unit", Title: "Patterning", HeardAtMs: base - 480_000},
		{StationUUID: "seed:tvn", StationName: "TVN Radio", Artist: "Hermanez", Title: "Hold Me", HeardAtMs: base - 720_000},
		{StationUUID: somaCard.UUID, StationName: somaCard.Name, Artist: "The Orb", Title: "Little Fluffy Clouds", HeardAtMs: base - 960_000},
		{StationUUID: "seed:dronezone", StationName: "Drone Zone", Artist: "Tycho", Title: "Awake", HeardAtMs: base - 1_200_000},
	}
	return rows
}

// longLink is the 200-character YouTube link a listener might paste: a
// watch page with a playlist along for the ride.
const longLink = "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLFsQleAWXsj_4yDeebiIADdH5FMayBiJo" +
	"&index=12345&start_radio=1&pp=iAQB&utm_source=share&utm_medium=member_desktop&utm_campaign=front-door-fixture-padding"

// Both arrays are legal only when the link is exactly 200 characters.
var (
	_ [len(longLink) - 200]struct{}
	_ [200 - len(longLink)]struct{}
)

// FIND's four rows: a phrase, a song a link started from, and two more
// phrases, newest first.
func sampleDiscoveries() []DiscoveryRow {
	h := time.Hour.Milliseconds()
	return []DiscoveryRow{
		{UUID: "disc:0000000000000001", Name: "swedish death metal", LastAtMs: fixtureNowMs - 2*h},
		{UUID: "disc:0000000000000002", Name: "Boards of Canada – Roygbiv", LastAtMs: fixtureNowMs - 30*h},
		{UUID: "disc:0000000000000003", Name: "dungeon synth", LastAtMs: fixtureNowMs - 9*24*h},
		{UUID: "disc:0000000000000004", Name: "blackgaze", LastAtMs: fixtureNowMs - 17*24*h},
	}
}

func sampleLoved(n int) []LovedRow {
	src := sampleRecent()
	out := make([]LovedRow, 0, n)
	for i := 0; i < n; i++ {
		e := src[i%len(src)]
		out = append(out, LovedRow{
			ArtistKey:   "artist:" + e.Artist,
			Artist:      e.Artist,
			Title:       e.Title,
			StationUUID: e.StationUUID,
			StationName: e.StationName,
			LovedAtMs:   e.HeardAtMs - int64(i)*86_400_000,
			LoveCount:   3 + i%4,
			HeardCount:  9 + i%11,
		})
	}
	return out
}

func sampleStations() []StationRow {
	return []StationRow{
		{UUID: somaCard.UUID, Name: somaCard.Name, TotalSec: 141_600, PresetSlot: 3, LoveCount: 7},
		{UUID: dsoCard.UUID, Name: dsoCard.Name, TotalSec: 91_200, LoveCount: 4},
		{UUID: "seed:dronezone", Name: "Drone Zone", TotalSec: 55_800, PresetSlot: 1, LoveCount: 2},
		{UUID: "seed:neuradio", Name: "NEU Radio", TotalSec: 28_200, LoveCount: 1},
		{UUID: "seed:tvn", Name: "TVN Radio", TotalSec: 9_000, LoveCount: 0},
	}
}

func sampleTaste() TasteView {
	return TasteView{
		TotalSec: 316_800, WeekSec: 50_400, LovedTracks: 12,
		Dayparts: []DaypartRow{
			{Name: "morning", Sec: 28_800},
			{Name: "day", Sec: 86_400},
			{Name: "evening", Sec: 158_400},
			{Name: "night", Sec: 43_200},
		},
		TopArtists: []ArtistRow{
			{Artist: "Boards of Canada", Plays: 21},
			{Artist: "Stellardrone", Plays: 14},
			{Artist: "Tycho", Plays: 9},
		},
		TopTags: []TagRow{
			{Tag: "ambient", Weight: 0.9},
			{Tag: "electronic", Weight: 0.6},
			{Tag: "downtempo", Weight: 0.35},
		},
		TopStations: sampleStations()[:3],
	}
}

func sampleInfo() InfoView {
	return InfoView{
		Name: somaCard.Name, Country: "US", Codec: "MP3", Bitrate: 128,
		Tags:        []string{"ambient", "electronic", "downtempo"},
		Homepage:    "https://somafm.com/groovesalad",
		StreamHost:  "ice1.somafm.com",
		ListenedSec: 141_600, Visits: 12,
		FirstHeardMs: fixtureNowMs - 40*86_400_000,
		LastHeardMs:  fixtureNowMs - 3_600_000,
		LovedTracks:  7, Heard: true, Match: 0.82, Daypart: "evening",
		PresetSlot: 3, FailCount: 0, MaxFail: 3,
	}
}

func withFlags(base session.Snapshot, phase string, f func(*session.Snapshot)) session.Snapshot {
	s := base
	s.Phase = phase
	f(&s)
	return s
}

// startFixtureServer serves the real embedded frontend under /, plus the
// test-only fixture bridge, the guard script and the fixture data.
func startFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	assets, err := frontendFS()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/fixture.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/fixture.js")
	})
	mux.HandleFunc("/guard.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/guard.js")
	})
	mux.HandleFunc("/keys-test.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/keys-test.js")
	})
	// The fixture bridge's contract: every callable name and its argument
	// count, reflected from *Bridge so JS can never drift from Go.
	mux.HandleFunc("/arity.json", func(w http.ResponseWriter, r *http.Request) {
		typ := reflect.TypeOf((*Bridge)(nil))
		arity := map[string]int{}
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			arity[m.Name] = m.Func.Type().NumIn() - 1 // minus the receiver
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(arity)
	})
	mux.HandleFunc("/fixtures.json", func(w http.ResponseWriter, r *http.Request) {
		list := fixtures()
		payload, err := json.Marshal(struct {
			NowMs    int64     `json:"nowMs"`
			Fixtures []fixture `json:"fixtures"`
		}{NowMs: fixtureNowMs, Fixtures: list})
		if err != nil {
			t.Fatalf("marshal fixtures: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
