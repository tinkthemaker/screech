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

// A discovery the config names is cached on every launch, and one the
// config doesn't (started from FIND, or taken out of the file) is history
// that a later launch must leave alone, while the custom: set follows the
// file exactly.
func TestStartCachesDiscoveryStations(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "screech.log")
	dbPath := filepath.Join(dir, "screech.db")
	link := config.Station{Name: "My Link", URL: "https://mine.example/live.mp3"}
	disc := config.Station{Name: "Dungeon", Discover: "dungeon synth"}
	start := func(stations ...config.Station) {
		t.Helper()
		_, err := StartFrom(NewLog(p), func() (config.Config, string, error) {
			return config.Config{DataDir: dir, MpvPath: filepath.Join(dir, "no-mpv"), Stations: stations}, dbPath, nil
		})
		var se *StartError
		if !errors.As(err, &se) || se.Stage != StageMpv {
			t.Fatalf("got %v, want the mpv stage to fail after the database opened", err)
		}
	}
	cached := func() map[string]core.Station {
		t.Helper()
		store, err := core.OpenStoreReadOnly(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		sts, err := store.LoadStations()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]core.Station{}
		for _, st := range sts {
			out[st.UUID] = st
		}
		return out
	}

	start(link, disc)
	got := cached()
	st, ok := got[core.DiscoveryUUID("dungeon synth")]
	if !ok || st.Name != "Dungeon" || st.StreamURL() != "discover:dungeon synth" {
		t.Fatalf("discovery not cached as configured: %+v (found %v)", st, ok)
	}
	if _, ok := got[core.CustomUUID(link.URL)]; !ok {
		t.Fatal("the link was not cached beside it")
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "discovery stations from config: 1") {
		t.Errorf("log = %q, want the discovery count", b)
	}

	start()
	got = cached()
	if _, ok := got[core.DiscoveryUUID("dungeon synth")]; !ok {
		t.Error("a discovery left out of the config must stay: it is history")
	}
	if _, ok := got[core.CustomUUID(link.URL)]; ok {
		t.Error("a link left out of the config must go")
	}
}

func TestCustomStationsSplitsLinksFromDiscoveries(t *testing.T) {
	links, discs := customStations([]config.Station{
		{Name: "L", URL: "https://l.example/", Tags: "jazz"},
		{Name: "D", Discover: "Dungeon Synth", Tags: "ambient"},
	})
	if len(links) != 1 || links[0].URL != "https://l.example/" || links[0].UUID != "" {
		t.Errorf("links = %+v", links)
	}
	want := core.Station{UUID: core.DiscoveryUUID("dungeon synth"), Name: "D", URL: "discover:Dungeon Synth", Tags: "ambient"}
	if len(discs) != 1 || discs[0].UUID != want.UUID || discs[0].Name != want.Name || discs[0].URL != want.URL || discs[0].Tags != want.Tags {
		t.Errorf("discoveries = %+v, want %+v", discs, want)
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
