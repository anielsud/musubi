package speaker

import (
	"testing"
	"time"
)

func TestTrackerJoinsCloseFingerprints(t *testing.T) {
	tracker := NewTracker(0.8)
	emb1 := []float32{0, 0, 1, 0, 0}
	emb2 := []float32{0, 0.02, 0.98, 0, 0}
	label1, _ := tracker.Assign(emb1, 2*time.Second)
	label2, _ := tracker.Assign(emb2, 2*time.Second)
	if label1 != "Person 1" || label2 != label1 {
		t.Errorf("labels %q then %q, want Person 1 for both", label1, label2)
	}
	if n := tracker.NumSpeakers(); n != 1 {
		t.Errorf("%d speakers, want 1", n)
	}
}

func TestTrackerSplitsUnrelatedFingerprints(t *testing.T) {
	tracker := NewTracker(0.8)
	emb1 := []float32{0, 0, 0, 1, 0}
	emb2 := []float32{0, 0, 0, 0, 1}
	label1, _ := tracker.Assign(emb1, 2*time.Second)
	label2, _ := tracker.Assign(emb2, 2*time.Second)
	if label1 != "Person 1" || label2 != "Person 2" {
		t.Errorf("labels %q then %q, want Person 1 then Person 2", label1, label2)
	}
	if n := tracker.NumSpeakers(); n != 2 {
		t.Errorf("%d speakers, want 2", n)
	}
}

func TestTrackerThresholdSeparatesNearMatches(t *testing.T) {
	// With a very high threshold, similar vectors should still be
	// different speakers -- once the sticky-speaker grace window (see
	// stickyGraceWindow) has elapsed, so this isolates the pure
	// threshold check from that separate, intentional exception.
	tracker := NewTracker(0.999)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now
	// About 0.95 apart: one speaker at any ordinary threshold.
	emb1 := []float32{0, 1, 0}
	emb2 := []float32{0.3, 0.95, 0}
	tracker.Assign(emb1, 2*time.Second)
	clock.advance(DefaultTuning().StickyGraceWindow + time.Second)
	label2, _ := tracker.Assign(emb2, 2*time.Second)
	if label2 != "Person 2" {
		t.Errorf("near match under a 0.999 threshold went to %q, want a new speaker", label2)
	}
}

func TestTrackerResetStartsNumberingAgain(t *testing.T) {
	tracker := NewTracker(0.8)
	tracker.Assign([]float32{1, 0, 0}, 2*time.Second)
	tracker.Assign([]float32{0, 1, 0}, 2*time.Second)
	if n := tracker.NumSpeakers(); n != 2 {
		t.Fatalf("%d speakers before Reset, want 2", n)
	}
	tracker.Reset()
	if n := tracker.NumSpeakers(); n != 0 {
		t.Fatalf("%d speakers after Reset, want none", n)
	}
	label, _ := tracker.Assign([]float32{1, 0, 0}, 2*time.Second)
	if label != "Person 1" {
		t.Errorf("first speaker after Reset is %q, want Person 1", label)
	}
}

func TestTrackerUnknownWithoutFingerprint(t *testing.T) {
	tracker := NewTracker(0.8)
	label, _ := tracker.Assign(nil, 2*time.Second)
	again, _ := tracker.Assign([]float32{}, 2*time.Second)
	if label != "Unknown" || again != "Unknown" {
		t.Errorf("labels for no fingerprint = %q, %q; want Unknown", label, again)
	}
	if n := tracker.NumSpeakers(); n != 0 {
		t.Errorf("an empty fingerprint made %d speakers", n)
	}
}

func TestTrackerOutOfRangeThresholdUsesDefault(t *testing.T) {
	tracker := NewTracker(0)
	if got := tracker.Tuning().Threshold; got != DefaultThreshold {
		t.Errorf("threshold = %v, want %v", got, DefaultThreshold)
	}
	for _, bad := range []float64{-1, 1.5} {
		tracker = NewTracker(bad)
		if got := tracker.Tuning().Threshold; got != DefaultThreshold {
			t.Errorf("threshold = %v, want %v", got, DefaultThreshold)
		}
	}
	if got := NewTracker(1).Tuning().Threshold; got != 1 {
		t.Errorf("NewTracker(1) threshold = %v, want 1 (in range)", got)
	}
}

