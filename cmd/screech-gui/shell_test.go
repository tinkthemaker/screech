//go:build windows

package main

// Placement maths, off Win32: fake window rect, fake monitors, recorded
// saves. The cases cover the plan's four plus the owner's three.

import (
	"fmt"
	"testing"

	"github.com/tinkthemaker/screech/internal/core"
)

type fakeOps struct {
	r       rect
	scale   float64
	regions []bool
}

func (f *fakeOps) Rect() (rect, bool) { return f.r, true }
func (f *fakeOps) SetRect(x, y, w, h int) {
	f.r = rect{x, y, x + w, y + h}
}
func (f *fakeOps) Scale() float64 { return f.scale }
func (f *fakeOps) SetRegion(w, h int, scale float64, unfolded bool) {
	if w != f.r.width() || h != f.r.height() || scale != f.scale {
		panic("region does not match resized window")
	}
	f.regions = append(f.regions, unfolded)
}

func TestWindowOutlineFollowsFaceCorners(t *testing.T) {
	for _, tc := range []struct {
		h        int
		scale    float64
		unfolded bool
		corners  [4]w32Point
	}{
		{128, 1, false, [4]w32Point{{0, 48}, {480, 6}, {466, 128}, {0, 80}}},
		{428, 1, true, [4]w32Point{{0, 48}, {480, 6}, {474, 428}, {0, 380}}},
		{192, 1.5, false, [4]w32Point{{0, 72}, {720, 9}, {699, 192}, {0, 120}}},
	} {
		w := round(480 * tc.scale)
		pts := windowOutline(w, tc.h, tc.scale, tc.unfolded)
		got := [4]w32Point{pts[0], pts[33], pts[50], pts[67]}
		if got != tc.corners {
			t.Errorf("outline %dx%d unfolded=%v: corners %v, want %v", w, tc.h, tc.unfolded, got, tc.corners)
		}
	}
}

type fakeMons struct {
	areas   []rect
	primary rect
}

func (m fakeMons) WorkAreaAt(x, y int) (rect, bool) {
	for _, a := range m.areas {
		if a.contains(x, y) {
			return a, true
		}
	}
	return rect{}, false
}
func (m fakeMons) Primary() rect { return m.primary }

func testShell(ops hwndOps, m monitors, saved core.WindowPlacement) (*shell, *[]core.WindowPlacement) {
	var saves []core.WindowPlacement
	return &shell{
		self:  func() (hwndOps, bool) { return ops, true },
		mons:  m,
		place: func() core.WindowPlacement { return saved },
		save: func(p core.WindowPlacement) {
			saves = append(saves, p)
		},
		unfoldTop: sentinelY,
		logf:      func(string, ...any) {},
	}, &saves
}

// A quit must say which path took it — the first reason inside the Once
// wins, whether Q raced a taskbar close or not.
func TestQuitLogsFirstReason(t *testing.T) {
	var lines []string
	sh := &shell{
		logf:      func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
		place:     func() core.WindowPlacement { return core.WindowPlacement{} },
		save:      func(core.WindowPlacement) {},
		self:      func() (hwndOps, bool) { return nil, false },
		unfoldTop: sentinelY,
	}
	sh.Quit()
	sh.doBeforeClose("window closed (Alt+F4 or taskbar)")
	if len(lines) != 1 || lines[0] != "quit: q" {
		t.Fatalf("quit lines = %v, want [quit: q]", lines)
	}
}

func TestUnfoldNearBottomGrowsUp(t *testing.T) {
	// Work area bottom 1040; strip at y=1000. Unfold wants 428 tall:
	// 388 px of overflow, so the window climbs to y=612. Folding (no
	// drag) drops it back to 1000.
	ops := &fakeOps{r: rect{100, 1000, 580, 1128}, scale: 1}
	s, saves := testShell(ops, fakeMons{areas: []rect{{0, 0, 1920, 1040}}}, core.WindowPlacement{})

	s.SetUnfolded(true)
	if ops.r != (rect{100, 612, 580, 1040}) {
		t.Fatalf("unfolded rect %+v, want {100 612 580 1040}", ops.r)
	}
	s.SetUnfolded(false)
	if ops.r != (rect{100, 1000, 580, 1128}) {
		t.Fatalf("folded rect %+v, want {100 1000 580 1128}", ops.r)
	}
	if len(ops.regions) != 2 || !ops.regions[0] || ops.regions[1] {
		t.Fatalf("regions = %v, want unfolded then folded", ops.regions)
	}
	want := []core.WindowPlacement{
		{X: 100, Y: 612, Unfolded: true, Saved: true},
		{X: 100, Y: 1000, Unfolded: false, Saved: true},
	}
	if len(*saves) != 2 || (*saves)[0] != want[0] || (*saves)[1] != want[1] {
		t.Fatalf("saves %+v, want %+v", *saves, want)
	}
}

