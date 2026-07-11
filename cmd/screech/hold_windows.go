//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
)

// holdConsoleOnExit keeps the console window open when screech was launched
// by double-click: in that case the console belongs to this process alone and
// would vanish with it, taking the error message along.
func holdConsoleOnExit() {
	var pids [8]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		return // launched from a shell; its window outlives us
	}
	fmt.Fprint(os.Stderr, "\npress enter to close this window... ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