func TestTrackerRecognisesReturningSpeakers(t *testing.T) {
	tracker := NewTracker(0.7)
	voices := [][]float32{{0, 0, 0, 1}, {0, 0, 1, 0}, {0, 1, 0, 0}}
	want := []string{"Person 1", "Person 2", "Person 3"}
	for round := 1; round <= 2; round++ {
		for i, emb := range voices {
			label, _ := tracker.Assign(emb, 2*time.Second)
			if label != want[i] {
				t.Errorf("round %d, voice %d: %q, want %q", round, i, label, want[i])
			}
		}
	}
	if n := tracker.NumSpeakers(); n != len(voices) {
		t.Errorf("%d speakers, want %d", n, len(voices))
	}
}

func TestTrackerSetHintForRecent(t *testing.T) {
	tracker := newRulesTracker(0.8)

	tracker.Assign([]float32{1, 0, 0}, 2*time.Second) // Person 1
	tracker.Assign([]float32{0, 1, 0}, 2*time.Second) // Person 2, most recently assigned

	if ok := tracker.SetHintForRecent("Nerissa", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	// The hint should attach to Person 2 (most recently assigned), not Person 1.
	label1, _ := tracker.Assign([]float32{0.99, 0.01, 0}, 2*time.Second)
	if label1 != "Person 1" {
		t.Errorf("Person 1 label = %q, want unchanged %q", label1, "Person 1")
	}
	label2, _ := tracker.Assign([]float32{0, 0.99, 0.01}, 2*time.Second)
	if label2 != "Person 2 (Nerissa)" {
		t.Errorf("Person 2 label = %q, want %q", label2, "Person 2 (Nerissa)")
	}
}

func TestTrackerSetHintForRecent_KeepsFullNameOverLaterTruncation(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tracker.Assign([]float32{0, 1, 0}, 2*time.Second) // Person 1

	if ok := tracker.SetHintForRecent("Nerissa Ravelin", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}
	// Re-select Person 1 as most recently assigned, then attach a
	// truncated read of the same name (as a tile label might produce
	// on a later OCR pass) -- it must not clobber the full name.
	tracker.Assign([]float32{0, 0.99, 0.01}, 2*time.Second)
	if ok := tracker.SetHintForRecent("Nerissa Rave…", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	label, _ := tracker.Assign([]float32{0, 0.98, 0.02}, 2*time.Second)
	if label != "Person 1 (Nerissa Ravelin)" {
		t.Errorf("label = %q, want full name kept, got truncated overwrite", label)
	}
}

func TestTrackerSetHintForRecent_UpgradesTruncatedNameToFuller(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tracker.Assign([]float32{0, 1, 0}, 2*time.Second) // Person 1

	tracker.SetHintForRecent("Nerissa Rave…", time.Minute)
	tracker.Assign([]float32{0, 0.99, 0.01}, 2*time.Second)
	if ok := tracker.SetHintForRecent("Nerissa Ravelin", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	label, _ := tracker.Assign([]float32{0, 0.98, 0.02}, 2*time.Second)
	if label != "Person 1 (Nerissa Ravelin)" {
		t.Errorf("label = %q, want the fuller name to replace the truncated one", label)
	}
}

func TestTrackerSetHintForRecent_UnrelatedNameOverwrites(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tracker.Assign([]float32{0, 1, 0}, 2*time.Second) // Person 1

	tracker.SetHintForRecent("Nerissa Ravelin", time.Minute)
	tracker.Assign([]float32{0, 0.99, 0.01}, 2*time.Second)
	if ok := tracker.SetHintForRecent("Cador Wexingworth", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	label, _ := tracker.Assign([]float32{0, 0.98, 0.02}, 2*time.Second)
	if label != "Person 1 (Cador Wexingworth)" {
		t.Errorf("label = %q, want an unrelated name to overwrite normally", label)
	}
}

func TestTrackerSetHintForRecent_TooOld(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tracker.Assign([]float32{1, 0, 0}, 2*time.Second)

	if ok := tracker.SetHintForRecent("Someone", -time.Second); ok {
		t.Error("SetHintForRecent() with a negative maxAge = true, want false")
	}
}

func TestTrackerSetHintForRecent_NoSpeakersYet(t *testing.T) {
	tracker := newRulesTracker(0.8)
	if ok := tracker.SetHintForRecent("Someone", time.Minute); ok {
		t.Error("SetHintForRecent() before any Assign = true, want false")
	}
}

func TestTrackerResetClearsHints(t *testing.T) {
	tracker := NewTracker(0.8)
	tracker.Assign([]float32{1, 0, 0}, 2*time.Second)
	tracker.SetHintForRecent("Nerissa", time.Minute)
	tracker.Reset()

	label, _ := tracker.Assign([]float32{1, 0, 0}, 2*time.Second)
	if label != "Person 1" {
		t.Errorf("after reset, label = %q, want plain %q (hint should be cleared)", label, "Person 1")
	}
}

// fakeClock lets tests deterministically control Tracker.nowFn without
// sleeping — see stickyGraceWindow's doc comment for why Assign needs
// to be time-aware at all.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestTrackerStickySpeaker_AcceptsNearMissFromRecentSpeaker(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	label1, _ := tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	if label1 != "Person 1" {
		t.Fatalf("first label = %q, want %q", label1, "Person 1")
	}

	// A noisier embedding from the same speaker's next short sentence:
	// similarity to Person 1 falls just under the strict 0.8 threshold,
	// but within the sticky margin (0.15), and it's well within the
	// grace window.
	clock.advance(500 * time.Millisecond)
	near := []float32{0.7, 0.7141428, 0, 0} // cosine sim to {1,0,0,0} ~= 0.70
	label2, _ := tracker.Assign(near, 2*time.Second)
	if label2 != "Person 1" {
		t.Errorf("near-miss recent-speaker label = %q, want %q (sticky match)", label2, "Person 1")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d, want 1 (sticky match should not create a new speaker)", tracker.NumSpeakers())
	}
}

func TestTrackerStickySpeaker_DoesNotApplyPastGraceWindow(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1

	clock.advance(DefaultTuning().StickyGraceWindow + time.Second)
	near := []float32{0.7, 0.7141428, 0, 0}
	label2, _ := tracker.Assign(near, 2*time.Second)
	if label2 != "Person 2" {
		t.Errorf("near-miss outside grace window label = %q, want %q (new speaker)", label2, "Person 2")
	}
}

func TestTrackerStickySpeaker_DoesNotApplyToADifferentBestMatch(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.Assign([]float32{0, 0, 0, 1}, 2*time.Second) // Person 2, now the most recently assigned

	// A genuinely poor match for everyone (best match is Person 1, but
	// nowhere near even the sticky margin) must still become a new
	// speaker, regardless of timing.
	clock.advance(100 * time.Millisecond)
	label, _ := tracker.Assign([]float32{0.3, 0.3, 0.3, 0.3}, 2*time.Second)
	if label != "Person 3" {
		t.Errorf("poor match label = %q, want %q (new speaker)", label, "Person 3")
	}
}

func TestTrackerStickySpeaker_DoesNotPolluteCentroid(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1's centroid: {1,0,0,0}

	clock.advance(time.Second)
	tracker.Assign([]float32{0.7, 0.7141428, 0, 0}, 2*time.Second) // sticky match, must not update the centroid

	// A fresh, perfectly-orthogonal embedding should still be judged
	// against Person 1's ORIGINAL centroid, not one dragged toward the
	// noisier sticky-matched sample.
	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second)
	if label == "Person 1" {
		t.Errorf("orthogonal embedding matched Person 1 — centroid was polluted by the sticky match")
	}
}

func TestTrackerShortSegment_DefaultsToLastSpeakerEvenOnPoorMatch(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1

	// A short interjection ("Um") whose embedding doesn't even clear the
	// sticky margin against Person 1 -- without the short-segment
	// fallback this would become a brand-new speaker, reproducing the
	// real-world failure this fix targets (a dozen-plus new "Person N"
	// labels for one continuous speaker's filler words).
	clock.advance(2 * time.Second)
	label, _ := tracker.Assign([]float32{0, 0, 1, 0}, 300*time.Millisecond)
	if label != "Person 1" {
		t.Errorf("short poor-match segment label = %q, want %q (default to last speaker)", label, "Person 1")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d, want 1 (short segment must not spawn a new speaker)", tracker.NumSpeakers())
	}
}

func TestTrackerShortSegment_DoesNotApplyPastGraceWindow(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1

	clock.advance(DefaultTuning().ShortSegmentGraceWindow + time.Second)
	label, _ := tracker.Assign([]float32{0, 0, 1, 0}, 300*time.Millisecond)
	if label != "Person 2" {
		t.Errorf("short segment far past the grace window label = %q, want %q (new speaker)", label, "Person 2")
	}
}

func TestTrackerShortSegment_NoPriorAssignmentStillCreatesNewSpeaker(t *testing.T) {
	tracker := newRulesTracker(0.8)

	// The very first segment ever seen: there's no "last speaker" to
	// default to, so a short/noisy embedding must still found a real
	// speaker rather than being silently dropped.
	label, _ := tracker.Assign([]float32{1, 0, 0, 0}, 300*time.Millisecond)
	if label != "Person 1" {
		t.Errorf("first-ever short segment label = %q, want %q", label, "Person 1")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d, want 1", tracker.NumSpeakers())
	}
}

func TestTrackerShortSegment_DoesNotPolluteCentroid(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1's centroid: {1,0,0,0}

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 0, 1, 0}, 300*time.Millisecond) // short-segment fallback, must not update the centroid

	// A fresh, perfectly-orthogonal embedding should still be judged
	// against Person 1's ORIGINAL centroid, not one dragged toward the
	// noisy short segment.
	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second)
	if label == "Person 1" {
		t.Errorf("orthogonal embedding matched Person 1 — centroid was polluted by the short-segment fallback")
	}
}