func TestUnfoldMidScreenKeepsTop(t *testing.T) {
	// 200 + 428 = 628 sits well under a 1040 work area: the top stays.
	ops := &fakeOps{r: rect{100, 200, 580, 328}, scale: 1}
	s, _ := testShell(ops, fakeMons{areas: []rect{{0, 0, 1920, 1040}}}, core.WindowPlacement{})

	s.SetUnfolded(true)
	if ops.r != (rect{100, 200, 580, 628}) {
		t.Fatalf("unfolded rect %+v, want {100 200 580 628}", ops.r)
	}
	s.SetUnfolded(false)
	if ops.r != (rect{100, 200, 580, 328}) {
		t.Fatalf("folded rect %+v, want {100 200 580 328}", ops.r)
	}
}

func TestUnfoldNearBottomGrowsUpAt150(t *testing.T) {
	// The same geometry at 150%: 428 logical is 642 physical. Work area
	// bottom 1560; top 1500 → 582 overflow → top 918, back to 1500.
	ops := &fakeOps{r: rect{150, 1500, 870, 1692}, scale: 1.5}
	s, _ := testShell(ops, fakeMons{areas: []rect{{0, 0, 2880, 1560}}}, core.WindowPlacement{})

	s.SetUnfolded(true)
	if ops.r != (rect{150, 918, 870, 1560}) {
		t.Fatalf("unfolded rect %+v, want {150 918 870 1560}", ops.r)
	}
	s.SetUnfolded(false)
	if ops.r != (rect{150, 1500, 870, 1692}) {
		t.Fatalf("folded rect %+v, want {150 1500 870 1692}", ops.r)
	}
}

func TestFoldAfterDragKeepsTop(t *testing.T) {
	// Unfold climbed the window to 612, then the user dragged it to
	// y=400: folding keeps the dragged top and forgets the shift.
	ops := &fakeOps{r: rect{100, 1000, 580, 1128}, scale: 1}
	s, saves := testShell(ops, fakeMons{areas: []rect{{0, 0, 1920, 1040}}}, core.WindowPlacement{})

	s.SetUnfolded(true)
	ops.r.top, ops.r.bottom = 400, 828 // dragged
	s.SetUnfolded(false)
	if ops.r != (rect{100, 400, 580, 528}) {
		t.Fatalf("folded-after-drag rect %+v, want {100 400 580 528}", ops.r)
	}
	if got := (*saves)[len(*saves)-1]; got.Y != 400 {
		t.Fatalf("saved placement %+v, want y=400", got)
	}
}

func TestUnfoldClampsToWorkTop(t *testing.T) {
	// A work area shorter than the console: overflow would lift the top
	// negative, so it clamps to the work area's top instead.
	ops := &fakeOps{r: rect{100, 100, 580, 228}, scale: 1}
	s, _ := testShell(ops, fakeMons{areas: []rect{{0, 0, 1920, 300}}}, core.WindowPlacement{})

	s.SetUnfolded(true)
	if ops.r.top != 0 || ops.r.height() != 428 {
		t.Fatalf("unfolded rect %+v, want top=0 h=428", ops.r)
	}
	// Only the clamped 100 px of shift comes back on fold.
	s.SetUnfolded(false)
	if ops.r.top != 100 {
		t.Fatalf("folded top %d, want 100", ops.r.top)
	}
}

func TestPlacementOffScreenCentres(t *testing.T) {
	// A placement saved on a monitor that no longer exists: the top-bar
	// point finds no work area, so the window centres on the primary.
	ops := &fakeOps{r: rect{720, 456, 1200, 584}, scale: 1}
	m := fakeMons{
		areas:   []rect{{0, 0, 1920, 1040}},
		primary: rect{0, 0, 1920, 1040},
	}
	s, _ := testShell(ops, m, core.WindowPlacement{X: -5000, Y: -5000, Unfolded: false, Saved: true})
	s.restore()
	if ops.r != (rect{720, 456, 1200, 584}) {
		t.Fatalf("restored rect %+v, want centred {720 456 1200 584}", ops.r)
	}
}

func TestPlacementRestoredOnScreen(t *testing.T) {
	// A placement whose top bar is still on a monitor restores as-is,
	// including the fold height.
	ops := &fakeOps{r: rect{720, 456, 1200, 584}, scale: 1}
	m := fakeMons{areas: []rect{{0, 0, 1920, 1040}}, primary: rect{0, 0, 1920, 1040}}
	s, _ := testShell(ops, m, core.WindowPlacement{X: 300, Y: 200, Unfolded: true, Saved: true})
	s.restore()
	if ops.r != (rect{300, 200, 780, 628}) {
		t.Fatalf("restored rect %+v, want {300 200 780 628}", ops.r)
	}
	if !s.unfolded {
		t.Fatal("unfolded state not restored")
	}
}
