package voices

import (
	"fmt"
	"slices"
	"time"
)

// Two answers on a profile's sample (docs/recorder-screens.md, "Not them
// and Junk on a sample"):
//
//   - Not them: the sample leaves the profile (Heard.ExcludedSpans) and
//     is kept as a ReturnedSample, which the Voices page asks about under
//     "Who's this?". Naming it there adds its fingerprints to the named
//     profile (AssignReturned); "Can't tell" stops asking (SkipReturned).
//     Either way it stays out of the profile it came from, through any
//     relearn, and even when that meeting's contribution is dropped and
//     learnt again later: learning reads the returned samples, not only
//     the meeting's ExcludedSpans.
//   - Junk: the stretch is noise, crosstalk or the like. It is kept per
//     meeting (JunkSpan), and no profile learns from it again.

// ReturnedSample is a sample the owner said isn't the profile that
// learnt it. Voice is its fingerprints when known (nil for those taken
// out before 2026-11-12, worked out again from the meeting when needed);
// Label is the transcript's speaker over it. To is the profile it was
// named as under "Who's this?"; Skipped: "Can't tell".
type ReturnedSample struct {
	Session  string    `json:"session"`
	Span     Clip      `json:"span"`
	From     string    `json:"from"`
	FromName string    `json:"fromName,omitempty"`
	Label    string    `json:"label,omitempty"`
	At       time.Time `json:"at"`
	Voice    *Voice    `json:"voice,omitempty"`
	To       string    `json:"to,omitempty"`
	ToName   string    `json:"toName,omitempty"`
	Skipped  bool      `json:"skipped,omitempty"`
}

// Pending: still to be asked about.
func (r ReturnedSample) Pending() bool { return r.To == "" && !r.Skipped }

// AssignedSample is a returned sample the owner named as this profile.
type AssignedSample struct {
	Span  Clip  `json:"span"`
	Voice Voice `json:"voice"`
}

// JunkSpan is a stretch of a meeting no profile learns from.
type JunkSpan struct {
	Session string `json:"session"`
	Span    Clip   `json:"span"`
}

// migrateExcludedLocked makes every taken-out sample a returned one (the
// owner's choice, 2026-11-12: those taken out before "Who's this?" took
// samples are asked about too). Idempotent: a sample already returned,
// named or skipped is left as it is.
func (s *Store) migrateExcludedLocked() int {
	n := 0
	for _, p := range s.profiles {
		for _, h := range p.Meetings {
			for _, sp := range h.ExcludedSpans {
				if s.returnedIndexLocked(h.Session, sp, p.ID) < 0 {
					s.returned = append(s.returned, ReturnedSample{Session: h.Session, Span: sp, From: p.ID, FromName: p.Name, At: h.At})
					n++
				}
			}
		}
	}
	return n
}

// returnedIndexLocked is the returned sample of session's span from
// profile from ("" any), -1 if none.
func (s *Store) returnedIndexLocked(session string, span Clip, from string) int {
	for i, r := range s.returned {
		if r.Session == session && sameSpan(r.Span, span) && (from == "" || r.From == from) {
			return i
		}
	}
	return -1
}

func sameSpan(a, b Clip) bool {
	return a.Start-b.Start < spanSlack && b.Start-a.Start < spanSlack && a.End-b.End < spanSlack && b.End-a.End < spanSlack
}

// notThemLocked are the spans of session the owner said aren't profile
// id.
func (s *Store) notThemLocked(id, session string) []Clip {
	var out []Clip
	for _, r := range s.returned {
		if r.From == id && r.Session == session {
			out = append(out, r.Span)
		}
	}
	return out
}

// junkLocked are session's junk spans.
func (s *Store) junkLocked(session string) []Clip {
	var out []Clip
	for _, j := range s.junk {
		if j.Session == session {
			out = append(out, j.Span)
		}
	}
	return out
}

// appendSpans adds the spans not in xs already.
func appendSpans(xs []Clip, add ...Clip) []Clip {
	for _, a := range add {
		if !slices.ContainsFunc(xs, func(x Clip) bool { return sameSpan(x, a) }) {
			xs = append(xs, a)
		}
	}
	return xs
}

// Returned is a copy of every returned sample.
func (s *Store) Returned() []ReturnedSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	out := make([]ReturnedSample, len(s.returned))
	for i, r := range s.returned {
		out[i] = r
		if r.Voice != nil {
			v := Voice{Sum: slices.Clone(r.Voice.Sum), N: r.Voice.N}
			out[i].Voice = &v
		}
	}
	return out
}

// HeldSpans are the stretches of session left out of profile id: those
// the owner said aren't them, and junk.
func (s *Store) HeldSpans(id, session string) (notThem, junk []Clip) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	for _, p := range s.profiles {
		if p.ID != id {
			continue
		}
		for _, h := range p.Meetings {
			if h.Session == session {
				notThem = appendSpans(notThem, h.ExcludedSpans...)
			}
		}
	}
	return appendSpans(notThem, s.notThemLocked(id, session)...), s.junkLocked(session)
}

