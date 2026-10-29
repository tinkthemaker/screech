package tui

import (
	"github.com/mattn/go-runewidth"
)

// helpRows is the ? sheet: every key the main screen and the library take,
// in the same title/key-column/dim-description language as the compact
// theme picker. Rows are cut from the bottom on short terminals, so the
// LISTEN group rides first — it's the one that must survive.
func (m Model) helpRows(iw, budget int) []string {
	dot := " " + m.th.G.Dot + " "
	type entry struct{ key, desc string }
	groups := []struct {
		name  string
		items []entry
	}{
		{"LISTEN", []entry{
			{"SPACE / N", `next station (a quick skip teaches "not for me")`},
			{"L", "love the track; again takes it back"},
			{"F", "save the station to a preset slot"},
			{"1-9", "recall a preset"},
			{"P", "pause / resume (media keys work too)"},
			{"Z", "sleep timer: 15, 30, 45, 60, 90 min, off"},
		}},
		{"FIND", []entry{
			{"/", "discover: a genre, an artist, a station"},
			{"H", "library: recent, loved, saved (TAB switches)"},
			{"I", "about this station"},
		}},
		{"SOUND & LOOK", []entry{
			{"V", "volume (- + or [ ] step it)"},
			{"T", "theme"},
		}},
		{"IN THE LIBRARY", []entry{
			{"J/K", "move" + dot + "/ filter" + dot + "ENTER tune"},
			{"R", "station" + dot + "X remove" + dot + "TAB next" + dot + "ESC back"},
		}},
	}
	keyW := 0
	for _, g := range groups {
		for _, e := range g.items {
			if w := runewidth.StringWidth(e.key); w > keyW {
				keyW = w
			}
		}
	}
	keyW += 2

	rows := []string{m.th.Accent.Bold(true).Render(
		runewidth.Truncate("KEYS", iw, m.th.G.Ellipsis))}
	for _, g := range groups {
		rows = append(rows, m.th.Mid.Bold(true).Render(
			runewidth.Truncate(g.name, iw, m.th.G.Ellipsis)))
		for _, e := range g.items {
			if iw < 2+keyW {
				// Narrower than the key column: the key alone, truncated,
				// still says something.
				rows = append(rows, m.th.Dim.Render(
					runewidth.Truncate(e.key, iw, m.th.G.Ellipsis)))
				continue
			}
			rows = append(rows, "  "+m.th.Accent.Render(runewidth.FillRight(e.key, keyW))+
				m.th.Dim.Render(runewidth.Truncate(e.desc, iw-2-keyW, m.th.G.Ellipsis)))
		}
	}
	rows = append(rows, m.th.Dim.Render(runewidth.Truncate(
		"Q quit"+dot+"? or ESC closes this", iw, m.th.G.Ellipsis)))
	if len(rows) > budget {
		rows = rows[:budget]
	}
	return rows
}
