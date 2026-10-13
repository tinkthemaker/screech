package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

func themeChoiceIndex(name string) int {
	name = normalizeThemeName(name)
	for i, choice := range themeChoices {
		if choice.ID == name {
			return i
		}
	}
	return 0
}

func (m *Model) previewTheme(name string) {
	m.themeName = normalizeThemeName(name)
	// Previews render under the current daypart: what you see in the picker
	// is what the theme looks like right now, not an abstract noon version.
	m.th = NewDaypartTheme(m.themeName, m.themeAccent, m.themeASCII, m.daypart)
	m.wave.ResetPalette()
}

func (m Model) handleThemeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	choices := ThemeChoices()
	switch key {
	case "ctrl+c", "q":
		m.quitting = true
		m.core.EndListen(time.Now())
		_ = m.pl.Close()
		return m, tea.Quit
	case "esc", "t", "T":
		m.previewTheme(m.themeOriginal)
		m.themeOpen = false
		return m, nil
	case "up", "k", "shift+tab":
		m.themePos = (m.themePos - 1 + len(choices)) % len(choices)
		m.previewTheme(choices[m.themePos].ID)
		return m, nil
	case "down", "j", "tab":
		m.themePos = (m.themePos + 1) % len(choices)
		m.previewTheme(choices[m.themePos].ID)
		return m, nil
	case "enter":
		m.previewTheme(choices[m.themePos].ID)
		if err := m.core.SetTheme(m.themeName); err != nil {
			m.note = "could not save theme " + m.th.G.Dot + " " + err.Error()
			return m, nil
		}
		m.themeOpen = false
		m.setFeedback("THEME  " + choices[m.themePos].Label + " applied")
		return m, nil
	}
	if len(key) == 1 && key[0] >= '1' && int(key[0]-'1') < len(choices) {
		m.themePos = int(key[0] - '1')
		m.previewTheme(choices[m.themePos].ID)
	}
	return m, nil
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
