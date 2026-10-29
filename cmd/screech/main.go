package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/tinkthemaker/screech/internal/app"
	"github.com/tinkthemaker/screech/internal/session"
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
	"                          (1 config, 2 database, 4 mpv, 8 ipc, 16 network,\n" +
	"                          32 yt-dlp when ytdlp_path is set)\n" +
	"  screech export loved    write the loved-track list as CSV to stdout\n" +
	"  screech export heard    write the heard-track history as CSV to stdout\n" +
	"  screech export backup <path>\n" +
	"                          write a consistent SQLite backup to <path>\n" +
	"  screech add <link> [--name N] [--tags T]\n" +
	"                          add your own station (any http/https stream) to\n" +
	"                          config.toml; a running screech sees it on its next launch.\n" +
	"                          A YouTube link becomes a discovery station seeded from it\n" +
	"  screech add --discover <phrase> [--name N] [--tags T]\n" +
	"                          add a discovery station: songs found from a phrase\n" +
	"                          (an artist, a song or a genre) or a YouTube link\n" +
	"  screech remove <link|phrase|id>\n" +
	"                          take one of your own stations back out of config.toml\n" +
	"  screech --help          show this message\n" +
	"  screech --version       show the version\n\n" +
	"Flags:\n" +
	"  --reduced-motion        calm the animations (no boot, scramble, static,\n" +
	"                          scrolling titles, blinking, or love flash)\n\n" +
	"Environment:\n" +
	"  SCREECH_REDUCED_MOTION  set to any value for the same effect as --reduced-motion\n"

var reducedMotion bool

type command int

const (
	cmdRun command = iota
	cmdDoctor
	cmdExport
	cmdAdd
	cmdRemove
	cmdHelp
	cmdVersion
)

// invocation is what the command line asked for: which command to run and
// the one flag that matters to it.
type invocation struct {
	cmd           command
	exportKind    string // loved, heard or backup — only set for cmdExport
	exportPath    string // the backup destination; empty for the CSV forms
	link          string // cmdAdd's link, or cmdRemove's link, phrase or custom:/disc: id
	discover      string // cmdAdd's --discover phrase or YouTube link
	name, tags    string // cmdAdd's --name and --tags
	reducedMotion bool
}

// parseArgs maps argv onto an invocation. One command at most, and
// --reduced-motion is accepted anywhere so `screech doctor --reduced-motion`
// and `screech --reduced-motion` both parse rather than silently falling
// into the TUI. export, add and remove take operands: export a kind (and
// for backup exactly one path), add a link and its optional --name and
// --tags, remove one link or id.
func parseArgs(args []string) (invocation, error) {
	var inv invocation
	var seen string
	var operands []string
	for _, a := range args {
		if a == "--reduced-motion" {
			inv.reducedMotion = true
			continue
		}
		if seen == "export" || seen == "add" || seen == "remove" {
			operands = append(operands, a)
			continue
		}
		var cmd command
		switch a {
		case "doctor":
			cmd = cmdDoctor
		case "export":
			cmd = cmdExport
		case "add":
			cmd = cmdAdd
		case "remove":
			cmd = cmdRemove
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
	switch inv.cmd {
	case cmdAdd:
		if err := parseAdd(&inv, operands); err != nil {
			return inv, err
		}
	case cmdRemove:
		if len(operands) != 1 {
			return inv, fmt.Errorf("remove takes one station: its link or phrase, or the id screech add printed")
		}
		inv.link = operands[0]
	}
	return inv, nil
}

// parseAdd reads `add <link> [--name N] [--tags T]` and `add --discover
// <phrase> [--name N] [--tags T]`, with any flag also accepted as --name=N.
// A station is a link or a discovery, never both.
func parseAdd(inv *invocation, operands []string) error {
	for i := 0; i < len(operands); i++ {
		a := operands[i]
		switch {
		case a == "--name" || a == "--tags" || a == "--discover":
			if i+1 == len(operands) {
				return fmt.Errorf("%s needs a value", a)
			}
			i++
			switch a {
			case "--name":
				inv.name = operands[i]
			case "--tags":
				inv.tags = operands[i]
			default:
				inv.discover = operands[i]
			}
		case strings.HasPrefix(a, "--name="):
			inv.name = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--tags="):
			inv.tags = strings.TrimPrefix(a, "--tags=")
		case strings.HasPrefix(a, "--discover="):
			inv.discover = strings.TrimPrefix(a, "--discover=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("add: unknown flag %q", a)
		case inv.link == "":
			inv.link = a
		default:
			return fmt.Errorf("add takes one link; got %q after %q (quote a name with spaces)", a, inv.link)
		}
	}
	switch {
	case inv.discover != "" && inv.link != "":
		return fmt.Errorf("add takes a link or --discover, not both (got %q and %q; quote a phrase with spaces)", inv.link, inv.discover)
	case inv.discover != "":
		if strings.TrimSpace(inv.discover) == "" {
			return fmt.Errorf("--discover needs a phrase or a YouTube link")
		}
	case inv.link == "":
		return fmt.Errorf("add needs a link: screech add <link> [--name N] [--tags T], or screech add --discover <phrase>")
	}
	return nil
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
	case cmdAdd:
		os.Exit(runAdd(inv.link, inv.discover, inv.name, inv.tags))
	case cmdRemove:
		os.Exit(runRemove(inv.link))
	default:
		os.Exit(run())
	}
}

func run() (exit int) {
	log := app.NewLog(app.DefaultLogPath())
	// Nothing may die silently: panics land in the log and the console stays
	// open when Windows created it just for us (double-click launch).
	defer func() {
		if r := recover(); r != nil {
			log.Open()
			msg := fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
			log.Linef("%s", msg)
			fmt.Fprintf(os.Stderr, "screech crashed. Details written to:\n  %s\n\n%s\n", log.Path(), msg)
			holdConsoleOnExit()
			exit = 2
		}
	}()

	rt, err := app.Start(log)
	if err != nil {
		if errors.Is(err, app.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, "screech: "+err.Error())
			holdConsoleOnExit()
			return 1
		}
		return fail("%s", err.Error())
	}
	defer rt.Close()

	opts := tui.Options{Accent: rt.Config.Accent, ASCII: rt.Config.ASCII, ReducedMotion: reducedMotion,
		Session: session.Options{SyncLimit: rt.Config.SyncLimit, AutoHopAds: rt.Config.AutoHopAds, Notice: rt.Notice}}
	if os.Getenv("SCREECH_REDUCED_MOTION") != "" {
		reducedMotion = true
		opts.ReducedMotion = true
	}
	if err := tui.Run(rt.Core, rt.Player, opts); err != nil {
		log.Linef("FATAL: ui: %v", err)
		return fail("ui: %v", err)
	}
	log.Linef("clean exit")
	return 0
}

func fail(format string, args ...any) int {
	// The FATAL line is written where the failure happened — app.Start
	// logs startup failures, run() logs the UI's — so fail is only the
	// user-facing half: stderr, console hold, exit code.
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, "screech: "+msg)
	holdConsoleOnExit()
	return 1
}
