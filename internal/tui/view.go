package tui

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const wordmark = "S C R E E C H"

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

	idle := m.idle()
	compact := m.h < 14
	tiny := m.h < 9

	var rows []string
	add := func(s string) { rows = append(rows, pad+s) }
	blank := func() {
		if !compact {
			rows = append(rows, "")
		}
	}

	if !tiny {
		add(m.headerRow(iw, idle))
		add(m.th.Dim.Render(strings.Repeat(m.th.G.Rule, iw)))
		blank()
	}
	add(m.heroRow(iw, idle))
	if tiny && m.prompt {
		add(m.promptRow(iw))
	} else {
		add(m.trackRow(iw, idle))
	}
	blank()
	if iw >= 28 && !compact {
		add(m.wave.Render(m.th))
		add(m.bandRow(iw, idle))
	}
	if !tiny {
		if m.prompt {
			add(m.promptRow(iw))
		} else {
			add(m.reasonRow(iw))
		}
	}

	// Vertical placement: content sits in the upper third, footer pinned.
	content := len(rows)
	topPad := maxI(0, (m.h-2-content)/3)
	var b strings.Builder
	for i := 0; i < topPad; i++ {
		b.WriteString("\n")
	}
	b.WriteString(strings.Join(rows, "\n"))
	fill := m.h - 1 - topPad - content
	for i := 0; i < fill; i++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(pad + m.footerRow(iw))
	return b.String()
}

// --- rows ---

func (m Model) headerRow(iw int, idle bool) string {
	markStyle := m.th.Accent
	if idle {
		markStyle = m.th.Dim
	}
	leftS := markStyle.Render(wordmark)

	right := ""
	switch {
	case m.seeking:
		right = "seeking" + m.th.G.Ellipsis
	case m.syncing:
		right = "syncing directory" + m.th.G.Ellipsis
	case m.ph == phBuffer:
		right = "buffering" + m.th.G.Ellipsis
	case m.ph == phTune:
		right = "tuning" + m.th.G.Ellipsis
	case m.ph == phPlay && m.haveSt:
		right = m.readout()
	}
	if m.suspect && m.ph == phPlay {
		right += "  break?"
	}
	return lrRow(leftS, m.th.Dim.Render(right), iw)
}

func (m Model) readout() string {
	parts := []string{}
	if c := strings.ToLower(m.st.Codec); c != "" {
		parts = append(parts, c)
	}
	if m.st.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%dk", m.st.Bitrate))
	}
	if !m.playStart.IsZero() {
		parts = append(parts, fmtElapsed(m.now.Sub(m.playStart)))
	}
	return strings.Join(parts, " "+m.th.G.Dot+" ")
}

func (m Model) heroRow(iw int, idle bool) string {
	if !m.haveSt {
		text := "no station"
		switch {
		case m.syncing:
			text = "consulting the aether" + m.th.G.Ellipsis
		case m.prompt:
			text = m.th.G.Dot + " " + m.th.G.Dot + " " + m.th.G.Dot
		}
		return m.th.Dim.Render(runewidth.Truncate(text, iw, m.th.G.Ellipsis))
	}
	if m.decrypt.Active(m.now) {
		return m.decrypt.Render(m.now, m.th, iw)
	}
	style := m.th.Bright
	if idle {
		style = m.th.Dim
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

	// New titles step dim -> mid; reads as a fade.
	style := m.th.Mid
	if age := m.now.Sub(m.trackAt); age < 300*time.Millisecond {
		style = m.th.Dim
	}
	if idle {
		style = m.th.Mid // the track title is what idle mode keeps readable
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
			heart = m.th.Dim.Render(m.th.G.Heart)
		}
	}
	return lrRow(style.Render(text), heart, iw)
}

