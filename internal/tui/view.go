package tui

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"screech/internal/core"
)

const wordmark = "SCREECH"

// View composes the faceplate: a header bar and rule pinned to the top of the
// terminal, a key strip pinned to the bottom, and one centered content column
// between them. The app claims the whole surface; the content sits on a grid.
func (m Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	iw := m.innerWidth()
	left := maxI(0, (m.w-iw)/2)
	pad := strings.Repeat(" ", left)

	if boot := m.now.Sub(m.start); boot < bootDur {
		return m.bootView(iw, pad, float64(boot)/float64(bootDur))
	}

	compact := m.h < 14
	small := m.h < 8

	footer := m.footerBar()
	var top []string
	if !small {
		top = []string{m.headerBar(), m.th.Dim.Render(strings.Repeat(m.th.G.Rule, m.w))}
	}
	contentH := m.h - len(top) - 1

	var body []string
	if m.history {
		body = m.historyRows(iw, compact, contentH)
	} else {
		body = m.mainRows(iw, compact, contentH)
	}
	if contentH < 0 {
		contentH = 0
	}
	if len(body) > contentH {
		body = body[:contentH]
	}

	topPad := maxI(0, (contentH-len(body))/2)
	all := make([]string, 0, m.h)
	all = append(all, top...)
	for i := 0; i < topPad; i++ {
		all = append(all, "")
	}
	for _, r := range body {
		all = append(all, pad+r)
	}
	for len(all) < m.h-1 {
		all = append(all, "")
	}
	all = append(all, footer)
	return strings.Join(all, "\n")
}

// --- chrome: header bar, rule, footer strip ---

func (m Model) headerBar() string {
	style := m.th.AccentDim.Bold(true)
	if m.idle() {
		style = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
	}
	left := style.Render(" " + wordmark)
	if m.w >= 58 {
		left += m.th.Dim.Render("  PERSONAL RADIO")
	}

	right := ""
	rightStyle := m.th.Dim
	switch {
	case m.seeking:
		right = "SEEKING" + m.th.G.Ellipsis
	case m.syncing:
		right = "SYNCING DIRECTORY" + m.th.G.Ellipsis
	case m.ph == phBuffer:
		// Buffering blinks: the ellipsis alternates bright/dim so the stall
		// reads as activity, not a frozen readout.
		if (m.now.UnixMilli()/600)%2 == 0 {
			right = "BUFFERING" + m.th.G.Ellipsis
			rightStyle = m.th.Accent
		} else {
			right = "BUFFERING"
		}
	case m.ph == phTune:
		right = "TUNING" + m.th.G.Ellipsis
	case m.ph == phPlay && m.haveSt:
		right = m.th.G.Knob + " LIVE"
		if readout := m.readout(); readout != "" {
			right += "  " + readout
		}
		rightStyle = m.th.Accent // live: the readout is the one accent-lit instrument
	}
	rendered := rightStyle.Render(right)
	if m.suspect && m.ph == phPlay {
		rendered += m.th.Accent.Bold(true).Render("  BREAK")
	}
	return lrRow(left, rendered+" ", m.w)
}

func (m Model) readout() string {
	parts := []string{}
	if c := strings.ToUpper(m.st.Codec); c != "" {
		parts = append(parts, c)
	}
	if m.st.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%dK", m.st.Bitrate))
	}
	if !m.playStart.IsZero() {
		parts = append(parts, fmtClock(m.now.Sub(m.playStart)))
	}
	return strings.Join(parts, " "+m.th.G.Dot+" ")
}

