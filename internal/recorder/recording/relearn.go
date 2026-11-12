package recording

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// When the rules for what a meeting teaches a voice profile change
// (voices.LearnRules), profiles built under the old ones are learnt
// again from every saved meeting, once, in the background after the
// models load: only between recordings, at low priority. The rules
// version is stored only when the relearn is done, so one cut short (a
// crash, a quit) simply runs again at the next start; learning a meeting
// again replaces what it taught, so running twice changes nothing more.

// relearnWait is how often a relearn waiting for a recording to end
// looks again.
var relearnWait = 30 * time.Second

// relearnBusy is Status.Busy while relearning.
const relearnBusy = "Updating voice profiles for new rules"

// relearnBusyLine is Status.Busy before the i-th of n meetings.
func relearnBusyLine(i, n int) string { return fmt.Sprintf("%s (%d of %d)", relearnBusy, i+1, n) }

// relearnForRules relearns every saved meeting if any store's profiles
// were built under older rules, then records the rules and what changed
// in each store. Returns whether it ran.
func (a *Recorder) relearnForRules(ctx context.Context) bool {
	stores, err := a.rulesStores()
	if err != nil {
		fmt.Printf("voices: can't check the learning rules: %v\n", err)
		return false
	}
	before := map[string]map[string]voices.Voice{}
	for model, s := range stores {
		if v, _ := s.Rules(); v < voices.LearnRules {
			before[model] = learntVoices(s)
		}
	}
	if len(before) == 0 {
		return false
	}
	if !a.learnMu.TryLock() {
		return false // learn_saved is running; the next start checks again
	}
	defer a.learnMu.Unlock()
	began := time.Now()
	fmt.Printf("voices: profiles built under older learning rules; learning every saved meeting again (rules %d)\n", voices.LearnRules)
	var meetings int
	var stopped bool
	diarize.LowPriority(func() {
		_, meetings, err = a.learnAllSaved(func(i, n int) bool {
			if !a.waitNotRecording(ctx) {
				stopped = true
				return false
			}
			a.setBusy(relearnBusyLine(i, n))
			return true
		})
	})
	a.clearBusy(relearnBusy)
	if err != nil || stopped {
		fmt.Printf("voices: relearn for new rules not finished (%v); it runs again at the next start\n", err)
		return true
	}
	after, err := a.rulesStores()
	if err != nil {
		fmt.Printf("voices: relearn done but not recorded: %v\n", err)
		return true
	}
	for model, s := range after {
		old, stale := before[model]
		if !stale {
			continue
		}
		sum := voices.Relearned{At: time.Now(), Meetings: meetings}
		for id, v := range learntVoices(s) {
			if o, ok := old[id]; !ok {
				sum.Added++
			} else if !sameVoice(o, v) {
				sum.Changed++
			}
		}
		if err := s.SetRules(voices.LearnRules, sum); err != nil {
			fmt.Printf("voices: relearn for %s done but not recorded: %v\n", model, err)
			continue
		}
		fmt.Printf("voices: %s relearnt under rules %d from %d meetings in %.0fs: %d profiles changed, %d new\n",
			model, voices.LearnRules, meetings, time.Since(began).Seconds(), sum.Changed, sum.Added)
	}
	return true
}

// rulesStores are the profile stores the relearn checks (all that exist).
func (a *Recorder) rulesStores() (map[string]*voices.Store, error) {
	if a.relearnStores != nil {
		return a.relearnStores()
	}
	return a.voiceStores()
}

// waitNotRecording waits while a meeting records or a stopped one is
// still being saved, with the relearn's busy line cleared meanwhile;
// false if ctx ended first.
func (a *Recorder) waitNotRecording(ctx context.Context) bool {
	for {
		a.mu.Lock()
		busy := a.rec != nil || len(a.saving) > 0
		a.mu.Unlock()
		if !busy {
			return ctx.Err() == nil
		}
		a.clearBusy(relearnBusy)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(relearnWait):
		}
	}
}

// clearBusy clears Status.Busy if it still starts with what.
func (a *Recorder) clearBusy(what string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.busy) >= len(what) && a.busy[:len(what)] == what {
		a.busy = ""
		a.publishStatusLocked()
	}
}

// learntVoices is each profile's learnt voice, by ID.
func learntVoices(s *voices.Store) map[string]voices.Voice {
	minLearn := s.MinLearn()
	out := map[string]voices.Voice{}
	for _, p := range s.Profiles() {
		out[p.ID] = p.LearnedVoice(minLearn)
	}
	return out
}

// sameVoice: the same fingerprints, allowing for the order they were
// summed in.
func sameVoice(a, b voices.Voice) bool {
	if a.N != b.N || len(a.Sum) != len(b.Sum) {
		return false
	}
	for i := range a.Sum {
		if math.Abs(float64(a.Sum[i]-b.Sum[i])) > 1e-3 {
			return false
		}
	}
	return true
}
