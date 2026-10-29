//go:build !windows

package ytdlp

import "os/exec"

// noWindow has nothing to do here; only Windows opens a console per child.
func noWindow(cmd *exec.Cmd) {}
