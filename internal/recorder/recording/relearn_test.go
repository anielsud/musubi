package recording

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// A meeting's specks of crosstalk (one- and two-fingerprint samples)
// don't add up to a second voice: only samples of MinLearn fingerprints
// or more count.
func TestSampleVoicesIgnoreSpecks(t *testing.T) {
	params := voices.DefaultParams()
	l := &voices.Learned{Name: "Ilse Novak", Source: voices.FromTile}
	for i := range 15 { // the person: one stretch of 15
		l.Parts = append(l.Parts, voices.Part{Start: float64(i), End: float64(i) + 1, Emb: axis(0, 1+i%3, 0.1)})
	}
	for k := range 7 { // someone else: 7 specks of 2, far apart
		at := 100 + 20*float64(k)
		for j := range 2 {
			l.Parts = append(l.Parts, voices.Part{Start: at + float64(j)*0.5, End: at + float64(j)*0.5 + 1, Emb: axis(2, 3, 0.1)})
		}
	}
	if m := samplesOf(l, nil, nil, nil, params); m.voices != 1 || len(m.kept) != 8 {
		t.Fatalf("%d voices in %d samples, want 1 in 8", m.voices, len(m.kept))
	}
	// The same 14 fingerprints in one stretch are a second voice.
	l.Parts = l.Parts[:15]
	for j := range 14 {
		l.Parts = append(l.Parts, voices.Part{Start: 100 + float64(j), End: 101 + float64(j), Emb: axis(2, 3, 0.1)})
	}
	if m := samplesOf(l, nil, nil, nil, params); m.voices != 2 {
		t.Fatalf("%d voices, want 2", m.voices)
	}
}

// writeOldVoices writes a voices file as profiles built before the rules
// version was recorded: one profile that learnt meeting id from a tile.
func writeOldVoices(t *testing.T, model, id string) {
	t.Helper()
	var v voices.Voice
	for range 12 {
		v.Add(axis(3, 0, 0.1))
	}
	f := map[string]any{"version": 1, "profiles": []map[string]any{{
		"id": "old0000000000001", "name": "Odo Example", "created": time.Now(),
		"meetings": []voices.Heard{{Session: id, At: time.Now(), Source: voices.FromTile, Voice: v}},
	}}}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	path := voices.Path(config.DataDir(), model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Profiles built under older rules are learnt again from every saved
// meeting once, not while a meeting records, and the rules and what
// changed are recorded; the next start doesn't relearn.
func TestRelearnForNewRules(t *testing.T) {
	r := newTestRecorder(t)
	model := "test-relearn-" + t.Name()
	const id = "11111111-2222-3333-4444-666666666666"
	saveSamplesMeeting(t, r, id, model, time.Now().Add(-time.Hour))
	writeOldVoices(t, model, id)
	store, err := diarize.VoiceStore(r.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	r.relearnStores = func() (map[string]*voices.Store, error) { return map[string]*voices.Store{model: store}, nil }
	defer func(w time.Duration) { relearnWait = w }(relearnWait)
	relearnWait = 5 * time.Millisecond

	r.mu.Lock()
	r.rec = &activeRecording{} // a meeting is recording
	r.mu.Unlock()
	done := make(chan bool)
	go func() { done <- r.relearnForRules(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if v, _ := store.Rules(); v != 1 || len(store.Profiles()) != 1 {
		t.Fatalf("relearnt while recording: rules %d, %d profiles", v, len(store.Profiles()))
	}
	if st := r.Status(); st.Busy != "" {
		t.Errorf("busy while waiting: %q", st.Busy)
	}
	r.mu.Lock()
	r.rec = nil
	r.mu.Unlock()
	select {
	case ran := <-done:
		if !ran {
			t.Fatal("didn't relearn")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relearn didn't finish")
	}
	v, sum := store.Rules()
	if v != voices.LearnRules || sum == nil || sum.Meetings != 1 || sum.Changed != 1 || sum.Added != 1 || time.Since(sum.At) > time.Minute {
		t.Fatalf("rules %d, summary %+v", v, sum)
	}
	// Odo's automatic contribution from the meeting is gone; Ilse, whom
	// the owner named there, is new.
	for _, p := range store.Profiles() {
		if p.Name == "Odo Example" && len(p.Meetings) != 0 {
			t.Errorf("Odo kept %+v", p.Meetings)
		}
	}
	if st := r.Status(); st.Busy != "" {
		t.Errorf("busy after: %q", st.Busy)
	}
	if r.relearnForRules(context.Background()) {
		t.Error("relearnt again under the same rules")
	}
}

// The busy line counts the meetings.
func TestRelearnBusyLine(t *testing.T) {
	r := newTestRecorder(t)
	model := "test-relearn-" + t.Name()
	const id = "11111111-2222-3333-4444-777777777777"
	saveSamplesMeeting(t, r, id, model, time.Now().Add(-time.Hour))
	writeOldVoices(t, model, id)
	store, err := diarize.VoiceStore(r.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	r.relearnStores = func() (map[string]*voices.Store, error) { return map[string]*voices.Store{model: store}, nil }
	var seen []string
	_, _, _ = r.learnAllSaved(func(i, n int) bool {
		r.setBusy(relearnBusyLine(i, n))
		seen = append(seen, r.Status().Busy)
		return true
	})
	r.clearBusy(relearnBusy)
	if len(seen) != 1 || seen[0] != "Updating voice profiles for new rules (1 of 1)" || !strings.HasPrefix(seen[0], relearnBusy) || r.Status().Busy != "" {
		t.Fatalf("busy %q, then %q", seen, r.Status().Busy)
	}
}

func TestAddRelearned(t *testing.T) {
	a := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	v := addRelearned(nil, &voices.Relearned{At: a, Meetings: 5, Changed: 2, Added: 1})
	v = addRelearned(v, &voices.Relearned{At: a.Add(time.Minute), Meetings: 4, Changed: 1})
	if !v.At.Equal(a.Add(time.Minute)) || v.Meetings != 5 || v.Changed != 3 || v.Added != 1 {
		t.Fatalf("%+v", v)
	}
}
