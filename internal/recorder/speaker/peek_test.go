package speaker

import (
	"testing"
	"time"
)

func TestTrackerPeekLeavesStateAlone(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now
	expect := func(emb []float32, want, when string) {
		t.Helper()
		if got := tracker.Peek(emb); got != want {
			t.Errorf("Peek %s = %q, want %q", when, got, want)
		}
	}

	expect([]float32{1, 0, 0, 0}, "Person 1", "before anyone has spoken")
	if n := tracker.NumSpeakers(); n != 0 {
		t.Fatalf("Peek created %d speakers", n)
	}

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	expect([]float32{0.98, 0.12, 0, 0}, "Person 1", "on a close match")

	// Someone new starts just after Person 1: provisionally still
	// Person 1, and the next new speaker once the grace window is over.
	clock.advance(400 * time.Millisecond)
	expect([]float32{0, 0, 1, 0}, "Person 1", "inside the grace window")
	clock.advance(DefaultTuning().StickyGraceWindow)
	expect([]float32{0, 0, 1, 0}, "Person 2", "after the grace window")

	expect(nil, "Unknown", "without a fingerprint")
	if n := tracker.NumSpeakers(); n != 1 {
		t.Errorf("%d speakers after peeking, want 1", n)
	}
}

func TestTrackerPeekRespectsStickyThresholdMarginDisabled(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tuning := tracker.Tuning()
	tuning.StickyThresholdMargin = 0
	tracker.SetTuning(tuning)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	clock.advance(time.Second)                           // well within StickyGraceWindow

	// With the sticky heuristic off (see Assign), Peek must not guess
	// "still Person 1" from elapsed time alone either -- a provisional
	// label should never show sticky continuity the final Assign has
	// been configured to never apply.
	if got := tracker.Peek([]float32{0, 1, 0, 0}); got != "Person 2" {
		t.Errorf("Peek with sticky disabled = %q, want %q (next new speaker)", got, "Person 2")
	}
}
