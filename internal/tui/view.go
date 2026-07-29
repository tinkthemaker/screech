package tui

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
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
		top = []string{m.headerBar(), m.ruleLine(m.w)}
	}
	contentH := m.h - len(top) - 1

	var body []string
	if m.themeOpen {
		body = m.themeRows(iw, contentH)
	} else if m.history {
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
	} else {
		style = scaleStyle(style, m.settleScale())
	}
	left := style.Render(" " + wordmark)
	if m.w >= 58 {
		left += scaleStyle(m.th.Dim, m.settleScale()).Render("  PERSONAL RADIO")
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

// settleScale is the idle-settle dimmer: 1 while the room is awake, easing
// to an ember floor over settleFade once nothing has happened for
// settleAfter. Chrome scales its colors by this at render time — no theme
// rebuild, just a quieter room. The signal instruments never read it.
func (m Model) settleScale() float64 {
	over := m.now.Sub(m.lastAct) - settleAfter
	if over <= 0 {
		return 1
	}
	k := 1 - 0.45*float64(over)/float64(settleFade)
	if k < 0.55 {
		k = 0.55
	}
	return k
}

// scaleStyle multiplies a style's foreground and background colors by k at
// the application layer. Colors that aren't set or aren't 7-char hex pass
// through untouched.
func scaleStyle(s lipgloss.Style, k float64) lipgloss.Style {
	if k >= 1 {
		return s
	}
	scale := func(c lipgloss.TerminalColor) lipgloss.TerminalColor {
		hex := fmt.Sprint(c)
		if len(hex) != 7 || hex[0] != '#' {
			return c
		}
		r, g, b := hexRGB(hex)
		return lipgloss.Color(rgbHex(int(float64(r)*k), int(float64(g)*k), int(float64(b)*k)))
	}
	if fg := s.GetForeground(); fg != nil {
		s = s.Foreground(scale(fg))
	}
	if bg := s.GetBackground(); bg != nil {
		s = s.Background(scale(bg))
	}
	return s
}

// borderStyle is the frame metal under the idle settle.
func (m Model) borderStyle() lipgloss.Style {
	return scaleStyle(m.th.PanelBorder, m.settleScale())
}

// loveWash is the loved-track surface color: a coral flash at the moment of
// love, cooling over two seconds into the permanent LoveFill wash. In the
// austere theme LoveFill is the selection surface, so the wash stays
// monochrome automatically.
func (m Model) loveWash() lipgloss.TerminalColor {
	base := m.th.LoveFill.GetBackground()
	since := m.now.Sub(m.loveAt)
	if base == nil || since < 0 || since >= 2*time.Second {
		return base
	}
	bhex, lhex := fmt.Sprint(base), fmt.Sprint(m.th.Love.GetForeground())
	if len(bhex) != 7 || bhex[0] != '#' || len(lhex) != 7 || lhex[0] != '#' {
		return base
	}
	br, bg, bb := hexRGB(bhex)
	lr, lg, lb := hexRGB(lhex)
	f := float64(since) / float64(2*time.Second)
	cool := func(lo, hi int) int {
		start := float64(hi) * 0.45
		return int(start + (float64(lo)-start)*f)
	}
	return lipgloss.Color(rgbHex(cool(br, lr), cool(bg, lg), cool(bb, lb)))
}

func (m Model) footerItems() [][2]string {
	if m.themeOpen {
		return [][2]string{{"J/K", "preview"}, {"ENTER", "apply"}, {"ESC", "cancel"}}
	}
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
		{"/", "discover"}, {"H", "library"}, {"V", fmt.Sprintf("%d%%", m.volume)}, {"T", "theme"}, {"Q", "quit"},
	}
}