func TestTrackerShortSegment_PrefersABetterMatchingOtherSpeaker(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	// Two already-distinguished speakers, established with confident
	// matches so each has a real centroid of their own.
	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1 ("Aldo")
	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // reinforce Person 1's centroid
	clock.advance(time.Second)
	tracker.Assign([]float32{0, 0, 1, 0}, 2*time.Second) // Person 2 ("Ruvo")
	tracker.Assign([]float32{0, 0, 1, 0}, 2*time.Second) // reinforce Person 2's centroid

	// Aldo speaks again, briefly, immediately followed by an
	// even briefer reply from Ruvo -- a real quick back-and-forth.
	// Without the fix, Ruvo's short reply would default to "whoever
	// spoke last" (Aldo) purely because it's short and recent, even
	// though it's clearly a much better match for Ruvo's own centroid.
	clock.advance(time.Second)
	tracker.Assign([]float32{0.99, 0, 0.01, 0}, 2*time.Second) // Aldo again (long enough)

	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 0, 0.99, 0.01}, 300*time.Millisecond) // Ruvo's brief reply
	if label != "Person 2" {
		t.Errorf("short reply label = %q, want %q (Ruvo's own centroid, not blindly Aldo's)", label, "Person 2")
	}
}

func TestTrackerShortSegment_StillDefaultsToLastSpeakerWithoutABetterMatch(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1

	// A short, noisy segment with no strong match anywhere -- the
	// original "default to last speaker" behavior must still apply;
	// the fix only redirects when some OTHER cluster is clearly the
	// better match, not whenever bestIdx happens to differ at all.
	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 1, 0, 0}, 300*time.Millisecond)
	if label != "Person 1" {
		t.Errorf("label = %q, want %q (no better match exists, keep the default)", label, "Person 1")
	}
	if tracker.NumSpeakers() != 1 {
		t.Errorf("NumSpeakers() = %d, want 1", tracker.NumSpeakers())
	}
}

