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
	versionpkg "screech/internal/version"
)

// version is the one string every surface reports. It lives in
// internal/version so the mpv and radio-browser User-Agents move with it.
var version = versionpkg.Current

var usage = "screech " + version + " - terminal internet radio\n\n" +
	"Usage:\n" +
	"  screech                 start the TUI (default)\n" +
	"  screech doctor          run a read-only diagnostic check\n" +
	"  screech --help          show this message\n" +
	"  screech --version       show the version\n\n" +
	"Environment:\n" +
	"  SCREECH_REDUCED_MOTION  set to any value to disable the love-heart flash\n"

var reducedMotion bool

func main() {
	if code, exit := maybeHandleFlags(); exit {
		os.Exit(code)
	}
	os.Exit(run())
}

func maybeHandleFlags() (int, bool) {
	if len(os.Args) <= 1 {
		return 0, false
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0, true
	case "-v", "--version", "version":
		fmt.Println(version)
		return 0, true
	case "--reduced-motion":
		reducedMotion = true
		// Continue so "screech --reduced-motion" still launches the TUI.
		return 0, false
	}
	return 0, false
}

func run() (exit int) {
	// Nothing may die silently: panics land in the log and the console stays
	// open when Windows created it just for us (double-click launch).
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
			logLine("%s", msg)
			fmt.Fprintf(os.Stderr, "screech crashed. Details written to:\n  %s\n\n%s\n", logPath(), msg)
			holdConsoleOnExit()
			exit = 2
		}
	}()

	openLog()
	defer closeLog()
	logLine("screech %s starting", version)

	cfg, dbPath, err := config.Load()
	if err != nil {
		return fail("config: %v", err)
	}
	logLine("config ok; db at %s", dbPath)

	release, err := acquireSingleInstance(cfg.DataDir)
	if err != nil {
		return fail("single instance lock: %v", err)
	}
	defer release()

	c, err := core.Open(dbPath)
	if err != nil {
		return fail("opening database: %v", err)
	}
	defer c.Close()
	logLine("database open; %d stations cached", c.StationCount())

	pl, err := player.NewMPV(cfg.MpvPath, cfg.DataDir)
	if err != nil {
		return fail("%v\n\nscreech needs mpv for playback.\n  windows:  scoop install mpv   (or choco install mpv, or https://mpv.io)\n  macos:    brew install mpv\n  linux:    apt/dnf/pacman install mpv\n\nJust installed it? Open a NEW terminal so PATH refreshes.\nmpv somewhere odd? Set mpv_path in the config file.\nRun `screech doctor` for a full checkup.", err)
	}
	defer pl.Close()
	logLine("mpv connected over IPC")

	if w, h, ok := terminalSize(); ok && (w < 80 || h < 24) {
		return fail("terminal is %dx%d; screech needs at least 80 columns by 24 rows", w, h)
	}
	if os.Getenv("SCREECH_REDUCED_MOTION") != "" {
		reducedMotion = true
	}
	if err := tui.Run(c, pl, tui.Options{Accent: cfg.Accent, ASCII: cfg.ASCII, SyncLimit: cfg.SyncLimit, ReducedMotion: reducedMotion}); err != nil {
		return fail("ui: %v", err)
	}
	logLine("clean exit")
	return 0
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
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if f, err := os.Create(p); err == nil {
		logF = f
	}
}

func closeLog() {
	if logF != nil {
		_ = logF.Close()
		logF = nil
	}
}

func logLine(format string, args ...any) {
	if logF == nil {
		return
	}
	fmt.Fprintf(logF, format+"\n", args...)
	_ = logF.Sync()
}

func fail(format string, args ...any) int {
	msg := fmt.Sprintf(format, args...)
	logLine("FATAL: %s", msg)
	fmt.Fprintln(os.Stderr, "screech: "+msg)
	holdConsoleOnExit()
	return 1
}