// footerBar is the key strip: a full-width surface plane pinned to the last
// row, keys as chips, labels quiet beside them.
func (m Model) footerBar() string {
	items := m.footerItems()
	// The strip settles with the room; a loved track's L chip keeps its
	// coral — state, like LIVE, stays readable.
	k := m.settleScale()
	fill := scaleStyle(m.th.FootFill, k)
	key := scaleStyle(m.th.FootKey, k)
	label := scaleStyle(m.th.FootLabel, k)
	sep := fill.Render("  ")
	var b strings.Builder
	b.WriteString(fill.Render(" "))
	for i, it := range items {
		if i > 0 {
			b.WriteString(sep)
		}
		keyStyle := key
		if m.lovedTrack && it[0] == "L" {
			keyStyle = keyStyle.Foreground(m.th.Love.GetForeground())
		}
		b.WriteString(keyStyle.Render(" " + it[0] + " "))
		if it[1] != "" {
			b.WriteString(label.Render(" " + it[1]))
		}
	}
	line := b.String()
	if lipgloss.Width(line) > m.w {
		keys := make([]string, len(items))
		for i, it := range items {
			keys[i] = it[0]
		}
		line = key.Render(runewidth.Truncate(" "+strings.Join(keys, "  "), m.w, ""))
	}
	if gap := m.w - lipgloss.Width(line); gap > 0 {
		line += fill.Render(strings.Repeat(" ", gap))
	}
	return line
}

// --- main screen: two tight groups with real air between them ---

