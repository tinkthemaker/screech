//go:build windows

package main

// The shell owns the window's place on screen and its exits. Placement
// maths runs behind monitors/hwndOps so every case — near-bottom grow,
// drag-and-fold, clamp, unplugged monitor — tests without Win32. Quit
// paths funnel through one beforeClose/sync.Once: Wails sends Q,
// Alt+F4, the taskbar's close and runtime.Quit all through
// OnBeforeClose (v2.16's runtime.Quit calls Frontend.Quit, which is
// OnBeforeClose), and shell.Quit runs the same Once before quitting so
// the runner stops exactly once however the window dies.

import (
	"context"
	"math"
	"sync"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/tinkthemaker/screech/internal/app"
	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// The page's two sizes, in logical px; the window's physical height is
// one of these times the monitor's scale.
const (
	stripW    = 480
	stripH    = 128
	consoleH  = 428
	sentinelY = math.MinInt32 // "no unfold top remembered"
)

// rect is a physical-pixel virtual-screen rect.
type rect struct{ left, top, right, bottom int }

func (r rect) width() int  { return r.right - r.left }
func (r rect) height() int { return r.bottom - r.top }
func (r rect) contains(x, y int) bool {
	return x >= r.left && x < r.right && y >= r.top && y < r.bottom
}

// monitors is the piece of Win32 the placement maths needs: the work
// area under a point (MONITOR_DEFAULTTONULL — off-screen is a "no"),
// and the primary work area to centre on when nothing is saved.
type monitors interface {
	WorkAreaAt(x, y int) (rect, bool)
	Primary() rect
}

// hwndOps is the piece of the window the shell drives. self() resolves
// it lazily — the HWND doesn't exist until Wails creates the window.
type hwndOps interface {
	Rect() (rect, bool)
	SetRect(x, y, w, h int)
	Scale() float64
	SetRegion(w, h int, scale float64, unfolded bool)
}

// foundWindow is what the handoff wakes: restore from the tray, then
// bring it forward.
type foundWindow interface {
	IsIconic() bool
	Restore()
	Foreground()
}

type windowFinder interface {
	Find(class string) (foundWindow, bool)
}

type shell struct {
	self  func() (hwndOps, bool)
	mons  monitors
	place func() core.WindowPlacement
	save  func(core.WindowPlacement)

	// mu covers the placement state — SetUnfolded arrives on Wails'
	// binding goroutine while restore and close run on others. self()
	// assumes the caller holds it.
	mu        sync.Mutex
	shift     int  // px the window moved up when it last unfolded
	unfoldTop int  // the top SetWindowPos set at unfold
	unfolded  bool // fold state the shell last drove, for persist

	ctx    context.Context // Wails' context, live from OnStartup
	runner *session.Runner
	rt     *app.Runtime
	hwnd   uintptr
	logf   func(format string, args ...any) // test seam over rt.Log.Linef

	beforeClose sync.Once
	shutdown    sync.Once
}

func newShell(rt *app.Runtime) *shell {
	s := &shell{
		rt:    rt,
		mons:  w32Monitors{},
		place: rt.Core.WindowPlacement,
		save: func(p core.WindowPlacement) {
			if err := rt.Core.SetWindowPlacement(p); err != nil {
				rt.Log.Linef("placement save failed: %v", err)
			}
		},
		unfoldTop: sentinelY,
		logf:      rt.Log.Linef,
	}
	s.self = func() (hwndOps, bool) {
		if s.hwnd == 0 {
			s.hwnd = findWindow(windowClass, currentPID())
		}
		if s.hwnd == 0 {
			return nil, false
		}
		return w32Ops(s.hwnd), true
	}
	return s
}

// SetUnfolded is the gui.Window face: grow to the console or shrink back
// to the strip, in one SetWindowPos, then remember the place.
func (s *shell) SetUnfolded(unfolded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops, ok := s.self()
	if !ok {
		return
	}
	r, ok := ops.Rect()
	if !ok {
		return
	}
	scale := ops.Scale()
	w := r.width()
	top := r.top
	if unfolded {
		newH := round(428 * scale)
		// Grow toward the bottom of the window's monitor work area;
		// cross it and the window climbs back up by the overflow.
		if wa, ok := s.mons.WorkAreaAt(r.left+w/2, r.top+6); ok {
			if over := top + newH - wa.bottom; over > 0 {
				top -= over
				if top < wa.top {
					top = wa.top
				}
			}
		}
		s.shift = r.top - top
		s.unfoldTop = top
		ops.SetRect(r.left, top, w, newH)
		ops.SetRegion(w, newH, scale, true)
	} else {
		newH := round(128 * scale)
		// Fold back down by the shift only when the user hasn't dragged
		// the window since it unfolded.
		if r.top == s.unfoldTop {
			top = r.top + s.shift
		}
		s.shift = 0
		s.unfoldTop = sentinelY
		ops.SetRect(r.left, top, w, newH)
		ops.SetRegion(w, newH, scale, false)
	}
	s.unfolded = unfolded
	s.persist(r.left, top, unfolded)
}

// Quit is the gui.Window face for the page's Q. The Once means a Q at
// the same moment as a taskbar close still stops the runner once.
func (s *shell) Quit() {
	s.doBeforeClose("q")
	if s.ctx != nil {
		wailsruntime.Quit(s.ctx)
	}
}

func (s *shell) onStartup(ctx context.Context) { s.ctx = ctx }

// onDomReady puts the window where it belongs, then shows it — hidden
// until the rect is final, so nothing visibly jumps.
func (s *shell) onDomReady() {
	s.restore()
	if s.ctx != nil {
		wailsruntime.WindowShow(s.ctx)
	}
}

// doBeforeClose is shared by OnBeforeClose and Quit: log which quit got
// here first (a silent exit is undiagnosable otherwise), save the place,
// stop the runner — exactly once.
func (s *shell) doBeforeClose(reason string) {
	s.beforeClose.Do(func() {
		s.logf("quit: %s", reason)
		s.persistCurrent()
		if s.runner != nil {
			s.runner.Stop()
		}
	})
}

// onShutdown closes the runtime — the player, core, lock and log —
// exactly once.
func (s *shell) onShutdown() {
	s.shutdown.Do(func() {
		if s.rt != nil {
			s.rt.Close()
		}
	})
}

// restore puts the saved placement back, or centres on the primary work
// area — whenever nothing is saved, or the saved top bar sits on no
// monitor (an unplugged screen).
func (s *shell) restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops, ok := s.self()
	if !ok {
		return
	}
	saved := s.place()
	scale := ops.Scale()
	w := round(480 * scale)
	h := round(128 * scale)
	s.unfolded = false
	if saved.Unfolded {
		h = round(428 * scale)
		s.unfolded = true
	}
	x, y := saved.X, saved.Y
	// The top bar point, 48 over and 6 down, must land on a monitor.
	if _, on := s.mons.WorkAreaAt(x+round(48*scale), y+round(6*scale)); !saved.Saved || !on {
		wa := s.mons.Primary()
		x = wa.left + (wa.width()-w)/2
		y = wa.top + (wa.height()-h)/2
	}
	s.shift = 0
	s.unfoldTop = sentinelY
	ops.SetRect(x, y, w, h)
	ops.SetRegion(w, h, scale, s.unfolded)
}

func (s *shell) persist(x, y int, unfolded bool) {
	s.save(core.WindowPlacement{X: x, Y: y, Unfolded: unfolded, Saved: true})
}

func (s *shell) persistCurrent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops, ok := s.self()
	if !ok {
		return
	}
	if r, ok := ops.Rect(); ok {
		s.persist(r.left, r.top, s.unfolded)
	}
}

func round(f float64) int { return int(math.Round(f)) }
