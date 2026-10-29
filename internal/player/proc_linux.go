//go:build linux

package player

import (
	"os/exec"
	"syscall"
)

func configureCmd(cmd *exec.Cmd) {
	// Pdeathsig asks the kernel to SIGTERM mpv when screech dies, so a
	// crash or kill -9 can't leave an orphaned player running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

func afterStart(cmd *exec.Cmd) {}

// adoptChild has no group to make here: Pdeathsig covers Screech dying, and
// a context kill reaches the child itself.
func adoptChild(cmd *exec.Cmd) (release func()) { return func() {} }

// ResolveMpv has nothing to fix here — the .com wrapper is a Windows
// PATHEXT artifact, so the looked-up path is the binary.
func ResolveMpv(path string) string { return path }
