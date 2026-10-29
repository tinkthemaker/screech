//go:build windows

package main

// When the single-instance lock says another screech owns the session,
// that other one might be this window still booting — poll for it for a
// few seconds before blaming a terminal. Found means the window is
// restored (if minimized) and brought forward, and this launch exits 0.

import "time"

func handoff(f windowFinder, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if w, ok := f.Find(windowClass); ok {
			if w.IsIconic() {
				w.Restore()
			}
			w.Foreground()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
