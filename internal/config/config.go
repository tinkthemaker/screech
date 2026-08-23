// Package config: one TOML file, created with commented defaults on first
// run. Screech has no general settings UI; the in-app theme picker persists
// its presentation choice alongside other local runtime state.
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

# Auto-hop away from suspected ad breaks. Placeholder: this key is read but
# nothing acts on it yet. Detection is conservative and DJs legitimately
# trip it, so hopping stays off until it earns its keep.
auto_hop_ads = false

# How many stations to cache from the directory (top slice by votes).
# The directory refreshes in the background weekly.
sync_limit = 20000
`

// Read finds an existing config and returns it along with the resolved
// database path and data directory. It does not create files or directories;
// use Load for first-run setup.
func Read() (Config, string, string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	dir := filepath.Join(base, "screech")
	path := filepath.Join(dir, "config.toml")
	cfg := defaults()
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = dir
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, filepath.Join(dataDir, "screech.db"), dataDir, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, filepath.Join(dataDir, "screech.db"), dataDir, err
	}
	dataDir = cfg.DataDir
	if dataDir == "" {
		dataDir = dir
	}
	return cfg, filepath.Join(dataDir, "screech.db"), dataDir, nil
}

// Load reads (creating if needed) the config and returns it plus the
// database path. On first run it creates the config directory with 0700
// permissions and a default config file.
func Load() (Config, string, error) {
	cfg, dbPath, dataDir, err := Read()
	if os.IsNotExist(err) {
		base, berr := os.UserConfigDir()
		if berr != nil {
			base = "."
		}
		dir := filepath.Join(base, "screech")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return cfg, "", err
		}
		_ = os.Chmod(dir, 0o700)
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(defaultFile), 0o644); err != nil {
			return cfg, "", err
		}
		_ = os.Chmod(path, 0o600)
		cfg, dbPath, dataDir, err = Read()
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
