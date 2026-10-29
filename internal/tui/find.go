package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// promptKind is which of the prompt's two searches is open. The prompt is
// one input with two destinations, and Tab flips between them.
type promptKind int

const (
	// findMode starts a discovery station from whatever was typed: an
	// artist, a song, a genre or a YouTube link. It is what / opens.
	findMode promptKind = iota
	// radioMode is today's search: the best radio-browser match for a
	// station, a genre or a country.
	radioMode
)

const (
	// maxFindQuery is longer than the radio search's maxQuery because a
	// pasted YouTube link runs past 40 columns, and a link cut short is a
	// different video.
	maxFindQuery = 200
	// findRecentMax is the most "pick up where you left off" rows. Three
	// is a glance; more is a library, and H already is one.
	findRecentMax = 3
	// findLabelW pads FIND and RADIO to one width so the field doesn't
	// slide a column when Tab flips between them.
	findLabelW = 5
	// findListOverhead is the rows the list costs beyond its entries: the
	// gap above it and its label.
	findListOverhead = 2
	// noFindRow is findPos while no recent row is selected, which is how the
	// list opens: the first down arrow selects the newest, as in the window.
	noFindRow = -1
)

const (
	findPlaceholder  = "artist, song, genre or YouTube link"
	radioPlaceholder = "station, genre or country"
	findRecentLabel  = "pick up where you left off"
)

// openFind opens the prompt on FIND, the way / always does. The recent list
// is read here, once: it is a handful of rows from an indexed query, and a
// list that re-read on every key would flicker as listens landed.
func (m *Model) openFind() {
	m.prompt = true
	m.buf = nil
	m.promptMode = findMode
	m.finds = m.recentDiscoveries()
	m.findPos = noFindRow
}

// closePrompt puts the prompt away and forgets its state.
func (m *Model) closePrompt() {
	m.prompt = false
	m.buf = nil
	m.finds = nil
	m.findPos = noFindRow
}

// recentDiscoveries reads the discovery stations worth picking up. The one
// on air is left out: it is where the listener already is, and tuning it
// again would do nothing.
func (m Model) recentDiscoveries() []core.DiscoveryRow {
	if m.core == nil {
		return nil
	}
	// One extra row, so leaving the current station out still leaves a
	// full list.
	rows, err := m.core.RecentDiscoveries(findRecentMax + 1)
	if err != nil {
		return nil
	}
	out := make([]core.DiscoveryRow, 0, findRecentMax)
	for _, r := range rows {
		if m.s.HaveStation && r.UUID == m.s.Station.UUID {
			continue
		}
		if len(out) == findRecentMax {
			break
		}
		out = append(out, r)
	}
	return out
}

// queryLimit is how many runes the open prompt takes.
func (m Model) queryLimit() int {
	if m.promptMode == findMode {
		return maxFindQuery
	}
	return maxQuery
}

// switchPromptMode flips FIND and RADIO. What was typed stays, trimmed to
// the radio search's shorter limit when it goes there.
func (m *Model) switchPromptMode() {
	if m.promptMode == findMode {
		m.promptMode = radioMode
	} else {
		m.promptMode = findMode
	}
	if lim := m.queryLimit(); len(m.buf) > lim {
		m.buf = m.buf[:lim]
	}
	m.findPos = noFindRow
}

// findListRows is how many recent rows the sheet has room for in budget
// rows, whatever is typed: the space stays reserved while the list is
// hidden, so the bar doesn't move under the listener's fingers. The list is
// whole or gone. A short terminal loses it before the hint or the bar.
func (m Model) findListRows(budget int) int {
	n := minI(len(m.finds), findRecentMax)
	// bar + hint + the list and what heads it.
	if n == 0 || budget < 2+findListOverhead+n {
		return 0
	}
	return n
}

// findShown is how many recent rows are on screen right now: the list
// belongs to FIND and to an empty bar. Key handling asks this, not the
// renderer, so the selection can never sit on a row nobody can see.
func (m Model) findShown() int {
	if !m.prompt || m.promptMode != findMode || len(m.buf) > 0 {
		return 0
	}
	return m.findListRows(m.contentHeight())
}

