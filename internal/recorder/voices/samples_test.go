package voices

import (
	"path/filepath"
	"testing"
	"time"
)

// learnedWithParts is a voice of n fingerprints one second apart from
// start, each emb, its parts kept.
func learnedWithParts(name string, start float64, embs ...[]float32) Learned {
	l := Learned{Name: name, Source: FromTile}
	for i, e := range embs {
		pt := Part{Start: start + float64(i), End: start + float64(i) + 1, Emb: e}
		l.Voice.Add(e)
		l.Parts = append(l.Parts, pt)
	}
	l.Clip = Clip{Start: start, End: start + 1}
	return l
}

func TestExcludeAndIncludeASample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.json")
	s, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	a, b := unit(0, 1, 0.1), unit(2, 3, 0.1)
	var embs [][]float32
	for range 12 {
		embs = append(embs, a)
	}
	for range 3 {
		embs = append(embs, b) // 12..14 s: someone else
	}
	l := learnedWithParts("Ilse Novak", 0, embs...)
	at := time.Now()
	if _, err := s.Learn("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	id := s.Profiles()[0].ID
	heard := func() Heard { return s.Profiles()[0].Meetings[0] }
	if heard().Voice.N != 15 {
		t.Fatalf("learnt %d", heard().Voice.N)
	}
	span := Clip{Start: 12, End: 15}
	other := voiceOf(3, b)
	if err := s.ExcludeSpan(id, "m1", span, other, ""); err != nil {
		t.Fatal(err)
	}
	h := heard()
	if h.Voice.N != 12 || Similarity(h.Voice.Centroid(), a) < 0.9999 || len(h.ExcludedSpans) != 1 {
		t.Fatalf("after taking the sample out: %+v", h)
	}
	if err := s.ExcludeSpan(id, "m1", span, other, ""); err == nil {
		t.Error("taking a sample out twice was allowed")
	}

	// Learning the meeting again leaves the span out, and keeps it.
	if _, err := s.Learn("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	h = heard()
	if h.Voice.N != 12 || len(h.ExcludedSpans) != 1 || Similarity(h.Voice.Centroid(), a) < 0.9999 {
		t.Fatalf("relearnt: %+v", h)
	}
	// And so does the store read again from disk.
	s2, err := Open(path, sameFold, DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if h := s2.Profiles()[0].Meetings[0]; h.Voice.N != 12 || len(h.ExcludedSpans) != 1 {
		t.Fatalf("reread: %+v", h)
	}

	if err := s.IncludeSpan(id, "m1", span, other); err != nil {
		t.Fatal(err)
	}
	h = heard()
	if h.Voice.N != 15 || len(h.ExcludedSpans) != 0 {
		t.Fatalf("put back: %+v", h)
	}
	if err := s.IncludeSpan(id, "m1", span, other); err == nil {
		t.Error("putting back a sample that isn't out was allowed")
	}
	if _, err := s.Learn("m1", at, []Learned{l}); err != nil {
		t.Fatal(err)
	}
	if h := heard(); h.Voice.N != 15 {
		t.Fatalf("relearnt after putting back: %+v", h)
	}
}

func TestWithoutSpansMovesTheClip(t *testing.T) {
	a := unit(0, 1, 0)
	l := learnedWithParts("Ilse Novak", 0, a, a, a)
	l.Clip = Clip{Start: 0, End: 1}
	got := withoutSpans(l, []Clip{{Start: 0, End: 1}})
	if got.Voice.N != 2 || got.Clip.Start < 1 {
		t.Errorf("%+v", got)
	}
	if got := withoutSpans(Learned{Voice: voiceOf(3, a)}, []Clip{{Start: 0, End: 9}}); got.Voice.N != 3 {
		t.Errorf("a voice without parts changed: %+v", got)
	}
}

func TestVoiceSub(t *testing.T) {
	v := voiceOf(3, unit(0, 1, 0))
	if err := v.Sub(voiceOf(4, unit(0, 1, 0))); err == nil {
		t.Error("took out more than there is")
	}
	if err := v.Sub(voiceOf(3, unit(0, 1, 0))); err != nil || v.N != 0 || v.Sum[0] != 0 {
		t.Errorf("%v %+v", err, v)
	}
}
