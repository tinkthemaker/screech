package gui

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/player"
	"github.com/tinkthemaker/screech/internal/session"
)

// Bridge tests use the seed directory; a background sync must never
// fetch a live directory or change the fixture while an assertion runs.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in GUI tests")
}

func TestMain(m *testing.M) {
	http.DefaultTransport = offlineTransport{}
	os.Exit(m.Run())
}

// fakePlayer is the gui tests' player.Player: an event channel plus a
// call log, like the session package's own stub.
type fakePlayer struct {
	mu     sync.Mutex
	ch     chan player.Event
	volume int
	urls   []string
	closed bool
}

func newFakePlayer() *fakePlayer { return &fakePlayer{ch: make(chan player.Event, 64)} }
func (f *fakePlayer) Play(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	return nil
}
func (f *fakePlayer) SetVolume(p int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volume = p
	return nil
}
func (f *fakePlayer) SetPaused(bool) error { return nil }
func (f *fakePlayer) Events() <-chan player.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ch
}
func (f *fakePlayer) Restart() error { return nil }
func (f *fakePlayer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.ch)
	}
	return nil
}
func (f *fakePlayer) emit(ev player.Event) { f.ch <- ev }

// recWindow records SetUnfolded calls and counts Quit and Rescaled.
type recWindow struct {
	mu       sync.Mutex
	unfolded []bool
	quits    int
	rescales int
}

func (w *recWindow) SetUnfolded(u bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unfolded = append(w.unfolded, u)
}
func (w *recWindow) Quit() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.quits++
}
func (w *recWindow) Rescaled() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rescales++
}

// recEmitter records (event, data) pairs in order.
type recEmitter struct {
	mu   sync.Mutex
	evts []emitted
}
type emitted struct {
	name string
	data any
}

func (e *recEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evts = append(e.evts, emitted{name, data})
}
func (e *recEmitter) events() []emitted {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]emitted(nil), e.evts...)
}

func testBridge(t *testing.T, opts session.Options) (*Bridge, *core.Core, *fakePlayer, *session.Runner, *recWindow, *recEmitter) {
	t.Helper()
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	pl := newFakePlayer()
	win := &recWindow{}
	em := &recEmitter{}
	b := New(c, win, Prefs{})
	s := session.New(c, pl, opts)
	r := session.NewRunner(s, b.Sink())
	b.Attach(r)
	b.SetEmitter(em)
	r.Start()
	t.Cleanup(r.Stop)
	return b, c, pl, r, win, em
}

// waitSnap polls the runner's last published snapshot until cond or the
// deadline.
func waitSnap(t *testing.T, r *session.Runner, d time.Duration, cond func(session.Snapshot) bool) session.Snapshot {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if sn := r.Current(); cond(sn) {
			return sn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no matching snapshot within %v (last %+v)", d, r.Current())
	return session.Snapshot{}
}

func TestIntentsReachSession(t *testing.T) {
	b, c, pl, r, _, _ := testBridge(t, session.Options{Dot: "·"})
	// A snapshot names the pick before the asynchronous play command opens
	// its listen. Finish each tune before testing the next serial intent.
	waitReady := func(wantPlays int) {
		waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
			pl.mu.Lock()
			plays := len(pl.urls)
			pl.mu.Unlock()
			return !s.Syncing && s.HaveStation && plays >= wantPlays &&
				c.ListenOpen() && c.ResumeUUID() == s.Station.UUID
		})
	}
	waitReady(1)
	b.Next()
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.Feedback == "NEXT  Finding another station"
	})
	waitReady(2)

	b.TogglePreset()
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.CurrentSlot == 1 })

	b.SetVolume(150)
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.Volume == 100 })

	before := r.Current()
	b.RecallPreset(0)
	b.RecallPreset(10)
	time.Sleep(150 * time.Millisecond)
	if r.Current() != before {
		t.Fatalf("out-of-range preset recall changed the snapshot: %+v → %+v", before, r.Current())
	}
}

