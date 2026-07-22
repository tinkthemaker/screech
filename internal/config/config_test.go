package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withConfigHome points os.UserConfigDir at a temp dir for the duration of a
// test so Load touches a scratch location instead of the real config.
func withConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// HOME is the fallback on platforms without XDG; keep both consistent.
	t.Setenv("HOME", dir)
	return dir
}

func TestDefaults(t *testing.T) {
	d := defaults()
	if d.Accent != "#FFB000" {
		t.Errorf("Accent = %q, want #FFB000", d.Accent)
	}
	if d.ASCII {
		t.Error("ASCII should default to false")
	}
	if d.MpvPath != "mpv" {
		t.Errorf("MpvPath = %q, want mpv", d.MpvPath)
	}
	if d.DataDir != "" {
		t.Errorf("DataDir = %q, want empty", d.DataDir)
	}
	if d.AutoHopAds {
		t.Error("AutoHopAds should default to false")
	}
}

func TestLoadCreatesDefaultFile(t *testing.T) {
	base := withConfigHome(t)

	cfg, dbPath, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The config file should have been written with defaults.
	cfgPath := filepath.Join(base, "screech", "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if cfg != defaults() {
		t.Errorf("Load returned %+v, want defaults %+v", cfg, defaults())
	}

	// Empty data_dir means the db lives next to the config file.
	wantDB := filepath.Join(base, "screech", "screech.db")
	if dbPath != wantDB {
		t.Errorf("dbPath = %q, want %q", dbPath, wantDB)
	}
}

func TestLoadWrittenDefaultsParse(t *testing.T) {
	withConfigHome(t)
	// First call writes the commented default file.
	if _, _, err := Load(); err != nil {
		t.Fatalf("first Load: %v", err)
	}
	// Second call parses the file it just wrote; it must decode cleanly and
	// still equal the defaults.
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if cfg != defaults() {
		t.Errorf("re-parsed config = %+v, want %+v", cfg, defaults())
	}
}

func TestLoadReadsExistingValues(t *testing.T) {
	base := withConfigHome(t)
	dir := filepath.Join(base, "screech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `accent = "#00FF00"
ascii = true
mpv_path = "/opt/mpv"
auto_hop_ads = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accent != "#00FF00" {
		t.Errorf("Accent = %q, want #00FF00", cfg.Accent)
	}
	if !cfg.ASCII {
		t.Error("ASCII should be true")
	}
	if cfg.MpvPath != "/opt/mpv" {
		t.Errorf("MpvPath = %q, want /opt/mpv", cfg.MpvPath)
	}
	if !cfg.AutoHopAds {
		t.Error("AutoHopAds should be true")
	}
}

func TestLoadCustomDataDir(t *testing.T) {
	base := withConfigHome(t)
	dir := filepath.Join(base, "screech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(base, "custom-data")
	body := "data_dir = \"" + dataDir + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, dbPath, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dataDir)
	}
	if want := filepath.Join(dataDir, "screech.db"); dbPath != want {
		t.Errorf("dbPath = %q, want %q", dbPath, want)
	}
	// The custom data dir should have been created.
	if _, err := os.Stat(dataDir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	base := withConfigHome(t)
	dir := filepath.Join(base, "screech")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("this = = broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, dbPath, err := Load()
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	// On error Load returns defaults and an empty db path.
	if cfg != defaults() {
		t.Errorf("on error config = %+v, want defaults", cfg)
	}
	if dbPath != "" {
		t.Errorf("on error dbPath = %q, want empty", dbPath)
	}
}