func TestTrackerShortSegment_AtMinDurationBehavesAsNormal(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1

	// Exactly at minAssignDuration (not below it) — the fallback must
	// NOT apply; a poor match at this duration is a real new speaker.
	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 0, 1, 0}, ExperimentalTuning().MinAssignDuration)
	if label != "Person 2" {
		t.Errorf("poor match exactly at minAssignDuration label = %q, want %q (new speaker)", label, "Person 2")
	}
}

func TestTrackerSetHintForRecent_MergesClustersOnMatchingHint(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.SetHintForRecent("Dorvel Sward", time.Minute)

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second) // Person 2, a distinct-sounding embedding
	if tracker.NumSpeakers() != 2 {
		t.Fatalf("NumSpeakers() = %d, want 2 before the matching hint lands", tracker.NumSpeakers())
	}

	// A video hint resolves Person 2 to the SAME real name already
	// attached to Person 1 -- independent evidence they're one person,
	// even though their embeddings didn't cluster together.
	clock.advance(time.Second)
	if ok := tracker.SetHintForRecent("Dorvel Sward", time.Minute); !ok {
		t.Fatal("SetHintForRecent() = false, want true")
	}

	if got := tracker.NumSpeakers(); got != 1 {
		t.Errorf("NumSpeakers() = %d, want 1 after merge", got)
	}

	// A fresh embedding matching either original centroid should now
	// resolve to the SAME merged identity.
	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 0.99, 0.01, 0}, 2*time.Second)
	if label != "Person 1 (Dorvel Sward)" {
		t.Errorf("label after merge = %q, want %q", label, "Person 1 (Dorvel Sward)")
	}
}

