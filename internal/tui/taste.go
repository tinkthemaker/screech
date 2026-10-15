package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
)

// tasteRows is the TASTE tab: how much has actually been heard, when in
// the day, and which artists, tags and stations the hours keep landing
// on — the label-column language of the station sheet.
func (m Model) tasteRows(rows []string, iw, budget int) []string {
	t := m.taste
	if t.Total <= 0 {
		rows = append(rows, m.th.Dim.Render(runewidth.Truncate(
			"Nothing yet. Listen for a while and this fills in.", iw, m.th.G.Ellipsis)))
		if len(rows) > budget {
			rows = rows[:budget]
		}
		return rows
	}

	const labelW = 9
	dot := " " + m.th.G.Dot + " "
	indent := strings.Repeat(" ", 3+labelW)
	mid := func(s string, w int) string {
		return m.th.Mid.Render(runewidth.Truncate(s, maxI(w, 1), m.th.G.Ellipsis))
	}
	// put renders one line: the section's accent label on its first row,
	// the label column left blank after. Too narrow for the column, the
	// plain form carries the line alone. The trailing space after the
	// label matters: LISTENING fills the column exactly.
	put := func(first bool, name, plain, styled string) string {
		if iw < 3+labelW {
			return m.th.Dim.Render(runewidth.Truncate(plain, iw, m.th.G.Ellipsis))
		}
		l := indent
		if first {
			l = "  " + m.th.AccentDim.Render(runewidth.FillRight(name, labelW)) + " "
		}
		return l + styled
	}
	textW := iw - 3 - labelW
	// Counts close a bounded column rather than the far edge of a wide
	// terminal, so a number still reads as belonging to its name.
	listW := textW
	if listW > 48 {
		listW = 48
	}
	// emit appends a section only when its heading and at least one item
	// still fit; a dangling heading cut at the fold reads as breakage.
	emit := func(block []string) {
		if len(block) == 0 || len(rows)+2 > budget {
			return
		}
		rows = append(rows, block...)
	}

	listen := fmtTotalTime(t.Total) + " all time" + dot + fmtTotalTime(t.Week) + " this week"
	emit([]string{put(true, "LISTENING", listen, mid(listen, textW))})

	// Dayparts keep the clock's order, not the leaderboard's. The section
	// label rides the first emitted row — a zero-time daypart is skipped,
	// so it is not always the first name in the list.
	var dayparts []string
	for _, dp := range []string{
		core.DaypartMorning, core.DaypartDay, core.DaypartEvening, core.DaypartNight,
	} {
		d := t.ByDaypart[dp]
		if d <= 0 {
			continue
		}
		pct := int(float64(d)/float64(t.Total)*100 + 0.5)
		plain := dp + " " + fmt.Sprintf("%d%%", pct)
		styled := mid(plain, textW)
		if iw >= 3+labelW+7+1+10+1+4 {
			styled = mid(runewidth.FillRight(dp, 7)+" ", 8) +
				m.fillBar(float64(d)/float64(t.Total), 10) + " " + mid(fmt.Sprintf("%d%%", pct), 4)
		}
		dayparts = append(dayparts, put(len(dayparts) == 0, "DAYPARTS", plain, styled))
	}
	emit(dayparts)

	var artists []string
	for i, a := range t.TopArtists {
		plays := fmt.Sprintf("%d", a.Plays)
		w := maxI(listW-2-len(plays), 8)
		plain := runewidth.FillRight(runewidth.Truncate(a.Artist, w, m.th.G.Ellipsis), w) + "  " + plays
		artists = append(artists, put(i == 0, "ARTISTS", a.Artist+" "+plays, mid(plain, textW)))
	}
	emit(artists)

	var tags []string
	maxW := 0.0
	for _, tag := range t.TopTags {
		if tag.Weight > maxW {
			maxW = tag.Weight
		}
	}
	// The weight itself is a decayed pseudo-count with no unit a listener
	// could read; rank and the bar relative to the strongest tag say it.
	for i, tag := range t.TopTags {
		plain := tag.Tag
		styled := mid(plain, textW)
		if maxW > 0 && iw >= 3+labelW+12+1+10 {
			styled = mid(runewidth.FillRight(runewidth.Truncate(tag.Tag, 12, m.th.G.Ellipsis), 12)+" ", 13) +
				m.fillBar(tag.Weight/maxW, 10)
		}
		tags = append(tags, put(i == 0, "TAGS", plain, styled))
	}
	emit(tags)

	var stations []string
	for i, e := range t.TopStations {
		dur := fmtTotalTime(e.Total)
		if dur == "" {
			dur = "<1m" // tuned but its open listen hasn't banked a minute
		}
		w := maxI(listW-2-len(dur), 8)
		plain := runewidth.FillRight(runewidth.Truncate(e.Name, w, m.th.G.Ellipsis), w) + "  " + dur
		stations = append(stations, put(i == 0, "STATIONS", e.Name+" "+dur, mid(plain, textW)))
	}
	emit(stations)

	loved := "1 track"
	if t.LovedTracks != 1 {
		loved = fmt.Sprintf("%d tracks", t.LovedTracks)
	}
	emit([]string{put(true, "LOVED", loved, mid(loved, textW))})

	if len(rows) > budget {
		rows = rows[:budget]
	}
	return rows
}

// fillBar draws frac of a cells-wide meter — solid accent cells over a
// quiet band. The fill is a different glyph, not just a brighter band:
// austere themes, NO_COLOR and screen readers would otherwise read ten
// identical dashes whatever the value.
func (m Model) fillBar(frac float64, cells int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(cells) + 0.5)
	if frac > 0 && filled == 0 {
		filled = 1 // a real share never draws as none
	}
	full := string(m.th.G.Blocks[len(m.th.G.Blocks)-1])
	var b strings.Builder
	for i := 0; i < cells; i++ {
		if i < filled {
			b.WriteString(m.th.Accent.Render(full))
		} else {
			b.WriteString(m.th.Dim.Render(m.th.G.Band))
		}
	}
	return b.String()
}
