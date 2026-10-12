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
	"github.com/charmbracelet/x/ansi"
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

	if boot := m.now.Sub(m.start); m.motion() && boot < bootDur {
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
	if m.idle() && m.motion() {
		style = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
	} else {
		style = scaleStyle(style, m.settleScale())
	}
	word := style.Render(" " + wordmark)

	// The subtitle is a fact about what the app is doing — a directory sync
	// matters more than the tagline — and it shows whenever it fits rather
	// than behind a fixed width cutoff.
	sub := "  PERSONAL RADIO"
	if m.syncing {
		sub = "  SYNCING DIRECTORY" + m.th.G.Ellipsis
	}
	sub = scaleStyle(m.th.Dim, m.settleScale()).Render(sub)

	// Right-side precedence: the outage warnings outrank the ceremony
	// states, which outrank the instrument readout. A directory sync never
	// touches this side — it lives in the subtitle now.
	status := ""
	statusStyle := m.th.Dim
	switch {
	case m.ph == phOffline:
		status = "OFFLINE"
		statusStyle = m.th.Love // coral is the warning color
	case m.ph == phBuffer:
		status = "BUFFERING" + m.th.G.Ellipsis
		// Buffering blinks by style, not text: an ellipsis that comes and
		// goes would make the right-aligned header jitter a column on
		// every beat. Under reduced motion it just holds bright.
		if m.motion() && (m.now.UnixMilli()/600)%2 == 1 {
			statusStyle = m.th.Dim
		} else {
			statusStyle = m.th.Accent
		}
	case m.ph == phTune && m.reconnecting:
		status = "RECONNECTING" + m.th.G.Ellipsis
	case m.seeking:
		status = "SEEKING" + m.th.G.Ellipsis
	case m.ph == phTune:
		status = "TUNING" + m.th.G.Ellipsis
	case m.ph == phPlay && m.haveSt:
		status = m.th.G.Knob + " LIVE"
		statusStyle = m.th.Accent // live: the readout is the one accent-lit instrument
	case m.ph == phDead:
		status = "STOPPED"
		statusStyle = m.th.Love
	}
	breakChip := ""
	if m.suspect && m.ph == phPlay && m.haveSt {
		breakChip = m.th.Love.Bold(true).Render("  BREAK") // coral is the warning color
	}
	statusR := statusStyle.Render(status)
	right := statusR
	if m.ph == phPlay && m.haveSt {
		if r := m.readout(); r != "" {
			right += statusStyle.Render("  " + r)
		}
	}
	right += breakChip

	// Degradation ladder, first combination that fits wins: subtitle, then
	// the readout, then BREAK, then the wordmark, then finally the status
	// word clipped to the terminal. LIVE must survive to the last rung —
	// "is it playing" is the one thing the header owes the listener.
	for _, cut := range [][2]string{
		{word + sub, right},
		{word, right},
		{word, statusR + breakChip},
		{word, statusR},
		{"", statusR},
	} {
		if lipgloss.Width(cut[0])+lipgloss.Width(cut[1])+1 <= m.w {
			return lrRow(cut[0], cut[1]+" ", m.w)
		}
	}
	return clip(statusR, m.w)
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

// hasColor reports whether c carries a real color. lipgloss getters return
// NoColor{} — never nil — for "unset", so both answers mean "no color here".
func hasColor(c lipgloss.TerminalColor) bool {
	if c == nil {
		return false
	}
	_, none := c.(lipgloss.NoColor)
	return !none
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
	if fg := s.GetForeground(); hasColor(fg) {
		s = s.Foreground(scale(fg))
	}
	if bg := s.GetBackground(); hasColor(bg) {
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
	if !hasColor(base) || since < 0 || since >= 2*time.Second {
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

// footerItem is one key chip and its label. prio orders the labels for
// narrow terminals: 0 drops first. State labels outrank navigation labels —
// "100%" and "loved" tell you something a key legend doesn't.
type footerItem struct {
	key, label string
	prio       int
}

func (m Model) footerItems() []footerItem {
	item := func(k, l string) footerItem { return footerItem{k, l, 1} }
	if m.themeOpen {
		return []footerItem{item("J/K", "preview"), item("ENTER", "apply"), item("ESC", "cancel")}
	}
	if m.history {
		switch m.historyView {
		case historyLoved:
			if m.libraryFind {
				return []footerItem{item("TYPE", "filter"), item("ENTER", "done"), item("ESC", "done")}
			}
			return []footerItem{item("J/K", "move"), item("/", "find"), item("ENTER", "similar"), item("R", "station"), item("X", "remove"), item("TAB", ""), item("ESC", "")}
		case historyStations:
			if m.savedFind {
				return []footerItem{item("TYPE", "filter"), item("ENTER", "done"), item("ESC", "done")}
			}
			return []footerItem{item("J/K", "move"), item("/", "find"), item("ENTER", "tune"), item("X", "remove"), item("1-9", "preset"), item("TAB", ""), item("ESC", "")}
		default:
			return []footerItem{item("J/K", "scroll"), item("TAB", "next view"), item("ESC", "close")}
		}
	}
	if m.prompt {
		return []footerItem{item("ENTER", "tune"), item("ESC", "cancel")}
	}
	if m.volumeOpen {
		return []footerItem{item("LEFT/RIGHT", "adjust"), item("V", "close")}
	}
	if m.ph == phDead {
		// Space/l/f// do nothing while the backend is dead; the strip shows
		// only the keys that still work.
		return []footerItem{item("H", "library"), item("T", "theme"), footerItem{"Q", "quit", 0}}
	}
	save := "save"
	if slot := m.currentSlot(); slot > 0 {
		save = fmt.Sprintf("preset %d", slot)
	}
	love := "love"
	if m.lovedTrack {
		love = "loved"
	}
	skip := "skip"
	if m.ph == phOffline {
		skip = "retry"
	}
	return []footerItem{
		{"SPACE", skip, 2}, {"L", love, 3}, {"F", save, 3},
		{"/", "discover", 1}, {"H", "library", 1}, {"V", fmt.Sprintf("%d%%", m.volume), 3},
		{"T", "theme", 1}, {"Q", "quit", 0},
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

	render := func(showLabel []bool, sep string) string {
		var b strings.Builder
		b.WriteString(fill.Render(" "))
		for i, it := range items {
			if i > 0 {
				b.WriteString(fill.Render(sep))
			}
			keyStyle := key
			if m.lovedTrack && it.key == "L" {
				keyStyle = keyStyle.Foreground(m.th.Love.GetForeground())
			}
			b.WriteString(keyStyle.Render(" " + it.key + " "))
			if showLabel[i] && it.label != "" {
				b.WriteString(label.Render(" " + it.label))
			}
		}
		return b.String()
	}
	fits := func(line string) bool { return lipgloss.Width(line) <= m.w }

	show := make([]bool, len(items))
	for i := range show {
		show[i] = true
	}
	allOff := func() {
		for i := range show {
			show[i] = false
		}
	}
	line := render(show, "  ")
	if !fits(line) {
		line = render(show, " ")
		// Narrow the gap between chips first; then labels come off one at
		// a time, least important first (ties drop rightmost).
		for !fits(line) {
			victim := -1
			for i, it := range items {
				if show[i] && it.label != "" && (victim < 0 || it.prio <= items[victim].prio) {
					victim = i
				}
			}
			if victim < 0 {
				break
			}
			show[victim] = false
			line = render(show, " ")
		}
	}
	if !fits(line) {
		// Labels all gone and chips still wide: keys alone, tight spacing,
		// then a bare truncation as the last resort.
		allOff()
		line = render(show, "  ")
		if !fits(line) {
			line = render(show, " ")
		}
		if !fits(line) {
			keys := make([]string, len(items))
			for i, it := range items {
				keys[i] = it.key
			}
			line = key.Render(runewidth.Truncate(" "+strings.Join(keys, " "), m.w, ""))
		}
	}
	if gap := m.w - lipgloss.Width(line); gap > 0 {
		line += fill.Render(strings.Repeat(" ", gap))
	}
	return line
}

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

// themeRows is a small, modal finish selector. Moving the cursor previews the
// complete palette immediately; the footer makes the Enter/Escape transaction
// explicit.
func (m Model) themeRows(iw, budget int) []string {
	choices := ThemeChoices()
	// The framed panel is six chrome rows on top of the choices; switching
	// to it a row early used to cost 100x16 its bottom border.
	if iw < 50 || budget < len(choices)+6 {
		rows := []string{m.th.Accent.Bold(true).Render(
			runewidth.Truncate("THEME", iw, m.th.G.Ellipsis))}
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

// The faceplate's fixed row budget, broken down the way the panel actually
// spends it rather than as one guessed constant — the old receiverFixedRows
// pretended the meter's first two rows were free, which let View's
// truncation silently eat the status row and bottom frame on short
// terminals.
const (
	// Chrome around the bays: top rule, status readout, bottom rule.
	receiverChromeRows = 3
	// Left bay: label, title, artist, gap, BROADCAST, station, gap.
	receiverLeftRows = 7
	// Left bay addition when the station carries tags: GENRE, tags, gap.
	receiverGenreRows = 3
	// Right bay besides the meter: SIGNAL label, LOW/HIGH, gap,
	// STATION MEMORY, band, digits, legend — plus one when the density
	// strip shows and 2*perChannel for the meter itself.
	receiverRightRows = 7
	// The two-bay faceplate needs a left bay that reads as a panel, not a
	// sliver: 74 inner columns is a 78-wide terminal, so 80x24 — the most
	// common terminal there is — gets the real receiver.
	receiverMinInner = 74
)

// maxReceiverPad caps the blank rows inside the panel. Past this the
// faceplate stops reading as a machined object and starts reading as a box
// someone dragged the corner of. Height beyond this is deliberately left as
// margin around a well-proportioned instrument.
const maxReceiverPad = 3

// receiverLayout decides whether the faceplate fits and how it spends the
// height it's given. Extra rows go to the signal bay first, since the meter
// is the one part of the panel that's actually alive, then to padding.
//
// This is why the panel used to sit at a fixed ten rows and float in a sea
// of black on a tall terminal: it never asked how much room it had. Note
// that filling the terminal completely is not the goal — a radio faceplate
// stretched down eighty rows would be worse than the original problem. The
// panel grows to a substantial size and then centers.
func receiverLayout(contentH, leftRows, rightFixed int) (perChannel, pad int, ok bool) {
	need := func(p int) int { return receiverChromeRows + maxI(leftRows, rightFixed+2*p) }
	if need(1) > contentH {
		return 1, 0, false
	}
	// Each extra per-channel row costs two rows of panel: both channels
	// grow together. Keep one row in reserve so the panel never reaches
	// the edge.
	perChannel = 1
	for p := 2; p <= maxWaveRows; p++ {
		if need(p)+1 <= contentH {
			perChannel = p
		}
	}
	pad = minI(maxI(contentH-need(perChannel)-1, 0), maxReceiverPad)
	return perChannel, pad, true
}

// receiverMetrics measures the faceplate's bays as they stand: the left
// bay's fixed rows (taller when the station's tags give the GENRE block
// something to say) and the right bay's fixed rows (taller when listening
// history gives the density strip something to draw — judged from the
// totals, not the bucketed strip, which is sized by a width that depends on
// this very decision).
func (m Model) receiverMetrics() (iw, leftRows, rightFixed int) {
	iw = m.innerWidth()
	leftW, _, _ := receiverColumns(iw)
	leftRows = receiverLeftRows
	if stationTags(&m.st, leftW, m.th.G.Ellipsis) != "" {
		leftRows += receiverGenreRows
	}
	rightFixed = receiverRightRows
	for _, d := range m.listenTotals {
		if d > 0 {
			rightFixed++
			break
		}
	}
	return iw, leftRows, rightFixed
}

// receiverShape reports whether the terminal gets the two-bay faceplate or
// the compact stack. One decision, shared by the render path and the wave
// sizing, so the meter is never laid out for a panel that isn't drawn.
func (m Model) receiverShape() bool {
	iw, leftRows, rightFixed := m.receiverMetrics()
	if iw < receiverMinInner || m.h < 14 {
		return false
	}
	_, _, ok := receiverLayout(m.contentHeight(), leftRows, rightFixed)
	return ok
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
	if bg := m.th.PanelFill.GetBackground(); hasColor(bg) {
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
	if width <= 0 {
		return ""
	}
	// The cell owns its width: a caption longer than the bay shrinks to fit
	// rather than pushing the right border past the chassis edge.
	if lipgloss.Width(content) > width {
		content = ansi.Truncate(content, width, "") + "\x1b[0m"
	}
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
	if idle && m.motion() {
		markerStyle = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
		if bg := th.Accent.GetBackground(); hasColor(bg) {
			markerStyle = markerStyle.Background(bg)
		}
	}
	// Love lands on the dial: one coral pulse of the needle, seated on the
	// same surface as its neighbors.
	if m.lovedTrack && m.now.Sub(m.loveAt) < 600*time.Millisecond {
		markerStyle = th.Love
		if bg := th.Accent.GetBackground(); hasColor(bg) {
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
		if bg := th.Accent.GetBackground(); hasColor(bg) {
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

// motion reports whether animated effects run. Reduced motion keeps every
// instrument but holds them still: no boot sequence, no decrypt scramble,
// no static collapse, no scrolling titles, no blinking or breathing.
func (m Model) motion() bool { return !m.reduceMotion }

// typedText renders a detail line on the typewriter's clock — or the full
// text at once under reduced motion.
func (m Model) typedText(text string) string {
	if m.reduceMotion {
		return text
	}
	return m.tw.RenderOver(m.now, text)
}

// marqueeText scrolls an overlong title once per the marquee contract —
// or truncates with an ellipsis under reduced motion.
func (m Model) marqueeText(text string, width int, anchor time.Time) string {
	if m.reduceMotion {
		return runewidth.Truncate(text, width, m.th.G.Ellipsis)
	}
	return marquee(text, width, m.now, anchor, m.th.G.Ellipsis)
}

// cursorGlyph is the prompt/find cursor: on the blink-off beat (never under
// reduced motion) it's a space, otherwise the block.
func (m Model) cursorGlyph() string {
	if m.motion() && (m.now.UnixMilli()/530)%2 == 1 {
		return " "
	}
	return m.th.G.Cursor
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

// --- boot: the wordmark resolves out of glyph noise over a growing rule ---

func (m Model) bootView(iw int, pad string, p float64) string {
	ease := 1 - math.Pow(1-p, 3)
	ruleW := int(ease * float64(iw))
	if ruleW < 1 {
		ruleW = 1
	}
	ruleLeft := (iw - ruleW) / 2
	rule := strings.Repeat(" ", ruleLeft) + m.ruleLine(ruleW)

	// The letterspaced wordmark is 13 columns; a terminal narrower than
	// that gets the plain mark rather than a clipped scramble.
	mark := letterspace(wordmark)
	if runewidth.StringWidth(mark) > iw {
		mark = wordmark
	}
	markRunes := []rune(mark)
	shown := 0
	if p > 0.25 {
		shown = int((p - 0.25) / 0.75 * float64(len(markRunes)+1))
	}
	if shown > len(markRunes) {
		shown = len(markRunes)
	}
	markLeft := (iw - runewidth.StringWidth(mark)) / 2
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

	// Assemble as rows, clip to the terminal, cap to its height: the old
	// trailing newline could paint a line past the bottom edge.
	top := maxI(0, (m.h-3)/3)
	rows := make([]string, 0, top+2)
	for i := 0; i < top; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, pad+mb.String(), pad+rule)
	for i, r := range rows {
		rows[i] = clip(r, m.w)
	}
	if len(rows) > m.h {
		rows = rows[:m.h]
	}
	return strings.Join(rows, "\n")
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
		return clip(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// clip is the escape hatch for the pathologically narrow end: most rows
// negotiate their width up front, but rows assembled from fixed furniture
// (prompt chevrons, status glyphs, find prefixes) can still outgrow a
// handful of columns. ansi.Truncate is sequence-aware, so a styled row
// cut mid-segment keeps its SGR runs well-formed.
func clip(line string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(line) <= w {
		return line
	}
	return ansi.Truncate(line, w, "") + "\x1b[0m"
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
