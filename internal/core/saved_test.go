package core

import (
	"testing"
	"time"
)

// SavedStations unifies presets and loved stations, sorted by listen time,
// with the preset slot attached for the dial tick.
func TestSavedStationsUnifiesPresetsAndLoves(t *testing.T) {
	c := openTestCore(t)
	now := time.Now().Add(-time.Hour)

	// WFMU: long listen + preset. Spacestation: shorter listen + loved track.
	// Groove Salad: loved track, never listened. Drone Zone: nothing (absent).
	listen := func(uuid string, start time.Time, dur time.Duration) {
		id, _ := c.store.InsertListen(uuid, start, DaypartFor(start))
		_ = c.store.FinishListen(id, start.Add(dur), false, false)
	}
	listen("seed:wfmu", now, 60*time.Minute)
	listen("seed:somafm-spacestation", now, 15*time.Minute)

	// Make wfmu current so TogglePreset can see it; the extra listen is
	// zero-length so it doesn't disturb the 60-minute sort lead.
	c.StartListen("seed:wfmu", now)
	c.TogglePreset(now)
	c.EndListen(now)

	// Love a track on spacestation and on groove salad (never listened).
	c.StartListen("seed:somafm-spacestation", now)
	c.NoteTitle("Artist X - Song Y", now)
	c.Love(now.Add(time.Minute))
	c.EndListen(now)
	c.store.InsertLoved("seed:somafm-groovesalad", "artist z", "Artist Z", "Song Z", now)

	got, err := c.SavedStations()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 saved stations (preset + 2 loved), got %d: %+v", len(got), got)
	}
	// Sorted by listen time: wfmu (60m) > spacestation (15m) > groove salad (0).
	if got[0].UUID != "seed:wfmu" || got[1].UUID != "seed:somafm-spacestation" || got[2].UUID != "seed:somafm-groovesalad" {
		t.Errorf("sort order wrong: %+v", got)
	}
	if got[0].PresetSlot == 0 {
		t.Errorf("wfmu should carry its preset slot: %+v", got[0])
	}
	if got[2].PresetSlot != 0 {
		t.Errorf("groove salad is loved, not a preset: %+v", got[2])
	}
	if got[2].Total != 0 {
		t.Errorf("loved-but-unheard station should have zero listen time: %+v", got[2])
	}
}

// RemoveStation clears the preset slot and the loved rows but leaves listen
// history and bandit counts intact — recall is not taste.
func TestRemoveStationClearsRecallKeepsHistory(t *testing.T) {
	c := openTestCore(t)
	now := time.Now().Add(-time.Hour)
	listen := func(uuid string, start time.Time, dur time.Duration) {
		id, _ := c.store.InsertListen(uuid, start, DaypartFor(start))
		_ = c.store.FinishListen(id, start.Add(dur), false, false)
	}
	listen("seed:wfmu", now, 30*time.Minute)
	c.StartListen("seed:wfmu", now)
	c.TogglePreset(now)
	c.EndListen(now)
	c.store.InsertLoved("seed:wfmu", "artist x", "Artist X", "Song X", now)

	if err := c.RemoveStation("seed:wfmu"); err != nil {
		t.Fatal(err)
	}
	// Preset gone, loves gone.
	if len(c.presets) != 0 {
		t.Errorf("preset should be cleared: %+v", c.presets)
	}
	if c.loved["artist x"] {
		t.Error("loved artist set should forget the station's artist")
	}
	got, _ := c.SavedStations()
	for _, e := range got {
		if e.UUID == "seed:wfmu" {
			t.Errorf("removed station should leave the saved list: %+v", e)
		}
	}
	// Listen history survives.
	top, _ := c.TopListened(9)
	found := false
	for _, e := range top {
		if e.UUID == "seed:wfmu" && e.Total == 30*time.Minute {
			found = true
		}
	}
	if !found {
		t.Error("listen history must survive station removal")
	}
}
