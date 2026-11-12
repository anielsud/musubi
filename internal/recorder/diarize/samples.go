package diarize

import (
	"sort"

	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// SavedLearning is what LearnSaved teaches from a saved session, in the
// order it teaches it: the voices heard while a person's own tile was
// lit (except speakers the window and the voice disagree on, and while
// the transcript names someone else), then the speakers given a name
// (GivenName), which win. Each Learned keeps its
// fingerprints (Parts), so a profile can show the samples it was learnt
// from.
func SavedLearning(p *Prepared, offset float64, sess *session.Session, hints []Hint) []voices.Learned {
	learned := voicesFromTilesSkipping(p, hints, offset, unsureAt(sess.Segments, unsureLabels(sess)), lineNames(sess.Segments))
	return append(learned, VoicesFromSpeakers(p, offset, SpeakerAt(sess.Segments, GivenName))...)
}

// SampleGap is the longest pause (seconds) between fingerprints of one
// sample: a stretch of someone talking.
const SampleGap = 1.5

// Sample is a stretch of a meeting a voice was learnt from: consecutive
// fingerprints, each no more than SampleGap after the ones before.
type Sample struct {
	Start, End float64 // session time
	Parts      []voices.Part
}

// Voice is the sample's fingerprints together.
func (s Sample) Voice() voices.Voice {
	var v voices.Voice
	for _, pt := range s.Parts {
		v.Add(pt.Emb)
	}
	return v
}

// Span is the sample as a clip.
func (s Sample) Span() voices.Clip { return voices.Clip{Start: s.Start, End: s.End} }

// GroupSamples puts fingerprints into samples, in time order. A
// fingerprint with no speech span (End <= Start) is left out.
func GroupSamples(parts []voices.Part, gap float64) []Sample {
	ps := make([]voices.Part, 0, len(parts))
	for _, pt := range parts {
		if pt.End > pt.Start {
			ps = append(ps, pt)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Start < ps[j].Start })
	var out []Sample
	for _, pt := range ps {
		if n := len(out); n > 0 && pt.Start <= out[n-1].End+gap {
			out[n-1].Parts = append(out[n-1].Parts, pt)
			out[n-1].End = max(out[n-1].End, pt.End)
			continue
		}
		out = append(out, Sample{Start: pt.Start, End: pt.End, Parts: []voices.Part{pt}})
	}
	return out
}

// CountVoices is how many voices a meeting's samples sound like: the
// samples' means clustered as a meeting's fingerprints are (complete
// linkage, then merging clusters by centroid), both at similarity
// threshold, each sample weighted by its fingerprints. Only a cluster
// with at least minFingerprints fingerprints and minShare of them all
// counts: one person's samples leave a tail of small clusters (a few
// seconds through another microphone, laughter, a cough), and on saved
// meetings that tail was under 5% of a person's fingerprints while each
// of a room's four voices was over 5% (docs/architecture.md, "Voice
// samples"). 0 if none counts.
func CountVoices(samples []Sample, threshold float64, minFingerprints int, minShare float64) int {
	if len(samples) == 0 {
		return 0
	}
	means := make([][]float32, len(samples))
	for i, s := range samples {
		means[i] = s.Voice().Centroid()
		if means[i] == nil { // silent: a zero vector joins nothing
			means[i] = make([]float32, len(samples[0].Parts[0].Emb))
		}
	}
	bySample := clusterComplete(means, 1-threshold, 0)
	var embs [][]float32
	var clusters []int
	for i, s := range samples {
		for _, pt := range s.Parts {
			embs = append(embs, pt.Emb)
			clusters = append(clusters, bySample[i])
		}
	}
	merged := mergeByCentroid(embs, clusters, threshold)
	size := map[int]int{}
	for _, c := range merged {
		size[c]++
	}
	n := 0
	for _, k := range size {
		if k >= minFingerprints && float64(k) >= minShare*float64(len(embs)) {
			n++
		}
	}
	return n
}
