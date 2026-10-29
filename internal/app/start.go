// Package app holds the startup every face shares: config, the
// single-instance lock, the last-run log, the database and mpv. It
// returns errors instead of printing them so a face decides how a
// failure reaches its user.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	versionpkg "github.com/tinkthemaker/screech/internal/version"
)

// ErrAlreadyRunning is the single-instance lock's "someone else has it"
// answer. Startup must be able to tell it apart from a real lock failure:
// only this error leaves the running instance's log file alone.
var ErrAlreadyRunning = errors.New("another screech is already running")

// Stage names where startup failed, so the error can keep the wording the
// terminal has always printed.
type Stage string

const (
	StageConfig   Stage = "config"
	StageLock     Stage = "single instance lock"
	StageDatabase Stage = "opening database"
	StageMpv      Stage = "mpv"
)

// StartError is a startup failure tagged with its stage. Its message is
// the exact line a face should show: "<stage>: <err>", except mpv, which
// appends the install help instead of a bare stage name.
type StartError struct {
	Stage Stage
	Err   error
}

func (e *StartError) Error() string {
	if e.Stage == StageMpv {
		return fmt.Sprintf("%v\n\n%s", e.Err, MpvHelp)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// MpvHelp is the install checklist the terminal has always appended to an
// mpv startup failure.
const MpvHelp = "screech needs mpv for playback.\n" +
	"  windows:  scoop install mpv   (or choco install mpv, or https://mpv.io)\n" +
	"  macos:    brew install mpv\n" +
	"  linux:    apt/dnf/pacman install mpv\n" +
	"\n" +
	"Just installed it? Open a NEW terminal so PATH refreshes.\n" +
	"mpv somewhere odd? Set mpv_path in the config file.\n" +
	"Run `screech doctor` for a full checkup."

// Runtime is a started screech: everything a face needs to run and to
// shut down cleanly.
type Runtime struct {
	Config config.Config
	DBPath string
	Core   *core.Core
	Player player.Player
	// Notice reports a recovered database: "database was damaged; moved
	// aside to <file> and started fresh", or "".
	Notice string
	// NoYtdlp is true when startup found no yt-dlp (off, or nowhere to be
	// found): YouTube links cannot start a station, phrases still can.
	NoYtdlp bool
	Log     *Log

	release func() // single-instance lock
}

// Start runs the shared startup with the real config loader.
func Start(log *Log) (*Runtime, error) { return StartFrom(log, config.Load) }

// StartFrom is the startup sequence with the config source swapped, so
// tests never touch the OS config dir. It returns ErrAlreadyRunning
// unwrapped and, in that case only, never opens the log — it belongs to
// the running instance. Any other failure comes back as *StartError with
// the log opened and its FATAL line written.
func StartFrom(log *Log, load func() (config.Config, string, error)) (*Runtime, error) {
	log.Linef("screech %s starting", versionpkg.Current)

	cfg, dbPath, err := load()
	if err != nil {
		return nil, failStage(log, StageConfig, err)
	}
	log.Linef("config ok; db at %s", dbPath)
	for _, w := range cfg.Warnings {
		log.Linef("config warning: %s", w)
	}

	release, err := AcquireSingleInstance(cfg.DataDir)
	if err != nil {
		if errors.Is(err, ErrAlreadyRunning) {
			// Another instance owns the session — and its log. The
			// buffered lines die with this process rather than
			// truncating what the running screech is writing.
			return nil, err
		}
		return nil, failStage(log, StageLock, err)
	}

	log.Open()

	c, err := core.Open(dbPath)
	if err != nil {
		release()
		return nil, failStage(log, StageDatabase, err)
	}
	// The listener's own stations come from config.toml on every launch:
	// the file is their record, the database only a cache of it. A failure
	// here costs those stations, never the radio.
	links, discoveries := customStations(cfg.Stations)
	if err := c.SetCustomStations(links, time.Now()); err != nil {
		log.Linef("custom stations: %v", err)
	} else if n := len(links); n > 0 {
		log.Linef("custom stations from config: %d", n)
	}
	// Discovery stations the config names are upserted but never deleted:
	// the ones started from FIND live only in the database.
	if err := c.SetDiscoveryStations(discoveries, time.Now()); err != nil {
		log.Linef("discovery stations: %v", err)
	} else if n := len(discoveries); n > 0 {
		log.Linef("discovery stations from config: %d", n)
	}
	log.Linef("database open; %d stations cached", c.StationCount())

	var notice string
	if recovered := c.RecoveredFrom(); recovered != "" {
		notice = fmt.Sprintf("database was damaged; moved aside to %s and started fresh", filepath.Base(recovered))
		log.Linef("%s", notice)
	}

	pl, err := player.NewMPV(cfg.MpvPath, cfg.DataDir)
	if err != nil {
		_ = c.Close()
		release()
		return nil, failStage(log, StageMpv, err)
	}
	log.Linef("mpv connected over IPC")
	haveYtdlp := wireDiscovery(pl, discoveryDeps{Config: cfg, ExeDir: executableDir(), Known: c.KnownArtist, Log: log})

	// mpv boots at --volume=100; restore the persisted level now, on this
	// thread, so the first Play can't beat the face's async volume command
	// to the speakers.
	if err := pl.SetVolume(c.Volume()); err != nil {
		log.Linef("volume restore failed: %v", err)
	}

	return &Runtime{Config: cfg, DBPath: dbPath, Core: c, Player: pl, Notice: notice, NoYtdlp: !haveYtdlp, Log: log, release: release}, nil
}

// executableDir is the folder holding the running screech, one of the places
// yt-dlp is looked for; "" when the OS won't say.
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// customStations splits config's [[stations]] into the two kinds core
// keeps: links, and discoveries, whose sentinel URL carries their phrase
// (or YouTube link) after "discover:". Core fills in the rest. They go to
// different calls because SetCustomStations deletes the custom: rows the
// file no longer names, and a discovery must never be on that list.
func customStations(in []config.Station) (links, discoveries []core.Station) {
	for _, s := range in {
		st := core.Station{Name: s.Name, URL: s.URL, Tags: s.Tags,
			Codec: s.Codec, Bitrate: s.Bitrate, Country: s.Country, Homepage: s.Homepage}
		if s.Discover != "" {
			st.UUID = core.DiscoveryUUID(s.Discover)
			st.URL = core.DiscoverScheme + s.Discover
			discoveries = append(discoveries, st)
			continue
		}
		links = append(links, st)
	}
	return links, discoveries
}

// failStage reports a startup failure that isn't "already running": the
// log is safe to open (no other instance's is at risk) and gets its
// FATAL line — startup errors are exactly what the log is for.
func failStage(log *Log, stage Stage, err error) *StartError {
	e := &StartError{Stage: stage, Err: err}
	log.Open()
	log.Linef("FATAL: %s", e.Error())
	log.Close()
	return e
}

// Close releases everything Start took out, in reverse order: player,
// core, the instance lock, the log.
func (r *Runtime) Close() {
	if r.Player != nil {
		r.Player.Close()
	}
	if r.Core != nil {
		_ = r.Core.Close()
	}
	if r.release != nil {
		r.release()
	}
	if r.Log != nil {
		r.Log.Close()
	}
}