func (m Model) footerItems() [][2]string {
	if m.history {
		items := [][2]string{{"TAB", "next view"}, {"ESC", "close"}}
		if m.historyView == historyLoved {
			if m.libraryFind {
				items = [][2]string{{"TYPE", "filter"}, {"ENTER", "done"}, {"ESC", "done"}}
			} else {
				items = [][2]string{{"J/K", "move"}, {"/", "find"}, {"ENTER", "similar"}, {"R", "station"}, {"X", "remove"}, {"TAB", ""}, {"ESC", ""}}
			}
		} else if m.historyView == historyStations {
			if m.savedFind {
				items = [][2]string{{"TYPE", "filter"}, {"ENTER", "done"}, {"ESC", "done"}}
			} else {
				items = [][2]string{{"J/K", "move"}, {"/", "find"}, {"ENTER", "tune"}, {"X", "remove"}, {"1-9", "preset"}, {"TAB", ""}, {"ESC", ""}}
			}
		}
		return items
	}
	if m.prompt {
		return [][2]string{{"ENTER", "tune"}, {"ESC", "cancel"}}
	}
	if m.volumeOpen {
		return [][2]string{{"LEFT/RIGHT", "adjust"}, {"V", "close"}}
	}
	save := "save"
	if slot := m.currentSlot(); slot > 0 {
		save = fmt.Sprintf("preset %d", slot)
	}
	love := "love"
	if m.lovedTrack {
		love = "loved"
	}
	return [][2]string{
		{"SPACE", "skip"}, {"L", love}, {"F", save},
		{"/", "discover"}, {"H", "library"}, {"V", fmt.Sprintf("%d%%", m.volume)}, {"Q", "quit"},
	}
}

// footerBar is the key strip: a full-width surface plane pinned to the last
// row, keys as chips, labels quiet beside them.
func (m Model) footerBar() string {
	items := m.footerItems()
	sep := m.th.FootFill.Render("  ")
	var b strings.Builder
	b.WriteString(m.th.FootFill.Render(" "))
	for i, it := range items {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(m.th.FootKey.Render(" " + it[0] + " "))
		if it[1] != "" {
			b.WriteString(m.th.FootLabel.Render(" " + it[1]))
		}
	}
	line := b.String()
	if lipgloss.Width(line) > m.w {
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = it[0]
		}
		line = m.th.FootKey.Render(runewidth.Truncate(" "+strings.Join(keys, "  "), m.w, ""))
	}
	if gap := m.w - lipgloss.Width(line); gap > 0 {
		line += m.th.FootFill.Render(strings.Repeat(" ", gap))
	}
	return line
}

// --- main screen: two tight groups with real air between them ---

func (m Model) mainRows(iw int, compact bool, contentH int) []string {
	if iw >= 78 && !compact && contentH >= 11 {
		return m.receiverRows(iw)
	}
	var rows []string
	if !compact {
		micro := "NOW PLAYING"
		if !m.haveSt {
			micro = "STANDBY"
		}
		rows = append(rows, m.th.Dim.Render(micro))
	}
	rows = append(rows, m.heroRow(iw, m.idle()))
	rows = append(rows, m.trackRow(iw, m.idle()))

	if iw >= 28 && !compact && contentH >= 8 {
		rows = append(rows, "")
		rows = append(rows, m.wave.Render(m.th)...)
		rows = append(rows, m.bandRow(iw, m.idle()))
	}

	switch {
	case m.prompt:
		rows = append(rows, m.promptRow(iw))
	case m.volumeOpen:
		rows = append(rows, m.volumeRow(iw))
	default:
		rows = append(rows, m.reasonRow(iw))
	}
	return rows
}

// receiverColumns is shared with the resize path so the synthetic spectrum
// is generated at the width of its instrument bay, not cropped afterward.
func receiverColumns(iw int) (left, gap, right int) {
	usable := maxI(1, iw-4) // frame + one cell of inset on each side
	left = usable * 2 / 5
	if left < 30 {
		left = 30
	}
	if left > 36 {
		left = 36
	}
	gap = 3
	right = usable - left - gap
	if right < 1 {
		right = 1
	}
	return
}

