package phone

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBuilds is a Build whose builds are counted and can be held open.
type fakeBuilds struct {
	n    atomic.Int32
	hold atomic.Pointer[chan struct{}]
}

func (f *fakeBuilds) build(ctx context.Context, days int) ([]Event, error) {
	n := f.n.Add(1)
	if h := f.hold.Load(); h != nil {
		select {
		case <-*h:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []Event{{ID: "calendar:build-" + string(rune('0'+n))}}, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestCache(f *fakeBuilds, clk *fakeClock, live *atomic.Bool) *AgendaCache {
	return &AgendaCache{Build: f.build, Now: clk.now, Live: live.Load, Wait: 200 * time.Millisecond, LiveEvery: 5 * time.Millisecond, Tick: time.Hour}
}

func agendaID(t *testing.T, c *AgendaCache) string {
	t.Helper()
	evs, err := c.Agenda(context.Background(), defaultDays)
	if err != nil {
		t.Fatal(err)
	}
	return evs[0].ID
}

func TestAgendaCacheTriggersAndCap(t *testing.T) {
	ctx := context.Background()
	f := &fakeBuilds{}
	clk := &fakeClock{t: time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)}
	var live atomic.Bool
	c := newTestCache(f, clk, &live)

	if !c.step(ctx) {
		t.Fatal("no first build")
	}
	c.waitBuilt()
	if got := agendaID(t, c); got != "calendar:build-1" {
		t.Fatalf("first build: %s", got)
	}
	// Nothing changed: never rebuilt, however long.
	clk.add(time.Hour)
	if c.step(ctx) {
		t.Error("rebuilt with nothing changed")
	}
	// A change within 15 minutes of the last build waits for the gap.
	c.Invalidate()
	if !c.step(ctx) {
		t.Fatal("a change an hour after the last build: not rebuilt")
	}
	c.waitBuilt()
	clk.add(time.Minute)
	c.Invalidate()
	if c.step(ctx) {
		t.Error("rebuilt within 15 minutes of the last build")
	}
	clk.add(13 * time.Minute)
	if c.step(ctx) {
		t.Error("rebuilt 14 minutes after the last build")
	}
	clk.add(time.Minute)
	if !c.step(ctx) {
		t.Fatal("a change waiting 15 minutes: not rebuilt")
	}
	c.waitBuilt()
	if got := agendaID(t, c); got != "calendar:build-3" || f.n.Load() != 3 {
		t.Fatalf("after the gap: %s, %d builds", got, f.n.Load())
	}
	// A new day moves the window: rebuilt without a change.
	clk.add(24 * time.Hour)
	if !c.step(ctx) {
		t.Fatal("not rebuilt on a new day")
	}
	c.waitBuilt()
	if f.n.Load() != 4 {
		t.Errorf("%d builds", f.n.Load())
	}
}

func TestAgendaCacheNotDuringLiveMeeting(t *testing.T) {
	ctx := context.Background()
	f := &fakeBuilds{}
	clk := &fakeClock{t: time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)}
	var live atomic.Bool
	c := newTestCache(f, clk, &live)

	live.Store(true)
	if c.step(ctx) || f.n.Load() != 0 {
		t.Fatal("built during a live meeting")
	}
	live.Store(false)
	if !c.step(ctx) {
		t.Fatal("not built after the meeting")
	}
	c.waitBuilt()

	// A meeting that goes live stops a build under way; the last build
	// stays, and the change is built after the meeting.
	hold := make(chan struct{})
	f.hold.Store(&hold)
	clk.add(time.Hour)
	c.Invalidate()
	if !c.step(ctx) {
		t.Fatal("no rebuild")
	}
	live.Store(true)
	c.waitBuilt()
	if got := agendaID(t, c); got != "calendar:build-1" {
		t.Errorf("a stopped build replaced the agenda: %s", got)
	}
	f.hold.Store(nil)
	clk.add(time.Hour)
	if c.step(ctx) {
		t.Error("rebuilt during the meeting")
	}
	live.Store(false)
	if !c.step(ctx) {
		t.Fatal("the stopped build was not tried again after the meeting")
	}
	c.waitBuilt()
	if got := agendaID(t, c); got != "calendar:build-3" {
		t.Errorf("after the meeting: %s", got)
	}
}

func TestAgendaCacheSyncNeverWaitsOnABuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeBuilds{}
	hold := make(chan struct{})
	f.hold.Store(&hold)
	clk := &fakeClock{t: time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)}
	var live atomic.Bool
	c := newTestCache(f, clk, &live)
	go c.Run(ctx)

	// No build yet: the first sync waits at most Wait and sends nothing.
	t0 := time.Now()
	_, err := c.Agenda(ctx, defaultDays)
	if !errors.Is(err, ErrAgendaNotReady) || time.Since(t0) > 400*time.Millisecond {
		t.Fatalf("first sync: %v after %s", err, time.Since(t0))
	}
	close(hold)
	c.waitBuilt()
	// While a rebuild is held open, a sync gets the last build at once.
	hold2 := make(chan struct{})
	f.hold.Store(&hold2)
	clk.add(time.Hour)
	c.Invalidate()
	for i := 0; i < 100 && f.n.Load() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if f.n.Load() != 2 {
		t.Fatal("no rebuild started")
	}
	t0 = time.Now()
	if got := agendaID(t, c); got != "calendar:build-1" || time.Since(t0) > 50*time.Millisecond {
		t.Errorf("during a build: %s after %s", got, time.Since(t0))
	}
	close(hold2)
	c.waitBuilt()
	if got := agendaID(t, c); got != "calendar:build-2" {
		t.Errorf("after the build: %s", got)
	}
}

func TestSyncStatsAgendaPending(t *testing.T) {
	if s := (syncStats{agendaPending: true}).String(); s != "0 recordings on the phone, 0 fetched, 0 imported; agenda still being built" {
		t.Error(s)
	}
}