// findRows is the FIND overlay, a sheet like help and the theme picker:
// the bar, its hint, and under them the discoveries to pick up. Rows are
// cut from the bottom on short terminals, so the list goes first, then the
// hint, and the bar is the last thing standing.
func (m Model) findRows(iw, budget int) []string {
	rows := []string{m.findBar(iw), m.findHint(iw)}
	if n := m.findListRows(budget); n > 0 {
		shown := m.findShown()
		rows = append(rows, "")
		if shown == 0 {
			// Reserved, not drawn.
			rows = append(rows, make([]string, n+1)...)
		} else {
			rows = append(rows, m.th.Dim.Bold(true).Render(
				runewidth.Truncate(findRecentLabel, iw, m.th.G.Ellipsis)))
			rows = append(rows, m.findRecentRows(iw, shown)...)
		}
	}
	if len(rows) > budget {
		rows = rows[:maxI(0, budget)]
	}
	return rows
}

// findBar is the input line: the mode's label, an accent chevron, the
// query with its blinking cursor, or the dim placeholder while empty.
func (m Model) findBar(iw int) string {
	label, placeholder := "FIND", findPlaceholder
	if m.promptMode == radioMode {
		label, placeholder = "RADIO", radioPlaceholder
	}
	head := m.th.Accent.Bold(true).Render(runewidth.FillRight(label, findLabelW)) +
		" " + m.th.Accent.Render(m.th.G.Prompt) + " "
	cur := m.cursorGlyph()
	room := maxI(0, iw-findLabelW-3)
	if len(m.buf) == 0 {
		hint := runewidth.Truncate(placeholder, maxI(0, room-2), m.th.G.Ellipsis)
		return clip(head+m.th.Accent.Render(cur)+" "+m.th.Dim.Render(hint), iw)
	}
	// A long link scrolls: the tail is what's being typed.
	buf := string(m.buf)
	avail := maxI(0, room-1)
	if bw := runewidth.StringWidth(buf); bw > avail {
		buf = sliceCols(buf, bw-avail, avail)
	}
	return clip(head+m.th.Bright.Render(buf)+m.th.Accent.Render(cur), iw)
}

// findHint is the faint line of keys under the bar. Tab names where it
// goes, so the line reads true in both modes.
func (m Model) findHint(iw int) string {
	other := "radio"
	if m.promptMode == radioMode {
		other = "find"
	}
	line := "esc close " + m.th.G.Dot + " tab " + other
	return m.th.Dim.Render(runewidth.Truncate(line, iw, m.th.G.Ellipsis))
}

// findRecentRows draws the first n discoveries, the selected one on the
// same bar the library lists use.
func (m Model) findRecentRows(iw, n int) []string {
	rows := make([]string, 0, n)
	for i := 0; i < n && i < len(m.finds); i++ {
		name := runewidth.Truncate(m.finds[i].Name, maxI(1, iw-3), m.th.G.Ellipsis)
		if i == m.findPos {
			left := m.th.SelText.Render(" " + m.th.G.Pointer + " " + name)
			gap := maxI(0, iw-lipgloss.Width(left))
			rows = append(rows, clip(left+m.th.SelFill.Render(strings.Repeat(" ", gap)), iw))
			continue
		}
		rows = append(rows, clip("   "+m.th.Mid.Render(name), iw))
	}
	return rows
}

// findSelected is the recent row under the selection, if the list is on
// screen and an arrow has selected one. Enter on a bar nobody typed in, with
// nothing selected, picks nothing: it only closes.
func (m Model) findSelected() (core.DiscoveryRow, bool) {
	n := m.findShown()
	if n == 0 || m.findPos < 0 {
		return core.DiscoveryRow{}, false
	}
	return m.finds[minI(m.findPos, n-1)], true
}

// submitPrompt is Enter: it closes the prompt and sends the listener's
// intent. With text in the bar that is a discovery (FIND) or a radio search
// (RADIO); with an empty bar over a visible list it is the selected row, and
// with no row selected it is nothing.
func (m Model) submitPrompt() (Model, []session.Msg) {
	q := strings.TrimSpace(string(m.buf))
	row, picked := m.findSelected()
	mode := m.promptMode
	m.closePrompt()
	switch {
	case q != "" && mode == findMode:
		return m, []session.Msg{session.Discover{Query: q}}
	case q != "":
		return m, []session.Msg{session.Seed{Query: q}}
	case picked:
		// The session spells a discovery's reason itself, with the
		// theme's dot, when the reason starts with "discovering".
		return m, []session.Msg{session.TuneTo{UUID: row.UUID, Reason: "discovering"}}
	}
	return m, nil
}

// typePrompt appends typed or pasted runes up to the open mode's limit.
// Control characters are dropped: a pasted link's trailing newline would
// otherwise break the bar into two lines.
func (m *Model) typePrompt(runes []rune) {
	lim := m.queryLimit()
	for _, r := range runes {
		if len(m.buf) >= lim {
			break
		}
		if unicode.IsControl(r) {
			continue
		}
		m.buf = append(m.buf, r)
	}
}
