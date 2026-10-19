package session

import (
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
)

func TestSnapshotFeedbackExpires(t *testing.T) {
	s, _ := testState(t)
	s, _ = s.Update(Notify{Feedback: "X marked the spot"})
	snap := s.Snapshot()
	if snap.Feedback != "X marked the spot" {
		t.Fatalf("feedback missing from fresh snapshot: %q", snap.Feedback)
	}
	s.Now = s.FeedbackAt.Add(FeedbackDuration - time.Millisecond)
	if got := s.Snapshot().Feedback; got != "X marked the spot" {
		t.Fatalf("feedback should still show at +2399ms: %q", got)
	}
	s.Now = s.FeedbackAt.Add(FeedbackDuration)
	if got := s.Snapshot().Feedback; got != "" {
		t.Fatalf("feedback should be gone at +2400ms: %q", got)
	}
}

func TestSnapshotSlotsAndCurrent(t *testing.T) {
	s, _ := testState(t)
	a := core.SeedStations()[0]
	b := core.SeedStations()[1]
	s = drive(t, s, Tuned{Pick: pickSeed(b)})
	s.Presets = map[int]string{1: a.UUID, 3: b.UUID}
	snap := s.Snapshot()
	want := [9]bool{true, false, true}
	if snap.Slots != want {
		t.Fatalf("slots: got %v want %v", snap.Slots, want)
	}
	if snap.CurrentSlot != 3 {
		t.Fatalf("current slot: got %d want 3", snap.CurrentSlot)
	}
	if snap.Station.UUID != b.UUID || snap.Station.Name != b.Name {
		t.Fatalf("station card: %+v", snap.Station)
	}
}

func TestSnapshotOfflineCarriesRetry(t *testing.T) {
	s, _ := testState(t)
	s = drive(t, s, Next{})
	s, _ = offlineStreak(t, s)
	if s.Phase != Offline {
		t.Fatalf("want Offline, got %v", s.Phase)
	}
	snap := s.Snapshot()
	if snap.Phase != "offline" {
		t.Fatalf("phase string: %q", snap.Phase)
	}
	if snap.RetryAtMs != s.RetryAt.UnixMilli() {
		t.Fatalf("retryAtMs: got %d want %d", snap.RetryAtMs, s.RetryAt.UnixMilli())
	}
}