func (m Model) mainRows(iw int, compact bool, contentH int) []string {
	if iw >= 78 && !compact && contentH >= 11 {
		return m.receiverRows(iw, contentH)
	}
	var rows []string
	if !compact {
		micro := "NOW PLAYING"
		if !m.haveSt {
			micro = "STANDBY"
		}
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

// themeRows is a small, modal finish selector. Moving the cursor previews the
// complete palette immediately; the footer makes the Enter/Escape transaction
// explicit.
func (m Model) themeRows(iw, budget int) []string {
	choices := ThemeChoices()
	if iw < 50 || budget < len(choices)+5 {
		rows := []string{m.th.Accent.Bold(true).Render("THEME")}
		for i, choice := range choices {
			pointer := "  "
			style := m.th.Mid
			if i == m.themePos {
				pointer = m.th.G.Pointer + " "
				style = m.th.Bright.Bold(true)
			}
			line := pointer + fmt.Sprintf("%d  %s", i+1, strings.ToUpper(choice.Label))
			rows = append(rows, style.Render(runewidth.Truncate(line, iw, m.th.G.Ellipsis)))
		}
		if m.themePos >= 0 && m.themePos < len(choices) {
			rows = append(rows, m.th.Dim.Render(runewidth.Truncate(choices[m.themePos].Description, iw, m.th.G.Ellipsis)))
		}
		return rows
	}

	rows := []string{m.frameRule(iw, true, "APPEARANCE / THEME")}
	pt := m.th.OnPanel()
	rows = append(rows, m.panelFull(
		pt.Dim.Render("Choose an instrument finish "+m.th.G.Dot+" movement previews live"),
		iw, m.th.PanelFill))
	for i, choice := range choices {
		rows = append(rows, m.themeChoiceRow(iw, i, choice))
	}
	rows = append(rows, m.panelFull("", iw, m.th.PanelFill))
	preview := pt.Dim.Bold(true).Render("SIGNAL  ") + m.themeSwatch(minI(32, iw-16))
	rows = append(rows, m.panelFull(preview, iw, m.th.PanelFill))
	choice := choices[m.themePos]
	rows = append(rows, m.panelFull(pt.Mid.Render(choice.Description), iw, m.th.PanelFill))
	rows = append(rows, m.frameRule(iw, false, ""))
	return rows
}

func (m Model) themeChoiceRow(iw, index int, choice ThemeChoice) string {
	inner := maxI(1, iw-4)
	selected := index == m.themePos
	fill := m.th.PanelFill
	leftStyle, rightStyle := m.th.OnPanel().Mid, m.th.OnPanel().Dim
	pointer := "  "
	if selected {
		fill = m.th.SelFill
		leftStyle, rightStyle = m.th.SelText, m.th.SelMeta
		pointer = m.th.G.Pointer + " "
	}
	left := leftStyle.Bold(selected).Render(pointer + fmt.Sprintf("%d  %s", index+1, strings.ToUpper(choice.Label)))
	right := rightStyle.Render(choice.ID)
	return m.panelFull(surfaceLR(left, right, inner, fill), iw, fill)
}

func (m Model) themeSwatch(width int) string {
	if width < 1 {
		return ""
	}
	bg := m.th.PanelFill.GetBackground()
	var b strings.Builder
	for i := 0; i < width; i++ {
		idx := i * len(m.th.Ramp) / width
		if idx >= len(m.th.Ramp) {
			idx = len(m.th.Ramp) - 1
		}
		b.WriteString(m.th.Ramp[idx].Background(bg).Render(string(m.th.G.Blocks[len(m.th.G.Blocks)-1])))
	}
	return b.String()
}

func surfaceLR(left, right string, width int, fill lipgloss.Style) string {
	gap := maxI(0, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + fill.Render(strings.Repeat(" ", gap)) + right
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

// stackedFixedRows is the compact layout's non-stretching content: the
// NOW PLAYING microlabel, hero, track line, a spacer, the band line and the
// reason row. Only the signal bay grows.
const stackedFixedRows = 6

// stackedWaveRows sizes the compact layout's meter. A terminal too narrow
// for the two-bay faceplate is not necessarily short, and an 80-column
// window is the single most common terminal there is — it shouldn't get a
// two-row meter floating in half a screen of nothing.
func stackedWaveRows(contentH int) int {
	n := 1 + (contentH-stackedFixedRows-2)/2
	if n < 1 {
		n = 1
	}
	if n > maxWaveRows {
		n = maxWaveRows
	}
	return n
}

// receiverFixedRows is everything in the faceplate that doesn't stretch:
// both frame rules, the two label rows, title, artist, the LOW/HIGH scale,
// the station name, the preset legend and the status readout. The wave's
// first two rows sit alongside title and artist, so they cost nothing extra.
const receiverFixedRows = 10

// maxReceiverPad caps the blank rows inside the panel. Past this the
// faceplate stops reading as a machined object and starts reading as a box
// someone dragged the corner of. Height beyond this is deliberately left as
// margin around a well-proportioned instrument.
const maxReceiverPad = 3

// receiverLayout decides how the faceplate spends the height it's given.
// Extra rows go to the signal bay first, since the meter is the one part of
// the panel that's actually alive, then to padding.
//
// This is why the panel used to sit at a fixed ten rows and float in a sea
// of black on a tall terminal: it never asked how much room it had. Note
// that filling the terminal completely is not the goal — a radio faceplate
// stretched down eighty rows would be worse than the original problem. The
// panel grows to a substantial size and then centers.
func receiverLayout(contentH int) (wavePerChannel, pad int) {
	spare := contentH - receiverFixedRows
	if spare < 0 {
		spare = 0
	}
	// Each extra per-channel row costs two rows of panel: both channels grow
	// together. Keep one row in reserve so the panel never reaches the edge.
	wavePerChannel = 1 + (spare-1)/2
	if wavePerChannel < 1 {
		wavePerChannel = 1
	}
	if wavePerChannel > maxWaveRows {
		wavePerChannel = maxWaveRows
	}
	pad = spare - 2*(wavePerChannel-1) - 1
	if pad < 0 {
		pad = 0
	}
	if pad > maxReceiverPad {
		pad = maxReceiverPad
	}
	return
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

	// The BROADCAST name decrypt-resolves with the lock, like the compact
	// hero — the station isn't really "there" until the stream is.
	//
	// It is letterspaced at mid weight rather than bright and bold. The
	// panel had two heroes: on a receiver the track is the thing playing and
	// the station is where it comes from, so the station reads as an
	// engraved source plate and the title is left as the only bright
	// element on the faceplate.
	stationCell := pt.Mid.Render(plateText(station, leftW))
	if m.stDecrypt.Active(m.now) {
		stationCell = m.stDecrypt.Render(m.now, pt, leftW)
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
	left := []string{
		label(primaryLabel, m.playCountFigure()),
		titleCell,
		pt.Mid.Render(artist),
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
	if tags := stationTags(&m.st, leftW); tags != "" {
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
	_, pad := receiverLayout(contentH)
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
func stationTags(st *core.Station, width int) string {
	tags := st.TagList()
	if len(tags) == 0 || width < 8 {
		return ""
	}
	if len(tags) > 3 {
		tags = tags[:3]
	}
	return runewidth.Truncate(strings.Join(tags, ", "), width, "…")
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
func plateText(name string, width int) string {
	up := strings.ToUpper(strings.TrimSpace(name))
	// Same 14-character gate the compact layout's hero uses. Letterspacing
	// a long directory name is legible in principle and unreadable in
	// practice — the eye stops grouping it into words.
	if spaced := letterspace(up); runewidth.StringWidth(up) <= 14 &&
		runewidth.StringWidth(spaced) <= width {
		return spaced
	}
	return runewidth.Truncate(up, width, "…")
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
	return fmt.Sprintf("heard %d%s", m.trackPlays, "×")
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
	label, detail, hot := "WHY THIS STATION", m.tw.RenderOver(m.now, whyDetail(m.reason)), false
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

	cur := m.th.G.Cursor
	if (m.now.UnixMilli()/530)%2 == 1 {
		cur = " "
	}
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

// ruleLine renders a full-width header rule. Gradient finishes run the rule
// through the ramp (ember -> accent -> second hue); single-hue themes keep
// the flat dim line they have always had.
func (m Model) ruleLine(width int) string {
	k := m.settleScale()
	if width < 1 || m.th.AccentHex2 == "" {
		return scaleStyle(m.th.Dim, k).Render(strings.Repeat(m.th.G.Rule, maxI(0, width)))
	}
	var b strings.Builder
	span := maxI(1, width-1)
	for i := 0; i < width; i++ {
		r, g, bl := m.th.rampColor(float64(i) / float64(span))
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(rgbHex(r, g, bl)))
		b.WriteString(scaleStyle(style, k).Render(m.th.G.Rule))
	}
	return b.String()
}

func (m Model) frameRule(width int, top bool, label string) string {
	left, right := m.th.G.FrameTL, m.th.G.FrameTR
	if !top {
		left, right = m.th.G.FrameBL, m.th.G.FrameBR
	}
	border := m.borderStyle()
	labelW := runewidth.StringWidth(label)
	if label != "" && labelW+6 <= width {
		// left cap (4) + label + separator (1) + tail + right cap (1).
		// The previous -5 put the labeled top rail one cell past the body.
		tail := maxI(0, width-labelW-6)
		return border.Render(left+m.th.G.FrameH+m.th.G.FrameH+" ") +
			scaleStyle(m.th.AccentDim, m.settleScale()).Bold(true).Render(label) +
			border.Render(" "+strings.Repeat(m.th.G.FrameH, tail)+right)
	}
	return border.Render(left + strings.Repeat(m.th.G.FrameH, maxI(0, width-2)) + right)
}

// seamStyle is the gutter rule between the two bays: the frame metal at
// roughly half weight, so the seam reads as an internal division rather
// than competing with the chassis edge.
func (m Model) seamStyle() lipgloss.Style {
	s := scaleStyle(m.th.PanelBorder, 0.62*m.settleScale())
	if bg := m.th.PanelFill.GetBackground(); bg != nil {
		s = s.Background(bg)
	}
	return s
}

func (m Model) panelColumns(left, right string, leftW, gapW, rightW int) string {
	fill := m.th.PanelFill
	border := m.borderStyle()
	// The gutter was gapW plain spaces, which left the bays reading as two
	// text columns that happened to be adjacent. A rule down the middle is
	// what makes the faceplate read as one chassis with two compartments.
	gutter := fill.Render(strings.Repeat(" ", gapW))
	if gapW >= 3 {
		pad := strings.Repeat(" ", (gapW-1)/2)
		gutter = fill.Render(pad) + m.seamStyle().Render(m.th.G.Seam) +
			fill.Render(strings.Repeat(" ", gapW-1-len(pad)))
	}
	return border.Render(m.th.G.FrameV) + fill.Render(" ") +
		surfaceCell(left, leftW, fill) + gutter +
		surfaceCell(right, rightW, fill) + fill.Render(" ") +
		border.Render(m.th.G.FrameV)
}

func (m Model) panelFull(content string, width int, fill lipgloss.Style) string {
	inner := maxI(0, width-4)
	border := m.borderStyle()
	return border.Render(m.th.G.FrameV) + fill.Render(" ") +
		surfaceCell(content, inner, fill) + fill.Render(" ") +
		border.Render(m.th.G.FrameV)
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

// dialRows is the station-memory dial: a listening-density strip, the band
// with its needle and preset ticks, and the slot digits under the ticks
// they belong to.
//
// It used to be one row of band glyphs. That was defensible while the
// signal meter beside it was also one row, but the meter is six rows now
// and a hairline underneath it read as a placeholder. The digits are not
// decoration either: preset ticks were anonymous, so the dial could tell
// you a saved station lived at that position but never which one.
//
// The strip replaced a row of evenly spaced graduations. Graduations imply
// a quantity along the axis, and there isn't one — dial position is a hash
// of the station UUID, so a mark at 30% measured nothing while sitting
// directly above ticks that measured something real. Listening time is a
// genuine distribution over that same axis, so the row now carries it.
func (m Model) dialRows(width int, idle bool, th Theme) []string {
	if width < 1 {
		return []string{"", ""}
	}
	rows := make([]string, 0, 3)
	// No history yet means no distribution. Dropping the row is better than
	// drawing an empty one: the strip appears when it has something to say.
	if strip := m.dialDensityRow(width, th); strip != "" {
		rows = append(rows, strip)
	}
	return append(rows,
		m.bandRowTheme(width, idle, th),
		m.dialDigits(width, th),
	)
}

// buildDialDensity buckets cumulative listen time onto the dial axis and
// normalizes it to its own peak. The result is a shape, not a measurement:
// it answers "where do I actually live on this band" rather than "how many
// hours", which is what the readouts elsewhere are for.
func buildDialDensity(totals map[string]time.Duration, width int) []float64 {
	if width < 1 || len(totals) == 0 {
		return nil
	}
	raw := make([]float64, width)
	for uuid, d := range totals {
		if d <= 0 {
			continue
		}
		col := int(math.Round(stationDialPos(uuid) * float64(width-1)))
		if col >= 0 && col < width {
			raw[col] += d.Seconds()
		}
	}
	// A light blur either side. Without it a handful of stations render as
	// isolated single-cell spikes, which reads as noise rather than as a
	// distribution.
	out := make([]float64, width)
	peak := 0.0
	for i := range raw {
		out[i] = raw[i]
		if i > 0 {
			out[i] += 0.5 * raw[i-1]
		}
		if i < width-1 {
			out[i] += 0.5 * raw[i+1]
		}
		if out[i] > peak {
			peak = out[i]
		}
	}
	if peak <= 0 {
		return nil
	}
	for i := range out {
		out[i] /= peak
	}
	return out
}

// densityTopBlock caps the strip's tallest bar below a full cell. A run of
// full blocks is a solid slab, which is what made the strip read as a
// rendering fault rather than as a reading.
const densityTopBlock = 5

// dialDensityRow renders the strip as a one-row histogram: bar height
// carries the value, in a single quiet ember.
//
// Shade blocks were tried first, on the theory that ░▒▓ is literally a
// density scale. They failed for two reasons that only show up on a real
// screen. Fill density has no baseline, so adjacent heavy cells merge into
// one solid rectangle instead of reading as separate values; and the top of
// the scale measured 5.08 contrast against the panel, near the band line's
// own 5.28, so the busiest region of a background strip outweighed the
// instrument it sits under. Height against a common baseline reads as data
// at any density, and one colour keeps the strip subordinate.
func (m Model) dialDensityRow(width int, th Theme) string {
	d := m.density
	if len(d) != width {
		// Width changed since the last rebuild (a resize mid-frame). Skip
		// rather than render a strip that doesn't match the band below it.
		return ""
	}
	blocks := th.G.Blocks
	top := minI(densityTopBlock, len(blocks)-1)
	style := th.RampFor(1, 7)
	var b strings.Builder
	for _, v := range d {
		if v < 0.06 {
			b.WriteString(th.Dim.Render(" "))
			continue
		}
		lvl := int(math.Round(v * float64(top)))
		if lvl < 1 {
			lvl = 1
		}
		b.WriteString(style.Render(string(blocks[lvl])))
	}
	return b.String()
}

// dialDigits labels each preset tick with its slot number. The station
// currently playing takes the accent; the rest sit at ember, the same
// relationship the ticks themselves have on the band.
func (m Model) dialDigits(width int, th Theme) string {
	cells := make([]string, width)
	blank := th.Dim.Render(" ")
	for i := range cells {
		cells[i] = blank
	}
	current := m.currentSlot()
	slots := make([]int, 0, len(m.presets))
	for slot := range m.presets {
		slots = append(slots, slot)
	}
	sort.Ints(slots)

	// Dial positions are hashed, so two presets can land on neighbouring
	// columns and their digits then read as one number — a 1 beside a 7 is
	// "17", not two presets. Each digit therefore claims a cell with at
	// least one blank either side, searching outward for room. Low slots go
	// first so the one nearest its true position is the one you'd press.
	taken := make([]bool, width)
	free := func(c int) bool {
		if c < 0 || c >= width {
			return false
		}
		for d := -1; d <= 1; d++ {
			if n := c + d; n >= 0 && n < width && taken[n] {
				return false
			}
		}
		return true
	}
	for _, slot := range slots {
		want := int(math.Round(stationDialPos(m.presets[slot]) * float64(width-1)))
		col := -1
		for off := 0; off < width && col < 0; off++ {
			switch {
			case free(want - off):
				col = want - off
			case free(want + off):
				col = want + off
			}
		}
		if col < 0 {
			continue // no room left on the scale; the tick still shows
		}
		taken[col] = true
		style := th.AccentDim
		if slot == current {
			style = th.Accent.Bold(true)
		}
		cells[col] = style.Render(strconv.Itoa(slot))
	}
	return strings.Join(cells, "")
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
	// Love lands on the dial: one coral pulse of the needle, seated on the
	// same surface as its neighbors.
	if m.lovedTrack && m.now.Sub(m.loveAt) < 600*time.Millisecond {
		markerStyle = th.Love
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
	// the ramp with distance. Preset ticks win over the bleed. While the
	// needle is still traveling — or the stream hasn't locked — the bleed
	// becomes a detune smear: a wider trail of ramp-low dither glyphs that
	// clears back to the warm bleed once the spring settles on a lock.
	bleed := []struct {
		off  int
		frac float64
	}{{-2, 0.18}, {-1, 0.42}, {1, 0.42}, {2, 0.18}}
	detuning := !m.dial.Settled(m.dialTgt) || m.ph == phTune
	if detuning {
		bleed = []struct {
			off  int
			frac float64
		}{{-3, 0.18}, {-2, 0.30}, {-1, 0.42}, {1, 0.42}, {2, 0.30}, {3, 0.18}}
	}
	dither := "░"
	if th.G.Blocks[0] == '_' {
		dither = "#"
	}
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
		glyph := th.G.Band
		if detuning {
			off := bl.off
			if off < 0 {
				off = -off
			}
			glyph = th.G.Dot
			if off >= 2 {
				glyph = dither
			}
		}
		cells[j] = style.Render(glyph)
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
	return m.statusRow(m.tw.RenderOver(m.now, statusReason(m.reason)), iw, false)
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
			// Selection is a surface: a full-width bar on the love wash,
			// the heart in coral where the pointer would sit.
			bg := m.loveWash()
			selText := m.th.SelText.Background(bg)
			selMeta := m.th.SelMeta.Background(bg)
			selFill := m.th.SelFill.Background(bg)
			left := selText.Render(" ") + m.th.Love.Background(bg).Render(m.th.G.Heart) + selText.Render(" "+label)
			right := selMeta.Render(count + " ")
			gap := maxI(0, iw-lipgloss.Width(left)-lipgloss.Width(right))
			rows = append(rows, left+selFill.Render(strings.Repeat(" ", gap))+right)
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
	rule := strings.Repeat(" ", ruleLeft) + m.ruleLine(ruleW)

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
