package player

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tinkthemaker/screech/internal/streamurl"
)

// A discovery station has no stream to hold: its URL is the sentinel
// discover:<seed>, and what it plays is a walk through songs, each one a
// separate file. The player owns that walk. It loads a song, looks one up
// ahead while it plays, and loads the next the moment the first ends. Between
// songs it never emits EventStreamEnded: the session reads that as a radio
// stream the server dropped and would reconnect the same URL, restarting the
// station on every track change.

const discoverScheme = "discover:"

// IsDiscoverURL reports whether u is a discovery station's sentinel URL.
func IsDiscoverURL(u string) bool { return strings.HasPrefix(u, discoverScheme) }

// ErrSourceNetwork is what a SongSource wraps around a lookup that failed
// because the network is down, as opposed to a song that simply wouldn't
// resolve. Play (and EventStreamError, once a station is under way) pass it
// through, so the session takes its offline path instead of striking a
// station that did nothing wrong.
var ErrSourceNetwork = errors.New("player: song lookup failed on the network")

// Song is one playable track from a SongSource.
type Song struct {
	Key, Artist, Title string
	URL                string // the direct media URL: signed and short-lived, so never stored or logged
	NewToYou           bool
	Anchor             bool // a well-known track, not a find: loving it says nothing about the finds
}

// SongSource is the endless supply behind a discovery station. Next gets
// the following song; it is called for the first song under a short budget
// and, ahead of need, for the one after that. Skipped and Loved report what
// the listener did with the song now playing. The player calls them from its
// own goroutines, with a lookup possibly in flight, so an implementation must
// be safe for concurrent use.
type SongSource interface {
	Next(ctx context.Context) (Song, error)
	Skipped(quick bool)
	Loved()
}

// SongQueue is the part of the player a face reaches for while a discovery
// station plays. *MPV implements it; the session type-asserts for it.
type SongQueue interface {
	// NextSong moves on to the next song and returns at once: the song
	// being replaced keeps playing until its successor is found, and a
	// failure to find one arrives as an event, like any other end of stream.
	NextSong() error
	// SongLoved tells the source the song now playing was loved.
	SongLoved()
}

var _ SongQueue = (*MPV)(nil)

const (
	// firstSongBudget bounds the lookups behind the first song. The tune
	// watchdog gives a station 12 s from the pick to lock; this leaves mpv
	// the rest.
	firstSongBudget = 8 * time.Second
	// prefetchBudget is for the lookup of the song after the current one.
	// Nobody is waiting on it, so yt-dlp may take its time.
	prefetchBudget = 20 * time.Second
	// quickSkip is how long a song must play for NEXT not to count as a
	// quick skip.
	quickSkip = 60 * time.Second
	// songURLTTL keeps a prefetched media URL well inside the hours a
	// signed one stays valid. It only matters for songs longer than an hour.
	songURLTTL = time.Hour
	// maxSongFails is how many songs in a row may fail before the station
	// does. The session strikes it and moves on, as for a dead radio stream.
	maxSongFails = 3
	// oldNetworkError is how stale a prefetch's network failure may be and
	// still end the walk. The prefetch starts when a song loads and is read
	// when it ends, minutes later; an outage verdict that old is asked again.
	oldNetworkError = 5 * time.Second
)

var (
	errNoQueue    = errors.New("player: no discovery station is playing")
	errNoSongYet  = errors.New("player: the first song has not started yet")
	errBadSongURL = errors.New("song has no playable http(s) URL")
	errNoSource   = errors.New("no song source")
)

// songQueue is one discovery station's walk, from the Play that opened it to
// the next Play (or a stop, or the backend's death) that ends it.
type songQueue struct {
	src SongSource
	// events is the channel of the mpv generation this queue plays into. A
	// goroutine other than readLoop may emit on it only under MPV.mu while
	// MPV.queue is still this queue: readLoop drops the queue under that
	// lock before it closes the channel, so a late lookup can never send on
	// a closed one.
	events chan Event
	ctx    context.Context
	cancel context.CancelFunc

	// Everything below is guarded by MPV.mu.
	cur       Song
	haveCur   bool
	loadedAt  time.Time
	next      *prefetch // the lookup for the song after cur, if one has started
	advancing bool      // a goroutine is on its way to the next song
	fails     int       // songs that failed in a row; audio flowing resets it
	dead      bool      // gave up: the station has been reported failed
}

// prefetch is one Next call running ahead of need. song, err and at are
// written before done closes and read only after.
type prefetch struct {
	done chan struct{}
	song Song
	err  error
	at   time.Time // when the lookup finished: a signed URL ages from here
}

