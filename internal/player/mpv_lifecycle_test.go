package player

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

// sineWAV returns a stereo PCM WAV of a mid-level sine: astats reports
// -inf on silence, so a meter assertion needs real signal.
func sineWAV(dur time.Duration) []byte {
	const rate, chans, bps = 8000, 2, 2
	n := int(dur.Seconds()) * rate
	data := make([]byte, n*chans*bps)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(data[i*4+2:], uint16(v))
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))              // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(chans))          // channels
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))           // rate
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*chans*bps)) // byte rate
	_ = binary.Write(&b, binary.LittleEndian, uint16(chans*bps))      // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(8*bps))          // bits
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	_, _ = b.Write(data)
	return b.Bytes()
}

// waitLevel fails unless an EventLevel carrying real signal arrives
// within the deadline.
func waitLevel(t *testing.T, m *MPV, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-m.Events():
			if !ok {
				t.Fatal("event channel closed before any EventLevel arrived")
			}
			if ev.Type == EventLevel && ev.LevelL > 0 {
				return
			}
			if ev.Type == EventStreamEnded {
				t.Fatal("stream ended before any EventLevel arrived")
			}
		case <-deadline:
			t.Fatal("no EventLevel with signal before the deadline")
		}
	}
}

// TestMPVLifecycle drives a real mpv through the whole spawn → play → eof →
// kill → restart arc. Skipped without mpv on PATH or under -short.
func TestMPVLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("real mpv process; skipping in -short mode")
	}
	mpvPath, err := exec.LookPath("mpv")
	if err != nil {
		t.Skipf("mpv not on PATH: %v", err)
	}
	testExtraArgs = []string{"--ao=null"} // CI hosts can lack audio hardware
	t.Cleanup(func() { testExtraArgs = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(sineWAV(2500 * time.Millisecond))
	}))
	defer srv.Close()

	m, err := NewMPV(mpvPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Play(srv.URL + "/radio.wav"); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventPlaying)

	// The meter must carry real astats data: an unlabelled lavfi filter
	// left the observed keys empty and the wave flat forever.
	waitLevel(t, m, 5*time.Second)
	wantEvent(t, m, EventStreamEnded) // finite file ends on its own

	// Killing the process must surface as EventDied + a closed channel —
	// the readLoop's own teardown, not Close().
	if err := m.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventDied)
	select {
	case _, ok := <-m.Events():
		if ok {
			t.Fatal("channel must close after EventDied")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after process kill")
	}

	if err := m.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if err := m.Play(srv.URL + "/radio.wav"); err != nil {
		t.Fatal(err)
	}
	// Events() now yields the new process's stream — playing again proves
	// the restarted IPC link is live.
	wantEvent(t, m, EventPlaying)

	// Pause/resume round-trip against the real property.
	if err := m.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventPaused)
	if err := m.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, m, EventResumed)

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(); err == nil {
		t.Error("Restart on a closed MPV should fail")
	}
}
