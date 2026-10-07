package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFirstRunCreatesDefaults(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "screech")
	cfg, dbPath, err := loadFrom(dir)
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Accent != defaultAccent || cfg.SyncLimit != defaultSyncLimit {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if dbPath != filepath.Join(dir, "screech.db") {
		t.Fatalf("dbPath=%q, want next to the config", dbPath)
	}
	if cfg.DataDir != dir {
		t.Fatalf("DataDir=%q, want %q", cfg.DataDir, dir)
	}
	for _, p := range []string{filepath.Join(dir, "config.toml"), dir} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("first run should create %s: %v", p, err)
		}
	}
	// A second load reads what the first wrote, without rewriting it.
	cfg2, _, err := loadFrom(dir)
	if err != nil || cfg2.Accent != cfg.Accent {
		t.Fatalf("second load: cfg=%+v err=%v", cfg2, err)
	}
}

func TestReadDataDirTilde(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `data_dir = "~/screech-data-x"`+"\n")
	_, _, dataDir, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	want := filepath.Join(home, "screech-data-x")
	if dataDir != want {
		t.Fatalf("dataDir=%q, want %q", dataDir, want)
	}
}

func TestReadDataDirRelativeAnchorsToConfigDir(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `data_dir = "mydata"`+"\n")
	_, dbPath, dataDir, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if dataDir != filepath.Join(dir, "mydata") {
		t.Fatalf("dataDir=%q should resolve against the config dir, not CWD", dataDir)
	}
	if dbPath != filepath.Join(dir, "mydata", "screech.db") {
		t.Fatalf("dbPath=%q", dbPath)
	}
}

func TestReadUnknownKeyWarns(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "bogus_key = 1\n")
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], `unknown key "bogus_key"`) {
		t.Fatalf("warnings=%v, want an unknown-key warning", cfg.Warnings)
	}
}

func TestReadBadAccentResets(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `accent = "amber"`+"\n")
	cfg, _, _, err := readFrom(dir)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if cfg.Accent != defaultAccent {
		t.Fatalf("accent=%q, want reset to %s", cfg.Accent, defaultAccent)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "accent") {
		t.Fatalf("warnings=%v, want an accent warning", cfg.Warnings)
	}
}

func TestReadSyncLimitClamps(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, defaultSyncLimit},
		{-5, defaultSyncLimit},
		{99999, maxSyncLimit},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		writeConfig(t, dir, "sync_limit = "+strconv.Itoa(tc.in)+"\n")
		cfg, _, _, err := readFrom(dir)
		if err != nil {
			t.Fatalf("readFrom: %v", err)
		}
		if cfg.SyncLimit != tc.want {
			t.Errorf("sync_limit %d -> %d, want %d", tc.in, cfg.SyncLimit, tc.want)
		}
		if len(cfg.Warnings) == 0 {
			t.Errorf("sync_limit %d should warn", tc.in)
		}
	}
}

func TestReadInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "this is = = not toml\n")
	if _, _, _, err := readFrom(dir); err == nil {
		t.Fatal("invalid TOML must return an error")
	}
}
