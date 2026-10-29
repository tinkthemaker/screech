package player

import "os/exec"

// ProtectChild and AdoptChild lend the player's child-process protection to
// other children of Screech (yt-dlp), so that none of them outlives it. They
// are exported because the one package that runs yt-dlp (internal/ytdlp) is
// imported by this one and cannot import it back; the app, which sees both,
// hands them over.

// ProtectChild readies cmd to be started: no console window on Windows, and a
// parent-death signal on Linux. Call it before Start.
func ProtectChild(cmd *exec.Cmd) { configureCmd(cmd) }

// AdoptChild puts a child that has just started under a group of its own that
// dies with it, and returns the release that ends that group. On Windows the
// group is a kill-on-close job object: releasing it, or Screech dying, kills
// the child and everything the child started. That is the point for a
// standalone yt-dlp, whose launcher spawns the real work as a child; killing
// only the launcher on a timeout would leave the child running.
//
// Unlike mpv's, which lives as long as Screech does and so shares one job
// that is never closed, each child gets its own job so one timed-out lookup
// can be ended without touching the rest. release is safe to call more than
// once. Where there is no such mechanism it does nothing.
func AdoptChild(cmd *exec.Cmd) (release func()) { return adoptChild(cmd) }