func TestTrackerSetHintForRecent_MergeKeepsMoreCompleteName(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.SetHintForRecent("Dorvel Rave…", time.Minute)

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second) // Person 2

	clock.advance(time.Second)
	tracker.SetHintForRecent("Dorvel Ravelin", time.Minute)

	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	if label != "Person 1 (Dorvel Ravelin)" {
		t.Errorf("label after merge = %q, want the fuller name kept", label)
	}
}

func TestTrackerSetHintForRecent_DoesNotMergeUnrelatedNames(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.SetHintForRecent("Dorvel Sward", time.Minute)

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second) // Person 2

	clock.advance(time.Second)
	tracker.SetHintForRecent("Jelvina Draxmeier", time.Minute)

	if got := tracker.NumSpeakers(); got != 2 {
		t.Errorf("NumSpeakers() = %d, want 2 (unrelated names must not merge)", got)
	}
}

func TestTrackerSetHintForRecent_DoesNotMergeOnAmbiguousShortFragment(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.SetHintForRecent("Ann", time.Minute)

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second) // Person 2

	clock.advance(time.Second)
	// "Ann" is too short/ambiguous a fragment to safely merge on, even
	// though it IS technically a prefix of "Annika".
	tracker.SetHintForRecent("Annika", time.Minute)

	if got := tracker.NumSpeakers(); got != 2 {
		t.Errorf("NumSpeakers() = %d, want 2 (short ambiguous fragment must not trigger a merge)", got)
	}
}

func TestTrackerSetHintForRecent_MergeDoesNotRenumberUnrelatedClusters(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
	tracker.SetHintForRecent("Dorvel Sward", time.Minute)

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second) // Person 2

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 0, 1, 0}, 2*time.Second) // Person 3, unrelated to the merge below

	clock.advance(time.Second)
	tracker.Assign([]float32{0, 1, 0, 0}, 2*time.Second)  // re-select Person 2 as most recent
	tracker.SetHintForRecent("Dorvel Sward", time.Minute) // merges Person 2 into Person 1

	clock.advance(time.Second)
	label, _ := tracker.Assign([]float32{0, 0, 0.99, 0.01}, 2*time.Second)
	if label != "Person 3" {
		t.Errorf("label = %q, want %q (merging 1&2 must not renumber Person 3)", label, "Person 3")
	}
}

func TestTrackerAssign_NeedsHintUntilOneAttaches(t *testing.T) {
	tracker := NewTracker(0.8)

	_, needsHint := tracker.Assign([]float32{1, 0, 0}, 2*time.Second)
	if !needsHint {
		t.Error("brand new speaker: needsHint = false, want true")
	}

	tracker.SetHintForRecent("Nerissa", time.Minute)

	_, needsHint = tracker.Assign([]float32{0.99, 0.01, 0}, 2*time.Second)
	if needsHint {
		t.Error("speaker with an attached hint: needsHint = true, want false")
	}
}

func TestTrackerAssign_EmptyEmbeddingNeverNeedsHint(t *testing.T) {
	tracker := NewTracker(0.8)
	_, needsHint := tracker.Assign(nil, 2*time.Second)
	if needsHint {
		t.Error("empty embedding: needsHint = true, want false (no real speaker to hint)")
	}
}

func TestIsTruncationOf(t *testing.T) {
	cases := []struct {
		short, long string
		want        bool
	}{
		{"Nerissa Rave…", "Nerissa Ravelin", true},
		{"nerissa rave", "Nerissa Ravelin", true}, // case-insensitive
		{"Nerissa Ravelin", "Nerissa Ravelin", true},
		{"Nerissa Ravelin", "Nerissa Rave…", false}, // short is actually longer
		{"Cador", "Nerissa Ravelin", false},         // not a prefix at all
		{"", "Nerissa Ravelin", false},
		{"Nerissa", "", false},
	}
	for _, c := range cases {
		if got := isTruncationOf(c.short, c.long); got != c.want {
			t.Errorf("isTruncationOf(%q, %q) = %v, want %v", c.short, c.long, got, c.want)
		}
	}
}

