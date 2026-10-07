//go:build windows

package tui

import tea "github.com/charmbracelet/bubbletea"

// SIGHUP doesn't exist on Windows; bubbletea's own SIGTERM handling covers
// the console-close cases it can see.
func notifyHangup(p *tea.Program) func() {
	return func() {}
}
