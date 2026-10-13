package tui

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
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
