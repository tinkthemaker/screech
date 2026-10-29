package session

import (
	"fmt"
	"sync"

	"github.com/tinkthemaker/screech/internal/player"
)

type stubPlayer struct {
	mu       sync.Mutex // like MPV's: the event channel swaps under it
	ch       chan player.Event
	volume   int
	playURLs []string
	playErr  error // injected Play failure
	paused   bool
	calls    []string // ordered log across Play/SetVolume/SetPaused/Restart
	restarts int
	closed   bool
	playGate chan struct{} // non-nil: Play blocks on it after logging
}

func newStubPlayer() *stubPlayer { return &stubPlayer{ch: make(chan player.Event, 8)} }
func (s *stubPlayer) Play(url string) error {
	s.mu.Lock()
	s.playURLs = append(s.playURLs, url)
	s.calls = append(s.calls, "play "+url)
	gate := s.playGate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return s.playErr
}
func (s *stubPlayer) SetVolume(percent int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = percent
	s.calls = append(s.calls, fmt.Sprintf("vol %d", percent))
	return nil
}
func (s *stubPlayer) SetPaused(paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = paused
	s.calls = append(s.calls, fmt.Sprintf("pause %t", paused))
	return nil
}
func (s *stubPlayer) Events() <-chan player.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ch
}

// Restart swaps in a fresh event channel like a real backend respawn; the
// old channel is left open — nobody should be listening to it anymore.
func (s *stubPlayer) Restart() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts++
	s.calls = append(s.calls, "restart")
	s.ch = make(chan player.Event, 8)
	return nil
}
func (s *stubPlayer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.calls = append(s.calls, "close")
		close(s.ch)
	}
	return nil
}

// emit pushes an event the way a live backend would.
func (s *stubPlayer) emit(ev player.Event) {
	s.mu.Lock()
	ch := s.ch
	s.mu.Unlock()
	ch <- ev
}

func (s *stubPlayer) lastPlay() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.playURLs) == 0 {
		return ""
	}
	return s.playURLs[len(s.playURLs)-1]
}

func (s *stubPlayer) restartCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

func (s *stubPlayer) volumeLevel() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.volume
}

func (s *stubPlayer) failPlay(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playErr = err
}

// callsList returns the ordered command log — tests assert pause-then-
// restore and resume-vs-reload order without parsing playURLs alone.
func (s *stubPlayer) callsList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}
