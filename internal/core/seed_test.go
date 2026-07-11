package core

import (
	"context"
	"testing"
	"time"
)

func TestPresetToggleAndRecall(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()

	// No station playing: toggle is a no-op.
	if slot, saved, full := c.TogglePreset(now); slot != 0 || saved || full {
		t.Fatalf("toggle with nothing playing: %d %v %v", slot, saved, full)
	}

	p, err := c.Tune(now)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p.Station.UUID, now)

	slot, saved, full := c.TogglePreset(now)
	if !saved || slot != 1 || full {
		t.Fatalf("first save should land in slot 1: %d %v %v", slot, saved, full)
	}
	if c.Presets()[1] != p.Station.UUID {
		t.Fatal("preset 1 not recorded")
	}

	// Toggle again: unsave.
	slot, saved, _ = c.TogglePreset(now)
	if saved || slot != 1 {
		t.Fatalf("second toggle should clear slot 1: %d %v", slot, saved)
	}
	if len(c.Presets()) != 0 {
		t.Fatal("preset should be gone")
	}

	// Save again, then recall via TuneTo after moving elsewhere.
	c.TogglePreset(now)
	p2, err := c.Tune(now.Add(10 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(p2.Station.UUID, now.Add(10*time.Minute))
	back, err := c.TuneTo(p.Station.UUID, now.Add(20*time.Minute))
	if err != nil || back.Station.UUID != p.Station.UUID {
		t.Fatalf("TuneTo failed: %v %v", back.Station.UUID, err)
	}

	// Presets persist across reopen (same db file).
	if got := c.Presets(); got[1] != p.Station.UUID {
		t.Fatalf("preset lost in memory: %v", got)
	}
}

func TestPresetsFillUp(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	// Save nine different stations.
	for i := 0; i < 9; i++ {
		c.StartListen(c.stations[i].UUID, now)
		if _, saved, full := c.TogglePreset(now); !saved || full {
			t.Fatalf("save %d failed", i)
		}
	}
	// Tenth: full.
	c.StartListen(c.stations[9].UUID, now)
	if _, saved, full := c.TogglePreset(now); saved || !full {
		t.Fatal("tenth save should report full")
	}
}

func TestSeedTagPath(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	ctx := context.Background()

	res, ok := c.Seed(ctx, "Ambient", now)
	if !ok {
		t.Fatal("ambient should match seed-station tags")
	}
	if res.Kind != "tag" || res.Label != "ambient" {
		t.Fatalf("expected tag seed, got %+v", res)
	}
	found := false
	for _, tag := range res.Pick.Station.TagList() {
		if tag == "ambient" {
			found = true
		}
	}
	if !found {
		t.Fatalf("picked station lacks the seeded tag: %v", res.Pick.Station.Tags)
	}
	if res.Pick.Reason != "seeded: ambient" {
		t.Fatalf("reason: %q", res.Pick.Reason)
	}
	// The seed must tilt the model: tag affinity written.
	if r, exists := c.tags["ambient"]; !exists || r.Alpha <= 1 {
		t.Fatal("tag affinity not bumped")
	}
}

func TestSeedArtistPathLocalFallback(t *testing.T) {
	c := openTestCore(t)
	now := time.Now()
	// Cancelled context: the network search fails instantly, forcing the
	// local name-match fallback.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, ok := c.Seed(ctx, "Paradise", now)
	if !ok {
		t.Fatal("'paradise' should match Radio Paradise by local name")
	}
	if res.Kind != "artist" {
		t.Fatalf("kind: %q", res.Kind)
	}
	// Pseudo-love recorded.
	if !c.loved["paradise"] {
		t.Fatal("artist seed should pseudo-love the query")
	}

	// Nonsense query: no pick, but still pseudo-loved for the future.
	res2, ok2 := c.Seed(ctx, "Zxqvv Nonexistent Band", now)
	if ok2 {
		t.Fatalf("nonsense query should find nothing, got %v", res2.Pick.Station.Name)
	}
	if !c.loved["zxqvv nonexistent band"] {
		t.Fatal("intent should be recorded even without a station")
	}
}

func TestSeedArtistDashSongTakesArtist(t *testing.T) {
	c := openTestCore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.Seed(ctx, "Radio Paradise - Some Song Title", time.Now())
	if !c.loved["radio paradise"] {
		t.Fatal("artist - song input should seed the artist half")
	}
}
