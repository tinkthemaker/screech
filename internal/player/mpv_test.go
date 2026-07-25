package player

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func newTestMPV() *MPV {
	return &MPV{events: make(chan Event, 64)}
}

// feed delivers one property-change with a string metadata value, bypassing
// the emit throttle so each call is deterministic.
func feed(t *testing.T, m *MPV, name, value string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.lastLevelEmit = time.Time{}
	m.mu.Unlock()
	m.handleProperty(mpvMsg{Event: "property-change", Name: name, Data: raw})
}

func drain(m *MPV) []Event {
	var out []Event
	for {
		select {
		case ev := <-m.events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// wantUnit is the expected 0..1 mapping for a dB reading. Written in terms
// of the documented range so this test checks the plumbing — which astats
// property lands in which field — rather than restating the constants.
func wantUnit(db float64) float64 {
	return (db - meterFloorDB) / (meterCeilDB - meterFloorDB)
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestStereoLevelEventCombinesChannelsAndPeak(t *testing.T) {
	m := newTestMPV()
	feed(t, m, "af-metadata/lavfi.astats.Overall.RMS_level", "-24")
	feed(t, m, "af-metadata/lavfi.astats.1.RMS_level", "-12")
	feed(t, m, "af-metadata/lavfi.astats.2.RMS_level", "-30")
	feed(t, m, "af-metadata/lavfi.astats.Overall.Peak_level", "-6")

	events := drain(m)
	if len(events) != 4 {
		t.Fatalf("want 4 emitted windows, got %d", len(events))
	}
	ev := events[len(events)-1]
	if ev.Type != EventLevel {
		t.Fatalf("event type = %v, want EventLevel", ev.Type)
	}
	if !closeTo(ev.Level, wantUnit(-24)) {
		t.Errorf("overall level = %v, want %v", ev.Level, wantUnit(-24))
	}
	if !closeTo(ev.LevelL, wantUnit(-12)) {
		t.Errorf("left level = %v, want %v", ev.LevelL, wantUnit(-12))
	}
	if !closeTo(ev.LevelR, wantUnit(-30)) {
		t.Errorf("right level = %v, want %v", ev.LevelR, wantUnit(-30))
	}
	if !closeTo(ev.Peak, wantUnit(-6)) {
		t.Errorf("peak = %v, want %v", ev.Peak, wantUnit(-6))
	}
	if !ev.Stereo {
		t.Error("both channels reported, so the event should be flagged stereo")
	}
}

func TestMonoStreamMirrorsChannelIntoBoth(t *testing.T) {
	m := newTestMPV()
	feed(t, m, "af-metadata/lavfi.astats.1.RMS_level", "-24")

	events := drain(m)
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if !closeTo(ev.LevelL, wantUnit(-24)) || !closeTo(ev.LevelR, wantUnit(-24)) {
		t.Errorf("mono should mirror channel 1 into both: L=%v R=%v", ev.LevelL, ev.LevelR)
	}
	if ev.Stereo {
		t.Error("only channel 1 reported, so the event must not claim stereo")
	}
}

// The meter's whole job is showing the difference between a quiet stream
// and a loud one. Mapping the full -48..0 spent most of the scale on levels
// no broadcast stream ever sends, so everything real bunched up in a narrow
// band and the needle barely moved.
func TestMeterRangeCoversRealBroadcastLevels(t *testing.T) {
	quiet, typical, loud := dbToUnit(-30), dbToUnit(-14), dbToUnit(-6)
	t.Logf("quiet(-30dB)=%.2f  typical(-14dB)=%.2f  loud(-6dB)=%.2f", quiet, typical, loud)
	if typical-quiet < 0.30 {
		t.Errorf("a quiet stream and a typical one differ by only %.2f of the scale", typical-quiet)
	}
	if loud-typical < 0.15 {
		t.Errorf("a typical stream and a loud one differ by only %.2f of the scale", loud-typical)
	}
	if typical < 0.5 || typical > 0.85 {
		t.Errorf("typical broadcast loudness sits at %.2f; it should land in the meter's upper middle", typical)
	}
	if dbToUnit(-60) != 0 || dbToUnit(0) != 1 {
		t.Error("the curve must still clamp outside its range")
	}
}

func TestLevelEmitsAreThrottled(t *testing.T) {
	m := newTestMPV()
	raw, _ := json.Marshal("-12")
	msg := mpvMsg{Event: "property-change", Name: "af-metadata/lavfi.astats.1.RMS_level", Data: raw}

	m.handleProperty(msg) // first emit goes through immediately
	for i := 0; i < 10; i++ {
		m.handleProperty(msg) // same 50ms window: dropped
	}
	if n := len(drain(m)); n != 1 {
		t.Fatalf("throttle should emit once per window, got %d", n)
	}

	m.mu.Lock()
	m.lastLevelEmit = time.Now().Add(-time.Second) // next window
	m.mu.Unlock()
	m.handleProperty(msg)
	if n := len(drain(m)); n != 1 {
		t.Fatalf("a new window should emit again, got %d", n)
	}
}

func TestParseDBAcceptsStringsAndNumbers(t *testing.T) {
	if db, err := parseDB(json.RawMessage(`"-23.5"`)); err != nil || db != -23.5 {
		t.Errorf("string form: db=%v err=%v", db, err)
	}
	if db, err := parseDB(json.RawMessage(`-23.5`)); err != nil || db != -23.5 {
		t.Errorf("number form: db=%v err=%v", db, err)
	}
	if _, err := parseDB(json.RawMessage(`"n/a"`)); err == nil {
		t.Error("unparseable metadata should error, not silently meter")
	}
	// ffmpeg prints -inf for digital silence; it parses, and clamps to 0.
	if db, err := parseDB(json.RawMessage(`"-inf"`)); err != nil {
		t.Errorf("-inf should parse: %v", err)
	} else if v := dbToUnit(db); v != 0 {
		t.Errorf("-inf should meter as 0, got %v", v)
	}
}

func TestDBToUnitClamps(t *testing.T) {
	if v := dbToUnit(0); v != 1 {
		t.Errorf("0 dB = %v, want 1", v)
	}
	if v := dbToUnit(-48); v != 0 {
		t.Errorf("-48 dB = %v, want 0", v)
	}
	if v := dbToUnit(-96); v != 0 {
		t.Errorf("-96 dB = %v, want clamped 0", v)
	}
	if v := dbToUnit(3); v != 1 {
		t.Errorf("+3 dB = %v, want clamped 1", v)
	}
}
