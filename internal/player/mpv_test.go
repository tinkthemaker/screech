package player

import (
	"encoding/json"
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

func TestStereoLevelEventCombinesChannelsAndPeak(t *testing.T) {
	m := newTestMPV()
	feed(t, m, "af-metadata/lavfi.astats.Overall.RMS_level", "-24")
	feed(t, m, "af-metadata/lavfi.astats.1.RMS_level", "-12")
	feed(t, m, "af-metadata/lavfi.astats.2.RMS_level", "-36")
	feed(t, m, "af-metadata/lavfi.astats.Overall.Peak_level", "-6")

	events := drain(m)
	if len(events) != 4 {
		t.Fatalf("want 4 emitted windows, got %d", len(events))
	}
	ev := events[len(events)-1]
	if ev.Type != EventLevel {
		t.Fatalf("event type = %v, want EventLevel", ev.Type)
	}
	if ev.Level != 0.5 {
		t.Errorf("overall level = %v, want 0.5 (1 + -24/48)", ev.Level)
	}
	if ev.LevelL != 0.75 {
		t.Errorf("left level = %v, want 0.75", ev.LevelL)
	}
	if ev.LevelR != 0.25 {
		t.Errorf("right level = %v, want 0.25", ev.LevelR)
	}
	if ev.Peak != 0.875 {
		t.Errorf("peak = %v, want 0.875 (1 + -6/48)", ev.Peak)
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
	if ev.LevelL != 0.5 || ev.LevelR != 0.5 {
		t.Errorf("mono should mirror channel 1 into both: L=%v R=%v", ev.LevelL, ev.LevelR)
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
