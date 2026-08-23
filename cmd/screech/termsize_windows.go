//go:build windows

package main

import "golang.org/x/sys/windows"

func terminalSize() (width, height int, ok bool) {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0, false
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(h, &info); err != nil {
		return 0, 0, false
	}
	w := info.Window.Right - info.Window.Left + 1
	h2 := info.Window.Bottom - info.Window.Top + 1
	return int(w), int(h2), true
}
