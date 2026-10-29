package player

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// pipeMPV wires a real readLoop onto a net.Pipe: the test end of the pipe
// is what mpv would be — writes events, drains commands (net.Pipe is
// synchronous, so writes must be drained or send() blocks).
func pipeMPV(t *testing.T) (*MPV, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	m := &MPV{conn: a, events: make(chan Event, 64), stderr: newTailBuf()}
	exited := make(chan struct{})
	close(exited) // no process behind a pipe: the drain wait is a no-op
	go m.readLoop(a, m.events, m.stderr, exited)
	go func() { _, _ = io.Copy(io.Discard, b) }()
	t.Cleanup(func() { _ = b.Close() })
	return m, b
}

func sendLine(t *testing.T, conn net.Conn, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(conn, line); err != nil {
		t.Fatal(err)
	}
}

// wantEvent waits for the first event of a type, discarding others — real
// playback interleaves EventLevel constantly, so strict ordering would be
// flaky.
func wantEvent(t *testing.T, m *MPV, want EventType) Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case ev, ok := <-m.events:
			if !ok {
				t.Fatalf("event channel closed, want %v", want)
			}
			if ev.Type == want {
				return ev
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timeout waiting for %v", want)
			return Event{}
		}
	}
}

func wantQuiet(t *testing.T, m *MPV) {
	t.Helper()
	select {
	case ev := <-m.events:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestEndFileErrorCarriesFileError(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"end-file","reason":"error","file_error":"loading failed"}`)
	ev := wantEvent(t, m, EventStreamError)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "loading failed") {
		t.Fatalf("Err = %v, want file_error detail", ev.Err)
	}
}

func TestEndFileErrorWithoutDetailStillFails(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"end-file","reason":"error"}`)
	ev := wantEvent(t, m, EventStreamError)
	if ev.Err == nil {
		t.Fatal("Err must be set even without file_error")
	}
}

func TestEndFileEOFIsAStreamDrop(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"end-file","reason":"eof"}`)
	wantEvent(t, m, EventStreamEnded)
}

func TestEndFileOtherReasonsIgnored(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"end-file","reason":"quit"}`)
	wantQuiet(t, m)
}

// loadfile retires the old file before the new one starts: mpv reports that
// retirement as an end-file, and without the pending flag it looks like the
// NEW stream instantly died.
func TestPendingLoadDropsStaleEndFile(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := m.Play("http://radio.example/live"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer, `{"event":"end-file","reason":"error","file_error":"connection refused"}`)
	wantQuiet(t, m)
	sendLine(t, peer, `{"event":"start-file"}`)
	sendLine(t, peer, `{"event":"end-file","reason":"eof"}`)
	wantEvent(t, m, EventStreamEnded)
}

func TestConnDeathEmitsDiedThenCloses(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	ev := wantEvent(t, m, EventDied)
	if ev.Err == nil {
		t.Fatal("EventDied should carry an error")
	}
	select {
	case _, ok := <-m.events:
		if ok {
			t.Fatal("channel must close after EventDied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after conn death")
	}
}

func TestIcyTitleThenMediaTitleSuppressed(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"property-change","name":"metadata","data":{"icy-title":"Bill Evans - Waltz"}}`)
	if ev := wantEvent(t, m, EventTitle); ev.Title != "Bill Evans - Waltz" {
		t.Fatalf("title = %q", ev.Title)
	}
	// media-title would be a duplicate once ICY metadata is flowing.
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"Bill Evans - Waltz"}`)
	wantQuiet(t, m)
}

func TestMediaTitleAloneStillEmitted(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"Night Session"}`)
	if ev := wantEvent(t, m, EventTitle); ev.Title != "Night Session" {
		t.Fatalf("title = %q", ev.Title)
	}
}

// mpv fills media-title with the URL's last path segment until ICY metadata
// arrives — a filename must never surface as a track title.
func TestMediaTitleFilenameIsSuppressed(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := m.Play("https://h/x/aac-128"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"aac-128"}`)
	wantQuiet(t, m)
}

func TestMediaTitleEscapedFilenameIsSuppressed(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := m.Play("https://h/k%20exp.mp3"); err != nil {
		t.Fatal(err)
	}
	// Both the decoded and still-escaped forms are rejected.
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"k exp.mp3"}`)
	wantQuiet(t, m)
	m2, peer2 := pipeMPV(t)
	if err := m2.Play("https://h/k%20exp.mp3"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer2, `{"event":"property-change","name":"media-title","data":"k%20exp.mp3"}`)
	wantQuiet(t, m2)
}

func TestMediaTitleRealTitleStillEmits(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := m.Play("https://h/x/aac-128"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"Artist - Song"}`)
	if ev := wantEvent(t, m, EventTitle); ev.Title != "Artist - Song" {
		t.Fatalf("title = %q", ev.Title)
	}
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"Who Are You?"}`)
	if ev := wantEvent(t, m, EventTitle); ev.Title != "Who Are You?" {
		t.Fatalf("title = %q", ev.Title)
	}
}

// With no path segment at all, mpv's media-title fallback is the decoded
// text after the last '/' of the whole URL — the query comes with it.
func TestMediaTitleQueryFilenameIsSuppressed(t *testing.T) {
	m, peer := pipeMPV(t)
	if err := m.Play("https://h/rqqps6cbe3quv?rj-ttl=5&rj-tok=AA"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"rqqps6cbe3quv?rj-ttl=5&rj-tok=AA"}`)
	wantQuiet(t, m)
	m2, peer2 := pipeMPV(t)
	if err := m2.Play("https://h/s?tok=AA%2FBB%20cc"); err != nil {
		t.Fatal(err)
	}
	sendLine(t, peer2, `{"event":"property-change","name":"media-title","data":"BB cc"}`)
	wantQuiet(t, m2)
}

// A media-title equal to the filename property mpv last observed is the
// same fallback — needed for playlist entries whose URL we never saw.
func TestMediaTitleMpvFilenameIsSuppressed(t *testing.T) {
	m, peer := pipeMPV(t)
	sendLine(t, peer, `{"event":"property-change","name":"filename","data":"plentry-128.mp3"}`)
	sendLine(t, peer, `{"event":"property-change","name":"media-title","data":"plentry-128.mp3"}`)
	wantQuiet(t, m)
}

func TestPlayRejectsBadURLs(t *testing.T) {
	m, _ := pipeMPV(t)
	for _, u := range []string{"ftp://x", "file:///x", "http://", "not a url"} {
		if err := m.Play(u); err == nil {
			t.Errorf("Play(%q) should be rejected", u)
		}
	}
}
