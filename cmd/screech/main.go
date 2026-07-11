package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"screech/internal/config"
	"screech/internal/core"
	"screech/internal/player"
	"screech/internal/tui"
)

const version = "0.2.1"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "doctor", "--doctor":
			runDoctor()
			return
		case "version", "--version", "-v":
			fmt.Println("screech " + version)
			return
		}
	}

	// Nothing may die silently: panics land in the log and the console stays
	// open when Windows created it just for us (double-click launch).
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
			logLine("%s", msg)
			fmt.Fprintf(os.Stderr, "screech crashed. Details written to:\n  %s\n\n%s\n", logPath(), msg)
			holdConsoleOnExit()
			os.Exit(2)
		}
	}()

	openLog()
	logLine("screech %s starting", version)

	cfg, dbPath, err := config.Load()
	if err != nil {
		fail("config: %v", err)
	}
	logLine("config ok; db at %s", dbPath)

	c, err := core.Open(dbPath)
	if err != nil {
		fail("opening database: %v", err)
	}
	defer c.Close()
	logLine("database open; %d stations cached", c.StationCount())

	pl, err := player.NewMPV(cfg.MpvPath)
	if err != nil {
		fail("%v\n\nscreech needs mpv for playback.\n  windows:  scoop install mpv   (or choco install mpv, or https://mpv.io)\n  macos:    brew install mpv\n  linux:    apt/dnf/pacman install mpv\n\nJust installed it? Open a NEW terminal so PATH refreshes.\nmpv somewhere odd? Set mpv_path in the config file.\nRun `screech doctor` for a full checkup.", err)
	}
	defer pl.Close()
	logLine("mpv connected over IPC")

	if err := tui.Run(c, pl, tui.Options{Accent: cfg.Accent, ASCII: cfg.ASCII}); err != nil {
		fail("ui: %v", err)
	}
	logLine("clean exit")
}

var logF *os.File

func logPath() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "screech", "screech.log")
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "screech.log")
	}
	return "screech.log"
}

// openLog starts a fresh last-run log: one launch, one file, easy to paste.
func openLog() {
	p := logPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if f, err := os.Create(p); err == nil {
		logF = f
	}
}

func logLine(format string, args ...any) {
	if logF == nil {
		return
	}
	fmt.Fprintf(logF, format+"\n", args...)
	_ = logF.Sync()
}

func fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logLine("FATAL: %s", msg)
	fmt.Fprintln(os.Stderr, "screech: "+msg)
	holdConsoleOnExit()
	os.Exit(1)
}
