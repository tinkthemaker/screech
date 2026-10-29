package session

import (
	"sync"
	"time"

	"github.com/tinkthemaker/screech/internal/player"
)

// levelMinInterval throttles sink.Level: the meter animates between
// readings, so extras are dropped rather than queued.
const levelMinInterval = 33 * time.Millisecond

// Sink is what a runner publishes to. Both calls happen on the runner's
// loop goroutine; a sink that fans out must not block for long.
type Sink interface {
	Snapshot(Snapshot)
	Level(Level)
}

// Runner drives a State for faces that aren't Bubble Tea: one goroutine
// owns the state, commands run in their own goroutines, and snapshots are
// published only when they differ from the last one sent.
type Runner struct {
	s    State
	sink Sink

	msgs    chan Msg
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
	started bool

	tickInterval time.Duration // tests may shorten before Start

	mu        sync.Mutex // guards last/published
	last      Snapshot
	published bool
	lastLevel time.Time // loop-goroutine only
}

func NewRunner(s State, sink Sink) *Runner {
	return &Runner{
		s:            s,
		sink:         sink,
		msgs:         make(chan Msg, 64),
		done:         make(chan struct{}),
		stopped:      make(chan struct{}),
		tickInterval: 250 * time.Millisecond,
	}
}

// Start publishes the first snapshot, runs Init's commands, then starts
// the loop and its ticker.
func (r *Runner) Start() {
	r.started = true
	r.publish()
	go r.loop()
	for _, cmd := range r.s.Init() {
		r.runCmd(cmd)
	}
}

// Send queues a message for the state machine from any goroutine. After
// Stop it is a no-op.
func (r *Runner) Send(m Msg) {
	select {
	case r.msgs <- m:
	case <-r.done:
	}
}

// Current returns the last published snapshot.
func (r *Runner) Current() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Stop runs Update(Quit) on the loop — banking the listen and closing the
// backend — then exits the loop. Idempotent; returns after Quit ran.
func (r *Runner) Stop() {
	r.once.Do(func() {
		if !r.started {
			close(r.stopped)
			return
		}
		select {
		case r.msgs <- Quit{}:
		case <-r.done:
		}
		<-r.stopped
	})
	<-r.stopped
}

func (r *Runner) loop() {
	ticker := time.NewTicker(r.tickInterval)
	defer ticker.Stop()
	defer close(r.stopped)
	defer close(r.done)
	for {
		select {
		case m := <-r.msgs:
			_, quit := m.(Quit)
			r.step(m)
			if quit {
				return
			}
		case <-ticker.C:
			r.step(Tick{Now: time.Now()})
		}
	}
}

func (r *Runner) step(m Msg) {
	level := false
	if ev, ok := m.(PlayerEvent); ok && player.Event(ev).Type == player.EventLevel {
		level = true
	}
	var cmds []Cmd
	r.s, cmds = r.s.Update(m)
	r.publish()
	if level && time.Since(r.lastLevel) >= levelMinInterval {
		r.lastLevel = time.Now()
		r.sink.Level(r.s.Level)
	}
	for _, cmd := range cmds {
		r.runCmd(cmd)
	}
}

// publish sends the snapshot to the sink only when it differs from the
// last one sent.
func (r *Runner) publish() {
	snap := r.s.Snapshot()
	r.mu.Lock()
	changed := !r.published || snap != r.last
	if changed {
		r.last = snap
		r.published = true
	}
	r.mu.Unlock()
	if changed {
		r.sink.Snapshot(snap)
	}
}

// runCmd runs a session command off the loop; its result rejoins the
// queue unless the runner is stopping.
func (r *Runner) runCmd(c Cmd) {
	if c == nil {
		return
	}
	go func() {
		m := c()
		if m == nil {
			return
		}
		select {
		case r.msgs <- m:
		case <-r.done:
		}
	}()
}
