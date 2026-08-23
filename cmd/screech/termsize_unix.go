//go:build !windows

package main

import "golang.org/x/sys/unix"

func terminalSize() (width, height int, ok bool) {
	ws, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}
