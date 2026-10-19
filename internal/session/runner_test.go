package session

import (
	"sync"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

// recSink records what a runner publishes, in order.
type recSink struct {
	mu     sync.Mutex
	snaps  []Snapshot
	levels []Level
}

func (r *recSink) Snapshot(s Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, s)
}
func (r *recSink) Level(l Level) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels = append(r.levels, l)
}
func (r *recSink) snapshots() []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Snapshot(nil), r.snaps...)
}
func (r *recSink) levelCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.levels)
}

// waitFor polls until cond sees the wanted snapshot or the deadline hits.
func waitFor(t *testing.T, sink *recSink, d time.Duration, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		snaps := sink.snapshots()
		for i := len(snaps) - 1; i >= 0; i-- {
			if cond(snaps[i]) {
				return snaps[i]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no matching snapshot within %v; got %d", d, len(sink.snapshots()))
	return Snapshot{}
}

func TestRunnerPublishesOnChangeOnly(t *testing.T) {
	s, _ := testState(t)
	sink := &recSink{}
	r := NewRunner(s, sink)
	r.Start()
	defer r.Stop()

	r.Send(Next{})
	r.Send(Next{}) // second skip before the first lands
	waitFor(t, sink, 2*time.Second, func(sn Snapshot) bool { return sn.HaveStation })
	r.Send(Touch{}) // no visible change: must not republish
	time.Sleep(300 * time.Millisecond)

	snaps := sink.snapshots()
	if len(snaps) < 2 {
		t.Fatalf("expected at least the idle and tuned snapshots, got %d", len(snaps))
	}
	for i := 1; i < len(snaps); i++ {
		if snaps[i] == snaps[i-1] {
			t.Fatalf("identical snapshots published back to back at %d: %+v", i, snaps[i])
		}
	}
}

func TestRunnerRepublishesWhenFeedbackExpires(t *testing.T) {
	s, _ := testState(t)
	s = playing(t, s)
	s = drive(t, s, PlayerEvent(player.Event{Type: player.EventTitle, Title: "A - B"}))
	if !s.HaveTrack {
		t.Fatal("track required for love")
	}
	sink := &recSink{}
	r := NewRunner(s, sink)
	r.tickInterval = 25 * time.Millisecond
	r.Start()
	defer r.Stop()

	r.Send(Love{})
	waitFor(t, sink, 2*time.Second, func(sn Snapshot) bool { return sn.Feedback != "" })
	waitFor(t, sink, 3*time.Second, func(sn Snapshot) bool { return sn.Feedback == "" })
}

func TestRunnerThrottlesLevels(t *testing.T) {
	s, pl := testState(t)
	s = playing(t, s)
	sink := &recSink{}
	r := NewRunner(s, sink)
	r.Start()
	defer r.Stop()

	for i := 0; i < 100; i++ {
		pl.emit(player.Event{Type: player.EventLevel, Level: 0.5, Peak: 0.5, Stereo: false})
		time.Sleep(time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.levelCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let stragglers land
	got := sink.levelCount()
	if got < 2 || got > 5 {
		t.Fatalf("100 level events over ~100ms should yield 2-5 publishes, got %d", got)
	}
}

func TestRunnerStopWithSlowCommand(t *testing.T) {
	s, pl := testState(t)
	pl.playGate = make(chan struct{}) // Play blocks until released
	sink := &recSink{}
	r := NewRunner(s, sink)
	r.Start()

	r.Send(Next{})
	// Wait until the pick reached the player: Play is now parked on the gate.
	deadline := time.Now().Add(2 * time.Second)
	for !hasCall(pl, "play") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !hasCall(pl, "play") {
		t.Fatal("no play call reached the player")
	}
	start := time.Now()
	r.Stop()
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Fatalf("Stop blocked on the slow Play: %v", took)
	}
	close(pl.playGate)
	time.Sleep(50 * time.Millisecond)
	// Close runs inside Quit; no play may start after it.
	calls := pl.callsList()
	closeIdx := -1
	for i, c := range calls {
		if c == "close" {
			closeIdx = i
		}
	}
	if closeIdx < 0 {
		t.Fatal("player was never closed")
	}
	for i, c := range calls {
		if i > closeIdx && len(c) >= 4 && c[:4] == "play" {
			t.Fatalf("a play started after Close: %v", calls)
		}
	}
}

func TestRunnerSendAfterStopIsNoOp(t *testing.T) {
	s, _ := testState(t)
	r := NewRunner(s, &recSink{})
	r.Start()
	r.Stop()
	done := make(chan struct{})
	go func() {
		r.Send(Next{})
		r.Send(Quit{})
		r.Stop() // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send/Stop after Stop blocked")
	}
	_ = r.Current() // still readable
}
