package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/tui"
	versionpkg "github.com/tinkthemaker/screech/internal/version"
)

// version is the one string every surface reports. It lives in
// internal/version so the mpv and radio-browser User-Agents move with it.
var version = versionpkg.Current

var usage = "screech " + version + " - terminal internet radio\n\n" +
	"Usage:\n" +
	"  screech                 start the TUI (default)\n" +
	"  screech doctor          run a read-only diagnostic check\n" +
	"                          exit status is a bitmask of failed checks\n" +
	"                          (1 config, 2 database, 4 mpv, 8 ipc, 16 network)\n" +
	"  screech export loved    write the loved-track list as CSV to stdout\n" +
	"  screech export heard    write the heard-track history as CSV to stdout\n" +
	"  screech export backup <path>\n" +
	"                          write a consistent SQLite backup to <path>\n" +
	"  screech --help          show this message\n" +
	"  screech --version       show the version\n\n" +
	"Flags:\n" +
	"  --reduced-motion        calm the animations (no boot, scramble, static,\n" +
	"                          scrolling titles, blinking, or love flash)\n\n" +
	"Environment:\n" +
	"  SCREECH_REDUCED_MOTION  set to any value for the same effect as --reduced-motion\n"

var reducedMotion bool

// errAlreadyRunning is the single-instance lock's "someone else has it"
// answer. Startup must be able to tell it apart from a real lock failure:
// only this error leaves the running instance's log file alone.
var errAlreadyRunning = errors.New("another screech is already running")

type command int

const (
	cmdRun command = iota
	cmdDoctor
	cmdExport
	cmdHelp
	cmdVersion
)

// invocation is what the command line asked for: which command to run and
// the one flag that matters to it.
type invocation struct {
	cmd           command
	exportKind    string // loved, heard or backup — only set for cmdExport
	exportPath    string // the backup destination; empty for the CSV forms
	reducedMotion bool
}

// parseArgs maps argv onto an invocation. One command at most, and
// --reduced-motion is accepted anywhere so `screech doctor --reduced-motion`
// and `screech --reduced-motion` both parse rather than silently falling
// into the TUI. export is the only command with operands: a kind, and for
// backup exactly one path.
func parseArgs(args []string) (invocation, error) {
	var inv invocation
	var seen string
	var operands []string
	for _, a := range args {
		if a == "--reduced-motion" {
			inv.reducedMotion = true
			continue
		}
		if seen == "export" {
			operands = append(operands, a)
			continue
		}
		var cmd command
		switch a {
		case "doctor":
			cmd = cmdDoctor
		case "export":
			cmd = cmdExport
		case "help", "-h", "--help":
			cmd = cmdHelp
		case "version", "-v", "--version":
			cmd = cmdVersion
		default:
			return inv, fmt.Errorf("unknown argument %q", a)
		}
		if seen != "" {
			return inv, fmt.Errorf("screech takes one command; got %q after %q", a, seen)
		}
		seen = a
		inv.cmd = cmd
	}
	if inv.cmd == cmdExport {
		if len(operands) == 0 {
			return inv, fmt.Errorf("export needs a kind: loved, heard or backup")
		}
		switch operands[0] {
		case "loved", "heard":
			if len(operands) != 1 {
				return inv, fmt.Errorf("export %s takes no further arguments", operands[0])
			}
			inv.exportKind = operands[0]
		case "backup":
			if len(operands) != 2 {
				return inv, fmt.Errorf("export backup takes exactly one destination path")
			}
			inv.exportKind, inv.exportPath = operands[0], operands[1]
		default:
			return inv, fmt.Errorf("unknown export kind %q", operands[0])
		}
	}
	return inv, nil
}

func main() {
	inv, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "screech: %v\n\n%s", err, usage)
		os.Exit(2)
	}
	reducedMotion = inv.reducedMotion
	switch inv.cmd {
	case cmdHelp:
		fmt.Print(usage)
	case cmdVersion:
		fmt.Println("screech " + version)
	case cmdDoctor:
		os.Exit(runDoctor())
	case cmdExport:
		os.Exit(runExport(inv.exportKind, inv.exportPath))
	default:
		os.Exit(run())
	}
}