// FillReturned records a returned sample's transcript label and
// fingerprints, worked out again for one taken out before they were
// kept. Only what is missing is filled in.
func (s *Store) FillReturned(session string, span Clip, from, label string, v Voice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	i := s.returnedIndexLocked(session, span, from)
	if i < 0 {
		return fmt.Errorf("no such returned sample")
	}
	r := &s.returned[i]
	changed := false
	if r.Label == "" && label != "" {
		r.Label, changed = label, true
	}
	if (r.Voice == nil || r.Voice.N == 0) && v.N > 0 {
		c := Voice{Sum: slices.Clone(v.Sum), N: v.N}
		r.Voice, changed = &c, true
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}

// SkipReturned is "Can't tell" on a returned sample: it isn't asked
// about again, and stays out of the profile it came from.
func (s *Store) SkipReturned(session string, span Clip, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	i := s.returnedIndexLocked(session, span, from)
	if i < 0 || !s.returned[i].Pending() {
		return fmt.Errorf("that sample isn't waiting under “Who's this?”")
	}
	s.returned[i].Skipped = true
	return s.saveLocked()
}

// AssignReturned names a returned sample: its fingerprints v (the
// sample's, nil for those kept on it) go to a profile named name, as
// learning picks one (Learn): of the profiles with exactly that name
// (ignoring case and spacing) other than the one it was taken out of,
// the likest it's at least LearnFloor like, else a new profile with the
// name. They are kept on the profile's meeting (Heard.Assigned) as the
// owner's naming, so a relearn keeps them and a tile doesn't replace
// them. Returns the profile.
func (s *Store) AssignReturned(session string, span Clip, from, name string, v Voice) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	i := s.returnedIndexLocked(session, span, from)
	if i < 0 || !s.returned[i].Pending() {
		return Profile{}, fmt.Errorf("that sample isn't waiting under “Who's this?”")
	}
	r := &s.returned[i]
	if v.N == 0 && r.Voice != nil {
		v = *r.Voice
	}
	if v.N == 0 {
		return Profile{}, fmt.Errorf("no fingerprints for that sample")
	}
	v = Voice{Sum: slices.Clone(v.Sum), N: v.N}
	c := v.Centroid()
	var to *Profile
	best := -2.0
	for _, p := range s.profiles {
		if p.ID == r.From || !s.exactNameLocked(p, name) {
			continue
		}
		pv := p.LearnedVoice(s.params.MinLearn)
		if pv.N == 0 {
			pv = p.Voice()
		}
		if pv.N == 0 {
			if to == nil {
				to = p // nothing learnt: takes a voice no other fits
			}
			continue
		}
		if sim := Similarity(pv.Centroid(), c); sim >= s.params.LearnFloor && sim > best {
			to, best = p, sim
		}
	}
	if to == nil {
		to = &Profile{ID: newID(), Name: name, Created: time.Now()}
		s.profiles = append(s.profiles, to)
	}
	to.Excluded = removeString(to.Excluded, session)
	add := AssignedSample{Span: span, Voice: v}
	found := false
	for k := range to.Meetings {
		if h := &to.Meetings[k]; h.Session == session {
			h.Voice.Merge(v)
			h.Assigned = append(h.Assigned, add)
			h.Source = FromNamed
			found = true
		}
	}
	if !found {
		to.Meetings = append(to.Meetings, Heard{Session: session, At: r.At, Source: FromNamed, Voice: v, Clip: span, Assigned: []AssignedSample{add}})
	}
	to.refresh(s.params.MinLearn)
	var keptForgotten []string
	for _, f := range s.forgotten {
		if !s.sameLocked(f, to.Name) {
			keptForgotten = append(keptForgotten, f)
		}
	}
	s.forgotten = keptForgotten
	r.To, r.ToName = to.ID, to.Name
	out := *to
	return out, s.saveLocked()
}

// exactNameLocked: p has name, or once had it, ignoring case and
// spacing.
func (s *Store) exactNameLocked(p *Profile, name string) bool {
	for _, n := range append([]string{p.Name}, p.Aliases...) {
		if sameExactName(n, name) {
			return true
		}
	}
	return false
}

// JunkSpan is "Junk" on a sample of profile id: v (its fingerprints)
// leaves the meeting's voice, and span is kept for the meeting so no
// profile learns from it again.
func (s *Store) JunkSpan(id, sessionID string, span Clip, v Voice) error {
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
	if slices.ContainsFunc(s.junkLocked(sessionID), func(x Clip) bool { return sameSpan(x, span) }) {
		return fmt.Errorf("that sample is junk already")
	}
	nv := Voice{Sum: slices.Clone(h.Voice.Sum), N: h.Voice.N}
	if err := nv.Sub(v); err != nil {
		return err
	}
	h.Voice = nv
	if overlapsAny(h.Clip, []Clip{span}) {
		h.Clip = Clip{}
	}
	s.junk = append(s.junk, JunkSpan{Session: sessionID, Span: span})
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}

// UnjunkSpan undoes JunkSpan: v (the sample's fingerprints, worked out
// again) returns to profile id's meeting voice.
func (s *Store) UnjunkSpan(id, sessionID string, span Clip, v Voice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	h, p, err := s.heardLocked(id, sessionID)
	if err != nil {
		return err
	}
	k := slices.IndexFunc(s.junk, func(j JunkSpan) bool { return j.Session == sessionID && sameSpan(j.Span, span) })
	if k < 0 {
		return fmt.Errorf("that sample isn't junk")
	}
	if v.N > 0 && len(h.Voice.Sum) > 0 && len(v.Sum) != len(h.Voice.Sum) {
		return fmt.Errorf("that sample doesn't fit this voice")
	}
	nv := Voice{Sum: slices.Clone(h.Voice.Sum), N: h.Voice.N}
	nv.Merge(v)
	h.Voice = nv
	s.junk = slices.Delete(s.junk, k, k+1)
	if h.Clip.End <= h.Clip.Start {
		h.Clip = span
	}
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}
