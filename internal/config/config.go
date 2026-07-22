// Package config: one TOML file, created with commented defaults on first
// run. There is no config UI and there never will be.
package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Accent     string `toml:"accent"`
	ASCII      bool   `toml:"ascii"`
	MpvPath    string `toml:"mpv_path"`
	DataDir    string `toml:"data_dir"`
	AutoHopAds bool   `toml:"auto_hop_ads"`
	SyncLimit  int    `toml:"sync_limit"`
}

func defaults() Config {
	return Config{
		Accent:    "#FFB000",
		ASCII:     false,
		MpvPath:   "mpv",
		SyncLimit: 20000,
	}
}

const defaultFile = `# screech configuration

# The one accent color. Phosphor amber by default.
accent = "#FFB000"

# Swap every fancy glyph for 7-bit ASCII (bad SSH sessions, odd fonts).
ascii = false

# Path to the mpv binary if it isn't on PATH.
mpv_path = "mpv"

# Where the database lives. Empty = next to this file.
data_dir = ""

# Auto-hop away from suspected ad breaks. Off by default: detection is
# conservative and DJs legitimately trip it. (Not yet wired in v1.)
auto_hop_ads = false

# How many stations to cache from the directory (top slice by votes).
# The directory refreshes in the background weekly.
sync_limit = 20000
`

// Load reads (creating if needed) the config and returns it plus the
// database path.
func Load() (Config, string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	dir := filepath.Join(base, "screech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return defaults(), "", err
	}
	path := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(defaultFile), 0o644); err != nil {
			return defaults(), "", err
		}
	}
	cfg := defaults()
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return defaults(), "", err
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = dir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return cfg, "", err
	}
	return cfg, filepath.Join(dataDir, "screech.db"), nil
}
