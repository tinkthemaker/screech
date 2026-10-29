package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkthemaker/screech/internal/config"
	"github.com/tinkthemaker/screech/internal/core"
)

func TestStartRefusesWhileLockHeldAndLeavesLogAlone(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	p := filepath.Join(dir, "screech.log")
	before := []byte("first instance's log\n")
	if err := os.WriteFile(p, before, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = StartFrom(NewLog(p), func() (config.Config, string, error) {
		return config.Config{DataDir: dir}, filepath.Join(dir, "screech.db"), nil
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(before) {
		t.Fatalf("log touched: %q", b)
	}
}

func TestStartMpvFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "screech.log")
	_, err := StartFrom(NewLog(p), func() (config.Config, string, error) {
		return config.Config{DataDir: dir, MpvPath: filepath.Join(dir, "no-mpv")}, filepath.Join(dir, "screech.db"), nil
	})
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("got %v, want *StartError", err)
	}
	if se.Stage != StageMpv {
		t.Fatalf("stage = %q, want %q", se.Stage, StageMpv)
	}
	if !strings.HasSuffix(se.Error(), MpvHelp) {
		t.Fatalf("Error() = %q, want it to end with MpvHelp", se.Error())
	}
	release, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatalf("lock still held after failure: %v", err)
	}
	release()
	b, _ := os.ReadFile(p)
	s := string(b)
	db := strings.Index(s, "database open;")
	fatal := strings.Index(s, "FATAL:")
	if db < 0 || fatal < 0 || fatal < db {
		t.Fatalf("log = %q, want \"database open;\" then a \"FATAL:\" line", s)
	}
}

// A fresh install with an empty database and one [[stations]] entry: the
// link is cached before mpv is even started, so it is there for the very
// first pick, ahead of any directory sync.
func TestStartCachesCustomStations(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "screech.log")
	dbPath := filepath.Join(dir, "screech.db")
	link := config.Station{Name: "My Link", URL: "https://mine.example/live.mp3", Tags: "jazz"}
	_, err := StartFrom(NewLog(p), func() (config.Config, string, error) {
		return config.Config{DataDir: dir, MpvPath: filepath.Join(dir, "no-mpv"), Stations: []config.Station{link}}, dbPath, nil
	})
	var se *StartError
	if !errors.As(err, &se) || se.Stage != StageMpv {
		t.Fatalf("got %v, want the mpv stage to fail after the database opened", err)
	}
	store, err := core.OpenStoreReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sts, err := store.LoadStations()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, st := range sts {
		if st.UUID == core.CustomUUID(link.URL) {
			found = st.Name == link.Name && st.StreamURL() == link.URL && st.Tags == "jazz"
		}
	}
	if !found {
		t.Fatalf("custom station not cached as configured; stations: %d", len(sts))
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "custom stations from config: 1") {
		t.Errorf("log = %q, want the custom-station count", b)
	}
}

func TestStartConfigFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "screech.log")
	_, err := StartFrom(NewLog(p), func() (config.Config, string, error) {
		return config.Config{}, "", errors.New("bad toml")
	})
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("got %v, want *StartError", err)
	}
	if se.Stage != StageConfig {
		t.Fatalf("stage = %q, want %q", se.Stage, StageConfig)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "FATAL: config:") {
		t.Fatalf("log = %q, want a \"FATAL: config:\" line", b)
	}
}

func TestAcquireSingleInstanceExcludes(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireSingleInstance(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire got %v, want ErrAlreadyRunning", err)
	}
	release()
	third, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	third()
}
