package recording

import (
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

func sampleItem(v *control.VoicesView, start float64) *control.UnknownVoice {
	for i := range v.Unknown {
		if s := v.Unknown[i].Sample; s != nil && s.Start == start {
			return &v.Unknown[i]
		}
	}
	return nil
}

// Not them on a sample sends it to "Who's this?"; naming it there
// teaches a new profile (none has the name) its fingerprints; "Can't
// tell" drops it; Junk never reaches "Who's this?"; all stay out of the
// profile through learning the meeting again.
func TestSampleNotThemNamedSkippedAndJunk(t *testing.T) {
	r := newTestRecorder(t)
	model := models.SpeakerModels[0].ID
	m := r.config().Meeting
	const id = "21111111-2222-3333-4444-555555555555"
	dir := saveSamplesMeeting(t, r, id, model, time.Now().Add(-200*24*time.Hour)) // older than any list's window
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
	ilse := store.Profiles()[0].ID
	heardN := func(name string) int {
		for _, p := range store.Profiles() {
			if p.Name == name {
				for _, h := range p.Meetings {
					if h.Session == id {
						return h.Voice.N
					}
				}
			}
		}
		return -1
	}
	relearn := func() {
		t.Helper()
		if _, err := diarize.LearnSaved(m, dir, sess, nil); err != nil {
			t.Fatal(err)
		}
	}
	args := func(start, end float64) control.SampleArgs {
		return control.SampleArgs{Model: model, ID: ilse, Session: id, Start: start, End: end}
	}
	r.waitReviewIndex()

	// B (22-37 s, 12 fingerprints) isn't Ilse.
	if err := r.sampleEdit(control.OpExcludeSample, args(22, 37)); err != nil {
		t.Fatal(err)
	}
	v, err := r.Voices()
	if err != nil {
		t.Fatal(err)
	}
	u := sampleItem(v, 22)
	if u == nil || !u.Returned || u.ReturnedFrom != "Ilse Novak" || u.Label != "Ilse Novak" || u.Sample.Profile != ilse || u.Sample.End != 37 || u.Clip.End != 22+clipSeconds {
		t.Fatalf("who's this: %+v", v.Unknown)
	}
	// Named there: a new profile learns its 12 fingerprints.
	var named control.NamedSample
	b, err := r.Handle(t.Context(), control.OpNameReturnedSample, mustJSON(t, control.SampleArgs{Model: model, ID: ilse, Session: id, Start: 22, End: 37, Name: "Bo Example"}))
	if err != nil {
		t.Fatal(err)
	}
	named = *b.(*control.NamedSample)
	if named.Name != "Bo Example" || !named.Created || heardN("Bo Example") != 12 || heardN("Ilse Novak") != 18 {
		t.Fatalf("named %+v: Bo %d, Ilse %d", named, heardN("Bo Example"), heardN("Ilse Novak"))
	}
	if v, _ := r.Voices(); sampleItem(v, 22) != nil {
		t.Error("a named sample is still asked about")
	}

	// Junk: A's 2 fingerprints at 40 s.
	if _, err := r.Handle(t.Context(), control.OpJunkSample, mustJSON(t, args(40, 45))); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Voices(); sampleItem(v, 40) != nil || heardN("Ilse Novak") != 16 {
		t.Fatalf("after junk: Ilse %d", heardN("Ilse Novak"))
	}
	relearn()
	if heardN("Ilse Novak") != 16 || heardN("Bo Example") != 12 {
		t.Fatalf("relearnt: Ilse %d, Bo %d", heardN("Ilse Novak"), heardN("Bo Example"))
	}
	view, _ := r.ProfileSamples(model, ilse)
	var junk, notThem int
	for _, mv := range view.Meetings {
		for _, s := range mv.Samples {
			if s.Excluded && s.Junk {
				junk++
			} else if s.Excluded {
				notThem++
			}
		}
	}
	if junk != 1 || notThem != 1 {
		t.Errorf("samples view: %d junk, %d not them", junk, notThem)
	}
	// Put back, then Not them and Can't tell.
	if err := r.sampleEdit(control.OpIncludeSample, args(40, 45)); err != nil || heardN("Ilse Novak") != 18 {
		t.Fatalf("put back: %v, Ilse %d", err, heardN("Ilse Novak"))
	}
	if err := r.sampleEdit(control.OpExcludeSample, args(40, 45)); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Voices(); sampleItem(v, 40) == nil {
		t.Fatal("not asked about")
	}
	if err := r.SkipReturnedSample(args(40, 45)); err != nil {
		t.Fatal(err)
	}
	if v, _ := r.Voices(); sampleItem(v, 40) != nil {
		t.Error("asked about after Can't tell")
	}
	relearn()
	if heardN("Ilse Novak") != 16 {
		t.Errorf("relearnt after Can't tell: Ilse %d", heardN("Ilse Novak"))
	}
	if err := r.SkipReturnedSample(args(40, 45)); err == nil {
		t.Error("skipped twice")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// saveSplitMeeting saves a meeting where the owner named one speaker
// "Ilse Novak" though two voices spoke under it: A in 0-10 s and 20-30 s,
// B in 10-20 s, a blend of both in 30-40 s, and 46-47 s with no
// fingerprints; "You" in 40-46 s and an earlier "Bo Example" line in
// 50-55 s. Each chunk's fingerprint spans 4 s from its chunk number.
func saveSplitMeeting(t *testing.T, r *Recorder, id, model string) {
	t.Helper()
	p := &diarize.Prepared{Meta: diarize.Meta{SampleRate: 1, WindowSize: 4, WindowShift: 1, ReceptiveFieldSize: 1, ReceptiveFieldShift: 1}}
	for range 60 {
		p.Labels = append(p.Labels, [][]int8{{1}, {1}, {1}, {1}})
	}
	add := func(c int, e []float32) {
		p.Pairs = append(p.Pairs, diarize.ChunkSpeaker{Chunk: c})
		p.Embeddings = append(p.Embeddings, e)
	}
	blend := voices.Normalized([]float32{1, 0, 1, 0})
	for c := range 37 {
		switch {
		case c < 8 || (c >= 20 && c < 28):
			add(c, axis(0, 1, 0.1))
		case c < 18:
			add(c, axis(2, 3, 0.1))
		case c >= 30:
			add(c, blend)
		}
	}
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
	line := func(n int, sp string, a, b float64) session.Segment {
		return session.Segment{ID: string(rune('a' + n)), Speaker: sp, Text: "line", StartTime: a, EndTime: b, Source: "monitor"}
	}
	sess := &session.Session{ID: id, CreatedAt: time.Now(), Segments: []session.Segment{
		line(0, "Ilse Novak", 0, 10), line(1, "Ilse Novak", 10, 20), line(2, "Ilse Novak", 20, 30), line(3, "Ilse Novak", 30, 40),
		line(4, "You", 40, 46), line(5, "Ilse Novak", 46, 47), line(6, "Bo Example", 50, 55),
	}}
	if err := r.store.Save(sess); err != nil {
		t.Fatal(err)
	}
}

// split_label_by_voice gives each line to the profile its voice matches
// by the margin (owner-named lines included), leaves blends and lines
// without fingerprints, and undo puts back exactly the lines it moved.
func TestSplitLabelByVoice(t *testing.T) {
	r := newTestRecorder(t)
	model := models.SpeakerModels[0].ID
	const id = "31111111-2222-3333-4444-555555555555"
	saveSplitMeeting(t, r, id, model)
	store, err := r.voiceStore(model)
	if err != nil {
		t.Fatal(err)
	}
	var a, b voices.Voice
	for range 20 {
		a.Add(axis(0, 1, 0.1))
		b.Add(axis(2, 3, 0.1))
	}
	_, _ = store.Learn("other-1", time.Now(), []voices.Learned{{Name: "Ilse Novak", Source: voices.FromTile, Voice: a}, {Name: "Bo Example", Source: voices.FromTile, Voice: b}})
	bo := profileNamed(t, store, "Bo Example")

	args := control.SplitArgs{Session: id, Label: "Ilse Novak", Into: []control.SplitTarget{{Name: "ilse novak"}, {ID: bo}}, Preview: true}
	pre, err := r.SplitLabelByVoice(args)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Batch != "" || len(pre.Counts) != 2 || pre.Counts[0].Lines != 2 || pre.Counts[1].Lines != 1 || pre.Undecided != 2 || pre.Counts[1].Label != "Bo Example" {
		t.Fatalf("preview %+v", pre)
	}
	if s, _ := r.loadSession(id); s.Segments[1].Speaker != "Ilse Novak" {
		t.Fatal("a preview changed the session")
	}
	for _, l := range pre.Lines {
		if l.Start == 46 && (l.Fingerprints != 0 || l.Label != "") {
			t.Errorf("a line without fingerprints: %+v", l)
		}
		if l.Start == 30 && l.Label != "" {
			t.Errorf("a blend was decided: %+v", l)
		}
	}

	args.Preview = false
	var res control.SplitResult
	out, err := r.Handle(t.Context(), control.OpSplitLabelByVoice, mustJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	res = *out.(*control.SplitResult)
	if res.Batch == "" {
		t.Fatalf("split %+v", res)
	}
	s, _ := r.loadSession(id)
	want := []string{"Ilse Novak", "Bo Example", "Ilse Novak", "Ilse Novak", "You", "Ilse Novak", "Bo Example"}
	for i, w := range want {
		if s.Segments[i].Speaker != w {
			t.Errorf("line %d: %q, want %q", i, s.Segments[i].Speaker, w)
		}
	}
	if s.Segments[1].LiveLabel() != "Ilse Novak" {
		t.Errorf("live label %q", s.Segments[1].LiveLabel())
	}

	if _, err := r.UndoRelabel(res.Batch); err != nil {
		t.Fatal(err)
	}
	s, _ = r.loadSession(id)
	want[1] = "Ilse Novak"
	for i, w := range want {
		if s.Segments[i].Speaker != w {
			t.Errorf("after undo, line %d: %q, want %q", i, s.Segments[i].Speaker, w)
		}
	}

	// Refused: one voice, an unknown name, a label nobody has.
	for _, bad := range []control.SplitArgs{
		{Session: id, Label: "Ilse Novak", Into: []control.SplitTarget{{ID: bo}}},
		{Session: id, Label: "Ilse Novak", Into: []control.SplitTarget{{ID: bo}, {Name: "Nobody Example"}}},
		{Session: id, Label: "Nobody", Into: []control.SplitTarget{{ID: bo}, {Name: "Ilse Novak"}}},
	} {
		if _, err := r.SplitLabelByVoice(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