// receiverRows is the wide-screen identity of Screech: a single substantial
// faceplate rather than a narrow column of terminal output. Music owns the
// left bay; the living signal and station-memory dial own the right.
func (m Model) receiverRows(iw int) []string {
	leftW, gapW, rightW := receiverColumns(iw)
	pt := m.th.OnPanel()
	wave := m.wave.Render(pt)

	station := cleanStationName(m.st.Name)
	if station == "" {
		station = "NO SIGNAL"
	}
	artist, title := splitTrackDisplay(m.track)
	primaryLabel := "TRACK"
	if !m.haveTrack || strings.TrimSpace(title) == "" {
		primaryLabel = "NOW PLAYING"
		title = station
		artist = "Track metadata unavailable"
	}

	heartW := 0
	if m.lovedTrack {
		heartW = runewidth.StringWidth(m.th.G.Heart) + 1
	}
	title = marquee(title, maxI(4, leftW-heartW), m.now, m.trackAt, m.th.G.Ellipsis)
	titleCell := pt.Bright.Bold(true).Render(title)
	if m.lovedTrack {
		titleCell += pt.Mid.Render(" ") + m.receiverHeart(pt)
	}
	artist = runewidth.Truncate(artist, leftW, m.th.G.Ellipsis)
	station = runewidth.Truncate(station, leftW, m.th.G.Ellipsis)

	return []string{
		m.frameRule(iw, true, "RECEIVER / NOW PLAYING"),
		m.panelColumns(pt.Dim.Bold(true).Render(primaryLabel), pt.Dim.Bold(true).Render("SIGNAL"), leftW, gapW, rightW),
		m.panelColumns(titleCell, wave[0], leftW, gapW, rightW),
		m.panelColumns(pt.Mid.Render(artist), wave[1], leftW, gapW, rightW),
		m.panelColumns("", pt.Dim.Render(lrRow("LOW", "HIGH", rightW)), leftW, gapW, rightW),
		m.panelColumns(pt.Dim.Bold(true).Render("BROADCAST"), pt.Dim.Bold(true).Render("STATION MEMORY"), leftW, gapW, rightW),
		m.panelColumns(pt.Bright.Bold(true).Render(strings.ToUpper(station)), m.bandRowTheme(rightW, m.idle(), pt), leftW, gapW, rightW),
		m.panelColumns("", pt.Dim.Render(m.presetLegend(rightW)), leftW, gapW, rightW),
		m.receiverStatusRow(iw),
		m.frameRule(iw, false, ""),
	}
}

func splitTrackDisplay(track string) (artist, title string) {
	track = strings.TrimSpace(track)
	if parts := strings.SplitN(track, " · ", 2); len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "NOW PLAYING", track
}

func (m Model) receiverHeart(pt Theme) string {
	style := m.th.Love.Background(pt.PanelFill.GetBackground())
	since := m.now.Sub(m.loveAt)
	if since < 120*time.Millisecond {
		return m.th.Invert.Render(m.th.G.Heart)
	}
	if since >= 2*time.Second {
		style = pt.AccentDim
	}
	return style.Render(m.th.G.Heart)
}

func (m Model) presetLegend(width int) string {
	if slot := m.currentSlot(); slot > 0 {
		return runewidth.Truncate(fmt.Sprintf("PRESET %d  "+m.th.G.Dot+"  SAVED", slot), width, m.th.G.Ellipsis)
	}
	if len(m.presets) > 0 {
		return runewidth.Truncate(fmt.Sprintf("%d SAVED STATIONS", len(m.presets)), width, m.th.G.Ellipsis)
	}
	return runewidth.Truncate("NO PRESETS YET", width, m.th.G.Ellipsis)
}

func (m Model) receiverStatusRow(iw int) string {
	width := maxI(1, iw-4)
	label, detail, hot := "WHY THIS STATION", whyDetail(m.reason), false
	switch {
	case m.fatal != "":
		label, detail, hot = "ERROR", m.fatal, true
	case m.feedback != "" && m.now.Sub(m.feedbackAt) < feedbackDur:
		label, detail, hot = "UPDATED", strings.ToLower(m.feedback), true
	case m.suspect && m.ph == phPlay:
		label, detail, hot = "POSSIBLE BREAK", "Space skips without teaching against this station", true
	case m.note != "":
		label, detail = "NOTICE", m.note
	case !m.haveSt:
		label, detail = "STANDBY", "Waiting for a station"
	}

	raised := m.th.PanelRaised
	bg := raised.GetBackground()
	labelStyle := m.th.Accent.Bold(true).Background(bg)
	if hot && (strings.Contains(label, "BREAK") || label == "ERROR") {
		labelStyle = m.th.Love.Bold(true).Background(bg)
	}
	detailStyle := m.th.Mid.Background(bg)
	label = runewidth.Truncate(label, width, m.th.G.Ellipsis)
	remain := maxI(0, width-runewidth.StringWidth(label)-2)
	detail = runewidth.Truncate(detail, remain, m.th.G.Ellipsis)
	content := labelStyle.Render(label)
	if detail != "" && remain > 0 {
		content += raised.Render("  ") + detailStyle.Render(detail)
	}
	return m.panelFull(content, iw, raised)
}

