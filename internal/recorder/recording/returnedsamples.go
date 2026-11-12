package recording

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// Samples taken out of a profile with "Not them" (voices/returned.go) are
// asked about under "Who's this?", whatever their age: naming one teaches
// the named profile its fingerprints, "Can't tell" stops asking. They
// stay out of the profile they came from either way.

// returnedSamples are the pending returned samples of the indexed
// sessions, as "Who's this?" items.
func returnedSamples(sessions []*sessionReview, stores map[string]*voices.Store) []UnknownVoice {
	byID := make(map[string]*sessionReview, len(sessions))
	for _, r := range sessions {
		byID[r.id] = r
	}
	models := make([]string, 0, len(stores))
	for m := range stores {
		models = append(models, m)
	}
	sort.Strings(models)
	out := []UnknownVoice{}
	for _, model := range models {
		s := stores[model]
		for _, rs := range s.Returned() {
			r := byID[rs.Session]
			if r == nil || !rs.Pending() {
				continue
			}
			u := UnknownVoice{Session: rs.Session, Title: r.title, At: r.at, Label: rs.Label, Seconds: rs.Span.End - rs.Span.Start,
				Clip:     VoiceClip{Session: rs.Session, Start: rs.Span.Start, End: min(rs.Span.End, rs.Span.Start+clipSeconds)},
				Returned: true, ReturnedFrom: rs.FromName,
				Sample: &control.SampleRef{Model: model, Profile: rs.From, Start: rs.Span.Start, End: rs.Span.End}}
			if rs.Voice != nil && rs.Voice.N > 0 {
				if prof, sim := s.ClosestExcept(*rs.Voice, rs.From); prof != nil && sim >= suggestAbove {
					u.SoundsLike, u.Similarity = prof.Name, sim
				}
			}
			out = append(out, u)
		}
	}
	return out
}

// returnedFor is the returned sample p addresses (by its span, within
// sampleSlack), from the store of p.Model.
func (a *Recorder) returnedFor(p control.SampleArgs) (*voices.Store, voices.ReturnedSample, error) {
	s, err := a.voiceStore(p.Model)
	if err != nil {
		return nil, voices.ReturnedSample{}, err
	}
	for _, rs := range s.Returned() {
		if rs.Session == p.Session && rs.From == p.ID && math.Abs(rs.Span.Start-p.Start) < sampleSlack && math.Abs(rs.Span.End-p.End) < sampleSlack {
			return s, rs, nil
		}
	}
	return nil, voices.ReturnedSample{}, fmt.Errorf("no such sample under “Who's this?”")
}

// NameReturnedSample is naming a returned sample under "Who's this?".
func (a *Recorder) NameReturnedSample(p control.SampleArgs) (*control.NamedSample, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("a name is needed")
	}
	s, rs, err := a.returnedFor(p)
	if err != nil {
		return nil, err
	}
	var v voices.Voice
	if rs.Voice == nil || rs.Voice.N == 0 {
		if v, _, err = a.returnedSampleVoice(s, rs); err != nil {
			return nil, err
		}
	}
	before := map[string]bool{}
	for _, x := range s.Profiles() {
		before[x.ID] = true
	}
	prof, err := s.AssignReturned(rs.Session, rs.Span, rs.From, name, v)
	if err != nil {
		return nil, err
	}
	return &control.NamedSample{ID: prof.ID, Name: prof.Name, Created: !before[prof.ID]}, nil
}

// SkipReturnedSample is "Can't tell" on a returned sample.
func (a *Recorder) SkipReturnedSample(p control.SampleArgs) error {
	s, rs, err := a.returnedFor(p)
	if err != nil {
		return err
	}
	return s.SkipReturned(rs.Session, rs.Span, rs.From)
}

// returnedSampleVoice works a returned sample's fingerprints and
// transcript label out again from the saved meeting: the fingerprints
// within its span that learning gives the profile it came from (by its
// names, or the name it had then).
func (a *Recorder) returnedSampleVoice(s *voices.Store, rs voices.ReturnedSample) (voices.Voice, string, error) {
	var v voices.Voice
	names, source := []string{rs.FromName}, ""
	for _, p := range s.Profiles() {
		if p.ID == rs.From {
			names = append([]string{p.Name}, p.Aliases...)
			for _, h := range p.Meetings {
				if h.Session == rs.Session {
					source = h.Source
				}
			}
		}
	}
	sess, err := a.loadSession(rs.Session)
	if err != nil {
		return v, "", err
	}
	dir := filepath.Join(config.SessionDir(), rs.Session)
	p, info, err := loadSavedCached(dir)
	if err != nil {
		return v, "", fmt.Errorf("the meeting's fingerprints can't be read: %w", err)
	}
	label := labelOver(sess.Segments, rs.Span.Start, rs.Span.End)
	learned := diarize.SavedLearning(p, info.Offset, sess, savedHints(dir, sess, a.config().Meeting.SnapReadsToInvitees, a.config().OwnerName))
	if l := pickLearned(learned, names, source); l != nil {
		for _, pt := range l.Parts {
			if pt.In(rs.Span) {
				v.Add(pt.Emb)
			}
		}
	}
	if v.N == 0 {
		return v, label, fmt.Errorf("no fingerprints for that sample in the saved meeting")
	}
	return v, label, nil
}

// fillReturnedSamples works out the label and fingerprints of returned
// samples kept without them (taken out before 2026-11-12), once, so
// "Who's this?" can say whose line they are and whom they sound like.
func (a *Recorder) fillReturnedSamples() {
	stores, err := a.voiceStores()
	if err != nil {
		return
	}
	for _, s := range stores {
		for _, rs := range s.Returned() {
			if !rs.Pending() || (rs.Label != "" && rs.Voice != nil && rs.Voice.N > 0) {
				continue
			}
			v, label, _ := a.returnedSampleVoice(s, rs)
			if label != "" || v.N > 0 {
				_ = s.FillReturned(rs.Session, rs.Span, rs.From, label, v)
			}
		}
	}
}