func run() (exit int) {
	// Nothing may die silently: panics land in the log and the console stays
	// open when Windows created it just for us (double-click launch).
	defer func() {
		if r := recover(); r != nil {
			openLog()
			msg := fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
			logLine("%s", msg)
			fmt.Fprintf(os.Stderr, "screech crashed. Details written to:\n  %s\n\n%s\n", logPath(), msg)
			holdConsoleOnExit()
			exit = 2
		}
	}()

	logLine("screech %s starting", version)

	cfg, dbPath, err := config.Load()
	if err != nil {
		return fail("config: %v", err)
	}
	logLine("config ok; db at %s", dbPath)
	for _, w := range cfg.Warnings {
		logLine("config warning: %s", w)
	}

	release, err := acquireSingleInstance(cfg.DataDir)
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			// Another instance owns the session — and its log. The
			// buffered lines die with this process rather than
			// truncating what the running screech is writing.
			fmt.Fprintln(os.Stderr, "screech: "+err.Error())
			holdConsoleOnExit()
			return 1
		}
		return fail("single instance lock: %v", err)
	}
	defer release()
	openLog()
	defer closeLog()

	c, err := core.Open(dbPath)
	if err != nil {
		return fail("opening database: %v", err)
	}
	defer c.Close()
	logLine("database open; %d stations cached", c.StationCount())

	opts := tui.Options{Accent: cfg.Accent, ASCII: cfg.ASCII, SyncLimit: cfg.SyncLimit, ReducedMotion: reducedMotion, AutoHopAds: cfg.AutoHopAds}
	if recovered := c.RecoveredFrom(); recovered != "" {
		notice := fmt.Sprintf("database was damaged; moved aside to %s and started fresh", filepath.Base(recovered))
		logLine("%s", notice)
		opts.Notice = notice
	}

	pl, err := player.NewMPV(cfg.MpvPath, cfg.DataDir)
	if err != nil {
		return fail("%v\n\nscreech needs mpv for playback.\n  windows:  scoop install mpv   (or choco install mpv, or https://mpv.io)\n  macos:    brew install mpv\n  linux:    apt/dnf/pacman install mpv\n\nJust installed it? Open a NEW terminal so PATH refreshes.\nmpv somewhere odd? Set mpv_path in the config file.\nRun `screech doctor` for a full checkup.", err)
	}
	defer pl.Close()
	logLine("mpv connected over IPC")

	// mpv boots at --volume=100; restore the persisted level now, on this
	// thread, so the first Play can't beat the TUI's async volume command
	// to the speakers.
	if err := pl.SetVolume(c.Volume()); err != nil {
		logLine("volume restore failed: %v", err)
	}
	if os.Getenv("SCREECH_REDUCED_MOTION") != "" {
		reducedMotion = true
		opts.ReducedMotion = true
	}
	if err := tui.Run(c, pl, opts); err != nil {
		return fail("ui: %v", err)
	}
	logLine("clean exit")
	return 0
}

var (
	logF   *os.File
	logBuf []string
)

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
// It runs only once startup is allowed to touch the user's directory —
// after the single-instance lock, or when failing for any other reason —
// so a second screech never truncates the running instance's log.
func openLog() {
	if logF != nil {
		return
	}
	p := logPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	logF = f
	for _, l := range logBuf {
		fmt.Fprintln(logF, l)
	}
	logBuf = nil
	_ = logF.Sync()
}

func closeLog() {
	if logF != nil {
		_ = logF.Close()
		logF = nil
	}
}

// logLine buffers until the log file exists. Lines written before the
// single-instance lock (startup, config) still land, but only once openLog
// proves this process is allowed to write.
func logLine(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if logF == nil {
		logBuf = append(logBuf, line)
		return
	}
	fmt.Fprintln(logF, line)
	_ = logF.Sync()
}

func fail(format string, args ...any) int {
	// A startup failure that isn't "already running" still gets its log:
	// config errors are exactly what the log is for.
	openLog()
	msg := fmt.Sprintf(format, args...)
	logLine("FATAL: %s", msg)
	fmt.Fprintln(os.Stderr, "screech: "+msg)
	holdConsoleOnExit()
	return 1
}
