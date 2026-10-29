package gui

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tinkthemaker/screech/internal/core"
	"github.com/tinkthemaker/screech/internal/session"
)

// The bridge half of the FIND front door: one intent each for Discover and
// ResumeDiscovery, and two small core reads for the lists under the bar.

// recordIntents taps the bridge's send so a test sees exactly the messages
// an intent produced, while the runner still gets them.
func recordIntents(b *Bridge) *[]session.Msg {
	var got []session.Msg
	orig := b.send
	b.send = func(m session.Msg) {
		got = append(got, m)
		orig(m)
	}
	return &got
}

func TestDiscoverIsOneIntent(t *testing.T) {
	b, _, _, _, _, _ := testBridge(t, session.Options{})
	got := recordIntents(b)

	b.Discover("   ")
	if len(*got) != 0 {
		t.Fatalf("a blank phrase sent %v, want nothing", *got)
	}

	// Trimmed, not re-cased: the core's DiscoveryKey decides what makes two
	// phrases the same station.
	b.Discover("  Swedish Death Metal ")
	want := []session.Msg{session.Touch{}, session.Discover{Query: "Swedish Death Metal"}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("Discover sent %v, want %v", *got, want)
	}
}

func TestResumeDiscoveryTunesIt(t *testing.T) {
	b, c, _, r, _, _ := testBridge(t, session.Options{})
	waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.HaveStation && !s.Syncing
	})
	p, err := c.StartDiscovery("dungeon synth", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := recordIntents(b)
	b.ResumeDiscovery(p.Station.UUID)
	want := []session.Msg{session.Touch{}, session.TuneTo{UUID: p.Station.UUID, Reason: "discovering"}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("ResumeDiscovery sent %v, want %v", *got, want)
	}
	sn := waitSnap(t, r, 10*time.Second, func(s session.Snapshot) bool {
		return s.Station.UUID == p.Station.UUID
	})
	if sn.Reason != "discovering" {
		t.Fatalf("reason %q, want %q", sn.Reason, "discovering")
	}
}

// coreOnlyBridge is a bridge over a bare core, for the reads that need no
// runner.
func coreOnlyBridge(t *testing.T) (*Bridge, *core.Core) {
	t.Helper()
	c, err := core.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return New(c, &recWindow{}, Prefs{}), c
}

func TestRecentDiscoveriesRows(t *testing.T) {
	b, c := coreOnlyBridge(t)

	// Nothing yet is an empty list, never null: the page reads .length.
	if rows := b.RecentDiscoveries(); rows == nil || len(rows) != 0 {
		t.Fatalf("empty history gave %#v, want a non-nil empty slice", rows)
	}

	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	t1 := t0.Add(24 * time.Hour)
	one, err := c.StartDiscovery("swedish death metal", t0)
	if err != nil {
		t.Fatal(err)
	}
	two, err := c.StartDiscovery("dungeon synth", t0)
	if err != nil {
		t.Fatal(err)
	}
	c.StartListen(one.Station.UUID, t0)
	c.EndListen(t0.Add(time.Minute))
	c.StartListen(two.Station.UUID, t1)
	c.EndListen(t1.Add(time.Minute))
	// Five older ones, so the cap of four shows.
	for _, seed := range []string{"a", "b", "c", "d", "e"} {
		p, err := c.StartDiscovery(seed, t0)
		if err != nil {
			t.Fatal(err)
		}
		c.StartListen(p.Station.UUID, t0.Add(-time.Hour))
		c.EndListen(t0.Add(-time.Hour + time.Minute))
	}

	rows := b.RecentDiscoveries()
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want the 4 FIND shows: %+v", len(rows), rows)
	}
	if rows[0].UUID != two.Station.UUID || rows[0].Name != "dungeon synth" || rows[0].LastAtMs != t1.UnixMilli() {
		t.Fatalf("row 0 = %+v, want dungeon synth at %d", rows[0], t1.UnixMilli())
	}
	if rows[1].UUID != one.Station.UUID {
		t.Fatalf("row 1 = %+v, want swedish death metal", rows[1])
	}
}

func TestRadioTopLeavesOutOwnStations(t *testing.T) {
	b, c := coreOnlyBridge(t)

	if rows := b.RadioTop(); rows == nil || len(rows) != 0 {
		t.Fatalf("empty history gave %#v, want a non-nil empty slice", rows)
	}

	t0 := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	at := t0
	listen := func(uuid string, d time.Duration) {
		c.StartListen(uuid, at)
		c.EndListen(at.Add(d))
		at = at.Add(d + time.Minute)
	}
	disc, err := c.StartDiscovery("blackgaze", t0)
	if err != nil {
		t.Fatal(err)
	}
	// The listener's own stations out-listen every radio station here; the
	// radio list must not see them.
	listen(disc.Station.UUID, 5*time.Hour)
	listen("custom:0123456789abcdef", 4*time.Hour)
	listen("seed:somafm-groovesalad", 3*time.Hour)
	listen("seed:wfmu", 2*time.Hour)
	listen("seed:somafm-deepspaceone", time.Hour)

	// A preset and a loved track decorate the row the way STATIONS does.
	c.StartListen("seed:wfmu", at)
	if _, _, _, err := c.TogglePreset(at); err != nil {
		t.Fatal(err)
	}
	c.NoteTitle("Broadcast - Come On Let's Go", at)
	c.Love(at)
	c.EndListen(at.Add(time.Second))

	rows := b.RadioTop()
	var got []string
	for _, r := range rows {
		got = append(got, r.UUID)
	}
	want := []string{"seed:somafm-groovesalad", "seed:wfmu", "seed:somafm-deepspaceone"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RadioTop = %v, want %v", got, want)
	}
	if rows[0].Name == "" || rows[0].TotalSec != int64(3*time.Hour/time.Second) {
		t.Fatalf("row 0 = %+v, want a named row with 3 h", rows[0])
	}
	if rows[1].PresetSlot == 0 || rows[1].LoveCount != 1 {
		t.Fatalf("row 1 = %+v, want its preset slot and one love", rows[1])
	}
}

func TestRadioTopStopsAtSix(t *testing.T) {
	b, c := coreOnlyBridge(t)
	at := time.Now().Add(-10 * time.Hour).Truncate(time.Second)
	for i, uuid := range []string{
		"seed:a", "seed:b", "seed:c", "seed:d", "seed:e", "seed:f", "seed:g", "seed:h",
	} {
		c.StartListen(uuid, at)
		c.EndListen(at.Add(time.Duration(10-i) * time.Minute))
		at = at.Add(time.Hour)
	}
	if rows := b.RadioTop(); len(rows) != 6 || rows[0].UUID != "seed:a" {
		t.Fatalf("RadioTop = %+v, want the six most listened, seed:a first", rows)
	}
}
