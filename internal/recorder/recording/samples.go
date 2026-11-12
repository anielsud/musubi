package recording

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// A voice profile's samples: for each meeting it learnt from, the
// stretches of fingerprints learning picked for it, worked out again
// from the saved meeting (diarize.SavedLearning, as LearnSaved picks
// them), so the owner hears what the profile is a blend of and can take
// out a stretch that isn't the person.

// handleSamples carries out the sample ops; ok is false for any other op.
func (a *Recorder) handleSamples(op string, args []byte) (result any, ok bool, err error) {
	switch op {
	case control.OpProfileSamples:
		var p control.VoiceArgs
		if err := decode(args, &p); err != nil {
			return nil, true, err
		}
		v, err := a.ProfileSamples(p.Model, p.ID)
		return v, true, err
	case control.OpExcludeSample, control.OpIncludeSample:
		var p control.SampleArgs
		if err := decode(args, &p); err != nil {
			return nil, true, err
		}
		return nil, true, a.sampleEdit(op == control.OpExcludeSample, p)
	}
	return nil, false, nil
}

// meetingSamples is one meeting's samples for a profile, without what
// depends on the rest of the profile (worked out on each ask).
type meetingSamples struct {
	rebuilt       int
	voices        int
	noDiarization bool
	note          string
	kept          []sampleSum
	excluded      []sampleSum
}

// sampleSum is a sample's span, its fingerprints' sum and the
// transcript's speaker over it.
type sampleSum struct {
	span  voices.Clip
	voice voices.Voice
	label string
}

type samplesKey struct{ model, id, session string }

type samplesEntry struct {
	stamp string
	m     *meetingSamples
}

// Samples are worked out one meeting at a time (each reads megabytes of
// fingerprints) and kept until the profile's meeting, the session or its
// fingerprints change.
var (
	samplesMu    sync.Mutex
	samplesCache = map[samplesKey]samplesEntry{}
)

// ProfileSamples is every meeting's samples for a profile.
func (a *Recorder) ProfileSamples(model, id string) (*control.ProfileSamplesView, error) {
	store, prof, err := a.profileByID(model, id)
	if err != nil {
		return nil, err
	}
	params := store.Params()
	centroid := prof.LearnedVoice(params.MinLearn).Centroid()
	view := &control.ProfileSamplesView{Model: model, ID: id, Meetings: []control.MeetingSamplesView{}}
	for _, h := range prof.Meetings {
		m := a.meetingSamplesFor(model, prof, h, params)
		mv := control.MeetingSamplesView{Session: h.Session, Fingerprints: h.Voice.N, Rebuilt: m.rebuilt, Voices: m.voices,
			NoDiarization: m.noDiarization, Note: m.note, Clip: VoiceClip{Session: h.Session, Start: h.Clip.Start, End: h.Clip.End},
			Samples: sampleViews(h.Session, m, centroid)}
		view.Samples += len(m.kept)
		view.Voices = max(view.Voices, m.voices)
		view.Meetings = append(view.Meetings, mv)
	}
	sort.SliceStable(view.Meetings, func(i, j int) bool { return view.Meetings[i].Session < view.Meetings[j].Session })
	return view, nil
}

// sampleViews scores a meeting's samples against the profile and the
// meeting's other samples, worst match first, the taken-out ones last.
func sampleViews(sessionID string, m *meetingSamples, centroid []float32) []control.VoiceSampleView {
	var all voices.Voice
	for _, s := range m.kept {
		all.Merge(s.voice)
	}
	out := []control.VoiceSampleView{}
	view := func(s sampleSum, excluded bool) control.VoiceSampleView {
		rest := voices.Voice{Sum: append([]float32(nil), all.Sum...), N: all.N}
		if !excluded {
			_ = rest.Sub(s.voice)
		}
		c := s.voice.Centroid()
		return control.VoiceSampleView{Session: sessionID, Start: s.span.Start, End: s.span.End, Fingerprints: s.voice.N,
			Similarity: round3(voices.Similarity(c, centroid)), RestSimilarity: round3(voices.Similarity(c, rest.Centroid())), Label: s.label, Excluded: excluded}
	}
	for _, s := range m.kept {
		out = append(out, view(s, false))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity < out[j].Similarity })
	for _, s := range m.excluded {
		out = append(out, view(s, true))
	}
	return out
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// profileByID is a profile and its store.
func (a *Recorder) profileByID(model, id string) (*voices.Store, *voices.Profile, error) {
	store, err := a.voiceStore(model)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range store.Profiles() {
		if p.ID == id {
			return store, &p, nil
		}
	}
	return nil, nil, fmt.Errorf("no voice profile %q", id)
}

// meetingSamplesFor is a meeting's samples for a profile, from the cache
// while nothing they depend on has changed.
func (a *Recorder) meetingSamplesFor(model string, prof *voices.Profile, h voices.Heard, params voices.Params) *meetingSamples {
	names := append([]string{prof.Name}, prof.Aliases...)
	dir, err := sessionDir(h.Session)
	if err != nil {
		return &meetingSamples{note: "not a saved meeting"}
	}
	stamp := fmt.Sprintf("%d|%s|%q|%v|%g|%d|%s|%s", h.Voice.N, h.Source, names, h.ExcludedSpans, params.Threshold, max(params.MinFingerprints, params.MinLearn),
		modStamp(filepath.Join(dir, "session.json")), modStamp(filepath.Join(dir, "diarization.gob")))
	key := samplesKey{model, prof.ID, h.Session}
	samplesMu.Lock()
	defer samplesMu.Unlock()
	if e, ok := samplesCache[key]; ok && e.stamp == stamp {
		return e.m
	}
	m := a.computeMeetingSamples(dir, model, names, h, params)
	samplesCache[key] = samplesEntry{stamp, m}
	return m
}

func modStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "-"
	}
	return st.ModTime().Format(time.RFC3339Nano)
}

// computeMeetingSamples works a meeting's samples out from the saved
// meeting.
func (a *Recorder) computeMeetingSamples(dir, model string, names []string, h voices.Heard, params voices.Params) *meetingSamples {
	sess, err := a.loadSession(h.Session)
	if err != nil {
		return &meetingSamples{note: "the meeting isn't saved any more"}
	}
	p, info, err := diarize.LoadSaved(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return &meetingSamples{noDiarization: true}
	}
	if err != nil {
		return &meetingSamples{note: "the meeting's fingerprints can't be read"}
	}
	if info.Model != "" && info.Model != model {
		return &meetingSamples{note: "the meeting's fingerprints are from another speaker model"}
	}
	learned := diarize.SavedLearning(p, info.Offset, sess, savedHints(dir, sess, a.config().Meeting.SnapReadsToInvitees, a.config().OwnerName))
	return samplesOf(pickLearned(learned, names, h.Source), h.ExcludedSpans, sess.Segments, params)
}

// pickLearned is the voice learning gave a profile with these names
// from the meeting: the last one under any of its names (the user's
// naming comes after the tiles, and wins), from the same source if any
// is.
func pickLearned(learned []voices.Learned, names []string, source string) *voices.Learned {
	var named, same *voices.Learned
	for i := range learned {
		l := &learned[i]
		for _, n := range names {
			if diarize.SameName(l.Name, n) {
				named = l
				if l.Source == source {
					same = l
				}
				break
			}
		}
	}
	if same != nil {
		return same
	}
	return named
}

// samplesOf groups a learnt voice's fingerprints into samples: those in
// a span the owner took out make one sample per span, the rest are
// grouped by time.
func samplesOf(l *voices.Learned, spans []voices.Clip, segs []session.Segment, params voices.Params) *meetingSamples {
	m := &meetingSamples{}
	if l == nil {
		m.note = "learning no longer finds this voice in the meeting"
		return m
	}
	var kept []voices.Part
	out := make([][]voices.Part, len(spans))
	for _, pt := range l.Parts {
		in := -1
		for i, sp := range spans {
			if pt.In(sp) {
				in = i
				break
			}
		}
		if in < 0 {
			kept = append(kept, pt)
		} else {
			out[in] = append(out[in], pt)
		}
	}
	groups := diarize.GroupSamples(kept, diarize.SampleGap)
	for _, g := range groups {
		m.rebuilt += len(g.Parts)
		m.kept = append(m.kept, sampleSum{span: g.Span(), voice: g.Voice(), label: labelOver(segs, g.Start, g.End)})
	}
	for i, sp := range spans {
		var v voices.Voice
		for _, pt := range out[i] {
			v.Add(pt.Emb)
		}
		m.excluded = append(m.excluded, sampleSum{span: sp, voice: v, label: labelOver(segs, sp.Start, sp.End)})
	}
	m.voices = diarize.CountVoices(countable(groups, params.MinLearn), params.Threshold, max(params.MinFingerprints, params.MinLearn), voiceShare)
	return m
}

// countable are the samples that can count as a voice of their own: at
// least minLearn fingerprints. Specks of crosstalk (a profile's many
// one- and two-fingerprint samples, 2026-11-12) added up to a second
// voice that wasn't there.
func countable(groups []diarize.Sample, minLearn int) []diarize.Sample {
	var out []diarize.Sample
	for _, g := range groups {
		if len(g.Parts) >= minLearn {
			out = append(out, g)
		}
	}
	return out
}

// labelOver is the speaker of the transcript lines covering most of
// start..end ("" if none).
func labelOver(segs []session.Segment, start, end float64) string {
	by := map[string]float64{}
	for _, s := range segs {
		if o := min(end, s.EndTime) - max(start, s.StartTime); o > 0 {
			by[s.Speaker] += o
		}
	}
	best, most := "", 0.0
	for l, o := range by {
		if o > most || (o == most && l < best) {
			best, most = l, o
		}
	}
	return strings.TrimSpace(best)
}

// voiceShare is the least share of a meeting's fingerprints a voice
// among its samples must have to count (diarize.CountVoices).
const voiceShare = 0.05

// sampleSlack is how near a span asked for must be to one shown.
const sampleSlack = 0.001

// sampleEdit takes a sample out of a profile's meeting (exclude), or
// puts one taken out back. The sample is found again among those the
// meeting gives, never taken from the request as it stands.
func (a *Recorder) sampleEdit(exclude bool, p control.SampleArgs) error {
	store, prof, err := a.profileByID(p.Model, p.ID)
	if err != nil {
		return err
	}
	var h *voices.Heard
	for i := range prof.Meetings {
		if prof.Meetings[i].Session == p.Session {
			h = &prof.Meetings[i]
		}
	}
	if h == nil {
		return fmt.Errorf("%s didn't learn from that meeting", prof.Name)
	}
	m := a.meetingSamplesFor(p.Model, prof, *h, store.Params())
	near := func(sp voices.Clip) bool {
		return math.Abs(sp.Start-p.Start) < sampleSlack && math.Abs(sp.End-p.End) < sampleSlack
	}
	if exclude {
		for _, s := range m.kept {
			if near(s.span) {
				return store.ExcludeSpan(p.ID, p.Session, s.span, s.voice)
			}
		}
		return fmt.Errorf("no such sample in that meeting")
	}
	for _, s := range m.excluded {
		if near(s.span) {
			return store.IncludeSpan(p.ID, p.Session, s.span, s.voice)
		}
	}
	return fmt.Errorf("that sample isn't taken out")
}
