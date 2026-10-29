//go:build windows

package ytdlp

import (
	"os/exec"
	"syscall"
)

// noWindow keeps a console from flashing up: the GUI is a windowsgui
// program, so a console child it starts would otherwise open its own window.
func noWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
