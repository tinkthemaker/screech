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
	// EventStreamError: the current stream failed to play. On a discovery
	// station it means the station failed: three songs in a row would not
	// play, or the next song could not be looked up for want of a network,
	// in which case Err wraps ErrSourceNetwork.
	EventStreamError
	// EventStreamEnded: the current stream ended on its own. For live
	// radio that means the server dropped us — mpv does not reconnect
	// non-seekable ICY streams — not that the programme finished. A
	// discovery station never emits it: songs end all the time, and the
	// player simply loads the next.
	EventStreamEnded
	// EventDied: the backend itself is gone (mpv exited).
	EventDied
	// EventLevel: real audio loudness, 0 (silence) to 1 (loud). Sourced from
	// mpv's astats filter; absent on backends that can't measure.
	EventLevel
	// EventPaused: output paused, by screech or by anything else driving
	// mpv — media keys, the OS media flyout, MPRIS.
	EventPaused
	// EventResumed: output unpaused again, same sources as EventPaused.
	EventResumed
	// EventStopped: playback was stopped from outside (the mpv `stop`
	// command — nothing is loaded any more). Screech never sends stop.
	EventStopped
)

type Event struct {
	Type  EventType
	Title string
	// Level is the mono mixdown loudness 0..1 (EventLevel). LevelL and
	// LevelR are the per-channel loudness for the same window — a mono
	// stream mirrors one value into both — and Peak is the true digital
	// peak. All derive from mpv's astats filter with the same dB curve.
	Level  float64
	LevelL float64
	LevelR float64
	Peak   float64
	// Stereo reports whether the backend is actually measuring two
	// channels. A mono stream — or an astats build that only reports
	// channel 1 — mirrors one value into both, which makes the meter's two
	// halves identical. The UI says so rather than implying a stereo image
	// that isn't there.
	Stereo bool
	// NewToYou rides EventTitle while a discovery station's song queue
	// plays: the song's artist is one the listener has never heard here.
	// Radio titles never set it.
	NewToYou bool
	Err      error
}

type Player interface {
	// Play switches to the given stream URL. Returns quickly; progress
	// arrives as events.
	Play(url string) error
	// SetVolume changes this player's output only, from silent (0) to full (100).
	SetVolume(percent int) error
	// SetPaused pauses or resumes output without unloading the stream.
	SetPaused(paused bool) error
	// Events is the stream of title/state changes. Closed when the backend dies.
	Events() <-chan Event
	// Restart replaces a dead backend with a fresh process; Events()
	// returns the new stream afterwards.
	Restart() error
	// Close shuts the backend down.
	Close() error
}
