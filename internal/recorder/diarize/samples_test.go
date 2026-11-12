package diarize

import (
	"math"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// tilted is a 4-dimensional unit fingerprint along axis i, tipped a
// little toward axis j.
func tilted(i, j int, tilt float64) []float32 {
	v := make([]float32, 4)
	v[i] = float32(math.Sqrt(1 - tilt*tilt))
	v[j] += float32(tilt)
	return v
}

// samplesPrepared is a recording of chunks one second apart, each four
// frames of one second with its one local speaker talking throughout
// (chunk c's fingerprint spans c..c+4 s); fps maps a chunk to its
// fingerprint.
func samplesPrepared(chunks int, fps map[int][]float32) *Prepared {
	p := &Prepared{Meta: Meta{SampleRate: 1, WindowSize: 4, WindowShift: 1, ReceptiveFieldSize: 1, ReceptiveFieldShift: 1}}
	for range chunks {
		p.Labels = append(p.Labels, [][]int8{{1}, {1}, {1}, {1}})
	}
	for c := range chunks {
		if e, ok := fps[c]; ok {
			p.Pairs = append(p.Pairs, ChunkSpeaker{Chunk: c})
			p.Embeddings = append(p.Embeddings, e)
		}
	}
	return p
}

func sumOf(embs ...[]float32) []float32 {
	out := make([]float32, len(embs[0]))
	for _, e := range embs {
		for i, x := range e {
			out[i] += x
		}
	}
	return out
}

func sameSum(t *testing.T, what string, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: sum %v, want %v", what, got, want)
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("%s: sum %v, want %v", what, got, want)
		}
	}
}

// Learning keeps each fingerprint and when it was heard, and the voice
// it learns is the same sum, count and clip as before it kept them.
func TestLearningKeepsItsFingerprints(t *testing.T) {
	a0, a1, a2 := tilted(0, 1, 0.1), tilted(0, 2, 0.2), tilted(0, 3, 0.1)
	b0, b1 := tilted(1, 0, 0.1), tilted(1, 2, 0.1)
	p := samplesPrepared(16, map[int][]float32{0: a0, 1: a1, 2: a2, 10: b0, 11: b1})
	who := func(t float64) string { // session time: the audio starts at 100 s
		switch {
		case t < 108:
			return "Ilse Novak"
		case t >= 110:
			return "Bodil Ekdahl"
		}
		return ""
	}
	got := VoicesFromSpeakers(p, 100, who)
	if len(got) != 2 || got[0].Name != "Bodil Ekdahl" || got[1].Name != "Ilse Novak" {
		t.Fatalf("learnt %+v", got)
	}
	ilse, bodil := got[1], got[0]
	if ilse.Voice.N != 3 || bodil.Voice.N != 2 {
		t.Fatalf("counts %d, %d", ilse.Voice.N, bodil.Voice.N)
	}
	sameSum(t, "Ilse", ilse.Voice.Sum, sumOf(a0, a1, a2))
	sameSum(t, "Bodil", bodil.Voice.Sum, sumOf(b0, b1))
	// The clip: each fingerprint has four seconds of speech, so the
	// likest the voice wins, in session time.
	if ilse.Clip.End-ilse.Clip.Start != 4 || ilse.Clip.Start < 100 {
		t.Errorf("clip %+v", ilse.Clip)
	}
	if len(ilse.Parts) != 3 {
		t.Fatalf("parts %+v", ilse.Parts)
	}
	var embs [][]float32
	for i, pt := range ilse.Parts {
		embs = append(embs, pt.Emb)
		if pt.Start != 100+float64(i) || pt.End != pt.Start+4 {
			t.Errorf("part %d at %g-%g", i, pt.Start, pt.End)
		}
	}
	sameSum(t, "Ilse's parts", sumOf(embs...), ilse.Voice.Sum)

	// By tiles: the same selection as by the transcript here.
	var hints []Hint
	for s := 0.0; s < 8; s++ {
		hints = append(hints, Hint{T: s + hintLag, Name: "Ilse Novak"})
	}
	for s := 10.0; s < 16; s++ {
		hints = append(hints, Hint{T: s + hintLag, Name: "Bodil Ekdahl"})
	}
	byTile := VoicesFromTiles(p, hints, 0)
	if len(byTile) != 2 || byTile[1].Voice.N != 3 || byTile[1].Source != voices.FromTile {
		t.Fatalf("by tiles %+v", byTile)
	}
	sameSum(t, "Ilse by tile", byTile[1].Voice.Sum, sumOf(a0, a1, a2))

	// SavedLearning: the tiles, then the speakers the owner named.
	sess := &session.Session{Segments: []session.Segment{{Speaker: "Bodil Ekdahl", StartTime: 10, EndTime: 16}}}
	saved := SavedLearning(p, 0, sess, hints)
	if len(saved) != 3 || saved[2].Name != "Bodil Ekdahl" || saved[2].Source != voices.FromNamed || saved[2].Voice.N != 2 {
		t.Fatalf("saved learning %+v", saved)
	}
}

func TestGroupSamples(t *testing.T) {
	e := tilted(0, 1, 0)
	parts := []voices.Part{
		{Start: 11, End: 13, Emb: e},
		{Start: 0, End: 4, Emb: e},
		{Start: 5.5, End: 6, Emb: e}, // 1.5 s after the first: the same sample
		{Start: 1, End: 2, Emb: e},   // inside the first
		{Start: 8, End: 8, Emb: e},   // no speech: left out
	}
	got := GroupSamples(parts, SampleGap)
	if len(got) != 2 {
		t.Fatalf("samples %+v", got)
	}
	if got[0].Start != 0 || got[0].End != 6 || len(got[0].Parts) != 3 || got[1].Start != 11 || got[1].End != 13 {
		t.Errorf("samples %+v", got)
	}
	if v := got[0].Voice(); v.N != 3 {
		t.Errorf("first sample's voice %+v", v)
	}
}

func TestCountVoices(t *testing.T) {
	sample := func(start float64, n int, emb func(k int) []float32) Sample {
		s := Sample{Start: start, End: start + float64(n)}
		for k := range n {
			s.Parts = append(s.Parts, voices.Part{Start: start + float64(k), End: start + float64(k) + 1, Emb: emb(k)})
		}
		return s
	}
	a := func(k int) []float32 { return tilted(0, 1+k%3, 0.15) }
	b := func(k int) []float32 { return tilted(2, k%2, 0.15) }
	one := []Sample{sample(0, 20, a), sample(30, 15, a), sample(60, 12, a)}
	if n := CountVoices(one, 0.7, 10, 0.05); n != 1 {
		t.Errorf("one voice counted as %d", n)
	}
	two := append(append([]Sample(nil), one...), sample(90, 20, b), sample(120, 11, b))
	if n := CountVoices(two, 0.7, 10, 0.05); n != 2 {
		t.Errorf("two voices counted as %d", n)
	}
	// A few seconds of another voice is a tail, not a voice.
	tail := append(append([]Sample(nil), one...), sample(90, 2, b))
	if n := CountVoices(tail, 0.7, 10, 0.05); n != 1 {
		t.Errorf("a short tail counted: %d", n)
	}
	if n := CountVoices(nil, 0.7, 10, 0.05); n != 0 {
		t.Errorf("no samples: %d", n)
	}
}