func whyDetail(reason string) string {
	switch {
	case reason == "where you left off":
		return "Resumed where you left off"
	case strings.HasPrefix(reason, "seeded: "):
		return "Following your " + strings.TrimPrefix(reason, "seeded: ") + " seed"
	case strings.HasPrefix(reason, "seed echo: "):
		return "Still following your " + strings.TrimPrefix(reason, "seed echo: ") + " seed"
	case strings.HasPrefix(reason, "preset "):
		return "Recalled saved preset " + strings.TrimPrefix(reason, "preset ")
	case reason == "from your history":
		return "Returned from your listening history"
	case reason == "from a loved track":
		return "Returned to the station behind a loved track"
	case reason == "wildcard":
		return "Exploring something new"
	case reason == "":
		return "Listening live"
	default:
		return strings.TrimSpace(reason)
	}
}

func (m Model) frameRule(width int, top bool, label string) string {
	left, right := m.th.G.FrameTL, m.th.G.FrameTR
	if !top {
		left, right = m.th.G.FrameBL, m.th.G.FrameBR
	}
	labelW := runewidth.StringWidth(label)
	if label != "" && labelW+6 <= width {
		// left cap (4) + label + separator (1) + tail + right cap (1).
		// The previous -5 put the labeled top rail one cell past the body.
		tail := maxI(0, width-labelW-6)
		return m.th.PanelBorder.Render(left+m.th.G.FrameH+m.th.G.FrameH+" ") +
			m.th.AccentDim.Bold(true).Render(label) +
			m.th.PanelBorder.Render(" "+strings.Repeat(m.th.G.FrameH, tail)+right)
	}
	return m.th.PanelBorder.Render(left + strings.Repeat(m.th.G.FrameH, maxI(0, width-2)) + right)
}

func (m Model) panelColumns(left, right string, leftW, gapW, rightW int) string {
	fill := m.th.PanelFill
	return m.th.PanelBorder.Render(m.th.G.FrameV) + fill.Render(" ") +
		surfaceCell(left, leftW, fill) + fill.Render(strings.Repeat(" ", gapW)) +
		surfaceCell(right, rightW, fill) + fill.Render(" ") +
		m.th.PanelBorder.Render(m.th.G.FrameV)
}

func (m Model) panelFull(content string, width int, fill lipgloss.Style) string {
	inner := maxI(0, width-4)
	return m.th.PanelBorder.Render(m.th.G.FrameV) + fill.Render(" ") +
		surfaceCell(content, inner, fill) + fill.Render(" ") +
		m.th.PanelBorder.Render(m.th.G.FrameV)
}

func surfaceCell(content string, width int, fill lipgloss.Style) string {
	gap := maxI(0, width-lipgloss.Width(content))
	return content + fill.Render(strings.Repeat(" ", gap))
}

func (m Model) heroRow(iw int, idle bool) string {
	if !m.haveSt {
		text := "NO SIGNAL"
		if m.syncing || m.seeking || m.ph == phTune {
			text = "SEARCHING" + m.th.G.Ellipsis
		}
		return m.th.Dim.Render(runewidth.Truncate(text, iw, m.th.G.Ellipsis))
	}
	if m.decrypt.Active(m.now) {
		return m.decrypt.Render(m.now, m.th, iw)
	}
	style := m.th.Bright.Bold(true)
	// A freshly resolved name glows accent for a beat, then settles to white.
	if settle := m.decrypt.start.Add(m.decrypt.dur); !m.decrypt.start.IsZero() &&
		m.now.After(settle) && m.now.Before(settle.Add(700*time.Millisecond)) {
		style = m.th.Accent.Bold(true)
	}
	if idle {
		style = m.th.Dim.Bold(true)
	}
	return style.Render(heroText(cleanStationName(m.st.Name), iw, m.th.G.Ellipsis))
}

