package live

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// A stepped replay plays a saved meeting line by line, and after each one
// does everything that line would set off in a live meeting (lookups,
// topic cards, the model's suggestions, summaries) to completion before
// the next. Its timers run on the meeting's clock, not the wall's, so
// what the panel shows doesn't depend on how fast the computer is or how
// fast the replay runs: it goes as fast as the work allows. How long each
// step took is still traced, as latency.
//
// One difference from a live meeting: there, a summary that waits too
// long behind the local model is skipped (staleSummary); here every one
// is written.

// now is the time timers measure against: the meeting's clock in a
// stepped replay, the wall's otherwise.
func (l *Live) now() time.Time {
	if l.stepped.Load() {
		return steppedEpoch.Add(time.Duration(l.stepClock() * float64(time.Second)))
	}
	return time.Now()
}

func (l *Live) since(t time.Time) time.Duration { return l.now().Sub(t) }

// steppedEpoch anchors the meeting clock as a time for the timers.
var steppedEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func (l *Live) stepClock() float64 { return math.Float64frombits(l.vclk.Load()) }

func (l *Live) setStepClock(t float64) { l.vclk.Store(math.Float64bits(t)) }

// spawn runs f in the background, or now in a stepped replay.
func (l *Live) spawn(f func()) {
	if l.stepped.Load() {
		f()
		return
	}
	go f()
}

// StepClock is a stepped replay's meeting clock (0 outside one); safe to
// call from an event handler.
func (l *Live) StepClock() float64 { return l.stepClock() }

// ReplayStepped plays a saved session through the panel as a stepped
// replay and returns when it is done, or once the meeting clock passes
// until (seconds; 0 for the whole meeting). The model's suggestions run as
// they would live: on new final text, and on drafts alone at most every
// draftRerun of meeting time (with suggest_every, at most that often).
func (l *Live) ReplayStepped(ctx context.Context, sessionID string, until float64) error {
	sess, segs, err := l.loadSession(sessionID)
	if err != nil {
		return err
	}
	sched := buildSchedule(segs)
	l.mu.Lock()
	l.stopReplayLocked()
	l.ctx = ctx
	l.mu.Unlock()
	l.stepped.Store(true)
	defer l.stepped.Store(false)
	l.setStepClock(0)
	l.handle(Event{V: 1, Type: "session", State: "started", Session: &sess}, "replay")
	l.trace(0, "feed", "stepped replay", 0, map[string]any{"lines": len(segs)})
	lastRun, lastFinals := -math.MaxFloat64, -1
	gap := float64(l.eng.Cfg.Live.SuggestEvery)
	for _, e := range sched {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if until > 0 && e.At > until {
			break
		}
		l.setStepClock(e.At)
		seg := e.Seg
		l.handle(Event{V: 1, Type: "segment", SessionID: sess.ID, Segment: &seg}, "replay")
		l.mu.Lock()
		m, sug := l.meeting, l.sug
		run := m != nil && sug != nil && m.Version() != l.lastModelVer &&
			(m.Finals() != lastFinals || !l.CardsOnly && e.At-lastRun >= draftRerun.Seconds()) &&
			(gap <= 0 || e.At-lastRun >= gap)
		var recent, scopeName string
		var lines []Segment
		var exclude []string
		if run {
			lastFinals, lastRun = m.Finals(), e.At
			l.lastModelVer = m.Version()
			window := float64(max(30, l.eng.Cfg.Live.Window))
			recent, lines = m.Recent(window), m.RecentLines(window)
			exclude = []string{"tomoe:" + m.Session.ID}
			scopeName = l.scope
		}
		l.mu.Unlock()
		if run && strings.TrimSpace(recent) != "" {
			l.modelCycle(ctx, sug, recent, lines, exclude, scopeName)
		}
	}
	l.handle(Event{V: 1, Type: "session", State: "stopped", Session: &sess}, "replay")
	l.trace(0, "feed", fmt.Sprintf("stepped replay done: %d events", len(sched)), 0, nil)
	return nil
}
