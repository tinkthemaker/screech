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
	"github.com/tinkthemaker/screech/internal/streamurl"
	"github.com/tinkthemaker/screech/internal/ytdlp"
)

type Config struct {
	Accent     string `toml:"accent"`
	ASCII      bool   `toml:"ascii"`
	MpvPath    string `toml:"mpv_path"`
	DataDir    string `toml:"data_dir"`
	AutoHopAds bool   `toml:"auto_hop_ads"`
	SyncLimit  int    `toml:"sync_limit"`
	// YtdlpPath says which yt-dlp Screech uses, for discovery stations and
	// for page links (YouTube, SoundCloud…) among the listener's own
	// stations. "" means find it (PATH, beside the executable, the data
	// folder), "off" means don't use it, anything else is the path as given.
	// The setting is kept as written; ytdlp.Find reads it.
	YtdlpPath string `toml:"ytdlp_path"`

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

# yt-dlp, which you install yourself, finds songs on YouTube Music for
# discovery stations and plays page links among your own stations -
# YouTube, SoundCloud, Vimeo, Mixcloud, Bandcamp, Twitch. Empty = find it
# (on PATH, beside screech, or in the data folder). "off" = never use it;
# discovery stations then play from Bandcamp alone. Or a full path.
# YouTube also wants a JavaScript runtime (deno, node or bun) on PATH.
ytdlp_path = ""

# Your own stations: any http or https stream link, one [[stations]] table
# each. "screech add <link>" writes one for you. name and url are required;
# tags, codec, bitrate, country and homepage are optional. They join the
# rotation the next time Screech starts and survive every directory refresh.
# Instead of a url, a station can be a discovery: discover = "<phrase or
# YouTube link>" finds songs from that sound, song after song.
# "screech add --discover <phrase>" writes one.
# Keep every setting above this point: in TOML, a line below a [[stations]]
# header belongs to that station.
#
# [[stations]]
# name = "My station"
# url = "https://example.com/stream.mp3"
# tags = "jazz, late night"
#
# [[stations]]
# name = "Dungeon synth"
# discover = "dungeon synth"
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
	cfg.Warnings = append(cfg.Warnings, pageLinkWarnings(cfg, dataDir)...)
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
	cfg.YtdlpPath = strings.TrimSpace(cfg.YtdlpPath)
	return cfg, nil
}

// executableDir is the folder holding the running screech, one of the three
// places yt-dlp is looked for. A variable so tests can aim it elsewhere.
var executableDir = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// pageLinkWarnings says which of the listener's own stations are page links
// that nothing will resolve. It waits for the data folder (one of the places
// yt-dlp is looked for), so it runs after the file is decoded, not inside
// decode.
//
// Only "off" and "not found" warn. A path the listener wrote out is theirs
// to get right, and doctor and the startup log say when it doesn't run, so a
// stale path doesn't also dress every page link in a warning here.
func pageLinkWarnings(cfg Config, dataDir string) []string {
	if _, ok := ytdlp.Find(cfg.YtdlpPath, executableDir(), dataDir); ok {
		return nil
	}
	why := "and none was found (put it on PATH, beside screech or in its data folder)"
	if strings.EqualFold(cfg.YtdlpPath, "off") {
		why = `which ytdlp_path = "off" turns off`
	}
	var warns []string
	for _, st := range cfg.Stations {
		if streamurl.IsPage(st.URL) {
			warns = append(warns, fmt.Sprintf("station %q is a page link, not a stream; it plays only with yt-dlp, %s", st.Name, why))
		}
	}
	return warns
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