func (m Model) trackRow(iw int, idle bool) string {
	heartW := runewidth.StringWidth(m.th.G.Heart)
	textW := iw - heartW - 2

	text := ""
	if m.haveTrack && m.track != "" {
		text = marquee(m.track, textW, m.now, m.trackAt, m.th.G.Ellipsis)
	}

	// New titles fade in, hold bright for their moment, then recede.
	style := m.th.Mid
	switch age := m.now.Sub(m.trackAt); {
	case age < 300*time.Millisecond:
		style = m.th.Dim
	case age < 10*time.Second:
		style = m.th.Bright
	}
	if idle {
		style = m.th.Mid // the track title is what idle mode keeps readable
	}
	// A loved track sits on the faintest accent surface: readable from
	// across the room, not just from the heart.
	if m.lovedTrack {
		style = style.Background(m.th.SelFill.GetBackground())
	}

	heart := strings.Repeat(" ", heartW)
	if m.lovedTrack {
		since := m.now.Sub(m.loveAt)
		switch {
		case since < 120*time.Millisecond:
			heart = m.th.Invert.Render(m.th.G.Heart) // one-frame flash
		case since < 2*time.Second:
			heart = m.th.Accent.Render(m.th.G.Heart)
		default:
			heart = m.th.AccentDim.Render(m.th.G.Heart)
		}
	}
	return lrRow(style.Render(text), heart, iw)
}

// bandRow is the dial: dim band, ember ticks where presets live, and an
// accent marker with a warm bleed that fades through the ramp — the
// needle glows like a VU meter, brightening at the playing position.
func (m Model) bandRow(iw int, idle bool) string {
	return m.bandRowTheme(iw, idle, m.th)
}

func (m Model) bandRowTheme(iw int, idle bool, th Theme) string {
	pos := clampF(m.dial.Pos, 0, 1)
	col := int(math.Round(pos * float64(iw-1)))
	markerStyle := th.Accent
	if idle {
		markerStyle = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
		if bg := th.Accent.GetBackground(); bg != nil {
			markerStyle = markerStyle.Background(bg)
		}
	}
	cells := make([]string, iw)
	isTick := make([]bool, iw)
	dimBand := th.Dim.Render(th.G.Band)
	for i := range cells {
		cells[i] = dimBand
	}
	for _, uuid := range m.presets {
		t := int(math.Round(stationDialPos(uuid) * float64(iw-1)))
		if t >= 0 && t < iw {
			cells[t] = th.AccentDim.Render(th.G.Tick)
			isTick[t] = true
		}
	}
	// Warm bleed: a 5-cell ember gradient around the marker, fading through
	// the ramp with distance. Preset ticks win over the bleed.
	bleed := []struct {
		off  int
		frac float64
	}{{-2, 0.18}, {-1, 0.42}, {1, 0.42}, {2, 0.18}}
	for _, bl := range bleed {
		j := col + bl.off
		if j < 0 || j >= iw || isTick[j] {
			continue
		}
		r, g, b := th.rampColor(bl.frac)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r, g, b)))
		if bg := th.Accent.GetBackground(); bg != nil {
			style = style.Background(bg)
		}
		cells[j] = style.Render(th.G.Band)
	}
	if col >= 0 && col < iw {
		cells[col] = markerStyle.Render(th.G.Marker)
	}
	return strings.Join(cells, "")
}

func (m Model) reasonRow(iw int) string {
	if m.fatal != "" {
		return m.statusRow("ERROR  "+m.fatal, iw, true)
	}
	if !m.haveSt {
		return ""
	}
	if m.feedback != "" && m.now.Sub(m.feedbackAt) < feedbackDur {
		return m.statusRow(m.feedback, iw, true)
	}
	// A suspected ad break earns a contextual hint: this is the moment the
	// user wants the skip and shouldn't have to remember which key it is.
	if m.suspect && m.ph == phPlay {
		return m.statusRow("BREAK  SPACE skips without penalty", iw, true)
	}
	if m.note != "" {
		return m.statusRow("NOTICE  "+m.note, iw, false)
	}
	return m.statusRow(statusReason(m.reason), iw, false)
}

