package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

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
	case m.ph == phTune && m.resuming:
		status = "RESUMING" + m.th.G.Ellipsis
	case m.seeking:
		status = "SEEKING" + m.th.G.Ellipsis
	case m.ph == phPaused:
		status = "PAUSED"
		statusStyle = m.th.AccentDim // held, not lit: neither LIVE nor warning
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
	if m.ph == phPaused {
		// Time since the hold, on the same degradation ladder as LIVE's
		// readout — first thing dropped when the chrome runs out of room.
		right += statusStyle.Render("  " + fmtClock(m.now.Sub(m.pausedAt)))
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
	if m.helpOpen || m.infoOpen {
		return []footerItem{item("ESC", "close")}
	}
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
		case historyTaste:
			return []footerItem{item("TAB", "next view"), item("ESC", "close")}
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
		return []footerItem{item("H", "library"), item("T", "theme"), item("?", "help"), footerItem{"Q", "quit", 0}}
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
	items := []footerItem{
		{"SPACE", skip, 2}, {"L", love, 3}, {"F", save, 3},
	}
	if m.haveSt {
		pauseLabel, pausePrio := "pause", 2
		if m.ph == phPaused {
			pauseLabel, pausePrio = "resume", 3
		}
		items = append(items, footerItem{"P", pauseLabel, pausePrio})
	}
	items = append(items,
		footerItem{"/", "discover", 1}, footerItem{"H", "library", 1},
		footerItem{"V", fmt.Sprintf("%d%%", m.volume), 3})
	if !m.sleepAt.IsZero() {
		// Whole minutes while there's one to round up to, seconds for the
		// last stretch — matching the fade the listener hears.
		var text string
		if left := m.sleepAt.Sub(m.now); left >= time.Minute {
			text = fmt.Sprintf("sleep %dm", int(left.Minutes()+0.999))
		} else {
			text = fmt.Sprintf("sleep %ds", int(left.Seconds()))
		}
		items = append(items, footerItem{"Z", text, 3})
	}
	items = append(items, footerItem{"?", "help", 2}, footerItem{"Q", "quit", 0})
	return items
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

func surfaceLR(left, right string, width int, fill lipgloss.Style) string {
	gap := maxI(0, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + fill.Render(strings.Repeat(" ", gap)) + right
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
