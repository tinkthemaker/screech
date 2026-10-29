package tui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
)

// infoRows is the i sheet: the tuned station's directory facts above, and
// what screech has learned about it below — same label-column language as
// the help sheet. Personal rows read "unavailable" if the query failed.
func (m Model) infoRows(iw, budget int) []string {
	dot := " " + m.th.G.Dot + " "
	const labelW = 9
	row := func(label, value string) string {
		if iw < 2+labelW {
			return m.th.Dim.Render(runewidth.Truncate(value, iw, m.th.G.Ellipsis))
		}
		return "  " + m.th.AccentDim.Render(runewidth.FillRight(label, labelW)) +
			m.th.Mid.Render(runewidth.Truncate(value, iw-2-labelW, m.th.G.Ellipsis))
	}

	rows := []string{m.th.Accent.Bold(true).Render(
		runewidth.Truncate(cleanStationName(m.s.Station.Name), iw, m.th.G.Ellipsis))}

	from := []string{}
	if c := strings.TrimSpace(m.s.Station.Country); c != "" {
		from = append(from, core.CleanText(c))
	}
	if codec := strings.ToUpper(strings.TrimSpace(m.s.Station.Codec)); codec != "" || m.s.Station.Bitrate > 0 {
		stream := codec
		if m.s.Station.Bitrate > 0 {
			if stream != "" {
				stream += " "
			}
			stream += fmt.Sprintf("%dK", m.s.Station.Bitrate)
		}
		from = append(from, stream)
	}
	fromText := "unknown"
	if len(from) > 0 {
		fromText = strings.Join(from, dot)
	}
	rows = append(rows, row("FROM", fromText))

	tags := strings.Join(m.s.Station.TagList(), ", ")
	tags = strings.TrimSpace(core.CleanText(tags))
	if tags == "" {
		tags = "none listed"
	}
	rows = append(rows, row("TAGS", tags))

	home := strings.TrimSpace(core.CleanText(m.s.Station.Homepage))
	home = strings.TrimPrefix(home, "https://")
	home = strings.TrimPrefix(home, "http://")
	home = strings.TrimSuffix(home, "/")
	if home == "" {
		home = "none listed"
	}
	rows = append(rows, row("HOME", home))

	stream := "none listed"
	if u, err := url.Parse(m.s.Station.StreamURL()); err == nil && u.Host != "" {
		stream = u.Host
	}
	rows = append(rows, row("STREAM", stream))

	// The personal rows all answer from the same query; one error marks
	// them all rather than half the sheet lying with zeroes.
	loaded := m.infoErr == nil && m.info.Daypart != ""

	switch {
	case m.infoErr != nil:
		rows = append(rows, row("HEARD", "unavailable"))
	case !loaded:
		rows = append(rows, row("HEARD", m.th.G.Ellipsis))
	case m.info.FirstHeard.IsZero() || (m.info.Visits == 0 && m.info.Listened < time.Minute):
		// The listen in progress already has a row; until it adds up to
		// a minute this is still a first visit, not "0 visits".
		rows = append(rows, row("HEARD", "first time here"))
	default:
		// The audible total is empty until a finished minute lands — a
		// just-tuned station reads "0 visits", not a leading dot.
		parts := []string{}
		if total := fmtTotalTime(m.info.Listened); total != "" {
			parts = append(parts, total)
		}
		parts = append(parts,
			fmt.Sprintf("%d visits", m.info.Visits),
			"first "+relativeTime(m.info.FirstHeard, m.now),
			"last "+relativeTime(m.info.LastHeard, m.now))
		rows = append(rows, row("HEARD", strings.Join(parts, dot)))
	}

	switch {
	case m.infoErr != nil:
		rows = append(rows, row("LOVES", "unavailable"))
	case !loaded:
		rows = append(rows, row("LOVES", m.th.G.Ellipsis))
	case m.info.LovedTracks == 0:
		rows = append(rows, row("LOVES", "none yet"))
	default:
		rows = append(rows, row("LOVES", fmt.Sprintf("%d tracks loved here", m.info.LovedTracks)))
	}

	switch {
	case m.infoErr != nil:
		rows = append(rows, row("MATCH", "unavailable"))
	case !loaded:
		rows = append(rows, row("MATCH", m.th.G.Ellipsis))
	case !m.info.Heard:
		rows = append(rows, row("MATCH", "still learning"))
	default:
		rows = append(rows, m.matchRow(iw, labelW))
	}

	if slot := m.s.CurrentSlot(); slot > 0 {
		rows = append(rows, row("PRESET", fmt.Sprintf("slot %d", slot)))
	} else {
		rows = append(rows, row("PRESET", "not saved"+dot+"F saves"))
	}

	if m.s.Station.FailCount > 0 {
		rows = append(rows, row("STRIKES", fmt.Sprintf("%d of %d", m.s.Station.FailCount, core.MaxFailCount)))
	}

	if len(rows) > budget {
		rows = rows[:budget]
	}
	return rows
}

// matchRow draws the picker's posterior mean as a ten-cell meter — the
// same band-and-knob language as the volume strip — and the daypart it
// was measured for.
func (m Model) matchRow(iw, labelW int) string {
	const cells = 10
	pct := int(m.info.Match*100 + 0.5)
	knob := int(m.info.Match*float64(cells-1) + 0.5)
	var bar strings.Builder
	for i := 0; i < cells; i++ {
		switch {
		case i == knob:
			bar.WriteString(m.th.Accent.Render(m.th.G.Knob))
		case i < knob:
			bar.WriteString(m.th.AccentDim.Render(m.th.G.Band))
		default:
			bar.WriteString(m.th.Mid.Render(m.th.G.Band))
		}
	}
	word := map[string]string{
		core.DaypartMorning: "this morning",
		core.DaypartDay:     "this afternoon",
		core.DaypartEvening: "this evening",
		core.DaypartNight:   "tonight",
	}[m.info.Daypart]
	if word == "" {
		word = "now"
	}
	text := fmt.Sprintf("%d%% %s", pct, word)
	if iw < 2+labelW+cells+1 {
		// Narrower than label + meter: the number alone still tells.
		return m.th.Dim.Render(runewidth.Truncate(text, iw, m.th.G.Ellipsis))
	}
	rest := maxI(0, iw-2-labelW-cells-1)
	return "  " + m.th.AccentDim.Render(runewidth.FillRight("MATCH", labelW)) +
		bar.String() + " " +
		m.th.Mid.Render(runewidth.Truncate(text, rest, m.th.G.Ellipsis))
}
