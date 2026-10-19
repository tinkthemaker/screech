package tui

func (m Model) innerWidth() int {
	iw := m.w - 4
	if iw > 92 {
		iw = 92
	}
	if iw < 16 {
		// The 10-column floor only applies while it fits: a narrower
		// terminal gets iw == w rather than rows overflowing the edge.
		iw = maxI(m.w-2, minI(10, m.w))
	}
	return iw
}

// waveRenderWidth follows the active responsive composition. Wide terminals
// give the signal its own instrument bay; compact terminals retain the full
// stacked width.
func (m Model) waveRenderWidth() int {
	iw := m.innerWidth()
	if m.receiverShape() {
		_, _, right := receiverColumns(iw)
		return right
	}
	return iw
}

// waveRenderRows mirrors waveRenderWidth for the vertical axis: the signal
// bay only stretches inside the wide faceplate. The compact stacked layout
// keeps a two-row meter, where it sits directly under the track and has no
// room to grow.
func (m Model) waveRenderRows() int {
	contentH := m.contentHeight()
	if m.receiverShape() {
		_, leftRows, rightFixed := m.receiverMetrics()
		perChannel, _, _ := receiverLayout(contentH, leftRows, rightFixed)
		return perChannel
	}
	if m.innerWidth() >= 28 && m.h >= 14 && contentH >= 8 {
		return stackedWaveRows(contentH)
	}
	return 1
}

// contentHeight is the room between the pinned header and the pinned key
// strip. View computes the same number; both must agree or the wave will be
// sized for a layout the renderer isn't using.
func (m Model) contentHeight() int {
	top := 2 // header bar + rule
	if m.h < 8 {
		top = 0
	}
	return m.h - top - 1
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
	if stationTags(&m.s.Station, leftW, m.th.G.Ellipsis) != "" {
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
