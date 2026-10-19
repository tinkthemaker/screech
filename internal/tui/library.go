package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

func (m *Model) loadHistory(view historyView) bool {
	switch view {
	case historyRecent:
		entries, err := m.core.RecentlyHeard(100)
		if err != nil {
			return false
		}
		m.recent = entries
		m.recentTop = 0
	case historyLoved:
		entries, err := m.core.LovedTracks(libraryLimit)
		if err != nil {
			return false
		}
		m.library = entries
		m.clampLibraryPos()
	case historyStations:
		entries, err := m.core.SavedStations()
		if err != nil {
			return false
		}
		m.saved = entries
		m.clampSavedPos()
		// The leaderboard still loads for the view's secondary section.
		if hist, err := m.core.TopListened(9); err == nil {
			m.hist = hist
		}
	case historyTaste:
		taste, err := m.core.Taste(time.Now())
		if err != nil {
			return false
		}
		m.taste = taste
	}
	if view != historyLoved {
		entries, err := m.core.LovedTracks(libraryLimit)
		if err == nil {
			m.library = entries
			m.clampLibraryPos()
		}
	}
	m.historyView = view
	m.history = true
	m.libraryFind = false
	m.forgetKey = ""
	return true
}

// handleHistoryKey drives the unified recent/loved/stations library.
func (m Model) handleHistoryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.historyView == historyLoved && m.libraryFind {
		return m.handleLibraryFindKey(msg)
	}
	if m.historyView == historyStations && m.savedFind {
		return m.handleSavedFindKey(msg)
	}
	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "esc":
		if m.historyView == historyLoved && len(m.libraryQuery) > 0 {
			m.libraryQuery = nil
			m.libraryPos = 0
			m.forgetKey = ""
			return m, nil
		}
		// Same contract as LOVED: a live filter eats the first esc —
		// otherwise a stale query greets the next visit while the empty
		// state swears "esc clears the filter".
		if m.historyView == historyStations && len(m.savedQuery) > 0 {
			m.savedQuery = nil
			m.savedPos = 0
			m.forgetKey = ""
			return m, nil
		}
		m.history = false
		return m, nil
	case "h":
		m.loadHistory(historyRecent)
		return m, nil
	case "H":
		m.loadHistory(historyLoved)
		return m, nil
	case "tab":
		m.loadHistory((m.historyView + 1) % 4)
		return m, nil
	case "shift+tab":
		m.loadHistory((m.historyView + 3) % 4)
		return m, nil
	case "?":
		m.helpOpen = true
		return m, nil
	}

	if m.historyView == historyLoved {
		switch key {
		case "/":
			m.libraryFind = true
			m.forgetKey = ""
			return m, nil
		case "up", "k":
			m.moveLibrary(-1)
			return m, nil
		case "down", "j":
			m.moveLibrary(1)
			return m, nil
		case "pgup":
			m.moveLibrary(-5)
			return m, nil
		case "pgdown":
			m.moveLibrary(5)
			return m, nil
		case "home":
			m.libraryPos = 0
			m.forgetKey = ""
			return m, nil
		case "end":
			m.libraryPos = maxI(0, len(m.filteredLibrary())-1)
			m.forgetKey = ""
			return m, nil
		case "enter":
			entry, ok := m.selectedLovedTrack()
			if !ok {
				return m, nil
			}
			query := strings.TrimSpace(entry.Artist)
			if query == "" {
				query = strings.TrimSpace(entry.Title)
			}
			if query == "" {
				return m, nil
			}
			m.history = false
			return m.intent(session.Seed{Query: query})
		case "r":
			entry, ok := m.selectedLovedTrack()
			if !ok || entry.StationUUID == "" {
				return m, nil
			}
			m.history = false
			// When the track's station is already on, the engine no-ops
			// and the music continues — the library simply closes.
			return m.intent(session.TuneTo{UUID: entry.StationUUID, Reason: "from a loved track"})
		case "x":
			entry, ok := m.selectedLovedTrack()
			if !ok {
				return m, nil
			}
			entryKey := lovedTrackKey(entry)
			if m.forgetKey != entryKey || m.now.Sub(m.forgetAt) > 3*time.Second {
				m.forgetKey = entryKey
				m.forgetAt = m.now
				return m, nil
			}
			removed, err := m.core.ForgetLovedTrack(entry.ArtistKey, entry.Title)
			if err != nil {
				return m.intent(session.Notify{Note: "could not remove loved track"})
			}
			if removed {
				m.loadHistory(historyLoved)
				return m.intent(session.Notify{Feedback: "LIBRARY  Track removed"})
			}
			return m, nil
		}
	}

	if m.historyView == historyStations {
		switch key {
		case "/":
			m.savedFind = true
			m.forgetKey = ""
			return m, nil
		case "up", "k":
			m.moveSaved(-1)
			return m, nil
		case "down", "j":
			m.moveSaved(1)
			return m, nil
		case "pgup":
			m.moveSaved(-5)
			return m, nil
		case "pgdown":
			m.moveSaved(5)
			return m, nil
		case "home":
			m.savedPos = 0
			m.forgetKey = ""
			return m, nil
		case "end":
			m.savedPos = maxI(0, len(m.filteredSaved())-1)
			m.forgetKey = ""
			return m, nil
		case "enter":
			entry, ok := m.selectedSaved()
			if !ok {
				return m, nil
			}
			m.history = false
			if m.s.HaveStation && entry.UUID == m.s.Station.UUID {
				return m, nil // already playing it
			}
			return m.intent(session.TuneTo{UUID: entry.UUID, Reason: "from saved stations"})
		case "x":
			entry, ok := m.selectedSaved()
			if !ok {
				return m, nil
			}
			if m.forgetKey != entry.UUID || m.now.Sub(m.forgetAt) > 3*time.Second {
				m.forgetKey = entry.UUID
				m.forgetAt = m.now
				return m, nil
			}
			if err := m.core.RemoveStation(entry.UUID); err != nil {
				return m.intent(session.Notify{Note: "could not remove station"})
			}
			m.loadHistory(historyStations)
			return m.intent(session.RefreshPresets{}, session.Notify{Feedback: "STATIONS  Removed"})
		}
		// Digits still tune from the saved list when a preset holds that slot.
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			slot := int(key[0] - '0')
			uuid, ok := m.s.Presets[slot]
			if !ok {
				return m, nil
			}
			m.history = false
			if m.s.HaveStation && uuid == m.s.Station.UUID {
				return m, nil
			}
			return m.intent(session.TuneTo{UUID: uuid, Reason: fmt.Sprintf("preset %d", slot)})
		}
		return m, nil
	}

	// RECENT rows have no cursor — the whole window scrolls, clamped to
	// the range the renderer could actually use. Free-running offsets
	// used to bury the list under dead keypresses.
	if m.historyView == historyRecent {
		switch key {
		case "up", "k":
			m.recentTop = maxI(0, m.recentTop-1)
		case "down", "j":
			m.recentTop = minI(m.recentTop+1, m.recentMaxTop())
		case "pgup":
			m.recentTop = maxI(0, m.recentTop-5)
		case "pgdown":
			m.recentTop = minI(m.recentTop+5, m.recentMaxTop())
		case "home":
			m.recentTop = 0
		case "end":
			m.recentTop = m.recentMaxTop()
		}
	}
	return m, nil
}

