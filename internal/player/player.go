// Package player abstracts audio playback behind a small interface so the
// mpv backend can be swapped for a pure-Go one later (Path 2) without the
// rest of screech noticing.
package player

type EventType int

const (
	// EventTitle carries a new stream title (ICY StreamTitle).
	EventTitle EventType = iota
	// EventPlaying: audio is actually flowing.
	EventPlaying
	// EventBuffering: connected but stalled/caching.
	EventBuffering
	// EventStreamError: the current stream failed to play.
	EventStreamError
	// EventDied: the backend itself is gone (mpv exited).
	EventDied
	// EventLevel: real audio loudness, 0 (silence) to 1 (loud). Sourced from
	// mpv's astats filter; absent on backends that can't measure.
	EventLevel
)

type Event struct {
	Type  EventType
	Title string
	Level float64
	Err   error
}

type Player interface {
	// Play switches to the given stream URL. Returns quickly; progress
	// arrives as events.
	Play(url string) error
	// SetVolume changes this player's output only, from silent (0) to full (100).
	SetVolume(percent int) error
	// Events is the stream of title/state changes. Closed when the backend dies.
	Events() <-chan Event
	// Close shuts the backend down.
	Close() error
}
