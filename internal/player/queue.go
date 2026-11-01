package player

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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
	Anchor             bool          // a well-known track, not a find: loving it says nothing about the finds
	Duration           time.Duration // how long the song runs; 0 when the source doesn't know
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
	// the rest. Kept at 8 s: a late lookup is transient (see playQueue) but
	// a late lock is a strike, and 9 s would leave exactly 3 s with nothing
	// for the gap between the pick and the lookup or a slow media host.
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
	// maxResumes is how many times one song is picked up again after a
	// reconnect. A song that cannot be kept playing after two is not worth a
	// third: the next one plays.
	maxResumes = 2
	// resumeNearEnd is how close to its end a song must be for a reconnect to
	// move on instead: a reload would be a second of silence for nothing.
	resumeNearEnd = 15 * time.Second
)

// How a discovery song ended, as the song log says it. A song also ends in
// "error: <mpv's reason>", which carries its reason with it.
const (
	endEOF        = "eof"         // it played out
	endNext       = "next"        // the listener pressed NEXT
	endReconnect  = "reconnect"   // a reconnect found it too near its end, or too often reloaded, to resume
	endResumed    = "resumed"     // a reconnect reloaded it where it had stopped; it plays on
	endNewStation = "new station" // another station was chosen
	endStopped    = "stopped"     // playback was stopped, from outside or by closing
	endDied       = "died"        // mpv went away under it
)

// firstSongWait is the budget playQueue applies; a variable only so a test can
// make the wait short.
var firstSongWait = firstSongBudget

var (
	errNoQueue    = errors.New("player: no discovery station is playing")
	errNoSongYet  = errors.New("player: the first song has not started yet")
	errBadSongURL = errors.New("song has no playable http(s) URL")
	errNoSource   = errors.New("no song source")
)

// songQueue is one discovery station's walk, from the Play that opened it to
// the next Play (or a stop, or the backend's death) that ends it.
type songQueue struct {
	seed string
	src  SongSource
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
	urlAt     time.Time // when cur's URL was handed out: a signed URL ages from here
	clock     songClock // where in cur the listener is
	resumes   int       // times cur has been reloaded where it stopped
	curDone   bool      // cur has ended and its ending is logged; its successor is on the way
	nextWhy   string    // why the next song replaces cur, when it isn't the listener's NEXT
	next      *prefetch // the lookup for the song after cur, if one has started
	advancing bool      // a goroutine is on its way to the next song
	fails     int       // songs that failed in a row; audio flowing resets it
	dead      bool      // gave up: the station has been reported failed
}

// songClock is where the listener is in a song: the time audio has been
// flowing, from wherever the song began. Wall time since the load would also
// count a pause and every second mpv sat waiting on its cache, and a
// reconnect that follows a 20 s stall would then resume 20 s too far on.
type songClock struct {
	base  time.Duration // where in the song the running spells before this one add up to
	since time.Time     // when the current spell began; zero while audio isn't flowing
}

// pos is how far into the song the listener is as of now.
func (c *songClock) pos(now time.Time) time.Duration {
	if c.since.IsZero() {
		return c.base
	}
	return c.base + max(0, now.Sub(c.since))
}

// flowing records whether audio is flowing as of now. Saying the same thing
// twice changes nothing.
func (c *songClock) flowing(on bool, now time.Time) {
	switch {
	case on && c.since.IsZero():
		c.since = now
	case !on && !c.since.IsZero():
		c.base = c.pos(now)
		c.since = time.Time{}
	}
}

// restart puts the clock at start with the audio not yet flowing.
func (c *songClock) restart(start time.Duration) { *c = songClock{base: start} }

