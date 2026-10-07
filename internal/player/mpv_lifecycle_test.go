package player

import (
	"bytes"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"
)

// silentWAV returns a PCM WAV of the given duration: silence only, but
// enough file for mpv to demux, decode, and end on its own.
func silentWAV(dur time.Duration) []byte {
	const rate, chans, bps = 8000, 1, 2
	data := make([]byte, int(dur.Seconds())*rate*chans*bps)
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
		_, _ = w.Write(silentWAV(1500 * time.Millisecond))
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

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(); err == nil {
		t.Error("Restart on a closed MPV should fail")
	}
}