func TestWildcardTouchesAndTunes(t *testing.T) {
	b, _, _, r, _, _ := testBridge(t, session.Options{Dot: "·", Notice: "database recovered"})
	b.Wildcard()
	sn := waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.Notice == "" && (s.Phase == "tuning" || s.HaveStation)
	})
	if sn.Notice != "" {
		t.Fatalf("wildcard did not count as a touch: notice %q", sn.Notice)
	}
}

func TestEverythingTouches(t *testing.T) {
	b, _, _, r, _, _ := testBridge(t, session.Options{Dot: "·", Notice: "database recovered"})
	if got := r.Current().Notice; got == "" {
		t.Fatal("startup notice should be in the first snapshot")
	}
	// Any bound intent clears it — use StepVolume, no station needed.
	b.StepVolume(5)
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.Notice == "" })
}

func TestLovedFilterMatchesCore(t *testing.T) {
	b, c, _, _, _, _ := testBridge(t, session.Options{})
	now := time.Now()
	c.StartListen("seed:wfmu", now)
	c.NoteTitle("Broadcast - Come On Let's Go", now)
	c.Love(now)
	c.NoteTitle("Someone - Broadcast News", now)
	c.Love(now)
	c.EndListen(now)

	got, err := b.Loved("broadcast")
	if err != nil {
		t.Fatalf("Loved: %v", err)
	}
	all, err := c.LovedTracks(500)
	if err != nil {
		t.Fatalf("LovedTracks: %v", err)
	}
	want := core.FilterLovedTracks(all, "broadcast")
	if len(got) != len(want) {
		t.Fatalf("bridge filtered %d, core %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ArtistKey != want[i].ArtistKey || got[i].Title != want[i].Title {
			t.Fatalf("row %d: bridge %+v vs core %+v", i, got[i], want[i])
		}
	}
	if len(got) > 0 && (got[0].LovedAtMs == 0 || got[0].StationName == "") {
		t.Fatalf("row carries lovedAt/stationName: %+v", got[0])
	}
}

func TestRemoveStationRefreshesPresets(t *testing.T) {
	b, c, _, r, _, _ := testBridge(t, session.Options{Dot: "·"})
	now := time.Now()
	c.StartListen("seed:wfmu", now)
	if _, _, _, err := c.TogglePreset(now); err != nil {
		t.Fatalf("TogglePreset: %v", err)
	}
	c.EndListen(now)
	r.Send(session.RefreshPresets{})
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.Slots[0] })

	if err := b.RemoveStation("seed:wfmu"); err != nil {
		t.Fatalf("RemoveStation: %v", err)
	}
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return !s.Slots[0] && s.Feedback == "STATIONS  Removed"
	})
}

func TestRemoveLovedMissingIsQuiet(t *testing.T) {
	b, _, _, r, _, _ := testBridge(t, session.Options{Dot: "·"})
	// Finish startup feedback before checking that a no-op leaves it alone.
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.HaveStation && !s.Syncing
	})
	before := r.Current().Feedback
	if err := b.RemoveLoved("no-such-artist", "No Such Track"); err != nil {
		t.Fatalf("removing an absent track should not error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := r.Current().Feedback; got != before {
		t.Fatalf("no-op removal changed feedback: %q -> %q", before, got)
	}
}

func TestInfoWithoutStation(t *testing.T) {
	b := New(nil, nil, Prefs{})
	_, err := b.Info()
	if err == nil || err.Error() != "nothing tuned yet" {
		t.Fatalf("want 'nothing tuned yet', got %v", err)
	}
}

func TestInfoWithStation(t *testing.T) {
	b, _, _, r, _, _ := testBridge(t, session.Options{Dot: "·"})
	b.Next()
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.HaveStation })
	info, err := b.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	sn := r.Current()
	if info.Name == "" || info.Name != sn.Station.Name {
		t.Fatalf("info name %q vs snapshot %q", info.Name, sn.Station.Name)
	}
}

