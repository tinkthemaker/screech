package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
)

func TestDigitRecallIgnoresEmptySlot(t *testing.T) {
	s, _ := testState(t)
	s2, cmds := s.Update(RecallPreset{Slot: 5})
	if len(cmds) != 0 {
		t.Fatal("empty preset slot should not tune")
	}
	if !strings.Contains(s2.Feedback, "PRESET") || !strings.Contains(s2.Feedback, "5 is empty") {
		t.Fatalf("feedback: %q", s2.Feedback)
	}
}

func TestStepVolumeControlsPlayerAndPersists(t *testing.T) {
	s, pl := testState(t)
	s.Volume = 50
	s = drive(t, s, StepVolume{Delta: 5})
	if s.Volume != 55 {
		t.Fatalf("step should raise volume: %d", s.Volume)
	}
	if got := pl.volumeLevel(); got != 55 {
		t.Fatalf("player volume=%d, want 55", got)
	}
	if got := s.core.Volume(); got != 55 {
		t.Fatalf("persisted volume=%d, want 55", got)
	}
}

func TestSetVolumeClamps(t *testing.T) {
	s, pl := testState(t)
	s = drive(t, s, SetVolume{Percent: 150})
	if s.Volume != 100 || pl.volumeLevel() != 100 {
		t.Fatalf("got %d/%d, want 100", s.Volume, pl.volumeLevel())
	}
	s = drive(t, s, SetVolume{Percent: -10})
	if s.Volume != 0 || pl.volumeLevel() != 0 {
		t.Fatalf("got %d/%d, want 0", s.Volume, pl.volumeLevel())
	}
	if got := s.core.Volume(); got != 0 {
		t.Fatalf("persisted volume=%d, want 0", got)
	}
}

func TestTouchClearsNotice(t *testing.T) {
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := New(c, newStubPlayer(), Options{Dot: "·", Notice: "database was damaged"})
	if s.Notice == "" {
		t.Fatal("Options.Notice should seed the session's notice")
	}
	s = drive(t, s, Touch{})
	if s.Notice != "" {
		t.Fatalf("Touch should clear the notice, got %q", s.Notice)
	}
}

// Notify carries face-originated text: a feedback string timestamps like
// setFeedback, a note lands on the note line, and either part is optional.
func TestNotifySetsFeedbackAndNote(t *testing.T) {
	s, _ := testState(t)
	s = drive(t, s, Notify{Feedback: "LIBRARY  Track removed", Note: "could not open history"})
	if s.Feedback != "LIBRARY  Track removed" {
		t.Fatalf("feedback %q", s.Feedback)
	}
	if s.FeedbackAt != s.Now {
		t.Fatal("feedback should timestamp off the session clock")
	}
	if s.Note != "could not open history" {
		t.Fatalf("note %q", s.Note)
	}
	s = drive(t, s, Notify{Feedback: "STATIONS  Removed"})
	if s.Feedback != "STATIONS  Removed" || s.Note != "could not open history" {
		t.Fatalf("a lone feedback must not touch the note: %q %q", s.Feedback, s.Note)
	}
}

func TestTogglePresetSavesThenClears(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = drive(t, s, TogglePreset{})
	if s.CurrentSlot() != 1 || !strings.HasPrefix(s.Feedback, "SAVED  Preset 1") {
		t.Fatalf("slot=%d feedback=%q", s.CurrentSlot(), s.Feedback)
	}
	s = drive(t, s, TogglePreset{})
	if s.CurrentSlot() != 0 || !strings.HasPrefix(s.Feedback, "CLEARED  Preset 1") {
		t.Fatalf("slot=%d feedback=%q", s.CurrentSlot(), s.Feedback)
	}
}

func TestLoveTogglesTrack(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventTitle, Title: "Artist - Title"}))
	s = drive(t, s, Love{})
	if !s.LovedTrack || !strings.HasPrefix(s.Feedback, "LOVED  Track and station") {
		t.Fatalf("loved=%v feedback=%q", s.LovedTrack, s.Feedback)
	}
	s = drive(t, s, Love{})
	if s.LovedTrack || !strings.HasPrefix(s.Feedback, "UNLOVED  Love returned") {
		t.Fatalf("loved=%v feedback=%q", s.LovedTrack, s.Feedback)
	}
}

// With last_station pointing at a healthy station, Init's resume cmd
// produces its pick rather than a fresh Tune.
func TestInitResumesLastStation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	c, err := core.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seeds := core.SeedStations()
	c.StartListen(seeds[0].UUID, time.Now())
	c.Close()

	c, err = core.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s := New(c, newStubPlayer(), Options{Dot: "·"})

	var tuned *Tuned
	for _, msg := range run(t, s.Init()) {
		if tm, ok := msg.(Tuned); ok {
			m2 := tm
			tuned = &m2
		}
	}
	if tuned == nil {
		t.Fatal("Init must yield a Tuned")
	}
	if tuned.Err != nil {
		t.Fatalf("resume pick failed: %v", tuned.Err)
	}
	if tuned.Pick.Station.UUID != seeds[0].UUID || tuned.Pick.Reason != "where you left off" {
		t.Fatalf("want the last station back with its resume reason, got %q / %q",
			tuned.Pick.Station.UUID, tuned.Pick.Reason)
	}
}