func TestTrackerStickySpeaker_OffWhenMarginNotPositive(t *testing.T) {
	for _, margin := range []float64{-0.25, 0} {
		tracker := newRulesTracker(0.8)
		tuning := tracker.Tuning()
		tuning.StickyThresholdMargin = margin
		tracker.SetTuning(tuning)
		clock := &fakeClock{t: time.Now()}
		tracker.nowFn = clock.now
		tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
		clock.advance(200 * time.Millisecond)
		label, _ := tracker.Assign([]float32{0.7, 0.7141428, 0, 0}, 2*time.Second) // ~0.70, a near-miss
		if label != "Person 2" {
			t.Errorf("margin %v: near-miss inside the grace window went to %q, want a new speaker", margin, label)
		}
	}
}

func TestTrackerShortSegment_DisabledByZeroMinAssignDuration(t *testing.T) {
	for _, minDur := range []time.Duration{0, -time.Second} {
		tracker := newRulesTracker(0.8)
		tuning := tracker.Tuning()
		tuning.MinAssignDuration = minDur
		tracker.SetTuning(tuning)
		clock := &fakeClock{t: time.Now()}
		tracker.nowFn = clock.now

		tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second) // Person 1
		clock.advance(time.Second)
		// A very short, poorly matching segment: the rule would give it
		// to Person 1, so with it off it must become a new speaker.
		label, _ := tracker.Assign([]float32{0, 0, 1, 0}, 100*time.Millisecond)
		if label != "Person 2" {
			t.Errorf("minAssignDuration %v: short-segment label = %q, want %q (rule off)", minDur, label, "Person 2")
		}
	}
}

func TestTrackerStickySpeaker_NarrowMarginRejectsWiderMiss(t *testing.T) {
	tracker := newRulesTracker(0.8)
	tuning := tracker.Tuning()
	tuning.StickyThresholdMargin = 0.05 // tighter than the ~0.10 miss below
	tracker.SetTuning(tuning)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now
	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	clock.advance(200 * time.Millisecond)
	label, _ := tracker.Assign([]float32{0.7, 0.7141428, 0, 0}, 2*time.Second)
	if label != "Person 2" {
		t.Errorf("a miss wider than the 0.05 margin went to %q, want a new speaker", label)
	}
}

func TestTracker_LastDecision(t *testing.T) {
	tracker := newRulesTracker(0.8)
	clock := &fakeClock{t: time.Now()}
	tracker.nowFn = clock.now

	if got := tracker.LastDecision(); got != "" {
		t.Errorf("LastDecision() before any Assign = %q, want empty", got)
	}

	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	if got := tracker.LastDecision(); got != DecisionNewSpeaker {
		t.Errorf("LastDecision() = %q, want %q", got, DecisionNewSpeaker)
	}

	clock.advance(time.Second)
	tracker.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	if got := tracker.LastDecision(); got != DecisionConfident {
		t.Errorf("LastDecision() = %q, want %q", got, DecisionConfident)
	}
}

// newRulesTracker is NewTracker with the sticky-speaker and short-segment
// rules turned on at ExperimentalTuning's values: they're off by default
// (see DefaultTuning), and these tests exercise them.
func newRulesTracker(threshold float64) *Tracker {
	t := NewTracker(threshold)
	tuning := t.Tuning()
	exp := ExperimentalTuning()
	tuning.StickyThresholdMargin = exp.StickyThresholdMargin
	tuning.MinAssignDuration = exp.MinAssignDuration
	t.SetTuning(tuning)
	return t
}

func TestDefaultTuning_MatchesMain(t *testing.T) {
	d := DefaultTuning()
	if d.Threshold != 0.65 || d.StickyThresholdMargin != 0 || d.MinAssignDuration != 0 {
		t.Errorf("DefaultTuning = %+v, want main's clustering: threshold 0.65, sticky and short-segment rules off", d)
	}
	// With both rules off, a near-miss and a short segment are new
	// speakers, exactly as on main.
	tr := NewTracker(0)
	clock := &fakeClock{t: time.Now()}
	tr.nowFn = clock.now
	tr.Assign([]float32{1, 0, 0, 0}, 2*time.Second)
	clock.advance(500 * time.Millisecond)
	if label, _ := tr.Assign([]float32{0.6, 0.8, 0, 0}, 100*time.Millisecond); label != "Person 2" {
		t.Errorf("short near-miss with default tuning = %q, want Person 2", label)
	}
}
