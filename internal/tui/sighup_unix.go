//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
)

// notifyHangup forwards SIGHUP into the program as a hangupMsg: a closed
// terminal kills screech outright otherwise, and the in-flight listen is
// never banked. The returned func releases the signal subscription.
func notifyHangup(p *tea.Program) func() {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		select {
		case <-ch:
			p.Send(hangupMsg{})
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