// handleSavedFindKey drives the STATIONS view's incremental search.
func (m Model) handleSavedFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc, tea.KeyEnter:
		m.savedFind = false
		return m, nil
	case tea.KeyBackspace:
		if len(m.savedQuery) > 0 {
			m.savedQuery = m.savedQuery[:len(m.savedQuery)-1]
			m.savedPos = 0
		}
		return m, nil
	case tea.KeyUp:
		m.moveSaved(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSaved(1)
		return m, nil
	case tea.KeySpace:
		if len(m.savedQuery) < maxQuery {
			m.savedQuery = append(m.savedQuery, ' ')
			m.savedPos = 0
		}
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if len(m.savedQuery) >= maxQuery {
				break
			}
			m.savedQuery = append(m.savedQuery, r)
		}
		m.savedPos = 0
		m.forgetKey = ""
		return m, nil
	}
	return m, nil
}

func (m Model) handleLibraryFindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m.quit()
	case tea.KeyEsc, tea.KeyEnter:
		m.libraryFind = false
		return m, nil
	case tea.KeyBackspace:
		if len(m.libraryQuery) > 0 {
			m.libraryQuery = m.libraryQuery[:len(m.libraryQuery)-1]
			m.libraryPos = 0
		}
		return m, nil
	case tea.KeyUp:
		m.moveLibrary(-1)
		return m, nil
	case tea.KeyDown:
		m.moveLibrary(1)
		return m, nil
	case tea.KeySpace:
		if len(m.libraryQuery) < maxQuery {
			m.libraryQuery = append(m.libraryQuery, ' ')
			m.libraryPos = 0
		}
		return m, nil
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if len(m.libraryQuery) >= maxQuery {
				break
			}
			m.libraryQuery = append(m.libraryQuery, r)
		}
		m.libraryPos = 0
		m.forgetKey = ""
		return m, nil
	}
	return m, nil
}

