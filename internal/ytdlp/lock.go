package ytdlp

import (
	"context"
	"slices"
	"sync"
)

// lock is the app-wide turn: every Runner call waits here, whatever its Path
// or Dir, so no two yt-dlp processes ever overlap. A reconnect storm of
// parallel lookups is what got the pilot throttled by YouTube.
var lock prioLock

type foregroundKey struct{}

// Foreground marks ctx as a tune's own lookup. The listener is waiting on it,
// so it goes ahead of every prefetch queued for the lock. It does not push
// out a process already running: yt-dlp can only be waited out, not paused.
func Foreground(ctx context.Context) context.Context {
	return context.WithValue(ctx, foregroundKey{}, true)
}

func isForeground(ctx context.Context) bool {
	fg, _ := ctx.Value(foregroundKey{}).(bool)
	return fg
}

// IsForeground reports whether ctx was marked by Foreground, for a caller
// that hands its context on and wants to know (or show in a test) which line
// the lookup will stand in.
func IsForeground(ctx context.Context) bool { return isForeground(ctx) }

// prioLock is a mutex with two lines of waiters. When the holder lets go the
// turn goes to the head of the foreground line, else the head of the
// background line: first come, first served inside each.
type prioLock struct {
	mu   sync.Mutex
	held bool
	fg   []*waiter
	bg   []*waiter
}

type waiter struct {
	granted chan struct{} // closed, under mu, at the moment the turn is handed over
}

// acquire waits for the turn, or gives up when ctx ends: a lookup that has
// already lost its deadline must not hold up the ones behind it. The
// context's time covers the wait as well as the run, which is what the tune
// path's budget needs.
func (l *prioLock) acquire(ctx context.Context, fg bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if !l.held {
		l.held = true
		l.mu.Unlock()
		return nil
	}
	w := &waiter{granted: make(chan struct{})}
	line := &l.bg
	if fg {
		line = &l.fg
	}
	*line = append(*line, w)
	l.mu.Unlock()

	select {
	case <-w.granted:
		return nil
	case <-ctx.Done():
	}
	l.mu.Lock()
	select {
	case <-w.granted:
		// The hand-over beat the cancel, so the turn is already ours. Pass it
		// on; keeping it would strand the lock with nobody to release it.
		l.mu.Unlock()
		l.release()
	default:
		if i := slices.Index(*line, w); i >= 0 {
			*line = slices.Delete(*line, i, i+1)
		}
		l.mu.Unlock()
	}
	return ctx.Err()
}

// release hands the turn to the next waiter. held stays true across a
// hand-over, so the lock is never briefly free for a newcomer to slip in
// ahead of the line.
func (l *prioLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range []*[]*waiter{&l.fg, &l.bg} {
		if len(*line) > 0 {
			w := (*line)[0]
			*line = slices.Delete(*line, 0, 1)
			close(w.granted)
			return
		}
	}
	l.held = false
}