func TestSinkEmitsSnapshotsAndLevels(t *testing.T) {
	_, _, pl, r, _, em := testBridge(t, session.Options{})
	r.Send(session.Next{})
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool { return s.HaveStation })
	pl.emit(player.Event{Type: player.EventLevel, Level: 0.4, Peak: 0.9, Stereo: false})

	deadline := time.Now().Add(10 * time.Second)
	for {
		evts := em.events()
		var sawSnap, sawLevel bool
		for _, e := range evts {
			switch e.name {
			case "snapshot":
				if _, ok := e.data.(session.Snapshot); !ok {
					t.Fatalf("snapshot payload is %T", e.data)
				}
				sawSnap = true
			case "level":
				lv, ok := e.data.([3]float64)
				if !ok {
					t.Fatalf("level payload is %T", e.data)
				}
				if lv != [3]float64{0.4, 0.4, 0.9} {
					t.Fatalf("level payload %v", lv)
				}
				sawLevel = true
			}
		}
		if sawSnap && sawLevel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing events (snap=%v level=%v) in %+v", sawSnap, sawLevel, evts)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEventsBeforeEmitterDropped(t *testing.T) {
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	b := New(c, &recWindow{}, Prefs{})
	pl := newFakePlayer()
	r := session.NewRunner(session.New(c, pl, session.Options{}), b.Sink())
	b.Attach(r)
	r.Start()
	defer r.Stop()
	time.Sleep(100 * time.Millisecond)
	// No emitter was ever set: nothing recorded, nothing panicked.
	em := &recEmitter{}
	b.SetEmitter(em)
	r.Send(session.Next{}) // a state change the sink must now emit
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range em.events() {
			if e.name == "snapshot" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("snapshot events should flow once an emitter is set")
}

func TestBoundSurface(t *testing.T) {
	want := []string{
		"Attach", "Current", "CycleSleep", "Info", "Love", "Loved",
		"MoreFromArtist", "Next", "Quit", "RecallPreset", "Recent",
		"RemoveLoved", "RemoveStation", "ReturnToStation", "Seed",
		"SetEmitter", "SetUnfolded", "SetVolume", "Sink", "Stations",
		"StepVolume", "Taste", "TogglePause", "TogglePreset", "TuneSaved",
		"Wildcard", "Prefs", "Rescaled",
	}
	typ := reflect.TypeOf((*Bridge)(nil))
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bound surface drifted:\n got %v\nwant %v", got, want)
	}
	for _, forbidden := range []string{"Snapshot", "Level"} {
		for _, m := range got {
			if m == forbidden {
				t.Fatalf("%s must not be bound to JS", forbidden)
			}
		}
	}
}

func TestQuitCallsWindow(t *testing.T) {
	b, _, _, _, win, _ := testBridge(t, session.Options{})
	b.Quit()
	win.mu.Lock()
	defer win.mu.Unlock()
	if win.quits != 1 {
		t.Fatalf("win.Quit calls: %d", win.quits)
	}
}

func TestRescaledCallsWindow(t *testing.T) {
	b, _, _, _, win, _ := testBridge(t, session.Options{})
	b.Rescaled()
	win.mu.Lock()
	defer win.mu.Unlock()
	if win.rescales != 1 {
		t.Fatalf("win.Rescaled calls: %d", win.rescales)
	}
}

func TestSetUnfoldedCallsWindow(t *testing.T) {
	b, _, _, _, win, _ := testBridge(t, session.Options{})
	b.SetUnfolded(true)
	win.mu.Lock()
	defer win.mu.Unlock()
	if len(win.unfolded) != 1 || !win.unfolded[0] {
		t.Fatalf("SetUnfolded did not reach the window: %v", win.unfolded)
	}
}

func TestSeedEmptyIgnored(t *testing.T) {
	b, _, _, r, _, _ := testBridge(t, session.Options{})
	// Let the startup commands land so the snapshot is settled.
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.HaveStation && !s.Syncing
	})
	before := r.Current()
	b.Seed("   ")
	time.Sleep(150 * time.Millisecond)
	if r.Current() != before {
		t.Fatal("blank seed changed the snapshot")
	}
	// Several embedded stations carry ambient, even if one is already playing.
	b.Seed("ambient")
	// Seeking is transient — it flips back off when the pick resolves —
	// so assert the seeded pick's reason instead.
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return strings.Contains(s.Reason, "seeded: ambient")
	})
}
