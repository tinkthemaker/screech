//go:build windows

package main

// The Win32 surface the shell needs, behind interfaces so the placement
// and handoff logic tests without a window. Everything here works in
// physical virtual-screen pixels — Wails' own SetPos adds the monitor's
// work-area origin while Pos() reports absolute coordinates, an
// asymmetry that makes it useless for placement maths.

import (
	"math"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowClass is the Wails window class winres/winres.json's
// WindowClassName sets — how our own HWND is found.
const windowClass = "ScreechLCARS"

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procIsIconic              = user32.NewProc("IsIconic")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procMonitorFromPoint      = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW       = user32.NewProc("GetMonitorInfoW")
	procGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	procSetWindowRgn          = user32.NewProc("SetWindowRgn")
	gdi32                     = windows.NewLazySystemDLL("gdi32.dll")
	procCreatePolygonRgn      = gdi32.NewProc("CreatePolygonRgn")
	procDeleteObject          = gdi32.NewProc("DeleteObject")
)

const (
	swRestore            = 9
	swpNoZOrder          = 0x4
	swpNoActivate        = 0x10
	monitorDefaultToNull = 0
	monitorPrimary       = 1 // MONITOR_DEFAULTTOPRIMARY
)

type w32Rect struct{ Left, Top, Right, Bottom int32 }
type w32Point struct{ X, Y int32 }
type w32MonitorInfo struct {
	CbSize  uint32
	Monitor w32Rect
	Work    w32Rect
	Flags   uint32
}

// messageBox puts up the only dialog the shell ever shows: an error the
// window can't display for itself yet.
func messageBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString("Screech")
	windows.MessageBox(0, t, c,
		windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}

func currentPID() uint32 { return uint32(os.Getpid()) }

// findWindow returns the HWND of the top window of our class. A pid of
// 0 accepts any owner — the handoff's target is the already-running
// instance — otherwise the window must belong to that process (the
// shell's own HWND lookup).
func findWindow(class string, pid uint32) uintptr {
	c, _ := windows.UTF16PtrFromString(class)
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(c)), 0)
	if hwnd == 0 {
		return 0
	}
	if pid == 0 {
		return hwnd
	}
	var wp uint32
	procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
	if wp != pid {
		return 0
	}
	return hwnd
}

// ---- handoff --------------------------------------------------------------

// w32Window is a HWND that can be woken and raised for the handoff.
type w32Window uintptr

func (w w32Window) IsIconic() bool {
	r, _, _ := procIsIconic.Call(uintptr(w))
	return r != 0
}
func (w w32Window) Restore()    { procShowWindow.Call(uintptr(w), swRestore) }
func (w w32Window) Foreground() { procSetForegroundWindow.Call(uintptr(w)) }

// w32Finder finds the running instance's window by class alone — the
// point is that it belongs to a *different* process.
type w32Finder struct{}

func (f w32Finder) Find(class string) (foundWindow, bool) {
	h := findWindow(class, 0)
	if h == 0 {
		return nil, false
	}
	return w32Window(h), true
}

// ---- monitors --------------------------------------------------------------

type w32Monitors struct{}

// WorkAreaAt is MonitorFromPoint with MONITOR_DEFAULTTONULL: off-screen
// is a "no", never a guess at the nearest monitor.
func (w32Monitors) WorkAreaAt(x, y int) (rect, bool) {
	// MonitorFromPoint takes its POINT by value: pack x and y into one
	// uintptr — a pointer here reads the address as coordinates.
	pt := uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32
	mon, _, _ := procMonitorFromPoint.Call(pt, monitorDefaultToNull)
	if mon == 0 {
		return rect{}, false
	}
	return monitorWorkArea(mon), true
}

func (w32Monitors) Primary() rect {
	mon, _, _ := procMonitorFromPoint.Call(0, monitorPrimary)
	if mon == 0 {
		// No monitors is not a real desktop; still never return zeroes.
		return rect{0, 0, 1920, 1040}
	}
	return monitorWorkArea(mon)
}

func monitorWorkArea(mon uintptr) rect {
	var mi w32MonitorInfo
	mi.CbSize = uint32(unsafe.Sizeof(mi))
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return rect{int(mi.Work.Left), int(mi.Work.Top), int(mi.Work.Right), int(mi.Work.Bottom)}
}

// ---- hwndOps ----------------------------------------------------------------

// w32Ops drives our own window's rect and scale through Win32.
type w32Ops uintptr

func (o w32Ops) Rect() (rect, bool) {
	var r w32Rect
	ok, _, _ := procGetWindowRect.Call(uintptr(o), uintptr(unsafe.Pointer(&r)))
	if ok == 0 {
		return rect{}, false
	}
	return rect{int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)}, true
}

func (o w32Ops) SetRect(x, y, w, h int) {
	procSetWindowPos.Call(uintptr(o), 0,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		swpNoZOrder|swpNoActivate)
}

func (o w32Ops) Scale() float64 {
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(o))
	if dpi == 0 {
		return 1
	}
	return float64(dpi) / 96
}

// SetRegion clips the frameless HWND to the same outside silhouette as
// the LCARS face: a broad left curve, a small top-right cap, and the
// button or bottom-bar radius at the lower right. Win32 takes ownership
// of the region only when SetWindowRgn succeeds.
func (o w32Ops) SetRegion(w, h int, scale float64, unfolded bool) {
	points := windowOutline(w, h, scale, unfolded)
	if len(points) == 0 {
		return
	}
	region, _, _ := procCreatePolygonRgn.Call(
		uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)), 2) // WINDING
	if region == 0 {
		return
	}
	if ok, _, _ := procSetWindowRgn.Call(uintptr(o), region, 1); ok == 0 {
		procDeleteObject.Call(region)
	}
}

func windowOutline(w, h int, scale float64, unfolded bool) []w32Point {
	if w <= 0 || h <= 0 {
		return nil
	}
	limit := min(w, h) / 2
	left := min(round(48*scale), limit)
	// The ✕ cap at the top right is a 10-px quarter-round; the bar that
	// closes the console is 6, the folded button row's end 14.
	topRight := min(round(10*scale), limit)
	bottomRight := min(round(6*scale), limit)
	if !unfolded {
		bottomRight = min(round(14*scale), limit)
	}
	var points []w32Point
	arc := func(cx, cy, radius int, start, end float64) {
		const steps = 16
		for i := 0; i <= steps; i++ {
			a := start + (end-start)*float64(i)/steps
			points = append(points, w32Point{
				X: int32(cx + round(float64(radius)*math.Cos(a))),
				Y: int32(cy + round(float64(radius)*math.Sin(a))),
			})
		}
	}
	arc(left, left, left, math.Pi, 3*math.Pi/2)
	arc(w-topRight, topRight, topRight, 3*math.Pi/2, 2*math.Pi)
	arc(w-bottomRight, h-bottomRight, bottomRight, 0, math.Pi/2)
	arc(left, h-left, left, math.Pi/2, math.Pi)
	return points
}