// bandRow is the dial: dim band, mid ticks where presets live, accent marker
// at the playing station's position.
func (m Model) bandRow(iw int, idle bool) string {
	pos := clampF(m.dial.Pos, 0, 1)
	col := int(math.Round(pos * float64(iw-1)))
	markerStyle := m.th.Accent
	if idle {
		markerStyle = m.th.BreatheStyle(m.now.Sub(m.start).Seconds())
	}
	cells := make([]string, iw)
	dimBand := m.th.Dim.Render(m.th.G.Band)
	for i := range cells {
		cells[i] = dimBand
	}
	for _, uuid := range m.presets {
		t := int(math.Round(stationDialPos(uuid) * float64(iw-1)))
		if t >= 0 && t < iw {
			cells[t] = m.th.Mid.Render(m.th.G.Tick)
		}
	}
	if col >= 0 && col < iw {
		cells[col] = markerStyle.Render(m.th.G.Marker)
	}
	return strings.Join(cells, "")
}

func (m Model) reasonRow(iw int) string {
	if m.fatal != "" {
		return m.th.Mid.Render(runewidth.Truncate(m.fatal, iw, m.th.G.Ellipsis))
	}
	if !m.haveSt {
		return ""
	}
	// A suspected ad break earns a contextual hint: this is the moment the
	// user wants the skip and shouldn't have to remember which key it is.
	if m.suspect && m.ph == phPlay {
		hint := "break? " + m.th.G.Dot + " space hops away"
		return m.th.Dim.Render(runewidth.Truncate(hint, iw, m.th.G.Ellipsis))
	}
	return m.th.Dim.Render(runewidth.Truncate(m.tw.Render(m.now), iw, m.th.G.Ellipsis))
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

func (m Model) footerRow(iw int) string {
	d := " " + m.th.G.Dot + " "
	if m.prompt {
		keys := "enter tune" + d + "esc cancel"
		return m.th.Dim.Render(runewidth.Truncate(keys, iw, m.th.G.Ellipsis))
	}
	save := "f save"
	if m.currentSlot() > 0 {
		save = "f unsave"
	}
	keys := "space next" + d + "l love" + d + save + d + "/ seed" + d + "q quit"
	if runewidth.StringWidth(keys) > iw {
		keys = runewidth.Truncate("space"+d+"l"+d+"f"+d+"q", iw, m.th.G.Ellipsis)
	}
	noteRoom := iw - runewidth.StringWidth(keys) - 2
	note := ""
	if noteRoom > 4 {
		note = runewidth.Truncate(m.note, noteRoom, m.th.G.Ellipsis)
	}
	return lrRow(m.th.Dim.Render(keys), m.th.Dim.Render(note), iw)
}

// --- boot: rules draw outward, wordmark letterspaces in ---

func (m Model) bootView(iw int, pad string, p float64) string {
	ease := 1 - math.Pow(1-p, 3)
	ruleW := int(ease * float64(iw))
	if ruleW < 1 {
		ruleW = 1
	}
	ruleLeft := (iw - ruleW) / 2
	rule := strings.Repeat(" ", ruleLeft) + m.th.Dim.Render(strings.Repeat(m.th.G.Rule, ruleW))

	markRunes := []rune(wordmark)
	shown := 0
	if p > 0.25 {
		shown = int((p - 0.25) / 0.75 * float64(len(markRunes)+1))
	}
	if shown > len(markRunes) {
		shown = len(markRunes)
	}
	markLeft := (iw - runewidth.StringWidth(wordmark)) / 2
	mark := strings.Repeat(" ", maxI(0, markLeft)) + m.th.Accent.Render(string(markRunes[:shown]))

	top := maxI(0, (m.h-3)/3)
	var b strings.Builder
	for i := 0; i < top; i++ {
		b.WriteString("\n")
	}
	b.WriteString(pad + mark + "\n")
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

// heroText letterspaces the station name if it fits; otherwise falls back to
// plain caps, then truncation. The hero must never wrap.
func heroText(name string, iw int, ellipsis string) string {
	up := strings.ToUpper(strings.TrimSpace(name))
	spaced := letterspace(up)
	if runewidth.StringWidth(spaced) <= iw {
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