func (m *Model) moveLibrary(delta int) {
	count := len(m.filteredLibrary())
	if count == 0 {
		m.libraryPos = 0
		return
	}
	m.libraryPos += delta
	if m.libraryPos < 0 {
		m.libraryPos = 0
	}
	if m.libraryPos >= count {
		m.libraryPos = count - 1
	}
	m.forgetKey = ""
}

func (m *Model) clampLibraryPos() {
	count := len(m.filteredLibrary())
	if count == 0 {
		m.libraryPos = 0
	} else if m.libraryPos >= count {
		m.libraryPos = count - 1
	}
}

// --- saved-stations list (STATIONS view) ---

func (m *Model) moveSaved(delta int) {
	count := len(m.filteredSaved())
	if count == 0 {
		m.savedPos = 0
		return
	}
	m.savedPos += delta
	if m.savedPos < 0 {
		m.savedPos = 0
	}
	if m.savedPos >= count {
		m.savedPos = count - 1
	}
	m.forgetKey = ""
}

func (m *Model) clampSavedPos() {
	count := len(m.filteredSaved())
	if count == 0 {
		m.savedPos = 0
	} else if m.savedPos >= count {
		m.savedPos = count - 1
	}
}

func (m Model) filteredSaved() []core.SavedStation {
	query := strings.ToLower(strings.TrimSpace(string(m.savedQuery)))
	if query == "" {
		return m.saved
	}
	var out []core.SavedStation
	for _, e := range m.saved {
		name := strings.ToLower(e.Name)
		ok := true
		for _, term := range strings.Fields(query) {
			if !strings.Contains(name, term) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

func (m Model) selectedSaved() (core.SavedStation, bool) {
	entries := m.filteredSaved()
	if m.savedPos < 0 || m.savedPos >= len(entries) {
		return core.SavedStation{}, false
	}
	return entries[m.savedPos], true
}

func (m Model) selectedLovedTrack() (core.LovedTrack, bool) {
	entries := m.filteredLibrary()
	if m.libraryPos < 0 || m.libraryPos >= len(entries) {
		return core.LovedTrack{}, false
	}
	return entries[m.libraryPos], true
}

func (m Model) filteredLibrary() []core.LovedTrack {
	query := strings.ToLower(strings.TrimSpace(string(m.libraryQuery)))
	if query == "" {
		return m.library
	}
	type match struct {
		entry core.LovedTrack
		score int
	}
	var matches []match
	for _, entry := range m.library {
		score, ok := lovedMatchScore(entry, query)
		if ok {
			matches = append(matches, match{entry: entry, score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			return matches[i].entry.LovedAt.After(matches[j].entry.LovedAt)
		}
		return matches[i].score > matches[j].score
	})
	out := make([]core.LovedTrack, len(matches))
	for i := range matches {
		out[i] = matches[i].entry
	}
	return out
}

func lovedMatchScore(entry core.LovedTrack, query string) (int, bool) {
	artist := strings.ToLower(entry.Artist)
	title := strings.ToLower(entry.Title)
	station := strings.ToLower(entry.StationName)
	all := artist + " " + title + " " + station
	score := 0
	for _, term := range strings.Fields(query) {
		if !strings.Contains(all, term) {
			return 0, false
		}
		switch {
		case strings.HasPrefix(title, term):
			score += 80
		case strings.HasPrefix(artist, term):
			score += 70
		case strings.Contains(title, term):
			score += 50
		case strings.Contains(artist, term):
			score += 40
		default:
			score += 10
		}
	}
	return score, true
}

func lovedTrackKey(entry core.LovedTrack) string {
	return entry.ArtistKey + "\x00" + strings.ToLower(strings.TrimSpace(entry.Title))
}

// --- library: tabs, then the active view, selection as a surface bar ---

func (m Model) historyRows(iw int, compact bool, budget int) []string {
	rows := []string{m.historyTabs(iw)}
	if budget <= 1 {
		return rows
	}
	if !compact && budget > 2 {
		rows = append(rows, "")
	}
	switch m.historyView {
	case historyLoved:
		return m.lovedRows(rows, iw, budget)
	case historyStations:
		return m.stationHistoryRows(rows, iw, budget)
	case historyTaste:
		return m.tasteRows(rows, iw, budget)
	default:
		return m.recentRows(rows, iw, budget)
	}
}

func (m Model) historyTabs(iw int) string {
	labels := []string{"RECENT", fmt.Sprintf("LOVED %d", len(m.library)), "STATIONS", "TASTE"}
	plain := strings.Join(labels, "   ")
	if runewidth.StringWidth(plain) > iw {
		return m.th.Accent.Bold(true).Render(
			runewidth.Truncate(labels[int(m.historyView)], iw, m.th.G.Ellipsis))
	}
	parts := make([]string, len(labels))
	for i, label := range labels {
		if historyView(i) == m.historyView {
			parts[i] = m.th.Accent.Bold(true).Render(label)
		} else {
			parts[i] = m.th.Dim.Render(label)
		}
	}
	return strings.Join(parts, "   ")
}

// recentBudget is the row budget historyRows grants the recent list at
// the current terminal size: content height minus the tabs row and, in
// the non-compact layout, the spacer beneath it.
func (m Model) recentBudget() int {
	used := 1 // the view-tabs row
	if m.h >= 14 && m.contentHeight() > 2 {
		used++ // the spacer under the tabs
	}
	return m.contentHeight() - used
}

// recentMaxTop is the largest scroll offset the renderer would honor:
// the entries past one screenful. The key handler clamps against this
// exact number — derived from the same budget math recentRows uses, so
// keys and view can't drift.
func (m Model) recentMaxTop() int {
	return maxI(0, len(m.recent)-minI(len(m.recent), m.recentBudget()))
}

func (m Model) recentRows(rows []string, iw int, budget int) []string {
	if len(m.recent) == 0 {
		return append(rows, m.th.Dim.Render(
			runewidth.Truncate("nothing heard yet", iw, m.th.G.Ellipsis)))
	}
	maxRows := minI(len(m.recent), budget-len(rows))
	// RECENT has no selection cursor — j/k scrolls a window instead. The
	// clamp also lives here so a resize can't strand the view.
	top := minI(maxI(0, m.recentTop), len(m.recent)-maxRows)
	for i := top; i < top+maxRows; i++ {
		e := m.recent[i]
		label := e.Title
		if e.Artist != "" {
			label = e.Artist + " " + m.th.G.Dot + " " + e.Title
		}
		mark := " "
		if e.Loved {
			mark = m.th.AccentDim.Render(m.th.G.Heart)
		}
		station := runewidth.Truncate(cleanStationName(e.StationName), 12, m.th.G.Ellipsis)
		right := m.th.Dim.Render(fmt.Sprintf("%12s", station)) + " " + mark
		left := m.th.Mid.Render(runewidth.Truncate(label, maxI(4, iw-16), m.th.G.Ellipsis))
		rows = append(rows, lrRow(left, right, iw))
	}
	return rows
}

func (m Model) lovedRows(rows []string, iw int, budget int) []string {
	entries := m.filteredLibrary()
	if len(rows) < budget {
		rows = append(rows, m.libraryFindRow(iw, len(entries)))
	}
	if len(entries) == 0 {
		message := "love a track and it will appear here"
		if len(m.libraryQuery) > 0 {
			message = "no matches " + m.th.G.Dot + " esc clears the filter"
		}
		return append(rows, m.th.Dim.Render(runewidth.Truncate(message, iw, m.th.G.Ellipsis)))
	}

	detail := budget-len(rows) >= 3
	maxRows := budget - len(rows)
	if detail {
		maxRows--
	}
	maxRows = minI(maxRows, len(entries))
	start := 0
	if m.libraryPos >= maxRows {
		start = m.libraryPos - maxRows + 1
	}
	if start+maxRows > len(entries) {
		start = maxI(0, len(entries)-maxRows)
	}
	for i := start; i < start+maxRows; i++ {
		entry := entries[i]
		label := entry.Title
		if entry.Artist != "" {
			label = entry.Artist + " " + m.th.G.Dot + " " + entry.Title
		}
		count := ""
		if entry.HeardCount > 1 {
			count = fmt.Sprintf("%d%s", entry.HeardCount, m.th.G.Times)
		}
		labelW := maxI(4, iw-runewidth.StringWidth(count)-5)
		label = runewidth.Truncate(label, labelW, m.th.G.Ellipsis)
		if i == m.libraryPos {
			// Selection is a surface: a full-width bar on the love wash,
			// the heart in coral where the pointer would sit.
			bg := m.loveWash()
			selText := m.th.SelText.Background(bg)
			selMeta := m.th.SelMeta.Background(bg)
			selFill := m.th.SelFill.Background(bg)
			left := selText.Render(" ") + m.th.Love.Background(bg).Render(m.th.G.Heart) + selText.Render(" "+label)
			right := selMeta.Render(count + " ")
			gap := maxI(0, iw-lipgloss.Width(left)-lipgloss.Width(right))
			rows = append(rows, clip(left+selFill.Render(strings.Repeat(" ", gap))+right, iw))
		} else {
			left := " " + m.th.Love.Render(m.th.G.Heart) + " " + m.th.Mid.Render(label)
			right := m.th.Dim.Render(count + " ")
			rows = append(rows, lrRow(left, right, iw))
		}
	}
	if detail {
		if entry, ok := m.selectedLovedTrack(); ok {
			rows = append(rows, m.lovedDetailRow(entry, iw))
		}
	}
	return rows
}

func (m Model) libraryFindRow(iw, matches int) string {
	if m.libraryFind || len(m.libraryQuery) > 0 {
		cursor := ""
		if m.libraryFind {
			cursor = m.cursorGlyph()
		}
		query := string(m.libraryQuery)
		if query == "" {
			return clip(m.th.AccentDim.Bold(true).Render("FIND")+"  "+
				m.th.Dim.Render(runewidth.Truncate("artist, title, or station", maxI(0, iw-7), m.th.G.Ellipsis))+
				m.th.Accent.Render(cursor), iw)
		}
		prefix := m.th.AccentDim.Bold(true).Render("FIND") + "  "
		count := m.th.Dim.Render(fmt.Sprintf("%d", matches))
		avail := maxI(0, iw-7-runewidth.StringWidth(fmt.Sprintf("%d", matches)))
		return lrRow(prefix+m.th.Bright.Render(runewidth.Truncate(query, avail, m.th.G.Ellipsis))+m.th.Accent.Render(cursor), count, iw)
	}
	artists := map[string]bool{}
	for _, entry := range m.library {
		if entry.ArtistKey != "" {
			artists[entry.ArtistKey] = true
		}
	}
	left := fmt.Sprintf("%d tracks", len(m.library))
	if len(artists) > 0 {
		left += " " + m.th.G.Dot + fmt.Sprintf(" %d artists", len(artists))
	}
	return lrRow(m.th.Mid.Render(left), m.th.AccentDim.Bold(true).Render("/ FIND"), iw)
}

func (m Model) lovedDetailRow(entry core.LovedTrack, iw int) string {
	if m.forgetKey == lovedTrackKey(entry) && m.now.Sub(m.forgetAt) <= 3*time.Second {
		return m.th.Love.Bold(true).Render(
			runewidth.Truncate("REMOVE?  X again to forget this track", iw, m.th.G.Ellipsis))
	}
	parts := []string{}
	if station := cleanStationName(entry.StationName); station != "" {
		parts = append(parts, station)
	}
	parts = append(parts, "loved "+relativeTime(entry.LovedAt, m.now))
	if entry.HeardCount > 0 {
		parts = append(parts, fmt.Sprintf("heard %d%s", entry.HeardCount, m.th.G.Times))
	}
	return m.th.Dim.Render(runewidth.Truncate(strings.Join(parts, " "+m.th.G.Dot+" "), iw, m.th.G.Ellipsis))
}

func (m Model) stationHistoryRows(rows []string, iw int, budget int) []string {
	entries := m.filteredSaved()
	if len(rows) < budget {
		rows = append(rows, m.savedFindRow(iw, len(entries)))
	}
	if len(entries) == 0 {
		message := "save a station with f, or love a track"
		if len(m.savedQuery) > 0 {
			message = "no matches " + m.th.G.Dot + " esc clears the filter"
		}
		return append(rows, m.th.Dim.Render(runewidth.Truncate(message, iw, m.th.G.Ellipsis)))
	}

	detail := budget-len(rows) >= 3
	maxRows := budget - len(rows)
	if detail {
		maxRows--
	}
	maxRows = minI(maxRows, len(entries))
	start := 0
	if m.savedPos >= maxRows {
		start = m.savedPos - maxRows + 1
	}
	if start+maxRows > len(entries) {
		start = maxI(0, len(entries)-maxRows)
	}
	// The preset slot reads as a channel number beside the name — a bare
	// digit in the meta column looked like a second love count (and in the
	// 7-bit palette "<31" read as "less than 31"). The name column is then
	// whatever the real meta width leaves, not a magic constant.
	loveW := 0
	for i := start; i < start+maxRows; i++ {
		if entries[i].LoveCount > 0 {
			if w := runewidth.StringWidth(fmt.Sprintf("%d %s", entries[i].LoveCount, m.th.G.Heart)); w > loveW {
				loveW = w
			}
		}
	}
	metaW := 7 + 1 // listen time plus its trailing space
	if loveW > 0 {
		metaW += 2 + loveW
	}
	for i := start; i < start+maxRows; i++ {
		e := entries[i]
		name := runewidth.Truncate(cleanStationName(e.Name), maxI(4, iw-5-metaW-1), m.th.G.Ellipsis)
		meta := fmt.Sprintf("%7s", fmtElapsed(e.Total))
		if loveW > 0 {
			cell := ""
			if e.LoveCount > 0 {
				cell = fmt.Sprintf("%d %s", e.LoveCount, m.th.G.Heart)
			}
			meta += "  " + strings.Repeat(" ", maxI(0, loveW-runewidth.StringWidth(cell))) + cell
		}
		digit := " "
		if e.PresetSlot > 0 {
			digit = strconv.Itoa(e.PresetSlot)
		}
		if i == m.savedPos {
			slot := m.th.SelMeta.Render(digit)
			if e.PresetSlot > 0 {
				slot = m.th.Accent.Background(m.th.SelFill.GetBackground()).Render(digit)
			}
			left := m.th.SelText.Render(" "+m.th.G.Pointer+" ") + slot + m.th.SelText.Render(" "+name)
			right := m.th.SelMeta.Render(meta + " ")
			gap := maxI(0, iw-lipgloss.Width(left)-lipgloss.Width(right))
			rows = append(rows, clip(left+m.th.SelFill.Render(strings.Repeat(" ", gap))+right, iw))
		} else {
			var leftStyle lipgloss.Style
			if i == 0 && len(m.savedQuery) == 0 {
				leftStyle = m.th.Bright.Bold(true) // most-listened earns the spotlight
			} else {
				leftStyle = m.th.Mid
			}
			left := "   " + m.th.AccentDim.Render(digit) + " " + leftStyle.Render(name)
			right := m.th.Dim.Render(meta + " ")
			rows = append(rows, lrRow(left, right, iw))
		}
	}
	if detail {
		if e, ok := m.selectedSaved(); ok {
			rows = append(rows, m.savedDetailRow(e, iw))
		}
	}
	return rows
}

func (m Model) savedFindRow(iw, matches int) string {
	if m.savedFind || len(m.savedQuery) > 0 {
		cursor := ""
		if m.savedFind {
			cursor = m.cursorGlyph()
		}
		query := string(m.savedQuery)
		if query == "" {
			return clip(m.th.AccentDim.Bold(true).Render("FIND")+"  "+
				m.th.Dim.Render(runewidth.Truncate("station name", maxI(0, iw-7), m.th.G.Ellipsis))+
				m.th.Accent.Render(cursor), iw)
		}
		prefix := m.th.AccentDim.Bold(true).Render("FIND") + "  "
		count := m.th.Dim.Render(fmt.Sprintf("%d", matches))
		avail := maxI(0, iw-7-runewidth.StringWidth(fmt.Sprintf("%d", matches)))
		return lrRow(prefix+m.th.Bright.Render(runewidth.Truncate(query, avail, m.th.G.Ellipsis))+m.th.Accent.Render(cursor), count, iw)
	}
	presets := 0
	for _, e := range m.saved {
		if e.PresetSlot > 0 {
			presets++
		}
	}
	left := fmt.Sprintf("%d saved", len(m.saved))
	if presets > 0 {
		left += " " + m.th.G.Dot + fmt.Sprintf(" %d presets", presets)
	}
	return lrRow(m.th.Mid.Render(left), m.th.AccentDim.Bold(true).Render("/ FIND"), iw)
}

func (m Model) savedDetailRow(e core.SavedStation, iw int) string {
	if m.forgetKey == e.UUID && m.now.Sub(m.forgetAt) <= 3*time.Second {
		msg := "REMOVE?  X again"
		if e.PresetSlot > 0 {
			msg = fmt.Sprintf("REMOVE?  X again "+m.th.G.Dot+" clears preset %d and its loves", e.PresetSlot)
		}
		return m.th.Love.Bold(true).Render(runewidth.Truncate(msg, iw, m.th.G.Ellipsis))
	}
	parts := []string{}
	if e.PresetSlot > 0 {
		parts = append(parts, fmt.Sprintf("preset %d", e.PresetSlot))
	}
	if e.LoveCount > 0 {
		parts = append(parts, fmt.Sprintf("%d loved tracks", e.LoveCount))
	}
	if e.Total > 0 {
		parts = append(parts, "listened "+fmtElapsed(e.Total))
	}
	if len(parts) == 0 {
		parts = append(parts, "saved")
	}
	return m.th.Dim.Render(runewidth.Truncate(strings.Join(parts, " "+m.th.G.Dot+" "), iw, m.th.G.Ellipsis))
}

func relativeTime(at, now time.Time) string {
	if at.IsZero() || now.Before(at) {
		return "just now"
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return at.Format("Jan 2")
	}
}