// statusRow renders "LABEL  detail" input as an aligned, dotted status
// line. The label carries a semantic category glyph and color: seed/love
// events glow accent, recall events sit mid, the wildcard stays dim.
func (m Model) statusRow(text string, iw int, active bool) string {
	text = strings.Replace(text, "  ", " "+m.th.G.Dot+" ", 1)
	text = runewidth.Truncate(text, iw, m.th.G.Ellipsis)
	parts := strings.SplitN(text, " "+m.th.G.Dot+" ", 2)
	labelStyle, detailStyle := m.th.AccentDim.Bold(true), m.th.Dim
	glyph := m.th.G.Pointer
	if active {
		labelStyle, detailStyle = m.th.Accent.Bold(true), m.th.Mid
	} else {
		// Category from the label itself, calmest first.
		switch {
		case strings.HasPrefix(parts[0], "SELECTED") || strings.HasPrefix(parts[0], "SEEDED") ||
			strings.HasPrefix(parts[0], "SEED") || strings.HasPrefix(parts[0], "LOVED"):
			labelStyle = m.th.AccentDim.Bold(true)
		case strings.HasPrefix(parts[0], "PRESET") || strings.HasPrefix(parts[0], "HISTORY") ||
			strings.HasPrefix(parts[0], "RESUMED"):
			labelStyle = m.th.Mid.Bold(true)
			glyph = m.th.G.Tick
		default:
			labelStyle = m.th.Dim
			glyph = m.th.G.Dot
		}
	}
	if len(parts) == 1 {
		return m.th.Dim.Render(glyph+" ") + labelStyle.Render(parts[0])
	}
	return m.th.Dim.Render(glyph+" ") + labelStyle.Render(parts[0]) +
		m.th.Dim.Render(" "+m.th.G.Dot+" ") + detailStyle.Render(parts[1])
}

func statusReason(reason string) string {
	switch {
	case reason == "where you left off":
		return "RESUMED  Previous station"
	case strings.HasPrefix(reason, "seeded: "):
		return "SEEDED  " + strings.TrimPrefix(reason, "seeded: ")
	case strings.HasPrefix(reason, "seed echo: "):
		return "SEED  " + strings.TrimPrefix(reason, "seed echo: ")
	case strings.HasPrefix(reason, "preset "):
		return "PRESET  Slot " + strings.TrimPrefix(reason, "preset ")
	case reason == "from your history":
		return "HISTORY  Station recall"
	case reason == "from a loved track":
		return "LOVED  Origin station"
	case reason == "":
		return ""
	default:
		return "SELECTED  " + reason
	}
}

// promptRow renders the seed input: accent chevron, bright query, blinking
// cursor, dim hint while empty.
func (m Model) promptRow(iw int) string {
	cur := m.th.G.Cursor
	if (m.now.UnixMilli()/530)%2 == 1 {
		cur = " "
	}
	prefix := m.th.Accent.Render("> ")
	if len(m.buf) == 0 {
		hint := "genre or artist " + m.th.G.Dot + " esc cancels"
		if m.virgin {
			hint = "type a genre or artist " + m.th.G.Dot + " space for a wildcard"
		}
		hint = runewidth.Truncate(hint, maxI(0, iw-5), m.th.G.Ellipsis)
		return prefix + m.th.Accent.Render(cur) + "  " + m.th.Dim.Render(hint)
	}
	buf := string(m.buf)
	avail := iw - 3
	if bw := runewidth.StringWidth(buf); bw > avail {
		buf = sliceCols(buf, bw-avail, avail)
	}
	return prefix + m.th.Bright.Render(buf) + m.th.Accent.Render(cur)
}

