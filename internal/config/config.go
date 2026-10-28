// Package config: one TOML file, created with commented defaults on first
// run. Screech has no general settings UI; the in-app theme picker persists
// its presentation choice alongside other local runtime state.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Accent     string `toml:"accent"`
	ASCII      bool   `toml:"ascii"`
	MpvPath    string `toml:"mpv_path"`
	DataDir    string `toml:"data_dir"`
	AutoHopAds bool   `toml:"auto_hop_ads"`
	SyncLimit  int    `toml:"sync_limit"`

	// Stations are the listener's own streams, the [[stations]] tables.
	// They're decoded on their own (parseStations) so one bad entry is a
	// warning rather than a failed load.
	Stations []Station `toml:"-"`

	// Warnings carries what Read repaired or ignored while loading. Every
	// entry is advisory — nothing here ever fails the load.
	Warnings []string `toml:"-"`
}

const (
	defaultAccent    = "#FFB000"
	defaultSyncLimit = 20000
	maxSyncLimit     = 50000 // beyond the directory's working-station count
)

var accentRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func defaults() Config {
	return Config{
		Accent:    defaultAccent,
		ASCII:     false,
		MpvPath:   "mpv",
		SyncLimit: defaultSyncLimit,
	}
}

const defaultFile = `# screech configuration

# The one accent color. Phosphor amber by default.
accent = "#FFB000"

# Swap every fancy glyph for 7-bit ASCII (bad SSH sessions, odd fonts).
ascii = false

# Path to the mpv binary if it isn't on PATH.
mpv_path = "mpv"

# Where the database lives. Empty = next to this file. "~" and relative
# paths resolve against home and this file's directory respectively.
data_dir = ""

# Hop to the next station when a stream's title literally announces a
# break ("advertisement", "commercial break"). Empty titles and station
# IDs never trigger it - DJs produce those too. At most one hop every 30
# seconds; a hopped break teaches the same as a skip during one.
auto_hop_ads = false

# How many stations to cache from the directory (top slice by votes).
# The directory refreshes in the background weekly.
sync_limit = 20000

# Your own stations: any http or https stream link, one [[stations]] table
# each. "screech add <link>" writes one for you. name and url are required;
# tags, codec, bitrate, country and homepage are optional. They join the
# rotation the next time Screech starts and survive every directory refresh.
#
# [[stations]]
# name = "My station"
# url = "https://example.com/stream.mp3"
# tags = "jazz, late night"
`

// Read finds an existing config and returns it along with the resolved
// database path and data directory. It does not create files or directories;
// use Load for first-run setup.
func Read() (Config, string, string, error) {
	return readFrom(configDir())
}

// Load reads (creating if needed) the config and returns it plus the
// database path. On first run it creates the config directory with 0700
// permissions and a default config file.
func Load() (Config, string, error) {
	return loadFrom(configDir())
}

func configDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "screech")
}

func readFrom(dir string) (Config, string, string, error) {
	path := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return defaults(), filepath.Join(dir, "screech.db"), dir, err
	}
	cfg, err := decode(data)
	if err != nil {
		return cfg, filepath.Join(dir, "screech.db"), dir, err
	}
	dataDir := resolveDataDir(cfg.DataDir, dir)
	return cfg, filepath.Join(dataDir, "screech.db"), dataDir, nil
}

// decode parses config.toml's contents over the defaults and repairs what
// it can, noting each repair in Warnings. Only invalid TOML is an error.
func decode(data []byte) (Config, error) {
	cfg := defaults()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return cfg, err
	}
	for _, k := range md.Undecoded() {
		if k[0] == "stations" {
			continue // parseStations names its own problems
		}
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("unknown key %q (ignored)", k.String()))
	}
	if !accentRe.MatchString(cfg.Accent) {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("accent %q is not #RRGGBB; using %s", cfg.Accent, defaultAccent))
		cfg.Accent = defaultAccent
	}
	switch {
	case cfg.SyncLimit <= 0:
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("sync_limit %d is not positive; using %d", cfg.SyncLimit, defaultSyncLimit))
		cfg.SyncLimit = defaultSyncLimit
	case cfg.SyncLimit > maxSyncLimit:
		// Radio-browser etiquette: the directory has fewer working
		// stations than that anyway, so a bigger ask is pure waste.
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("sync_limit %d exceeds %d; clamped", cfg.SyncLimit, maxSyncLimit))
		cfg.SyncLimit = maxSyncLimit
	}
	// A second pass for the stations alone: decoded into Config, a wrong
	// type in any one table would fail the whole file and stop the radio.
	var raw struct {
		Stations any `toml:"stations"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return cfg, err
	}
	var warns []string
	cfg.Stations, warns = parseStations(raw.Stations)
	cfg.Warnings = append(cfg.Warnings, warns...)
	return cfg, nil
}

func loadFrom(dir string) (Config, string, error) {
	cfg, dbPath, dataDir, err := readFrom(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return cfg, "", err
		}
		_ = os.Chmod(dir, 0o700)
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(defaultFile), 0o644); err != nil {
			return cfg, "", err
		}
		_ = os.Chmod(path, 0o600)
		cfg, dbPath, dataDir, err = readFrom(dir)
	}
	if err != nil {
		return cfg, "", err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return cfg, "", err
	}
	_ = os.Chmod(dataDir, 0o700)
	cfg.DataDir = dataDir
	return cfg, dbPath, nil
}

// resolveDataDir turns the configured data_dir into an absolute path.
// A leading "~" means the user's home; a relative path anchors to the
// config file's directory rather than the process CWD, which is not the
// same place when screech is double-clicked instead of launched from a
// shell. Empty stays "next to the config file".
func resolveDataDir(dataDir, cfgDir string) string {
	if dataDir == "" {
		return cfgDir
	}
	if dataDir == "~" || strings.HasPrefix(dataDir, "~/") ||
		(runtime.GOOS == "windows" && strings.HasPrefix(dataDir, `~\`)) {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, dataDir[1:])
		}
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(cfgDir, dataDir)
	}
	return dataDir
}
