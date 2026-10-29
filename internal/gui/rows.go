package gui

import (
	"net/url"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// The row and view types below carry exactly what the terminal's library,
// taste and info views show — times as unix milliseconds, durations as
// whole seconds — so the window's page never has to know about Go types.

// RecentRow is one RECENT line: the track, where it came from, and
// whether it's loved.
type RecentRow struct {
	StationUUID string `json:"stationUuid"`
	StationName string `json:"stationName"`
	Artist      string `json:"artist"`
	Title       string `json:"title"`
	HeardAtMs   int64  `json:"heardAtMs"`
	Loved       bool   `json:"loved"`
}

// LovedRow is one LOVED line: the track, the station that played it, and
// the counts the detail line shows.
type LovedRow struct {
	ArtistKey   string `json:"artistKey"`
	Artist      string `json:"artist"`
	Title       string `json:"title"`
	StationUUID string `json:"stationUuid"`
	StationName string `json:"stationName"`
	LovedAtMs   int64  `json:"lovedAtMs"`
	LoveCount   int    `json:"loveCount"`
	HeardCount  int    `json:"heardCount"`
}

// StationRow is one STATIONS line: name, cumulative listen time, preset
// slot and love count.
type StationRow struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	TotalSec   int64  `json:"totalSec"`
	PresetSlot int    `json:"presetSlot"`
	LoveCount  int    `json:"loveCount"`
}

// DaypartRow is one "when you listen" bar.
type DaypartRow struct {
	Name string `json:"name"`
	Sec  int64  `json:"sec"`
}

// ArtistRow is one artist leaderboard line.
type ArtistRow struct {
	Artist string `json:"artist"`
	Plays  int    `json:"plays"`
}

// TagRow is one tag-affinity line; Weight is the decayed share above the
// prior, and the page only ever draws it relative to the top row.
type TagRow struct {
	Tag    string  `json:"tag"`
	Weight float64 `json:"weight"`
}

// TasteView is the TASTE tab: the totals, the daypart bars in clock
// order, and the three leaderboards.
type TasteView struct {
	TotalSec    int64        `json:"totalSec"`
	WeekSec     int64        `json:"weekSec"`
	Dayparts    []DaypartRow `json:"dayparts"`
	TopArtists  []ArtistRow  `json:"topArtists"`
	TopTags     []TagRow     `json:"topTags"`
	TopStations []StationRow `json:"topStations"`
	LovedTracks int          `json:"lovedTracks"`
}

// InfoView is the station sheet: directory facts above, what screech has
// learned below.
type InfoView struct {
	Name         string   `json:"name"`
	Country      string   `json:"country"`
	Codec        string   `json:"codec"`
	Bitrate      int      `json:"bitrate"`
	Tags         []string `json:"tags"`
	Homepage     string   `json:"homepage"`
	StreamHost   string   `json:"streamHost"`
	ListenedSec  int64    `json:"listenedSec"`
	Visits       int      `json:"visits"`
	FirstHeardMs int64    `json:"firstHeardMs"`
	LastHeardMs  int64    `json:"lastHeardMs"`
	LovedTracks  int      `json:"lovedTracks"`
	Heard        bool     `json:"heard"`
	Match        float64  `json:"match"`
	Daypart      string   `json:"daypart"`
	PresetSlot   int      `json:"presetSlot"`
	FailCount    int      `json:"failCount"`
	MaxFail      int      `json:"maxFail"`
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func recentRows(tracks []core.RecentTrack) []RecentRow {
	out := make([]RecentRow, len(tracks))
	for i, e := range tracks {
		out[i] = RecentRow{
			StationUUID: e.StationUUID,
			StationName: e.StationName,
			Artist:      e.Artist,
			Title:       e.Title,
			HeardAtMs:   unixMs(e.HeardAt),
			Loved:       e.Loved,
		}
	}
	return out
}

func lovedRows(entries []core.LovedTrack) []LovedRow {
	out := make([]LovedRow, len(entries))
	for i, e := range entries {
		out[i] = LovedRow{
			ArtistKey:   e.ArtistKey,
			Artist:      e.Artist,
			Title:       e.Title,
			StationUUID: e.StationUUID,
			StationName: e.StationName,
			LovedAtMs:   unixMs(e.LovedAt),
			LoveCount:   e.LoveCount,
			HeardCount:  e.HeardCount,
		}
	}
	return out
}

func stationRows(entries []core.SavedStation) []StationRow {
	out := make([]StationRow, len(entries))
	for i, e := range entries {
		out[i] = StationRow{
			UUID:       e.UUID,
			Name:       e.Name,
			TotalSec:   int64(e.Total / time.Second),
			PresetSlot: e.PresetSlot,
			LoveCount:  e.LoveCount,
		}
	}
	return out
}

func tasteView(t core.Taste) TasteView {
	v := TasteView{
		TotalSec:    int64(t.Total / time.Second),
		WeekSec:     int64(t.Week / time.Second),
		LovedTracks: t.LovedTracks,
	}
	// Dayparts keep the clock's order, and a zero-time daypart is skipped —
	// same as the terminal view.
	for _, dp := range []string{
		core.DaypartMorning, core.DaypartDay, core.DaypartEvening, core.DaypartNight,
	} {
		d := t.ByDaypart[dp]
		if d <= 0 {
			continue
		}
		v.Dayparts = append(v.Dayparts, DaypartRow{Name: dp, Sec: int64(d / time.Second)})
	}
	for _, a := range t.TopArtists {
		v.TopArtists = append(v.TopArtists, ArtistRow{Artist: a.Artist, Plays: a.Plays})
	}
	for _, tag := range t.TopTags {
		v.TopTags = append(v.TopTags, TagRow{Tag: tag.Tag, Weight: tag.Weight})
	}
	for _, s := range t.TopStations {
		v.TopStations = append(v.TopStations, StationRow{
			UUID: s.UUID, Name: s.Name, TotalSec: int64(s.Total / time.Second),
		})
	}
	return v
}

func infoView(st core.Station, info core.StationInfo, slot int) InfoView {
	v := InfoView{
		Name:         st.Name,
		Country:      st.Country,
		Codec:        st.Codec,
		Bitrate:      st.Bitrate,
		Tags:         st.TagList(),
		Homepage:     st.Homepage,
		ListenedSec:  int64(info.Listened / time.Second),
		Visits:       info.Visits,
		FirstHeardMs: unixMs(info.FirstHeard),
		LastHeardMs:  unixMs(info.LastHeard),
		LovedTracks:  info.LovedTracks,
		Heard:        info.Heard,
		Match:        info.Match,
		Daypart:      info.Daypart,
		PresetSlot:   slot,
		FailCount:    st.FailCount,
		MaxFail:      core.MaxFailCount,
	}
	if u, err := url.Parse(st.StreamURL()); err == nil {
		v.StreamHost = u.Host
	}
	return v
}

// sink adapts the runner's publications onto the Wails event channel the
// page listens on.
type sink struct{ b *Bridge }

func (s sink) Snapshot(sn session.Snapshot) {
	if e := s.b.emitter(); e != nil {
		e.Emit("snapshot", sn)
	}
}
func (s sink) Level(l session.Level) {
	if e := s.b.emitter(); e != nil {
		e.Emit("level", [3]float64{l.L, l.R, l.Peak})
	}
}
