package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/tinkthemaker/screech/internal/core"
)

// --- main screen: two tight groups with real air between them ---

func (m Model) mainRows(iw int, compact bool, contentH int) []string {
	if m.receiverShape() {
		return m.receiverRows(iw, contentH)
	}
	var rows []string
	if !compact {
		micro := "NOW PLAYING"
		if !m.haveSt {
			micro = "STANDBY"
		}
		micro = runewidth.Truncate(micro, iw, m.th.G.Ellipsis)
		rows = append(rows, scaleStyle(m.th.Dim, m.settleScale()).Render(micro))
	}
	rows = append(rows, m.heroRow(iw, m.idle()))
	rows = append(rows, m.trackRow(iw, m.idle()))

	if iw >= 28 && !compact && contentH >= 8 {
		rows = append(rows, "")
		rows = append(rows, m.signalRows(iw, m.th)...)
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

// signalRows is the wave bay: real metering, or the tuning ceremony's
// static collapse while a retune is locking.
func (m Model) signalRows(width int, th Theme) []string {
	if m.staticActive() {
		return m.staticRows(width, th)
	}
	return m.wave.Render(th)
}

// staticRows renders the static collapse: two rows of dither noise on the
// ember-low ramp, a pure function of the ceremony clock (same elapsed,
// same frame). Density fades to zero across the window so the collapse
// fizzles into the dim baseline rather than cutting — that fizzle is also
// the failure path's ending. Rows are exactly two and exactly width cells,
// the same contract as Wave.Render.
func (m Model) staticRows(width int, th Theme) []string {
	rows := m.wave.Rows() * 2
	if width < 1 {
		return make([]string, rows)
	}
	elapsed := m.now.Sub(m.tuneAt)
	if elapsed < 0 {
		elapsed = 0
	}
	frame := int(elapsed / (50 * time.Millisecond))
	fade := 1 - float64(elapsed)/float64(staticDur)
	set := []rune("░▒▓")
	base := string(rune(brailleBlank + brailleBaselineBits))
	if th.G.Blocks[0] == '_' { // 7-bit palette: #%* noise on the block floor
		set = []rune("#%*")
		base = string(th.G.Blocks[0])
	}
	blank := string(rune(brailleBlank))
	if th.G.Blocks[0] == '_' {
		blank = " "
	}
	perChan := m.wave.Rows()
	low, mid := th.RampFor(1, 7), th.RampFor(3, 7)
	out := make([]string, rows)
	for row := 0; row < rows; row++ {
		// Only the bottom cell of each channel carries the baseline; the
		// cells stacked above it rest empty, exactly as the live meter
		// leaves them.
		rest := blank
		if row%perChan == perChan-1 {
			rest = base
		}
		var b strings.Builder
		for x := 0; x < width; x++ {
			h := staticHash(x, row, frame)
			if h%1000 < uint32(fade*1000) {
				style := th.Dim
				switch h >> 20 % 3 {
				case 0:
					style = low
				case 1:
					style = mid
				}
				b.WriteString(style.Render(string(set[h>>10%uint32(len(set))])))
			} else {
				b.WriteString(th.Dim.Render(rest))
			}
		}
		out[row] = b.String()
	}
	return out
}

// padColumn stretches a column of rows to height by distributing blank rows
// into its marked gaps. A gap is an empty string already present in the
// column; surplus is shared among them, with any remainder going to the
// last one so the block above stays anchored where it was written.
//
// This is what keeps the left bay from clumping: the track block and the
// broadcast block spread down the panel alongside the signal instead of
// stacking at the top with a hole underneath.
func padColumn(rows []string, height int) []string {
	if len(rows) >= height {
		return rows
	}
	var gaps []int
	for i, r := range rows {
		if r == "" {
			gaps = append(gaps, i)
		}
	}
	surplus := height - len(rows)
	if len(gaps) == 0 {
		return append(rows, make([]string, surplus)...)
	}
	extra := make([]int, len(gaps))
	for i := range extra {
		extra[i] = surplus / len(gaps)
	}
	for i := 0; i < surplus%len(gaps); i++ {
		extra[len(extra)-1-i]++
	}
	out := make([]string, 0, height)
	g := 0
	for i, r := range rows {
		out = append(out, r)
		if g < len(gaps) && gaps[g] == i {
			for n := 0; n < extra[g]; n++ {
				out = append(out, "")
			}
			g++
		}
	}
	return out
}

// receiverRows is the wide-screen identity of Screech: a single substantial
// faceplate rather than a narrow column of terminal output. Music owns the
// left bay; the living signal and station-memory dial own the right.
func (m Model) receiverRows(iw, contentH int) []string {
	leftW, gapW, rightW := receiverColumns(iw)
	pt := m.th.OnPanel()
	// Panel microlabels are chrome: they settle with the room. Content —
	// title, artist, wave, dial — keeps living.
	pt.Dim = scaleStyle(pt.Dim, m.settleScale())
	wave := m.signalRows(rightW, pt)

	station := cleanStationName(m.st.Name)
	if station == "" {
		station = "NO SIGNAL"
	}
	// The track's halves are stored fields, never re-split: the joined
	// string's separator differs per glyph set.
	primaryLabel := "TRACK"
	artist, title := m.trackArtist, m.trackTitle
	titleAnchor := m.trackAt
	switch {
	case m.haveTrack && strings.TrimSpace(title) != "":
		// A title-only track leaves the artist row blank, not populated
		// with a label masquerading as a credit.
	case m.haveSt:
		// No title yet: the station name holds the slot, scrolling off
		// the tune clock rather than a track clock that doesn't exist.
		primaryLabel = "NOW PLAYING"
		title = station
		titleAnchor = m.tuneAt
		artist = ""
	default:
		primaryLabel = "STANDBY"
		title = "No signal"
		if m.syncing || m.seeking || m.ph == phTune {
			title = "Searching" + m.th.G.Ellipsis
		}
		titleAnchor = m.tuneAt
		artist = ""
	}

	heartW := 0
	if m.lovedTrack {
		heartW = runewidth.StringWidth(m.th.G.Heart) + 1
	}
	title = m.marqueeText(title, maxI(4, leftW-heartW), titleAnchor)
	titleCell := pt.Bright.Bold(true).Render(title)
	if m.lovedTrack {
		titleCell += pt.Mid.Render(" ") + m.receiverHeart(pt)
	}
	artist = runewidth.Truncate(artist, leftW, m.th.G.Ellipsis)
	station = runewidth.Truncate(station, leftW, m.th.G.Ellipsis)

	// The BROADCAST name decrypt-resolves with the lock, like the compact
	// hero — the station isn't really "there" until the stream is.
	//
	// It is letterspaced at mid weight rather than bright and bold. The
	// panel had two heroes: on a receiver the track is the thing playing and
	// the station is where it comes from, so the station reads as an
	// engraved source plate and the title is left as the only bright
	// element on the faceplate.
	stationCell := pt.Mid.Render(plateText(station, leftW, m.th.G.Ellipsis))
	if m.motion() && m.stDecrypt.Active(m.now) {
		stationCell = m.stDecrypt.Render(m.now, pt, plateText(station, leftW, m.th.G.Ellipsis), leftW)
	}

	// The bottom readout row doubles as the overlay bay: the seed prompt or
	// the volume slider takes it over while open, exactly as promptRow and
	// volumeRow replace the reason line in the compact layout. Precedence
	// matches the Update dispatch and the compact switch: prompt first.
	bottomRow := m.receiverStatusRow(iw)
	switch {
	case m.prompt:
		bottomRow = m.receiverPromptRow(iw)
	case m.volumeOpen:
		bottomRow = m.receiverVolumeRow(iw)
	}

	// The two bays are composed independently and then zipped. They hold
	// different amounts of hardware — the right one carries the meter, the
	// scale, the dial and the legend — so laying them out row by row forced
	// the left bay's content into a clump at the top with a hole under it.
	// Empty strings mark the gaps padColumn is allowed to stretch.
	// Figures sit right-aligned on their own label rows, the way a real
	// instrument panel carries them. Every number screech knew about used to
	// live up in the header rail, leaving the panel itself numberless.
	label := func(text, figure string) string {
		if figure == "" {
			return pt.Dim.Bold(true).Render(text)
		}
		return surfaceLR(pt.Dim.Bold(true).Render(text), pt.Dim.Render(figure), leftW, pt.PanelFill)
	}
	// A blank artist slot is a styled single space, not "": padColumn
	// treats an empty string as a stretchable gap, which would let the
	// broadcast block swallow the missing row. After ten locked seconds
	// with no title the blank stops reading as patience and starts
	// reading as a hole — say why instead.
	artistCell := pt.Mid.Render(artist)
	if artist == "" {
		artistCell = pt.Mid.Render(" ")
		if m.haveSt && !m.haveTrack && m.ph == phPlay && !m.playStart.IsZero() &&
			m.now.Sub(m.playStart) >= 10*time.Second {
			artistCell = pt.Dim.Render(
				runewidth.Truncate("No track info sent", leftW, m.th.G.Ellipsis))
		}
	}
	left := []string{
		label(primaryLabel, m.playCountFigure()),
		titleCell,
		artistCell,
		"", // stretches: pushes the broadcast block down beside the meter
		label("BROADCAST", fmtTotalTime(m.stTotal)),
		stationCell,
		"", // stretches: keeps the block off the status readout
	}
	// Tags are the station's own description of itself and they drive tag
	// affinity, which is one of the three things picking your next station.
	// They had no representation on screen at all, while the left bay sat
	// empty next to a meter — so the bay gets filled with something true
	// rather than with more blank rows.
	if tags := stationTags(&m.st, leftW, m.th.G.Ellipsis); tags != "" {
		left = append(left, pt.Dim.Bold(true).Render("GENRE"), pt.Mid.Render(tags), "")
	}
	memory := ""
	if n := len(m.presets); n > 0 {
		memory = fmt.Sprintf("%d SAVED", n)
	}
	right := []string{surfaceLR(pt.Dim.Bold(true).Render(m.signalLabel()),
		pt.Dim.Render(m.stereoFigure()), rightW, pt.PanelFill)}
	right = append(right, wave...)
	right = append(right,
		pt.Dim.Render(lrRow("LOW", "HIGH", rightW)),
		"", // stretches: separates the signal bay from the memory dial
		surfaceLR(pt.Dim.Bold(true).Render("STATION MEMORY"),
			pt.Dim.Render(memory), rightW, pt.PanelFill),
	)
	right = append(right, m.dialRows(rightW, m.idle(), pt)...)
	right = append(right, pt.Dim.Render(m.presetLegend(rightW)))

	// Stretch the shorter bay's gaps to match the taller one, so the
	// broadcast block lands beside the meter rather than above or below it.
	// Panel breathing room is added afterwards, to both bays equally —
	// folding it into the stretch target instead pulls the two bays apart
	// and opens a dead zone between BROADCAST and STATION MEMORY.
	natural := maxI(len(left), len(right))
	left, right = padColumn(left, natural), padColumn(right, natural)
	// Breathing room measured from what was actually composed — chrome,
	// bays and the one-row reserve accounted — rather than re-derived.
	pad := minI(maxI(contentH-receiverChromeRows-natural-1, 0), maxReceiverPad)
	for i := 0; i < pad; i++ {
		left, right = append(left, ""), append(right, "")
	}

	body := len(left)
	rows := make([]string, 0, body+3)
	rows = append(rows, m.frameRule(iw, true, "RECEIVER / NOW PLAYING"))
	for i := 0; i < body; i++ {
		rows = append(rows, m.panelColumns(left[i], right[i], leftW, gapW, rightW))
	}
	return append(rows, bottomRow, m.frameRule(iw, false, ""))
}

// stationTags renders the station's self-reported tags for the GENRE block:
// the first few, comma separated, truncated to the bay. Returns "" when the
// station carries none, in which case the block is dropped entirely rather
// than left as an empty label.
func stationTags(st *core.Station, width int, ellipsis string) string {
	tags := st.TagList()
	if len(tags) == 0 || width < 8 {
		return ""
	}
	if len(tags) > 3 {
		tags = tags[:3]
	}
	return runewidth.Truncate(strings.Join(tags, ", "), width, ellipsis)
}

// A dithered bevel under the top rail was tried here and removed. Low
// contrast is not the same as low visual weight: at 1.34 against the panel
// it measured subtle, but shade blocks across a contiguous span form one
// unbroken field of colour, and the eye reads the shape long before it
// reads the contrast. It looked like a rendering fault at the top of the
// chassis. The seam between the bays carries the "engineered object" job on
// its own, without inventing area that means nothing.

// plateText renders a station name as an engraved source plate: uppercase,
// letterspaced when it fits, plain uppercase when it doesn't. Same rule the
// compact layout's hero uses, so a name reads the same in both layouts.
func plateText(name string, width int, ellipsis string) string {
	up := strings.ToUpper(strings.TrimSpace(name))
	// Same 14-character gate the compact layout's hero uses. Letterspacing
	// a long directory name is legible in principle and unreadable in
	// practice — the eye stops grouping it into words.
	if spaced := letterspace(up); runewidth.StringWidth(up) <= 14 &&
		runewidth.StringWidth(spaced) <= width {
		return spaced
	}
	return runewidth.Truncate(up, width, ellipsis)
}

// fmtTotalTime is the cumulative-listen readout: coarse on purpose, since a
// figure that ticks every second would fight an interface designed to be
// left alone for hours.
func fmtTotalTime(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// playCountFigure reports how often this track has come down any stream.
// Silent on the first play: "heard 1x" is noise, and a first hearing is the
// default case rather than a fact worth stating.
func (m Model) playCountFigure() string {
	if !m.haveTrack || m.trackPlays < 2 {
		return ""
	}
	return fmt.Sprintf("heard %d%s", m.trackPlays, m.th.G.Times)
}

// stereoFigure is the signal bay's right-hand readout: which channel layout
// the backend is actually measuring, stated once rather than implied.
func (m Model) stereoFigure() string {
	if m.ph != phPlay {
		return ""
	}
	if m.stereo {
		return "2 CH"
	}
	return "1 CH"
}

// signalLabel names the signal bay and, with it, the axis the two blocks of
// rows encode. The bay is split top-half left channel, bottom-half right,
// but the only labels near it read LOW and HIGH — which describe the
// horizontal axis. Without this, nothing on screen says the vertical split
// means anything at all.
func (m Model) signalLabel() string {
	if m.stereo {
		return "SIGNAL  L/R"
	}
	return "SIGNAL  MONO"
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
	label, detail, hot := "WHY THIS STATION", m.typedText(whyDetail(m.reason)), false
	switch {
	case m.fatal != "":
		label, detail, hot = "ERROR", m.fatal, true
	case m.feedback != "" && m.now.Sub(m.feedbackAt) < feedbackDur:
		// Feedback arrives as "LABEL  detail"; the readout keeps both
		// halves rather than flattening the detail under a generic label.
		label, detail, hot = "UPDATED", m.feedback, true
		if i := strings.Index(m.feedback, "  "); i > 0 {
			label, detail = m.feedback[:i], m.feedback[i+2:]
		}
	case m.ph == phOffline:
		label, detail, hot = "OFFLINE", m.offlineDetail(), true
	case m.notice != "":
		label, detail = "NOTICE", m.notice
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
	if hot && (label == "ERROR" || label == "OFFLINE" || strings.Contains(label, "BREAK")) {
		labelStyle = m.th.Love.Bold(true).Background(bg) // coral is the warning color
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

// receiverVolumeRow is the volume slider seated on the raised readout
// surface — the wide faceplate's counterpart of volumeRow, sharing its
// knob-and-band language and its theme-derived colors.
func (m Model) receiverVolumeRow(iw int) string {
	width := maxI(1, iw-4)
	raised := m.th.PanelRaised
	bg := raised.GetBackground()
	labelStyle := m.th.Accent.Bold(true).Background(bg)
	knobStyle := m.th.Accent.Background(bg)
	bandStyle := m.th.AccentDim.Background(bg)
	restStyle := m.th.Mid.Background(bg)

	percent := fmt.Sprintf("%d%%", m.volume)
	const label = "VOLUME"
	barW := width - runewidth.StringWidth(label) - runewidth.StringWidth(percent) - 4
	if barW > 36 {
		barW = 36
	}
	if barW < 4 {
		content := labelStyle.Render(label) + raised.Render("  ") + restStyle.Render(percent)
		return m.panelFull(content, iw, raised)
	}
	knob := int(math.Round(float64(m.volume) / 100 * float64(barW-1)))
	cells := make([]string, barW)
	for i := range cells {
		switch {
		case i == knob:
			cells[i] = knobStyle.Render(m.th.G.Knob)
		case i < knob:
			cells[i] = bandStyle.Render(m.th.G.Band)
		default:
			cells[i] = restStyle.Render(m.th.G.Band)
		}
	}
	content := labelStyle.Render(label) + raised.Render("  ") +
		strings.Join(cells, "") + raised.Render("  ") + restStyle.Render(percent)
	return m.panelFull(content, iw, raised)
}

// receiverPromptRow is the seed prompt seated on the raised readout
// surface — the wide faceplate's counterpart of promptRow, keeping its
// accent chevron, bright query, blinking cursor, and dim empty-hint.
func (m Model) receiverPromptRow(iw int) string {
	width := maxI(1, iw-4)
	raised := m.th.PanelRaised
	bg := raised.GetBackground()
	labelStyle := m.th.Accent.Bold(true).Background(bg)
	accent := m.th.Accent.Background(bg)
	bright := m.th.Bright.Background(bg)
	dim := m.th.Dim.Background(bg)

	cur := m.cursorGlyph()
	const label = "SEED"
	chev := accent.Render("> ")
	// Room left for the query after label, gap, chevron, and cursor.
	tail := maxI(0, width-runewidth.StringWidth(label)-2-runewidth.StringWidth("> ")-1)
	content := labelStyle.Render(label) + raised.Render("  ") + chev
	if len(m.buf) == 0 {
		hint := "genre or artist " + m.th.G.Dot + " esc cancels"
		if m.virgin {
			hint = "type a genre or artist " + m.th.G.Dot + " space for a wildcard"
		}
		hint = runewidth.Truncate(hint, maxI(0, tail-1), m.th.G.Ellipsis)
		return m.panelFull(content+accent.Render(cur)+raised.Render(" ")+dim.Render(hint), iw, raised)
	}
	buf := string(m.buf)
	if bw := runewidth.StringWidth(buf); bw > tail {
		buf = sliceCols(buf, bw-tail, tail)
	}
	return m.panelFull(content+bright.Render(buf)+accent.Render(cur), iw, raised)
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

func (m Model) heroRow(iw int, idle bool) string {
	if !m.haveSt {
		text := "NO SIGNAL"
		if m.syncing || m.seeking || m.ph == phTune {
			text = "SEARCHING" + m.th.G.Ellipsis
		}
		return m.th.Dim.Render(runewidth.Truncate(text, iw, m.th.G.Ellipsis))
	}
	if m.motion() && m.decrypt.Active(m.now) {
		return m.decrypt.Render(m.now, m.th,
			heroText(cleanStationName(m.st.Name), iw, m.th.G.Ellipsis), iw)
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
		text = m.marqueeText(m.track, textW, m.trackAt)
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
	// A loved track sits on the love wash: a coral flash at the moment of
	// love, cooling over two seconds into the permanent coral-dark surface.
	if m.lovedTrack {
		style = style.Background(m.loveWash())
	}

	heart := strings.Repeat(" ", heartW)
	if m.lovedTrack {
		since := m.now.Sub(m.loveAt)
		switch {
		case since < 120*time.Millisecond:
			heart = m.th.Invert.Render(m.th.G.Heart) // one-frame flash
		case since < 2*time.Second:
			heart = m.th.Love.Render(m.th.G.Heart) // coral: love is semantic
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

// offlineRetrySeconds is the countdown shown on the status line while
// offline: whole seconds until the scheduled retry, floored at zero so a
// lapsed timer waiting on the next tick never reads negative.
func (m Model) offlineRetrySeconds() int {
	return maxI(0, int(m.retryAt.Sub(m.now).Seconds()+0.999))
}

// offlineDetail is the status-row text while offline: a countdown while the
// timer is armed, "retrying now" in the window where a retry is in flight.
func (m Model) offlineDetail() string {
	if m.retryAt.IsZero() {
		return "retrying now"
	}
	return fmt.Sprintf("retrying in %ds "+m.th.G.Dot+" SPACE tries now", m.offlineRetrySeconds())
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
	if m.ph == phOffline {
		return m.statusRow("OFFLINE  "+m.offlineDetail(), iw, true)
	}
	if m.notice != "" {
		// The startup notice is the DB-recovered warning — active, not
		// calm dim text nobody reads.
		return m.statusRow("NOTICE  "+m.notice, iw, true)
	}
	// A suspected ad break earns a contextual hint: this is the moment the
	// user wants the skip and shouldn't have to remember which key it is.
	if m.suspect && m.ph == phPlay {
		return m.statusRow("BREAK  SPACE skips without penalty", iw, true)
	}
	if m.note != "" {
		return m.statusRow("NOTICE  "+m.note, iw, false)
	}
	return m.statusRow(m.typedText(statusReason(m.reason)), iw, false)
}

// statusRow renders "LABEL  detail" input as an aligned, dotted status
// line. The label carries a semantic category glyph and color: seed/love
// events glow accent, recall events sit mid, the wildcard stays dim.
func (m Model) statusRow(text string, iw int, active bool) string {
	text = strings.Replace(text, "  ", " "+m.th.G.Dot+" ", 1)
	text = runewidth.Truncate(text, iw, m.th.G.Ellipsis)
	parts := strings.SplitN(text, " "+m.th.G.Dot+" ", 2)
	labelStyle, detailStyle := m.th.Dim, m.th.Dim
	glyph := m.th.G.Pointer
	if active {
		labelStyle, detailStyle = m.th.Accent.Bold(true), m.th.Mid
		// Coral is the warning color: errors, outages and the break hint
		// all earn it in both layouts.
		if parts[0] == "ERROR" || parts[0] == "OFFLINE" || strings.Contains(parts[0], "BREAK") {
			labelStyle = m.th.Love.Bold(true)
		}
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
			// The dim label the row already starts with; only the glyph
			// steps down to the quietest mark.
			glyph = m.th.G.Dot
		}
	}
	if len(parts) == 1 {
		return clip(m.th.Dim.Render(glyph+" ")+labelStyle.Render(parts[0]), iw)
	}
	return clip(m.th.Dim.Render(glyph+" ")+labelStyle.Render(parts[0])+
		m.th.Dim.Render(" "+m.th.G.Dot+" ")+detailStyle.Render(parts[1]), iw)
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
	cur := m.cursorGlyph()
	prefix := m.th.Accent.Render("> ")
	if len(m.buf) == 0 {
		hint := "genre or artist " + m.th.G.Dot + " esc cancels"
		if m.virgin {
			hint = "type a genre or artist " + m.th.G.Dot + " space for a wildcard"
		}
		hint = runewidth.Truncate(hint, maxI(0, iw-5), m.th.G.Ellipsis)
		return clip(prefix+m.th.Accent.Render(cur)+"  "+m.th.Dim.Render(hint), iw)
	}
	buf := string(m.buf)
	avail := iw - 3
	if bw := runewidth.StringWidth(buf); bw > avail {
		buf = sliceCols(buf, bw-avail, avail)
	}
	return clip(prefix+m.th.Bright.Render(buf)+m.th.Accent.Render(cur), iw)
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
