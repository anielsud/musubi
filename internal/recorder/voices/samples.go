package voices

import (
	"fmt"
	"slices"
)

// A meeting's voice for a profile is many fingerprints, heard in
// stretches (samples). The owner can say a sample isn't the person:
// its fingerprints leave the meeting's voice, and its span is kept on
// the meeting (Heard.ExcludedSpans) and as a returned sample
// (returned.go) so learning the meeting again leaves them out too.

// Part is one fingerprint of a learnt voice and when its speaker talks
// in it (session time, seconds).
type Part struct {
	Start, End float64
	Emb        []float32
}

// spanSlack absorbs float rounding between a span as shown and as
// worked out again.
const spanSlack = 0.01

// In reports whether the part lies within span.
func (pt Part) In(span Clip) bool {
	return pt.Start >= span.Start-spanSlack && pt.End <= span.End+spanSlack
}

// Sub takes another voice's fingerprints out (the reverse of Merge). A
// voice of another size, or more fingerprints than v has, is refused.
func (v *Voice) Sub(o Voice) error {
	if o.N == 0 {
		return nil
	}
	if len(o.Sum) != len(v.Sum) || o.N > v.N {
		return fmt.Errorf("that voice isn't part of this one")
	}
	for i, x := range o.Sum {
		v.Sum[i] -= x
	}
	v.N -= o.N
	if v.N == 0 {
		clear(v.Sum)
	}
	return nil
}

// withoutSpans is l with the parts in any of spans left out of its
// voice; its clip moves to the longest part left when it was in one. A
// Learned without parts is returned as it is.
func withoutSpans(l Learned, spans []Clip) Learned {
	if len(spans) == 0 || len(l.Parts) == 0 {
		return l
	}
	in := func(pt Part) bool {
		for _, sp := range spans {
			if pt.In(sp) {
				return true
			}
		}
		return false
	}
	var v Voice
	var kept []Part
	for _, pt := range l.Parts {
		if !in(pt) {
			v.Add(pt.Emb)
			kept = append(kept, pt)
		}
	}
	if v.Sum == nil && len(l.Voice.Sum) > 0 {
		v.Sum = make([]float32, len(l.Voice.Sum))
	}
	l.Voice, l.Parts = v, kept
	if overlapsAny(l.Clip, spans) {
		l.Clip = Clip{}
		best := -1.0
		for _, pt := range kept {
			if d := pt.End - pt.Start; d > best {
				best, l.Clip = d, Clip{Start: pt.Start, End: pt.End}
			}
		}
	}
	return l
}

func overlapsAny(c Clip, spans []Clip) bool {
	for _, sp := range spans {
		if c.Start < sp.End && sp.Start < c.End {
			return true
		}
	}
	return false
}

// ExcludeSpan is "Not them" on a sample: v (the sample's fingerprints)
// leaves the meeting's voice, span is recorded so learning the meeting
// again leaves it out, and the sample is returned (returned.go) to be
// asked about under "Who's this?". label is the transcript's speaker
// over it.
func (s *Store) ExcludeSpan(id, sessionID string, span Clip, v Voice, label string) error {
	if span.End <= span.Start || v.N == 0 {
		return fmt.Errorf("no sample to take out")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	h, p, err := s.heardLocked(id, sessionID)
	if err != nil {
		return err
	}
	if slices.Contains(h.ExcludedSpans, span) {
		return fmt.Errorf("that sample is already taken out")
	}
	nv := Voice{Sum: slices.Clone(h.Voice.Sum), N: h.Voice.N}
	if err := nv.Sub(v); err != nil {
		return err
	}
	h.Voice = nv
	h.ExcludedSpans = append(slices.Clone(h.ExcludedSpans), span)
	if overlapsAny(h.Clip, []Clip{span}) {
		h.Clip = Clip{}
	}
	if i := s.returnedIndexLocked(sessionID, span, id); i >= 0 {
		s.returned = slices.Delete(s.returned, i, i+1)
	}
	sv := Voice{Sum: slices.Clone(v.Sum), N: v.N}
	s.returned = append(s.returned, ReturnedSample{Session: sessionID, Span: span, From: id, FromName: p.Name, Label: label, At: h.At, Voice: &sv})
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}

// IncludeSpan undoes ExcludeSpan: v (the sample's fingerprints, worked
// out again) returns to the meeting's voice.
func (s *Store) IncludeSpan(id, sessionID string, span Clip, v Voice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	h, p, err := s.heardLocked(id, sessionID)
	if err != nil {
		return err
	}
	i := slices.Index(h.ExcludedSpans, span)
	if i < 0 {
		return fmt.Errorf("that sample isn't taken out")
	}
	r := s.returnedIndexLocked(sessionID, span, id)
	if r >= 0 && s.returned[r].To != "" {
		return fmt.Errorf("that sample now teaches %s's voice", s.returned[r].ToName)
	}
	if v.N > 0 && len(h.Voice.Sum) > 0 && len(v.Sum) != len(h.Voice.Sum) {
		return fmt.Errorf("that sample doesn't fit this voice")
	}
	nv := Voice{Sum: slices.Clone(h.Voice.Sum), N: h.Voice.N}
	nv.Merge(v)
	h.Voice = nv
	h.ExcludedSpans = slices.Delete(slices.Clone(h.ExcludedSpans), i, i+1)
	if r >= 0 {
		s.returned = slices.Delete(s.returned, r, r+1)
	}
	if h.Clip.End <= h.Clip.Start {
		h.Clip = span
	}
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}

// heardLocked is what meeting sessionID taught profile id.
func (s *Store) heardLocked(id, sessionID string) (*Heard, *Profile, error) {
	p := s.byIDLocked(id)
	if p == nil {
		return nil, nil, fmt.Errorf("no voice profile %q", id)
	}
	for i := range p.Meetings {
		if p.Meetings[i].Session == sessionID {
			return &p.Meetings[i], p, nil
		}
	}
	return nil, nil, fmt.Errorf("%s didn't learn from that meeting", p.Name)
}