// resumePoint says whether cur should be picked up where it stopped when the
// station is played again, and where that is. Not when it has ended, has been
// reloaded twice already, or its URL is too old to trust (the source can only
// hand out what comes next, not re-resolve a song it already gave); and not
// when it is within its last seconds, or, if its length is unknown, hasn't
// played at all.
func (q *songQueue) resumePoint(now time.Time) (time.Duration, bool) {
	if !q.haveCur || q.curDone || q.resumes >= maxResumes || now.Sub(q.urlAt) >= songURLTTL {
		return 0, false
	}
	pos := q.clock.pos(now)
	if q.cur.Duration > 0 {
		return pos, pos < q.cur.Duration-resumeNearEnd
	}
	return pos, pos > 0
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

// SetSongLog makes the player say how each discovery song ended, one line
// per song: its key, where its audio came from, the length it was expected to
// have, how much of it played, and why it ended. Never the URL. logf is
// called with the player's lock held, so it must not call back into the
// player; a nil logf (the default) says nothing.
func (m *MPV) SetSongLog(logf func(format string, args ...any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.songLog = logf
}

// playQueue is Play for discover:<seed>. A station already playing is
// reconnected, not restarted (reconnectLocked); otherwise find the first song
// under the tune budget and load it, then start looking for the second. After
// an mpv restart the first song is the one the dead process was playing,
// from where it stopped.
func (m *MPV) playQueue(seed string) error {
	m.mu.Lock()
	open := m.openSource
	if open == nil {
		m.mu.Unlock()
		return fmt.Errorf("invalid stream URL %q: only http/https are supported", discoverScheme+seed)
	}
	m.playSeq++
	seq := m.playSeq
	if handled, err := m.reconnectLocked(seed); handled {
		m.mu.Unlock()
		return err
	}
	carried := m.carried
	m.carried = nil
	m.dropQueueLocked(endNewStation)
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
	q := &songQueue{seed: seed, src: src, events: m.events, ctx: ctx, cancel: cancel}
	m.queue = q
	if carried != nil && carried.seed == seed {
		if start, ok := carried.resumePoint(m.clockNow()); ok {
			err := m.resumeCarriedLocked(q, carried, start)
			m.mu.Unlock()
			return err
		}
	}
	// Silence the old station before the seconds a lookup takes: its titles
	// would otherwise land on the new station's listen. The stop's end-file
	// falls inside loadPending and is dropped, so it never reads as the
	// listener pressing Stop.
	m.loadPending = true
	_ = m.sendLocked("stop")
	m.mu.Unlock()

	lookup, stop := context.WithTimeout(q.ctx, firstSongWait)
	first, err := src.Next(lookup)
	// Read before stop() cancels it: did the budget run out?
	ranOut := errors.Is(lookup.Err(), context.DeadlineExceeded)
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
	if err != nil && (ranOut || errors.Is(err, context.DeadlineExceeded)) && !errors.Is(err, ErrSourceNetwork) {
		// A first lookup that ran out of its budget cannot tell a slow
		// network from a bad seed, so it must not strike a station that may
		// be perfectly good: it is transient, the session's no-strike road.
		// Later lookups are different: a song that timed out is a skipped
		// song (lookupFailedLocked), and three in a row are a verdict.
		err = fmt.Errorf("%w: no song within %v: %w", ErrSourceNetwork, firstSongWait, err)
	}
	if err != nil {
		m.dropQueueLocked(endNewStation)
		return songErr(err)
	}
	if err := m.loadSongLocked(q, first, m.clockNow()); err != nil {
		m.dropQueueLocked(endNewStation)
		return err
	}
	// mpv's pause survives loadfile; choosing a station must never start
	// silent, whoever paused it.
	return m.sendLocked("set_property", "pause", false)
}

// reconnectLocked answers a Play of the station that is already playing: the
// session reconnecting it after a buffering stall, a dropped network or a
// pause too long to unpause in place. It reports whether it took the Play;
// a station that isn't playing, or is playing another seed, or has no song
// yet, is for playQueue to start.
//
// The song the listener was in the middle of must not be lost to the
// reconnect, so it is loaded again from where it stopped, with the queue, its
// prefetch and what it has played kept. A song that can't be resumed (near
// its end, reloaded twice already, URL too old) is replaced by the next, which
// the prefetch usually has ready; the source is not told of a skip, because
// no one pressed NEXT. While a next song is already on its way, the Play
// changes nothing: that song replaces this one in a moment.
func (m *MPV) reconnectLocked(seed string) (handled bool, err error) {
	q := m.queue
	if q == nil || q.dead || !q.haveCur || q.seed != seed {
		return false, nil
	}
	if !q.advancing {
		if start, ok := q.resumePoint(m.clockNow()); ok {
			if err := m.resumeLocked(q, start); err != nil {
				return true, err
			}
		} else {
			q.advancing = true
			q.nextWhy = endReconnect
			go m.advance(q)
		}
	}
	// mpv's pause survives loadfile, and a reconnect after a long pause is
	// this very call: the station must come back audible.
	return true, m.sendLocked("set_property", "pause", false)
}

// resumeLocked loads q's current song again from start.
func (m *MPV) resumeLocked(q *songQueue, start time.Duration) error {
	if err := m.sendSongLocked(q.cur, start); err != nil {
		return err
	}
	m.logSongLocked(q, endResumed)
	q.resumes++
	q.clock.restart(start)
	m.syncSongClockLocked()
	return nil
}

// resumeCarriedLocked is playQueue's answer after an mpv restart, when the
// song the dead process was playing can go on: q, the new process's queue,
// takes it over at start, and looks ahead from there. Nothing is stopped
// first: the new process has nothing loaded.
func (m *MPV) resumeCarriedLocked(q, old *songQueue, start time.Duration) error {
	if err := m.sendSongLocked(old.cur, start); err != nil {
		m.dropQueueLocked(endNewStation)
		return err
	}
	q.cur, q.haveCur, q.loadedAt, q.urlAt = old.cur, true, old.loadedAt, old.urlAt
	q.resumes = old.resumes + 1
	q.clock.restart(start)
	m.syncSongClockLocked()
	m.startPrefetchLocked(q)
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

// sendSongLocked sends one song to mpv, to start start seconds in. The
// per-song state Play resets for a new station is reset here too: each song
// is its own stream as far as titles, filenames and channels go.
func (m *MPV) sendSongLocked(s Song, start time.Duration) error {
	m.sawIcy = false
	m.sawL, m.sawR = false, false
	m.loadPending = true
	m.junkTitles = streamurl.MpvFilenames(s.URL)
	m.mpvFilename = ""
	m.page = ""
	load := []any{"loadfile", s.URL}
	// A song's URL carries no ICY metadata and its filename is junk, so the
	// source's own artist and title ride media-title instead.
	if opts := songOptions(songTitle(s), start); opts != "" {
		load = append(load, "replace", -1, opts)
	}
	return m.sendLocked(load...)
}

// songOptions is loadfile's per-file option list for a song: where to start,
// if that is worth a seek, and what to call it. The title goes last and
// length-quoted, so nothing in it can end the list early.
func songOptions(title string, start time.Duration) string {
	var opts []string
	if start >= time.Second {
		opts = append(opts, "start="+strconv.FormatFloat(start.Round(time.Millisecond).Seconds(), 'f', -1, 64))
	}
	if title != "" {
		opts = append(opts, forceTitleOption(title))
	}
	return strings.Join(opts, ",")
}

// loadSongLocked sends a song q has not played to mpv, ends the one it
// replaces in the log if that had not already ended, and starts looking for
// the next. got is when the song's URL was handed out.
func (m *MPV) loadSongLocked(q *songQueue, s Song, got time.Time) error {
	if err := m.sendSongLocked(s, 0); err != nil {
		return err
	}
	why := q.nextWhy
	if why == "" {
		why = endNext
	}
	m.finishSongLocked(q, why)
	q.cur, q.haveCur, q.loadedAt, q.urlAt = s, true, m.clockNow(), got
	q.curDone, q.resumes, q.nextWhy = false, 0, ""
	q.clock.restart(0)
	m.syncSongClockLocked()
	m.startPrefetchLocked(q)
	return nil
}

// syncSongClockLocked starts or stops the current song's clock to match mpv:
// audio is flowing when it is neither paused nor idle. core-idle is also true
// while mpv waits on its cache, so a stall stops the clock too.
func (m *MPV) syncSongClockLocked() {
	if q := m.queue; q != nil && q.haveCur {
		q.clock.flowing(!m.paused && !m.coreIdle, m.clockNow())
	}
}

// clockNow is the time the player reasons in; a test's own clock stands in for
// the real one so it can say how long a song has played without waiting.
func (m *MPV) clockNow() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// finishSongLocked ends q's current song in the log, once. A song whose end
// was already logged (it played out, or mpv failed it) says nothing more when
// its successor replaces it.
func (m *MPV) finishSongLocked(q *songQueue, why string) {
	if !q.haveCur || q.curDone {
		return
	}
	q.curDone = true
	m.logSongLocked(q, why)
}

// logSongLocked writes the song log's line for q's current song.
func (m *MPV) logSongLocked(q *songQueue, why string) {
	if m.songLog == nil {
		return
	}
	s := q.cur
	expected := "?s"
	if s.Duration > 0 {
		expected = fmt.Sprintf("%ds", wholeSeconds(s.Duration))
	}
	m.songLog("%s", fmt.Sprintf("song %s %s expected %s played %ds ended %s",
		plainToken(s.Key), songSource(s), expected, wholeSeconds(q.clock.pos(m.clockNow())), why))
}

func wholeSeconds(d time.Duration) int { return int(d.Round(time.Second) / time.Second) }

// songSource says where a song's audio came from, for the song log: Bandcamp,
// or the YouTube client whose stream URL it is (the URL's c= parameter, which
// tells the clients apart when one of them proves unreliable). Never the URL.
func songSource(s Song) string {
	if strings.HasPrefix(s.Key, "bc:") {
		return "bandcamp"
	}
	if u, err := url.Parse(s.URL); err == nil {
		if c := u.Query().Get("c"); c != "" && len(c) <= 24 && plainToken(c) == c {
			return "c=" + c
		}
	}
	return "youtube"
}

// plainToken is s if it is one plain word, and "?" if it is empty or could
// carry anything else, a URL above all: a log gets pasted.
func plainToken(s string) string {
	if s == "" {
		return "?"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == ':' || r == '-' || r == '_' || r == '.':
		default:
			return "?"
		}
	}
	return s
}

// startPrefetchLocked begins the lookup of the song after the current one.
func (m *MPV) startPrefetchLocked(q *songQueue) {
	p := &prefetch{done: make(chan struct{})}
	q.next = p
	now := m.now
	if now == nil {
		now = time.Now
	}
	go func() {
		ctx, cancel := context.WithTimeout(q.ctx, prefetchBudget)
		defer cancel()
		p.song, p.err = q.src.Next(ctx)
		p.at = now()
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
		if err == nil && m.clockNow().Sub(p.at) >= songURLTTL {
			// The source can only hand out what comes next, not re-resolve
			// one it already gave, so a stale answer is dropped and asked
			// for afresh.
			m.mu.Unlock()
			continue
		}
		if err != nil {
			if !askedAgain && errors.Is(err, ErrSourceNetwork) && m.clockNow().Sub(p.at) > oldNetworkError {
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
		if err := m.loadSongLocked(q, p.song, p.at); err != nil {
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
// over, the listener stopped playback, or the backend is gone. why is what
// the song log says ended the song that was playing.
//
// When the backend is gone, the song it was playing is kept (m.carried), with
// the position it had reached: the session starts a new process and plays the
// station again, and that Play can pick the song up.
func (m *MPV) dropQueueLocked(why string) {
	q := m.queue
	if q == nil {
		return
	}
	q.cancel()
	m.queue = nil
	if why == endDied && q.haveCur && !q.curDone && !q.dead {
		// The song is not over, only its process: the log says so, and the
		// song stays as it was for the next Play to take up. However long
		// the session takes to notice, no audio is flowing.
		m.logSongLocked(q, why)
		q.clock.flowing(false, m.clockNow())
		m.carried = q
		return
	}
	m.finishSongLocked(q, why)
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
		m.finishSongLocked(q, errorEnd(why))
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
	} else {
		m.finishSongLocked(q, endEOF)
	}
	start := !q.advancing
	q.advancing = true
	m.mu.Unlock()
	if start {
		go m.advance(q)
	}
	return true
}

// errorEnd is how the song log says mpv failed a song: with mpv's own reason,
// unless that could be a URL.
func errorEnd(why string) string {
	why = strings.TrimSpace(why)
	if why == "" || strings.Contains(why, "://") {
		return "error"
	}
	return "error: " + why
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
	quick := m.clockNow().Sub(q.loadedAt) < quickSkip
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
