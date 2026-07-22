package player

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestIsURLish(t *testing.T) {
	cases := map[string]bool{
		"http://example.com":   true,
		"https://example.com":  true,
		"http://a":             true,
		"https://a":            true,
		"Aphex Twin - Rhubarb": false,
		"":                     false,
		"http":                 false,
		"http://":              false, // len == 7, not > 7
		"https:/":              false,
		"ftp://example.com":    false,
	}
	for in, want := range cases {
		if got := isURLish(in); got != want {
			t.Errorf("isURLish(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestEqualFold(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"icy-title", "ICY-TITLE", true},
		{"Icy-Title", "icy-title", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false},
		{"", "", true},
		{"MixedCase123", "mixedcase123", true},
	}
	for _, tc := range cases {
		if got := equalFold(tc.a, tc.b); got != tc.want {
			t.Errorf("equalFold(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIPCPathUnique(t *testing.T) {
	p := ipcPath()
	if p == "" {
		t.Fatal("ipcPath returned empty string")
	}
	if !strings.Contains(p, "screech-mpv-") {
		t.Errorf("ipcPath = %q, want it to contain screech-mpv-", p)
	}
	// Stable within a process (same PID).
	if p2 := ipcPath(); p2 != p {
		t.Errorf("ipcPath not stable: %q vs %q", p, p2)
	}
}

func TestSendNotConnected(t *testing.T) {
	m := &MPV{}
	if err := m.send("quit"); err == nil {
		t.Fatal("send with nil conn should error")
	}
}

func TestPlayResetsIcyAndErrorsWithoutConn(t *testing.T) {
	m := &MPV{sawIcy: true}
	// Play with no connection still errors (send fails), but must clear sawIcy.
	if err := m.Play("http://stream"); err == nil {
		t.Fatal("Play with nil conn should error")
	}
	if m.sawIcy {
		t.Error("Play should reset sawIcy to false")
	}
}

func TestCloseIdempotent(t *testing.T) {
	m := &MPV{}
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if !m.closed {
		t.Error("Close should mark closed")
	}
	// Second close is a no-op and must not panic.
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestEventsAccessor(t *testing.T) {
	ch := make(chan Event, 1)
	m := &MPV{events: ch}
	if m.Events() == nil {
		t.Fatal("Events returned nil")
	}
	ch <- Event{Type: EventPlaying}
	if ev := <-m.Events(); ev.Type != EventPlaying {
		t.Errorf("got event type %v, want EventPlaying", ev.Type)
	}
}

func TestEmitDropsWhenFull(t *testing.T) {
	m := &MPV{events: make(chan Event, 1)}
	m.emit(Event{Type: EventPlaying}) // fills the buffer
	// A second emit must not block; the event is dropped instead.
	done := make(chan struct{})
	go func() {
		m.emit(Event{Type: EventTitle, Title: "dropped"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emit blocked on a full channel")
	}
	// Only the first event should be present.
	if ev := <-m.events; ev.Type != EventPlaying {
		t.Errorf("first event = %v, want EventPlaying", ev.Type)
	}
}

// newTestMPV builds an MPV with a buffered event channel for handleProperty
// tests, and a helper to read the next event without blocking.
func newTestMPV() (*MPV, func(t *testing.T) (Event, bool)) {
	m := &MPV{events: make(chan Event, 8)}
	next := func(t *testing.T) (Event, bool) {
		t.Helper()
		select {
		case ev := <-m.events:
			return ev, true
		default:
			return Event{}, false
		}
	}
	return m, next
}

func prop(name string, data any) mpvMsg {
	raw, _ := json.Marshal(data)
	return mpvMsg{Event: "property-change", Name: name, Data: raw}
}

func TestHandlePropertyMetadataICY(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("metadata", map[string]string{"icy-title": "Boards of Canada - Roygbiv"}))

	ev, ok := next(t)
	if !ok {
		t.Fatal("expected a title event")
	}
	if ev.Type != EventTitle || ev.Title != "Boards of Canada - Roygbiv" {
		t.Errorf("got %+v, want EventTitle with icy-title", ev)
	}
	if !m.sawIcy {
		t.Error("sawIcy should be set after ICY metadata")
	}
}

func TestHandlePropertyMetadataEmptyICY(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("metadata", map[string]string{"icy-title": ""}))
	if _, ok := next(t); ok {
		t.Error("empty icy-title should not emit")
	}
	if m.sawIcy {
		t.Error("empty icy-title should not set sawIcy")
	}
}

func TestHandlePropertyMediaTitleFallback(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("media-title", "Some Station Name"))
	ev, ok := next(t)
	if !ok {
		t.Fatal("expected media-title fallback event")
	}
	if ev.Type != EventTitle || ev.Title != "Some Station Name" {
		t.Errorf("got %+v, want EventTitle fallback", ev)
	}
}

func TestHandlePropertyMediaTitleSuppressed(t *testing.T) {
	// URL-ish titles are ignored.
	m, next := newTestMPV()
	m.handleProperty(prop("media-title", "http://stream.example.com/x"))
	if _, ok := next(t); ok {
		t.Error("URL-ish media-title should be suppressed")
	}

	// Once ICY has been seen, media-title is no longer trusted.
	m2, next2 := newTestMPV()
	m2.sawIcy = true
	m2.handleProperty(prop("media-title", "Fallback"))
	if _, ok := next2(t); ok {
		t.Error("media-title should be suppressed after ICY seen")
	}
}

func TestHandlePropertyCoreIdle(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("core-idle", false))
	ev, ok := next(t)
	if !ok || ev.Type != EventPlaying {
		t.Errorf("core-idle=false should emit EventPlaying, got %+v ok=%v", ev, ok)
	}

	m2, next2 := newTestMPV()
	m2.handleProperty(prop("core-idle", true))
	if _, ok := next2(t); ok {
		t.Error("core-idle=true should not emit")
	}
}

func TestHandlePropertyPausedForCache(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("paused-for-cache", true))
	if ev, ok := next(t); !ok || ev.Type != EventBuffering {
		t.Errorf("paused-for-cache=true should emit EventBuffering, got %+v ok=%v", ev, ok)
	}

	m2, next2 := newTestMPV()
	m2.handleProperty(prop("paused-for-cache", false))
	if ev, ok := next2(t); !ok || ev.Type != EventPlaying {
		t.Errorf("paused-for-cache=false should emit EventPlaying, got %+v ok=%v", ev, ok)
	}
}

func TestHandlePropertyLevel(t *testing.T) {
	m, next := newTestMPV()
	// -24 dB -> 1 + (-24/48) = 0.5
	m.handleProperty(prop("af-metadata/lavfi.astats.Overall.RMS_level", "-24.0"))
	ev, ok := next(t)
	if !ok || ev.Type != EventLevel {
		t.Fatalf("expected EventLevel, got %+v ok=%v", ev, ok)
	}
	if ev.Level < 0.49 || ev.Level > 0.51 {
		t.Errorf("Level = %v, want ~0.5", ev.Level)
	}
}

func TestHandlePropertyLevelClamp(t *testing.T) {
	// Very loud clamps to 1, very quiet clamps to 0.
	m, next := newTestMPV()
	m.handleProperty(prop("af-metadata/lavfi.astats.Overall.RMS_level", "20"))
	if ev, ok := next(t); !ok || ev.Level != 1 {
		t.Errorf("loud level should clamp to 1, got %+v ok=%v", ev, ok)
	}

	m2, next2 := newTestMPV()
	m2.handleProperty(prop("af-metadata/lavfi.astats.Overall.RMS_level", "-100"))
	if ev, ok := next2(t); !ok || ev.Level != 0 {
		t.Errorf("quiet level should clamp to 0, got %+v ok=%v", ev, ok)
	}
}

func TestHandlePropertyLevelThrottled(t *testing.T) {
	m, next := newTestMPV()
	m.lastLevelEmit = time.Now()
	// Immediately after a prior emit, within 50ms window: dropped.
	m.handleProperty(prop("af-metadata/lavfi.astats.Overall.RMS_level", "-24"))
	if _, ok := next(t); ok {
		t.Error("level within throttle window should be dropped")
	}
}

func TestHandlePropertyLevelBadValue(t *testing.T) {
	m, next := newTestMPV()
	m.handleProperty(prop("af-metadata/lavfi.astats.Overall.RMS_level", "not-a-number"))
	if _, ok := next(t); ok {
		t.Error("unparseable level should not emit")
	}
}
