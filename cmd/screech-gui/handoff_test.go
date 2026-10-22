//go:build windows

package main

import (
	"testing"
	"time"
)

type fakeWindow struct {
	iconic   bool
	restores int
	fores    int
}

func (w *fakeWindow) IsIconic() bool { return w.iconic }
func (w *fakeWindow) Restore()       { w.restores++ }
func (w *fakeWindow) Foreground()    { w.fores++ }

type fakeFinder struct {
	win   *fakeWindow
	after int // succeeds on poll N (1-based)
	calls int
}

func (f *fakeFinder) Find(string) (foundWindow, bool) {
	f.calls++
	if f.calls < f.after {
		return nil, false
	}
	return f.win, true
}

func TestHandoffRetriesBeforeBlamingTerminal(t *testing.T) {
	// A window still starting gets the full wait: the finder comes up
	// empty four times, then the window appears on the fifth poll.
	win := &fakeWindow{iconic: true}
	f := &fakeFinder{win: win, after: 5}
	if !handoff(f, 3*time.Second) {
		t.Fatal("handoff gave up before the window appeared")
	}
	if f.calls != 5 {
		t.Fatalf("finder polled %d times, want 5", f.calls)
	}
	if win.restores != 1 || win.fores != 1 {
		t.Fatalf("restored %d, foregrounded %d — want 1 each", win.restores, win.fores)
	}
}

func TestHandoffTimesOut(t *testing.T) {
	f := &fakeFinder{win: &fakeWindow{}, after: 1 << 30}
	if handoff(f, 250*time.Millisecond) {
		t.Fatal("handoff found a window that never appears")
	}
	if f.calls < 2 {
		t.Fatalf("only %d polls in 250ms", f.calls)
	}
}
