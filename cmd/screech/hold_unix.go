//go:build !windows

package main

// On unix the launching shell's window survives the process; nothing to hold.
func holdConsoleOnExit() {}