// SetSongSources teaches Play the discover: sentinel. open turns the text
// after it into the station's SongSource; without it the sentinel is
// rejected like any other URL that isn't http(s).
func (m *MPV) SetSongSources(open func(seed string) (SongSource, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openSource = open
}

// playQueue is Play for discover:<seed>: find the first song under the
// tune budget and load it, then start looking for the second.
func (m *MPV) playQueue(seed string) error {
	m.mu.Lock()
	open := m.openSource
	if open == nil {
		m.mu.Unlock()
		return fmt.Errorf("invalid stream URL %q: only http/https are supported", discoverScheme+seed)
	}
	m.playSeq++
	seq := m.playSeq
	m.dropQueueLocked()
	m.mu.Unlock()

	src, err := open(seed)
	if err == nil && src == nil {
		err = errNoSource
	}
	m.mu.Lock()
	if m.playSeq != seq {
		m.mu.Unlock()
		return ErrSuperseded
	}
	if err != nil {
		m.mu.Unlock()
		return songErr(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &songQueue{src: src, events: m.events, ctx: ctx, cancel: cancel}
	m.queue = q
	// Silence the old station before the seconds a lookup takes: its titles
	// would otherwise land on the new station's listen. The stop's end-file
	// falls inside loadPending and is dropped, so it never reads as the
	// listener pressing Stop.
	m.loadPending = true
	_ = m.sendLocked("stop")
	m.mu.Unlock()

	lookup, stop := context.WithTimeout(q.ctx, firstSongBudget)
	first, err := src.Next(lookup)
	stop()

	m.mu.Lock()
	defer m.mu.Unlock()
	// A queue dropped under the lookup (the backend died, playback was
	// stopped from outside) cancelled it: that is no verdict on the station,
	// so it must not read as a failed resolve and earn a strike.
	if m.playSeq != seq || m.queue != q {
		return ErrSuperseded
	}
	if err == nil && !validStreamURL(first.URL) {
		err = errBadSongURL
	}
	if err != nil {
		m.dropQueueLocked()
		return songErr(err)
	}
	if err := m.loadSongLocked(q, first); err != nil {
		m.dropQueueLocked()
		return err
	}
	// mpv's pause survives loadfile; choosing a station must never start
	// silent, whoever paused it.
	return m.sendLocked("set_property", "pause", false)
}

// songErr is what Play says about a lookup that failed. Network trouble
// passes through untouched so the session can tell it from a station that is
// simply broken; anything else is ErrResolve, a strike like a bad page link.
// Neither names a media URL.
func songErr(err error) error {
	if errors.Is(err, ErrSourceNetwork) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrResolve, err)
}

// songTitle is what the strip shows for a song: the same "Artist - Title"
// shape an ICY title has, which the faces already split.
func songTitle(s Song) string {
	switch {
	case s.Artist != "" && s.Title != "":
		return s.Artist + " - " + s.Title
	case s.Title != "":
		return s.Title
	default:
		return s.Artist
	}
}

// loadSongLocked sends one song to mpv and starts looking for the next. The
// per-song state Play resets for a new station is reset here too: each song
// is its own stream as far as titles, filenames and channels go.
func (m *MPV) loadSongLocked(q *songQueue, s Song) error {
	m.sawIcy = false
	m.sawL, m.sawR = false, false
	m.loadPending = true
	m.junkTitles = streamurl.MpvFilenames(s.URL)
	m.mpvFilename = ""
	m.page = ""
	load := []any{"loadfile", s.URL}
	if title := songTitle(s); title != "" {
		// A song's URL carries no ICY metadata and its filename is junk, so
		// the source's own artist and title ride media-title instead.
		load = append(load, "replace", -1, forceTitleOption(title))
	}
	if err := m.sendLocked(load...); err != nil {
		return err
	}
	q.cur, q.haveCur, q.loadedAt = s, true, time.Now()
	m.startPrefetchLocked(q)
	return nil
}

// startPrefetchLocked begins the lookup of the song after the current one.
func (m *MPV) startPrefetchLocked(q *songQueue) {
	p := &prefetch{done: make(chan struct{})}
	q.next = p
	go func() {
		ctx, cancel := context.WithTimeout(q.ctx, prefetchBudget)
		defer cancel()
		p.song, p.err = q.src.Next(ctx)
		p.at = time.Now()
		close(p.done)
	}()
}

// prefetchLocked is the lookup for the next song, started now if nothing
// has started it.
func (m *MPV) prefetchLocked(q *songQueue) *prefetch {
	if q.next == nil {
		m.startPrefetchLocked(q)
	}
	return q.next
}

// advance loads the next song: the prefetched one when it is fresh, else a
// new lookup. A song that won't resolve or won't open is skipped; the third
// in a row, or a lookup that failed for want of a network, ends the station
// with one EventStreamError. A network failure that is more than a few
// seconds old is asked about once more first: the song that just ended may
// have played for minutes since the network went. One advance runs at a time
// (q.advancing).
func (m *MPV) advance(q *songQueue) {
	askedAgain := false
	for {
		m.mu.Lock()
		if m.queue != q || q.dead {
			m.mu.Unlock()
			return
		}
		p := m.prefetchLocked(q)
		m.mu.Unlock()

		select {
		case <-p.done:
		case <-q.ctx.Done():
			return
		}

		m.mu.Lock()
		if m.queue != q || q.dead {
			m.mu.Unlock()
			return
		}
		if q.next == p {
			q.next = nil
		}
		err := p.err
		if err == nil && !validStreamURL(p.song.URL) {
			err = errBadSongURL
		}
		if err == nil && time.Since(p.at) >= songURLTTL {
			// The source can only hand out what comes next, not re-resolve
			// one it already gave, so a stale answer is dropped and asked
			// for afresh.
			m.mu.Unlock()
			continue
		}
		if err != nil {
			if !askedAgain && errors.Is(err, ErrSourceNetwork) && time.Since(p.at) > oldNetworkError {
				askedAgain = true
				m.mu.Unlock()
				continue
			}
			goOn := m.lookupFailedLocked(q, err)
			m.mu.Unlock()
			if goOn {
				continue
			}
			return
		}
		if err := m.loadSongLocked(q, p.song); err != nil {
			// mpv's pipe is gone; readLoop reports EventDied and the session
			// brings up a new process and a new queue.
			q.advancing = false
			m.mu.Unlock()
			return
		}
		q.advancing = false
		m.mu.Unlock()
		return
	}
}

// lookupFailedLocked books a song that could not be found and reports
// whether the walk goes on. Network trouble ends it at once and never counts
// against the station.
func (m *MPV) lookupFailedLocked(q *songQueue, err error) bool {
	if errors.Is(err, ErrSourceNetwork) {
		m.killQueueLocked(q)
		emit(q.events, Event{Type: EventStreamError, Err: err})
		return false
	}
	q.fails++
	if q.fails >= maxSongFails {
		m.killQueueLocked(q)
		emit(q.events, Event{Type: EventStreamError, Err: fmt.Errorf("stream failed: %d songs in a row: %w", maxSongFails, err)})
		return false
	}
	return true
}

// killQueueLocked marks a queue that has given up. It stays registered so
// the end-files of its last songs are swallowed instead of read as a radio
// stream dropping; the next Play replaces it.
func (m *MPV) killQueueLocked(q *songQueue) {
	q.dead = true
	q.advancing = false
	q.cancel()
}

// dropQueueLocked ends whatever queue is registered: a radio station took
// over, the listener stopped playback, or the backend is gone.
func (m *MPV) dropQueueLocked() {
	if m.queue != nil {
		m.queue.cancel()
		m.queue = nil
	}
}

// queueSongEnded is readLoop's answer to an end-file while a queue may be
// playing. It reports whether the queue took it. failed says mpv could not
// play the song (reason error), why is mpv's own explanation.
func (m *MPV) queueSongEnded(events chan Event, failed bool, why string) bool {
	m.mu.Lock()
	q := m.queue
	if q == nil {
		m.mu.Unlock()
		return false
	}
	if q.dead || !q.haveCur {
		// Past the station's end, or before its first song: not a song of
		// this queue ending.
		m.mu.Unlock()
		return true
	}
	if failed {
		q.fails++
		if q.fails >= maxSongFails {
			m.killQueueLocked(q)
			m.mu.Unlock()
			err := fmt.Errorf("stream failed: %d songs in a row", maxSongFails)
			if why != "" {
				err = fmt.Errorf("%w: %s", err, why)
			}
			emit(events, Event{Type: EventStreamError, Err: err})
			return true
		}
	}
	start := !q.advancing
	q.advancing = true
	m.mu.Unlock()
	if start {
		go m.advance(q)
	}
	return true
}

// NextSong is NEXT on a discovery station. It charges the song just heard to
// the source (a skip inside the first minute is a quick one) and starts
// loading the next. A second NEXT while that is still under way changes
// nothing, so a lookup that takes a while can't be answered with two songs.
func (m *MPV) NextSong() error {
	m.mu.Lock()
	q := m.queue
	switch {
	case q == nil || q.dead:
		m.mu.Unlock()
		return errNoQueue
	case !q.haveCur:
		m.mu.Unlock()
		return errNoSongYet
	case q.advancing:
		m.mu.Unlock()
		return nil
	}
	q.advancing = true
	quick := time.Since(q.loadedAt) < quickSkip
	m.mu.Unlock()
	q.src.Skipped(quick)
	go m.advance(q)
	return nil
}

// SongLoved passes a LOVE on to the source, if a song is playing. An anchor is
// not passed on: the source reads a love as "the finds are working", and a
// well-known track says nothing about them.
func (m *MPV) SongLoved() {
	m.mu.Lock()
	q := m.queue
	live := q != nil && !q.dead && q.haveCur && !q.cur.Anchor
	m.mu.Unlock()
	if live {
		q.src.Loved()
	}
}
