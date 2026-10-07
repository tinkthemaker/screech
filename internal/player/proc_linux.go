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

func resolveMpv(path string) string { return path }
