package recording

import (
	"encoding/gob"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

func axis(i, j int, tilt float64) []float32 {
	v := make([]float32, 4)
	v[i] = float32(math.Sqrt(1 - tilt*tilt))
	v[j] += float32(tilt)
	return v
}

// saveSamplesMeeting saves a meeting whose speaker the owner named Ilse
// Novak, though two voices spoke under that name: A for 16 fingerprints
// (0-19 s) and 2 more (40-45 s), B for 12 (20-35 s, after a pause).
// Each chunk's fingerprint spans 4 s from its chunk number.
func saveSamplesMeeting(t *testing.T, r *Recorder, id, model string, at time.Time) string {
	t.Helper()
	p := &diarize.Prepared{Meta: diarize.Meta{SampleRate: 1, WindowSize: 4, WindowShift: 1, ReceptiveFieldSize: 1, ReceptiveFieldShift: 1}}
	for range 50 {
		p.Labels = append(p.Labels, [][]int8{{1}, {1}, {1}, {1}})
	}
	add := func(c int, e []float32) {
		p.Pairs = append(p.Pairs, diarize.ChunkSpeaker{Chunk: c})
		p.Embeddings = append(p.Embeddings, e)
	}
	for c := range 16 {
		add(c, axis(0, 1+c%3, 0.1))
	}
	for c := 22; c < 34; c++ {
		add(c, axis(2, c%2, 0.1))
	}
	add(40, axis(0, 3, 0.2))
	add(41, axis(0, 1, 0.2))
	dir := filepath.Join(config.SessionDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		t.Fatal(err)
	}
	if err := gob.NewEncoder(f).Encode(p); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, _ := json.Marshal(diarize.StreamInfo{Model: model})
	if err := os.WriteFile(filepath.Join(dir, "diarization.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &session.Session{ID: id, CreatedAt: at, Segments: []session.Segment{
		{ID: "1", Speaker: "Ilse Novak", StartTime: 0, EndTime: 50, Source: "monitor"},
	}}
	if err := r.store.Save(sess); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProfileSamples(t *testing.T) {
	r := newTestRecorder(t)
	model := "test-samples-" + t.Name()
	m := r.config().Meeting
	const id = "11111111-2222-3333-4444-555555555555"
	dir := saveSamplesMeeting(t, r, id, model, time.Now().Add(-time.Hour))
	sess, err := r.loadSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := diarize.LearnSaved(m, dir, sess, nil); err != nil {
		t.Fatal(err)
	}
	store, err := r.voiceStore(model)
	if err != nil {
		t.Fatal(err)
	}
	// A second meeting, whose fingerprints aren't saved.
	const bare = "99999999-2222-3333-4444-555555555555"
	if err := r.store.Save(&session.Session{ID: bare, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var v voices.Voice
	for range 12 {
		v.Add(axis(0, 1, 0.1))
	}
	if _, err := store.Learn(bare, time.Now(), []voices.Learned{{Name: "Ilse Novak", Source: voices.FromNamed, Voice: v, Clip: voices.Clip{Start: 5, End: 9}}}); err != nil {
		t.Fatal(err)
	}
	profs := store.Profiles()
	if len(profs) != 1 {
		t.Fatalf("profiles %+v", profs)
	}
	pid := profs[0].ID

	view, err := r.ProfileSamples(model, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Meetings) != 2 || view.Samples != 3 || view.Voices != 2 {
		t.Fatalf("view: %d meetings, %d samples, %d voices", len(view.Meetings), view.Samples, view.Voices)
	}
	mt, missing := view.Meetings[0], view.Meetings[1]
	if mt.Session != id {
		mt, missing = missing, mt
	}
	if !missing.NoDiarization || len(missing.Samples) != 0 || missing.Clip.End != 9 {
		t.Errorf("meeting without fingerprints: %+v", missing)
	}
	if mt.Fingerprints != 30 || mt.Rebuilt != 30 || mt.Voices != 2 || len(mt.Samples) != 3 {
		t.Fatalf("meeting: %+v", mt)
	}
	// Worst match first: B (12, unlike the rest), then A's samples.
	b := mt.Samples[0]
	if b.Start != 22 || b.End != 37 || b.Fingerprints != 12 || b.Label != "Ilse Novak" {
		t.Errorf("worst sample %+v", b)
	}
	for i := 1; i < len(mt.Samples); i++ {
		if mt.Samples[i].Similarity < mt.Samples[i-1].Similarity {
			t.Errorf("not worst first: %+v", mt.Samples)
		}
	}
	if b.RestSimilarity > 0.3 {
		t.Errorf("B sounds like the rest: %+v", b)
	}

	// Not them: B leaves the meeting's voice.
	if err := r.sampleEdit(true, control.SampleArgs{Model: model, ID: pid, Session: id, Start: b.Start, End: b.End}); err != nil {
		t.Fatal(err)
	}
	heardN := func() int {
		for _, h := range store.Profiles()[0].Meetings {
			if h.Session == id {
				return h.Voice.N
			}
		}
		return -1
	}
	if n := heardN(); n != 18 {
		t.Fatalf("after Not them: %d fingerprints", n)
	}
	view, _ = r.ProfileSamples(model, pid)
	for _, mv := range view.Meetings {
		if mv.Session != id {
			continue
		}
		if mv.Voices != 1 || len(mv.Samples) != 3 || !mv.Samples[2].Excluded || mv.Samples[2].Start != 22 {
			t.Errorf("after Not them: %+v", mv)
		}
	}
	if view.Samples != 2 || view.Voices != 1 {
		t.Errorf("profile after Not them: %d samples, %d voices", view.Samples, view.Voices)
	}
	// It survives learning the meeting again.
	if _, err := diarize.LearnSaved(m, dir, sess, nil); err != nil {
		t.Fatal(err)
	}
	if n := heardN(); n != 18 {
		t.Fatalf("relearnt: %d fingerprints", n)
	}
	// A span not shown is refused.
	if err := r.sampleEdit(true, control.SampleArgs{Model: model, ID: pid, Session: id, Start: 1, End: 2}); err == nil {
		t.Error("a made-up span was taken out")
	}
	// Undo.
	if err := r.sampleEdit(false, control.SampleArgs{Model: model, ID: pid, Session: id, Start: 22, End: 37}); err != nil {
		t.Fatal(err)
	}
	if n := heardN(); n != 30 {
		t.Fatalf("after undo: %d fingerprints", n)
	}
	if _, err := r.Handle(t.Context(), control.OpProfileSamples, json.RawMessage(`{"model":"`+model+`","id":"`+pid+`"}`)); err != nil {
		t.Errorf("profile_samples over the control socket: %v", err)
	}
}