func (m Model) volumeRow(iw int) string {
	percent := fmt.Sprintf("%d%%", m.volume)
	if iw < 20 {
		return m.th.Accent.Bold(true).Render(
			runewidth.Truncate("VOL "+percent, iw, m.th.G.Ellipsis))
	}
	barW := iw - 14
	if barW > 36 {
		barW = 36
	}
	if barW < 4 {
		barW = 4
	}
	knob := int(math.Round(float64(m.volume) / 100 * float64(barW-1)))
	cells := make([]string, barW)
	for i := range cells {
		switch {
		case i == knob:
			cells[i] = m.th.Accent.Render(m.th.G.Knob)
		case i < knob:
			cells[i] = m.th.AccentDim.Render(m.th.G.Band)
		default:
			cells[i] = m.th.Dim.Render(m.th.G.Band)
		}
	}
	return m.th.AccentDim.Bold(true).Render("VOLUME") + "  " +
		strings.Join(cells, "") + "  " + m.th.Mid.Render(percent)
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
	default:
		return m.recentRows(rows, iw, budget)
	}
}

func (m Model) historyTabs(iw int) string {
	labels := []string{"RECENT", fmt.Sprintf("LOVED %d", len(m.library)), "STATIONS"}
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

func (m Model) recentRows(rows []string, iw int, budget int) []string {
	if len(m.recent) == 0 {
		return append(rows, m.th.Dim.Render("nothing heard yet"))
	}
	maxRows := minI(len(m.recent), budget-len(rows))
	for i := 0; i < maxRows; i++ {
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
			count = fmt.Sprintf("%dx", entry.HeardCount)
		}
		labelW := maxI(4, iw-runewidth.StringWidth(count)-5)
		label = runewidth.Truncate(label, labelW, m.th.G.Ellipsis)
		if i == m.libraryPos {
			// Selection is a surface: a full-width bar, not a marker.
			left := m.th.SelText.Render(" " + m.th.G.Pointer + " " + label)
			right := m.th.SelMeta.Render(count + " ")
			gap := maxI(0, iw-lipgloss.Width(left)-lipgloss.Width(right))
			rows = append(rows, left+m.th.SelFill.Render(strings.Repeat(" ", gap))+right)
		} else {
			left := "   " + m.th.Mid.Render(label)
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
		if m.libraryFind && (m.now.UnixMilli()/530)%2 == 0 {
			cursor = m.th.G.Cursor
		}
		query := string(m.libraryQuery)
		if query == "" {
			return m.th.AccentDim.Bold(true).Render("FIND") + "  " +
				m.th.Dim.Render(runewidth.Truncate("artist, title, or station", maxI(0, iw-7), m.th.G.Ellipsis)) +
				m.th.Accent.Render(cursor)
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
		return m.th.Accent.Bold(true).Render(
			runewidth.Truncate("REMOVE?  X again to forget this track", iw, m.th.G.Ellipsis))
	}
	parts := []string{}
	if station := cleanStationName(entry.StationName); station != "" {
		parts = append(parts, station)
	}
	parts = append(parts, "loved "+relativeTime(entry.LovedAt, m.now))
	if entry.HeardCount > 0 {
		parts = append(parts, fmt.Sprintf("heard %dx", entry.HeardCount))
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
	for i := start; i < start+maxRows; i++ {
		e := entries[i]
		name := runewidth.Truncate(cleanStationName(e.Name), maxI(4, iw-22), m.th.G.Ellipsis)
		meta := fmt.Sprintf("%7s", fmtElapsed(e.Total))
		if e.LoveCount > 0 {
			meta += " " + m.th.G.Heart + fmt.Sprintf("%d", e.LoveCount)
		}
		if e.PresetSlot > 0 {
			meta += fmt.Sprintf(" %d", e.PresetSlot)
		}
		if i == m.savedPos {
			left := m.th.SelText.Render(" " + m.th.G.Pointer + " " + name)
			right := m.th.SelMeta.Render(meta + " ")
			gap := maxI(0, iw-lipgloss.Width(left)-lipgloss.Width(right))
			rows = append(rows, left+m.th.SelFill.Render(strings.Repeat(" ", gap))+right)
		} else {
			var leftStyle lipgloss.Style
			if i == 0 && len(m.savedQuery) == 0 {
				leftStyle = m.th.Bright.Bold(true) // most-listened earns the spotlight
			} else {
				leftStyle = m.th.Mid
			}
			left := "   " + leftStyle.Render(name)
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
		if m.savedFind && (m.now.UnixMilli()/530)%2 == 0 {
			cursor = m.th.G.Cursor
		}
		query := string(m.savedQuery)
		if query == "" {
			return m.th.AccentDim.Bold(true).Render("FIND") + "  " +
				m.th.Dim.Render(runewidth.Truncate("station name", maxI(0, iw-7), m.th.G.Ellipsis)) +
				m.th.Accent.Render(cursor)
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
		return m.th.Accent.Bold(true).Render(runewidth.Truncate(msg, iw, m.th.G.Ellipsis))
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

// --- boot: the wordmark resolves out of glyph noise over a growing rule ---

func (m Model) bootView(iw int, pad string, p float64) string {
	ease := 1 - math.Pow(1-p, 3)
	ruleW := int(ease * float64(iw))
	if ruleW < 1 {
		ruleW = 1
	}
	ruleLeft := (iw - ruleW) / 2
	rule := strings.Repeat(" ", ruleLeft) + m.th.Dim.Render(strings.Repeat(m.th.G.Rule, ruleW))

	markRunes := []rune(letterspace(wordmark))
	shown := 0
	if p > 0.25 {
		shown = int((p - 0.25) / 0.75 * float64(len(markRunes)+1))
	}
	if shown > len(markRunes) {
		shown = len(markRunes)
	}
	markLeft := (iw - runewidth.StringWidth(letterspace(wordmark))) / 2
	var mb strings.Builder
	mb.WriteString(strings.Repeat(" ", maxI(0, markLeft)))
	frame := int(p * 40)
	set := m.th.G.Decrypt
	for i, r := range markRunes {
		switch {
		case i < shown:
			mb.WriteString(m.th.Accent.Render(string(r)))
		case r == ' ':
			mb.WriteRune(' ')
		case p > 0.15:
			mb.WriteString(m.th.Dim.Render(string(set[(i*31+frame*17)%len(set)])))
		default:
			mb.WriteRune(' ')
		}
	}

	top := maxI(0, (m.h-3)/3)
	var b strings.Builder
	for i := 0; i < top; i++ {
		b.WriteString("\n")
	}
	b.WriteString(pad + mb.String() + "\n")
	b.WriteString(pad + rule + "\n")
	return b.String()
}

// --- helpers ---

// cleanStationName strips the directory junk stations carry in their names:
// trailing "(OGG)", "[128k]", dangling separators.
var trailingJunkRe = regexp.MustCompile(`\s*[(\[][^)\]]*[)\]]\s*$`)

func cleanStationName(s string) string {
	s = strings.TrimSpace(s)
	for {
		t := trailingJunkRe.ReplaceAllString(s, "")
		t = strings.TrimSpace(strings.TrimRight(t, " -·|"))
		if t == s || t == "" {
			break
		}
		s = t
	}
	return s
}

// heroText letterspaces only short station names. Long names use natural
// spacing and weight; tracking every letter makes directory names fragment.
func heroText(name string, iw int, ellipsis string) string {
	up := strings.ToUpper(strings.TrimSpace(name))
	spaced := letterspace(up)
	if runewidth.StringWidth(up) <= 14 && runewidth.StringWidth(spaced) <= iw {
		return spaced
	}
	return runewidth.Truncate(up, iw, ellipsis)
}

func letterspace(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		b.WriteRune(r)
		if i < len(runes)-1 {
			b.WriteRune(' ')
			if r == ' ' {
				b.WriteRune(' ') // word gaps read wider than letter gaps
			}
		}
	}
	return b.String()
}

// lrRow lays out styled left and right pieces on one line of width w.
func lrRow(left, right string, w int) string {
	lw := lipgloss.Width(left)
	rw := lipgloss.Width(right)
	gap := w - lw - rw
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func fmtElapsed(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	min := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, min)
	}
	return fmt.Sprintf("%dm", min)
}

func fmtClock(d time.Duration) string {
	seconds := int(d.Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	h, mm, s := seconds/3600, (seconds/60)%60, seconds%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mm, s)
	}
	return fmt.Sprintf("%02d:%02d", mm, s)
}
