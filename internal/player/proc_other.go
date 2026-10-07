//go:build !linux && !windows

package player

import "os/exec"

// No kernel-level parent-death mechanism exists here (linux has
// Pdeathsig, windows job objects) — mpv just gets the usual
// Close-on-shutdown path.
func configureCmd(cmd *exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {}

func resolveMpv(path string) string { return path }
