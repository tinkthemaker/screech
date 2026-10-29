//go:build !linux && !windows

package player

import "os/exec"

// No kernel-level parent-death mechanism exists here (linux has
// Pdeathsig, windows job objects) — mpv just gets the usual
// Close-on-shutdown path.
func configureCmd(cmd *exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {}

// adoptChild has no group to make here; see the note above.
func adoptChild(cmd *exec.Cmd) (release func()) { return func() {} }

// ResolveMpv has nothing to fix here — the .com wrapper is a Windows
// PATHEXT artifact, so the looked-up path is the binary.
func ResolveMpv(path string) string { return path }
