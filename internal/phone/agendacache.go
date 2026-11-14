package phone

import (
	"context"
	"errors"
	"sync"
	"time"
)

// A build of the agenda reads a prep for every event (seconds on a full
// dataset), and the phone syncs every few minutes while it is in front, so
// the agenda is built in the background and a sync sends the last build.

// ErrAgendaNotReady: no build has finished yet (the app just started); the
// sync sends no agenda and the phone keeps the one it has.
var ErrAgendaNotReady = errors.New("agenda not built yet")

const (
	// agendaMinGap: a changed agenda is rebuilt at most this often.
	agendaMinGap = 15 * time.Minute
	// agendaWait: how long a sync waits for the first build.
	agendaWait = 300 * time.Millisecond
	// agendaTick: how often the cache checks whether a rebuild is due
	agendaTick = 30 * time.Second
)

// AgendaCache keeps the last agenda built and rebuilds it in the
// background when its inputs changed (Invalidate), when the day changed
// (the window moves) or when there is none yet; at most every MinGap, and
// never while a meeting is live. Unchanged inputs are never rebuilt.
// The cache lives in memory only.
type AgendaCache struct {
	// Build makes the agenda for the next days days (BuildAgenda); it
	// should stop when ctx ends.
	Build func(ctx context.Context, days int) ([]Event, error)
	// Live reports a meeting in progress: nothing is built then, and a
	// build under way is stopped.
	Live   func() bool
	Now    func() time.Time
	Logf   func(format string, args ...any)
	MinGap time.Duration
	Tick   time.Duration
	Wait   time.Duration
	// LiveEvery: how often a build under way checks for a live meeting.
	LiveEvery time.Duration

	mu       sync.Mutex
	evs      []Event
	have     bool
	dirty    bool
	day      string // the local date of the last build
	started  time.Time
	building bool
	ready    chan struct{} // closed by the first build that succeeds
	wake     chan struct{}
	done     chan struct{} // closed when a build ends (for tests)
}

func (c *AgendaCache) init() {
	if c.ready == nil {
		c.ready = make(chan struct{})
		c.wake = make(chan struct{}, 1)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Live == nil {
		c.Live = func() bool { return false }
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	if c.MinGap == 0 {
		c.MinGap = agendaMinGap
	}
	if c.Tick == 0 {
		c.Tick = agendaTick
	}
	if c.Wait == 0 {
		c.Wait = agendaWait
	}
	if c.LiveEvery == 0 {
		c.LiveEvery = time.Second
	}
}

// Invalidate marks the agenda's inputs changed (a brief saved, an update
// that changed items or threads, the calendar).
func (c *AgendaCache) Invalidate() {
	c.mu.Lock()
	c.init()
	c.dirty = true
	c.mu.Unlock()
	c.poke()
}

func (c *AgendaCache) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Agenda is the last build, for a sync: at once when there is one, else
// after waiting at most Wait for the first (ErrAgendaNotReady).
func (c *AgendaCache) Agenda(ctx context.Context, _ int) ([]Event, error) {
	c.mu.Lock()
	c.init()
	ready := c.ready
	c.mu.Unlock()
	c.poke()
	t := time.NewTimer(c.Wait)
	defer t.Stop()
	select {
	case <-ready:
	case <-t.C:
		return nil, ErrAgendaNotReady
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evs, nil
}

// Run checks every Tick, and whenever poked, whether a build is due, until
// ctx ends.
func (c *AgendaCache) Run(ctx context.Context) {
	c.mu.Lock()
	c.init()
	c.mu.Unlock()
	t := time.NewTicker(c.Tick)
	defer t.Stop()
	for {
		c.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

// step starts a build if one is due, and reports whether it did.
func (c *AgendaCache) step(ctx context.Context) bool {
	c.mu.Lock()
	c.init()
	now := c.Now()
	day := now.Format("2006-01-02")
	due := !c.have || c.dirty || c.day != day
	if c.building || !due || c.have && now.Sub(c.started) < c.MinGap {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()
	// Asked outside the lock: Live may be slow, and a sync must never
	// wait for it.
	if c.Live() {
		return false
	}
	c.mu.Lock()
	if c.building {
		c.mu.Unlock()
		return false
	}
	c.building, c.dirty, c.started = true, false, now
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()
	go c.build(ctx, day, done)
	return true
}

func (c *AgendaCache) build(ctx context.Context, day string, done chan struct{}) {
	defer close(done)
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A meeting that goes live stops the build.
	stopped := make(chan struct{})
	go func() {
		t := time.NewTicker(c.LiveEvery)
		defer t.Stop()
		for {
			select {
			case <-bctx.Done():
				return
			case <-t.C:
				if c.Live() {
					close(stopped)
					cancel()
					return
				}
			}
		}
	}()
	start := time.Now()
	evs, err := c.Build(bctx, defaultDays)
	select {
	case <-stopped:
		err = errors.New("a meeting went live")
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.building = false
	if err != nil || bctx.Err() != nil {
		if err == nil {
			err = bctx.Err()
		}
		// Kept as it was; the next step tries again (after MinGap).
		c.dirty = true
		c.Logf("phone: agenda not built: %v", err)
		return
	}
	if evs == nil {
		evs = []Event{}
	}
	first := !c.have
	c.evs, c.have, c.day = evs, true, day
	if first {
		close(c.ready)
	}
	c.Logf("phone: agenda built: %d events in %s", len(evs), time.Since(start).Round(time.Millisecond))
}

// waitBuilt waits for the build under way, if any (tests).
func (c *AgendaCache) waitBuilt() {
	c.mu.Lock()
	d := c.done
	c.mu.Unlock()
	if d != nil {
		<-d
	}
}
