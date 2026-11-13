// Package voices keeps voice profiles: what a named person's voice
// fingerprint looks like, learnt across meetings, so a voice the meeting
// window can't name (in a meeting room, on the phone, in a call with no
// window to read) can still be named by who it sounds like.
//
// A profile is learnt only from strong evidence: fingerprints heard while
// the person's own tile was lit, and names the user gave a speaker. Each
// meeting a profile was heard in is kept separately, so learning a meeting
// again replaces what it taught, and a meeting can be forgotten. Profiles
// are local (DataDir/voices/<speaker model>.json) and only fingerprints are stored, never
// audio; a profile's clips point into the meeting's own recording.
//
// On saved meetings (docs/speaker-attribution-research.md, "Voice
// profiles"): a voice compared to profiles learnt from the other meetings
// was named in 112 of 119 cases at the default bar, all right, and voices
// with no profile were wrongly named in 1 of 57.
package voices

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Voice is a set of fingerprints: their sum (each one L2-normalized) and
// how many. Its direction is the voice.
type Voice struct {
	Sum []float32 `json:"sum"`
	N   int       `json:"n"`
}

// Add adds a fingerprint.
func (v *Voice) Add(emb []float32) {
	if v.Sum == nil {
		v.Sum = make([]float32, len(emb))
	}
	if len(emb) != len(v.Sum) {
		return
	}
	for i, x := range emb {
		v.Sum[i] += x
	}
	v.N++
}

// Merge adds another voice's fingerprints.
func (v *Voice) Merge(o Voice) {
	if o.N == 0 {
		return
	}
	if v.Sum == nil {
		v.Sum = make([]float32, len(o.Sum))
	}
	if len(o.Sum) != len(v.Sum) {
		return
	}
	for i, x := range o.Sum {
		v.Sum[i] += x
	}
	v.N += o.N
}

// Centroid is the voice's direction, L2-normalized (nil if empty).
func (v Voice) Centroid() []float32 {
	var s float64
	for _, x := range v.Sum {
		s += float64(x) * float64(x)
	}
	if v.N == 0 || s == 0 {
		return nil
	}
	s = math.Sqrt(s)
	out := make([]float32, len(v.Sum))
	for i, x := range v.Sum {
		out[i] = float32(float64(x) / s)
	}
	return out
}

// Similarity is the cosine similarity of two centroids (-2 if either is
// missing or they differ in size: another speaker model's).
func Similarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -2
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Clip is a stretch of a meeting's recording to play, in session time
// (seconds).
type Clip struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Source is where a meeting's fingerprints for a profile came from.
const (
	FromTile  = "tile"  // the person's own tile was lit
	FromNamed = "named" // the user named the speaker
)

// Heard is what one meeting taught a profile.
type Heard struct {
	Session string    `json:"session"`
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	Voice   Voice     `json:"voice"`
	Clip    Clip      `json:"clip"`
	// Confirmed: the owner listened and said this meeting's voice is
	// theirs, so it isn't offered for checking however unlike the others
	// it scores (a few seconds of voice score low against everything).
	Confirmed bool `json:"confirmed,omitempty"`
	// ExcludedSpans are stretches of the meeting the owner said aren't
	// the profile's person ("Not them" on a sample): their fingerprints
	// are left out of Voice, and out of it again when the meeting is
	// learnt again (see samples.go). Each is also a ReturnedSample, which
	// keeps it out when the meeting's contribution is dropped and learnt
	// again later.
	ExcludedSpans []Clip `json:"excludedSpans,omitempty"`
	// Assigned are samples of another profile's the owner named as this
	// person under "Who's this?" (AssignReturned): their fingerprints are
	// in Voice, and added again when the meeting is learnt again.
	Assigned []AssignedSample `json:"assigned,omitempty"`
}

// Profile is one person's voice.
type Profile struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Meetings []Heard   `json:"meetings"`
	Created  time.Time `json:"created"`
	// Apart lists profiles the user said are someone else, so they're
	// never offered as the same person again.
	Apart []string `json:"apart,omitempty"`
	// Aliases are names of profiles merged into this one: learning under
	// them teaches this profile.
	Aliases []string `json:"aliases,omitempty"`
	// Rejected are voices the user said aren't this person after the
	// profile named them: a voice more like one of these than like the
	// profile isn't named by it.
	Rejected []Heard `json:"rejected,omitempty"`
	// Excluded are meetings the user took out of the profile (or all its
	// meetings when it was started over): never learnt from again.
	Excluded []string `json:"excluded,omitempty"`
	// Confirmed: the user said this is the person's voice, so it names
	// voices before it's been heard in Params.MinMeetings.
	Confirmed bool `json:"confirmed,omitempty"`

	centroid []float32
	n        int
}

// Voice is all the profile's meetings together.
func (p *Profile) Voice() Voice {
	var v Voice
	for _, h := range p.Meetings {
		v.Merge(h.Voice)
	}
	return v
}

// LastHeard is the most recent meeting the profile was heard in.
func (p *Profile) LastHeard() time.Time {
	var t time.Time
	for _, h := range p.Meetings {
		if h.At.After(t) {
			t = h.At
		}
	}
	return t
}

func (p *Profile) refresh(minLearn int) {
	v := p.LearnedVoice(minLearn)
	p.centroid, p.n = v.Centroid(), v.N
}

// LearnedVoice is the profile's voice from the meetings with at least
// minLearn fingerprints (all of them with 0): what identifies it.
func (p *Profile) LearnedVoice(minLearn int) Voice {
	var v Voice
	for _, h := range p.Meetings {
		if h.Voice.N >= minLearn {
			v.Merge(h.Voice)
		}
	}
	return v
}

// Params are the rules for naming a voice from the profiles.
type Params struct {
	// Threshold is the least similarity to a profile that names a voice.
	Threshold float64
	// Margin is how far the best profile must beat the next.
	Margin float64
	// MinFingerprints is the least a voice, and a profile, must have.
	MinFingerprints int
	// MinLearn is the least a meeting's voice must have to teach a
	// profile, and to count in its voice and its meetings. Fewer is a few
	// seconds, whose average is noise: on 2026-11-12 every meeting at 8-9
	// fingerprints scored badly against its own (correct) profile, none
	// from 10 up did. Such a voice still names a speaker (MinFingerprints);
	// it just doesn't teach. 8 (MinFingerprints): learn from any, as before.
	MinLearn int
	// MinMeetings is how many meetings a profile must be heard in before
	// it names anyone, unless the user confirmed it (or named the person
	// themselves in one): one meeting's misread tile shouldn't name
	// people in the next.
	MinMeetings int
	// LearnFloor: a meeting's voice that's less like a profile of the
	// same name than this doesn't teach it: a misread name, a ring on
	// the wrong tile or another person of that name, not a new side of
	// the voice (Learn starts a new profile for it).
	LearnFloor float64
	// Deprecated: LearnFloorAfter was the size a profile needed before
	// LearnFloor applied; Learn now applies it at any size (pickLocked).
	LearnFloorAfter int
}

// DefaultParams are measured on saved meetings (see the package comment).
func DefaultParams() Params {
	return Params{Threshold: 0.7, Margin: 0.1, MinFingerprints: 8, MinLearn: 10, MinMeetings: 2, LearnFloor: 0.5, LearnFloorAfter: 30}
}

// Store is the profiles on disk. Safe for concurrent use.
type Store struct {
	path   string
	same   func(a, b string) bool
	params Params

	mu        sync.Mutex
	profiles  []*Profile
	forgotten []string
	// modTime is the file's when last read or written: another process
	// (the CLI's learn, the app) may have changed it since.
	modTime time.Time
	// rules is the LearnRules the profiles were last built under, and
	// relearned what the last relearn for new rules changed.
	rules     int
	relearned *Relearned
	// returned are samples the owner said aren't the profile that learnt
	// them, and junk the stretches no profile learns from (returned.go).
	returned []ReturnedSample
	junk     []JunkSpan
	deleted  []DeletedProfile
}

// LearnRules is the version of the rules for what a meeting teaches a
// profile. Bump it when they change so that profiles built under the old
// rules are learnt again from every saved meeting (the recorder does that
// once, in the background). A file without it was built under rules 1.
//
// 2 (2026-11-12): a lit tile doesn't teach while the transcript names
// someone else as speaking; learning a whole meeting again drops what it
// automatically taught a profile it no longer teaches (LearnMeeting).
// 3 (2026-11-12): a tile's voice never replaces a meeting the owner
// named or moved to the profile; a meeting's Confirmed mark, samples
// taken out ("Not them", returned.go), samples named as the profile and
// junk are kept when the meeting is learnt again.
const LearnRules = 3

// Relearned is what a relearn for new rules did.
type Relearned struct {
	At       time.Time `json:"at"`
	Meetings int       `json:"meetings"`
	// Changed: profiles whose learnt voice changed; Added: profiles the
	// relearn started.
	Changed int `json:"changed"`
	Added   int `json:"added"`
}

// Path is where a speaker model's profiles are kept.
func Path(dataDir, model string) string { return filepath.Join(dataDir, "voices", model+".json") }

// Open loads the profiles at path (none if it doesn't exist). same says
// whether two names are one person's (a tile's name cut short, a misread
// letter).
func Open(path string, same func(a, b string) bool, params Params) (*Store, error) {
	s := &Store{path: path, same: same, params: params}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetParams changes the naming rules.
func (s *Store) SetParams(p Params) {
	s.mu.Lock()
	s.params = p
	for _, pr := range s.profiles {
		pr.refresh(p.MinLearn)
	}
	s.mu.Unlock()
}

// Params are the naming rules in use.
func (s *Store) Params() Params {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params
}

// MinLearn is the store's MinLearn.
func (s *Store) MinLearn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params.MinLearn
}

func (s *Store) load() error {
	st, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		// Nothing learnt yet: whatever is learnt is under today's rules.
		s.profiles, s.modTime, s.rules, s.relearned, s.returned, s.junk = nil, time.Time{}, LearnRules, nil, nil, nil
		return nil
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("voice profiles %s: %w", s.path, err)
	}
	for _, p := range f.Profiles {
		// Merges before 2026-11-12 appended a meeting both profiles had
		// twice; read as one.
		p.Meetings = mergeMeetings(p.Meetings)
		p.refresh(s.params.MinLearn)
	}
	s.profiles, s.forgotten, s.modTime = f.Profiles, f.Forgotten, st.ModTime()
	s.rules, s.relearned = max(f.Rules, 1), f.Relearned
	s.returned, s.junk, s.deleted = f.Returned, f.Junk, f.Deleted
	s.migrateExcludedLocked()
	return nil
}

// mergeMeetings folds meetings listed more than once (the same session)
// into one: their voices summed, the earliest time, the longer clip, both
// sets of taken-out stretches, confirmed if either was.
func mergeMeetings(ms []Heard) []Heard {
	at := map[string]int{}
	out := ms[:0:0]
	for _, h := range ms {
		i, ok := at[h.Session]
		if !ok {
			at[h.Session] = len(out)
			out = append(out, h)
			continue
		}
		o := &out[i]
		o.Voice.Merge(h.Voice)
		if h.At.Before(o.At) {
			o.At = h.At
		}
		if h.Clip.End-h.Clip.Start > o.Clip.End-o.Clip.Start {
			o.Clip = h.Clip
		}
		o.ExcludedSpans = append(o.ExcludedSpans, h.ExcludedSpans...)
		o.Assigned = append(o.Assigned, h.Assigned...)
		o.Confirmed = o.Confirmed || h.Confirmed
	}
	return out
}

// syncLocked rereads the file if another process changed it. A file that
// can't be read keeps what's in memory.
func (s *Store) syncLocked() {
	st, err := os.Stat(s.path)
	if err == nil && !st.ModTime().Equal(s.modTime) {
		_ = s.load()
	}
}

type file struct {
	Version  int        `json:"version"`
	Profiles []*Profile `json:"profiles"`
	// Forgotten are names the user forgot, never learnt again.
	Forgotten []string `json:"forgotten,omitempty"`
	// Rules is the LearnRules the profiles were last built under (absent:
	// 1), Relearned what the last relearn for new rules did.
	Rules     int        `json:"rules,omitempty"`
	Relearned *Relearned `json:"relearned,omitempty"`
	// Returned are samples taken out of a profile ("Not them"), Junk the
	// stretches no profile learns from (returned.go).
	Returned []ReturnedSample `json:"returned,omitempty"`
	Junk     []JunkSpan       `json:"junk,omitempty"`
	// Deleted are profiles the owner deleted (not forgotten): their names
	// aren't learnt again from the same meetings, only from later ones.
	Deleted []DeletedProfile `json:"deleted,omitempty"`
}

// saveLocked writes the profiles atomically.
func (s *Store) saveLocked() error {
	b, err := json.Marshal(file{Version: 1, Profiles: s.profiles, Forgotten: s.forgotten, Rules: s.rules, Relearned: s.relearned, Returned: s.returned, Junk: s.junk, Deleted: s.deleted})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil {
		s.modTime = st.ModTime()
	}
	return nil
}

// Profiles returns a copy of every profile, most recently heard first.
func (s *Store) Profiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	out := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		c := *p
		c.Meetings = append([]Heard(nil), p.Meetings...)
		c.Apart = append([]string(nil), p.Apart...)
		c.Aliases = append([]string(nil), p.Aliases...)
		c.Rejected = append([]Heard(nil), p.Rejected...)
		c.Excluded = append([]string(nil), p.Excluded...)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastHeard().After(out[j].LastHeard()) })
	return out
}

// Learned is one name's voice in a meeting, to learn.
type Learned struct {
	Name   string
	Source string
	Voice  Voice
	Clip   Clip
	// Parts are Voice's fingerprints and when each was heard, when the
	// learner kept them (nil otherwise): a span the owner excluded is
	// left out by them (samples.go).
	Parts []Part
}

// Learn records what a meeting taught. Each name's voice goes to the
// profile it sounds like among those with the name (or once named it,
// or a name same says is the same): the likest one it's at least
// Params.LearnFloor like, replacing what that meeting taught it before.
// If it's like none of them, it starts a new profile with the name, so
// two people with one name get a profile each and a misread name doesn't
// spoil a profile; the Voices view asks whether same-named profiles are
// one person (SameNamed). Profiles are never merged here. A name given
// twice keeps the last voice (the speakers the user named come after
// the tiles, and win). Returns the names learnt.
func (s *Store) Learn(sessionID string, at time.Time, learned []Learned) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	done, _ := s.learnLocked(sessionID, at, learned)
	if len(done) == 0 {
		return nil, nil
	}
	return done, s.saveLocked()
}

// LearnMeeting is Learn for all a meeting teaches (learning it whole,
// again or for the first time): what it taught a profile automatically
// before (from a tile, not confirmed, not named or moved there by the
// owner) and teaches it no longer is dropped, so learning a meeting again
// replaces its automatic contributions entirely. What the owner named,
// confirmed or moved stays. Returns the names learnt.
func (s *Store) LearnMeeting(sessionID string, at time.Time, learned []Learned) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	done, taught := s.learnLocked(sessionID, at, learned)
	dropped := false
	for _, p := range s.profiles {
		if taught[p] {
			continue
		}
		kept := p.Meetings[:0:0]
		for _, h := range p.Meetings {
			if h.Session == sessionID && h.Source == FromTile && !h.Confirmed {
				dropped = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) != len(p.Meetings) {
			p.Meetings = kept
			p.refresh(s.params.MinLearn)
		}
	}
	if len(done) == 0 && !dropped {
		return nil, nil
	}
	return done, s.saveLocked()
}

// learnLocked records what a meeting taught (see Learn) without saving:
// the names learnt and the profiles taught.
func (s *Store) learnLocked(sessionID string, at time.Time, learned []Learned) ([]string, map[*Profile]bool) {
	taught := map[*Profile]bool{}
	last := map[string]int{}
	for i, l := range learned {
		last[l.Name] = i
	}
	var done []string
	for i, l := range learned {
		if l.Name == "" || last[l.Name] != i || l.Voice.N < max(s.params.MinFingerprints, s.params.MinLearn) || s.forgottenLocked(l.Name) || s.deletedLocked(l.Name, sessionID) {
			continue
		}
		p, ok := s.pickLocked(sessionID, l)
		if !ok {
			continue
		}
		if p == nil {
			p = &Profile{ID: newID(), Name: l.Name, Created: time.Now()}
			s.profiles = append(s.profiles, p)
		}
		var kept []Heard
		var before *Heard
		for i, h := range p.Meetings {
			if h.Session != sessionID {
				kept = append(kept, h)
			} else {
				before = &p.Meetings[i]
			}
		}
		// The owner's naming beats a tile: a meeting the owner named or
		// moved to the profile isn't replaced by what its tile showed (on
		// 2026-11-12 a relearn replaced a named meeting's voice so).
		if before != nil && before.Source == FromNamed && l.Source == FromTile {
			taught[p] = true
			continue
		}
		h := Heard{Session: sessionID, At: at, Source: l.Source}
		var spans []Clip
		if before != nil {
			spans = before.ExcludedSpans
			h.Confirmed, h.Assigned = before.Confirmed, before.Assigned
		}
		spans = appendSpans(spans, s.notThemLocked(p.ID, sessionID)...)
		h.ExcludedSpans = spans
		// Junk and samples named as the profile are left out of what the
		// meeting gives (the latter are added back whole, once).
		drop := appendSpans(append([]Clip(nil), spans...), s.junkLocked(sessionID)...)
		for _, a := range h.Assigned {
			drop = appendSpans(drop, a.Span)
		}
		l = withoutSpans(l, drop)
		h.Voice, h.Clip = l.Voice, l.Clip
		for _, a := range h.Assigned {
			h.Voice.Merge(a.Voice)
		}
		p.Meetings = append(kept, h)
		p.refresh(s.params.MinLearn)
		taught[p] = true
		done = append(done, p.Name)
	}
	return done, taught
}

// Rules is the LearnRules the profiles were last built under, and what
// the last relearn for new rules did (nil if none).
func (s *Store) Rules() (int, *Relearned) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	if s.relearned == nil {
		return s.rules, nil
	}
	r := *s.relearned
	return s.rules, &r
}

// SetRules records that the profiles are built under rules version, and
// what the relearn that did it changed.
func (s *Store) SetRules(version int, r Relearned) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	s.rules, s.relearned = version, &r
	return s.saveLocked()
}

// pickLocked is the profile a meeting's voice for a name teaches: of the
// profiles with the name, the likest the voice that's at least
// LearnFloor like it, by what its other meetings taught (or, if the
// meeting is all it has, what the meeting taught it before). The bar is
// the same for a young profile as for one with LearnFloorAfter
// fingerprints: a profile is never merged with another automatically, so
// a voice that doesn't fit one is better kept in its own (the user can
// merge them, "Two profiles named X?") than mixed in. A profile with
// nothing learnt (started over, or all its meetings taken out) takes a
// voice none of the others fit. nil, true: a new profile. false: none,
// as the user took this meeting out of a profile with the name and the
// voice fits no other one.
func (s *Store) pickLocked(sessionID string, l Learned) (*Profile, bool) {
	c := l.Voice.Centroid()
	var best, empty *Profile
	bestSim, excluded := -2.0, false
	for _, p := range s.namedLocked(l.Name) {
		if contains(p.Excluded, sessionID) {
			excluded = true
			continue
		}
		var others, before Voice
		for _, h := range p.Meetings {
			if h.Session != sessionID {
				others.Merge(h.Voice)
			} else {
				before.Merge(h.Voice)
			}
		}
		if others.N == 0 {
			others = before
		}
		if others.N == 0 {
			if empty == nil {
				empty = p
			}
			continue
		}
		if sim := Similarity(others.Centroid(), c); sim >= s.params.LearnFloor && sim > bestSim {
			best, bestSim = p, sim
		}
	}
	switch {
	case best != nil:
		return best, true
	case empty != nil:
		return empty, true
	case excluded:
		return nil, false
	}
	return nil, true
}

// namedLocked is every profile named name or once named it (an alias),
// by same as well as exactly.
func (s *Store) namedLocked(name string) []*Profile {
	var out []*Profile
	for _, p := range s.profiles {
		if s.hasNameLocked(p, name) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) hasNameLocked(p *Profile, name string) bool {
	if s.sameLocked(p.Name, name) {
		return true
	}
	for _, a := range p.Aliases {
		if s.sameLocked(a, name) {
			return true
		}
	}
	return false
}

func (s *Store) sameLocked(a, b string) bool {
	return a == b || (s.same != nil && s.same(a, b))
}

// forgottenLocked: name was forgotten (Delete voice print). The same
// name only, ignoring case and spacing: the loose match learning uses
// made forgetting "Nikhil Narayen2" also forget "Nikhil Narayen"
// (2026-11-12).
func (s *Store) forgottenLocked(name string) bool {
	for _, f := range s.forgotten {
		if sameExactName(f, name) {
			return true
		}
	}
	return false
}

// SamePerson reports whether a and b are the same name, or both names of
// one profile (its name or an old name kept when it was renamed): after a
// rename the meeting window still shows the old name while the transcript
// says the new one.
func (s *Store) SamePerson(a, b string) bool {
	if sameExactName(a, b) {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.profiles {
		names := append([]string{p.Name}, p.Aliases...)
		ha, hb := false, false
		for _, n := range names {
			ha = ha || sameExactName(n, a)
			hb = hb || sameExactName(n, b)
		}
		if ha && hb {
			return true
		}
	}
	return false
}

// Matched is a voice named from a profile.
type Matched struct {
	Name       string  `json:"name"`
	Profile    string  `json:"profile"` // ID
	Similarity float64 `json:"similarity"`
}

// Match names voices (by key) from the profiles: each voice gets the
// profile it's most like, if that's at least Threshold and beats the
// next by Margin, one voice per profile (the likest wins), and never a
// name taken says is already someone else's in this meeting. A profile
// doesn't name a voice more like one the user rejected for it than like
// the profile.
func (s *Store) Match(voices map[int]Voice, taken func(name string) bool) map[int]Matched {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	type cand struct {
		key int
		p   *Profile
		sim float64
	}
	var cands []cand
	for key, v := range voices {
		if v.N < s.params.MinFingerprints {
			continue
		}
		c := v.Centroid()
		best, second := cand{key: key, sim: -2}, -2.0
		for _, p := range s.profiles {
			if !s.inUseLocked(p) {
				continue
			}
			sim := Similarity(c, p.centroid)
			if rejectedLike(c, p, sim) {
				continue
			}
			if sim > best.sim {
				second, best = best.sim, cand{key, p, sim}
			} else if sim > second {
				second = sim
			}
		}
		if best.p != nil && best.sim >= s.params.Threshold && best.sim-second >= s.params.Margin {
			cands = append(cands, best)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	out := map[int]Matched{}
	used := map[string]bool{}
	for _, c := range cands {
		if used[c.p.ID] || (taken != nil && taken(c.p.Name)) {
			continue
		}
		used[c.p.ID] = true
		out[c.key] = Matched{c.p.Name, c.p.ID, c.sim}
	}
	return out
}

// Likest is, for each voice (by key), the profile in use it's most like
// and how alike, whether or not that's enough for Match to name it.
// Voices with too few fingerprints, and profiles the user rejected the
// voice for, are left out as in Match.
func (s *Store) Likest(voices map[int]Voice) map[int]Matched {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	out := map[int]Matched{}
	for key, v := range voices {
		if v.N < s.params.MinFingerprints {
			continue
		}
		c := v.Centroid()
		var best *Profile
		bestSim := -2.0
		for _, p := range s.profiles {
			if !s.inUseLocked(p) {
				continue
			}
			sim := Similarity(c, p.centroid)
			if !rejectedLike(c, p, sim) && sim > bestSim {
				best, bestSim = p, sim
			}
		}
		if best != nil {
			out[key] = Matched{best.Name, best.ID, bestSim}
		}
	}
	return out
}

// Knows reports whether any profile in use (see InUse) has the name (or
// once had it).
func (s *Store) Knows(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	for _, p := range s.namedLocked(name) {
		if s.inUseLocked(p) {
			return true
		}
	}
	return false
}

// InUse reports whether a profile names voices: enough fingerprints, and
// heard in MinMeetings meetings or confirmed by the user.
func (s *Store) InUse(p *Profile) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUseLocked(p)
}

func (s *Store) inUseLocked(p *Profile) bool {
	if p.n < s.params.MinFingerprints {
		return false
	}
	learnt := 0
	for _, h := range p.Meetings {
		if h.Voice.N < s.params.MinLearn {
			continue
		}
		if h.Source == FromNamed {
			return true
		}
		learnt++
	}
	return p.Confirmed || learnt >= s.params.MinMeetings
}

// Confirm records that the profile is the person's voice.
func (s *Store) Confirm(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	p.Confirmed = true
	return s.saveLocked()
}

// rejectedLike reports whether centroid c is at least as like a voice the
// user rejected for p as like p (sim).
func rejectedLike(c []float32, p *Profile, sim float64) bool {
	for _, r := range p.Rejected {
		if Similarity(c, r.Voice.Centroid()) >= sim {
			return true
		}
	}
	return false
}

// Reject records that a voice the profile named in a meeting isn't them.
func (s *Store) Reject(id, sessionID string, at time.Time, v Voice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	if v.N == 0 {
		return fmt.Errorf("no voice to reject")
	}
	kept := p.Rejected[:0]
	for _, r := range p.Rejected {
		if r.Session != sessionID {
			kept = append(kept, r)
		}
	}
	p.Rejected = append(kept, Heard{Session: sessionID, At: at, Source: "rejected", Voice: v})
	return s.saveLocked()
}

// StartOver clears what a profile learnt, keeping its name: its meetings
// so far are never learnt from again, and it learns from new ones.
func (s *Store) StartOver(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	for _, h := range p.Meetings {
		p.Excluded = appendOnce(p.Excluded, h.Session)
	}
	p.Meetings = nil
	p.Confirmed = false
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}

// Unforget lets a forgotten name be learnt again (the user named someone
// that, so they mean it).
func (s *Store) Unforget(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	kept := s.forgotten[:0]
	for _, f := range s.forgotten {
		if !s.sameLocked(f, name) {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(s.forgotten) {
		return nil
	}
	s.forgotten = kept
	return s.saveLocked()
}

// Rename renames a profile.
func (s *Store) Rename(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	if p.Name != name {
		p.Aliases = appendOnce(p.Aliases, p.Name)
		p.Name = name
	}
	return s.saveLocked()
}

// Merge folds profile from into into: one person under two names.
func (s *Store) Merge(into, from string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	a, b := s.byIDLocked(into), s.byIDLocked(from)
	if a == nil || b == nil || a == b {
		return fmt.Errorf("can't merge voice profiles %q and %q", into, from)
	}
	// A meeting in both keeps both: they were two voices then.
	a.Meetings = mergeMeetings(append(a.Meetings, b.Meetings...))
	for _, n := range append([]string{b.Name}, b.Aliases...) {
		if n != a.Name { // two profiles of one name: no alias needed
			a.Aliases = appendOnce(a.Aliases, n)
		}
	}
	// Whoever the user said from isn't, into isn't either.
	for _, id := range b.Apart {
		if id != a.ID {
			a.Apart = appendOnce(a.Apart, id)
			if o := s.byIDLocked(id); o != nil {
				o.Apart = appendOnce(o.Apart, a.ID)
			}
		}
	}
	a.refresh(s.params.MinLearn)
	s.removeLocked(from)
	for i := range s.returned {
		r := &s.returned[i]
		if r.From == b.ID {
			r.From = a.ID
		}
		if r.To == b.ID {
			r.To = a.ID
		}
	}
	return s.saveLocked()
}

// Apart records that two profiles are different people.
func (s *Store) Apart(a, b string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	pa, pb := s.byIDLocked(a), s.byIDLocked(b)
	if pa == nil || pb == nil {
		return fmt.Errorf("no such voice profiles")
	}
	pa.Apart = appendOnce(pa.Apart, b)
	pb.Apart = appendOnce(pb.Apart, a)
	return s.saveLocked()
}

// Forget deletes a profile, and its names are never learnt again.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	s.forgotten = appendOnce(s.forgotten, p.Name)
	for _, a := range p.Aliases {
		s.forgotten = appendOnce(s.forgotten, a)
	}
	s.removeLocked(id)
	return s.saveLocked()
}

// DeletedProfile is a deleted profile's names and the meetings it had
// learnt from.
type DeletedProfile struct {
	Names    []string `json:"names"`
	Sessions []string `json:"sessions"`
}

// Delete removes a profile without forgetting its name: a later meeting
// can teach a new one, but the meetings it had learnt from don't rebuild
// it (a relearn for new rules would otherwise bring it straight back).
// Forget is the one that never learns the name again.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	d := DeletedProfile{Names: append([]string{p.Name}, p.Aliases...)}
	for _, h := range p.Meetings {
		d.Sessions = appendOnce(d.Sessions, h.Session)
	}
	s.deleted = append(s.deleted, d)
	s.removeLocked(id)
	return s.saveLocked()
}

// deletedLocked: a deleted profile named name had learnt from sessionID.
func (s *Store) deletedLocked(name, sessionID string) bool {
	for _, d := range s.deleted {
		for _, n := range d.Names {
			if sameExactName(n, name) && contains(d.Sessions, sessionID) {
				return true
			}
		}
	}
	return false
}

// Forgotten is the names never learnt again (Forget), for the owner to
// see and allow again.
func (s *Store) Forgotten() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	return append([]string(nil), s.forgotten...)
}

// Allow takes exactly name off the forgotten names.
func (s *Store) Allow(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	kept := s.forgotten[:0:0]
	for _, f := range s.forgotten {
		if !sameExactName(f, name) {
			kept = append(kept, f)
		}
	}
	if len(kept) == len(s.forgotten) {
		return nil
	}
	s.forgotten = kept
	return s.saveLocked()
}

// ForgetMeeting drops what one meeting taught a profile, and it's never
// learnt from that meeting again.
// ConfirmMeeting marks what a meeting taught a profile as theirs.
func (s *Store) ConfirmMeeting(id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	for i := range p.Meetings {
		if p.Meetings[i].Session == sessionID {
			p.Meetings[i].Confirmed = true
			return s.saveLocked()
		}
	}
	return fmt.Errorf("%s didn't learn from that meeting", p.Name)
}

func (s *Store) ForgetMeeting(id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	p := s.byIDLocked(id)
	if p == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	kept := p.Meetings[:0]
	for _, h := range p.Meetings {
		if h.Session != sessionID {
			kept = append(kept, h)
		}
	}
	p.Meetings = kept
	p.Excluded = appendOnce(p.Excluded, sessionID)
	p.refresh(s.params.MinLearn)
	return s.saveLocked()
}

// Pair is two profiles that may be one person.
type Pair struct {
	A, B       string // profile IDs
	Similarity float64
	Name       string // SameNamed: the name they share
}

// LookAlike lists pairs of profiles whose voices are at least min alike
// and that the user hasn't said are different people, likest first: the
// same person under two names, or a profile learnt from a misread name.
func (s *Store) LookAlike(min float64) []Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	var out []Pair
	for i, a := range s.profiles {
		for _, b := range s.profiles[i+1:] {
			if contains(a.Apart, b.ID) {
				continue
			}
			if sim := Similarity(a.centroid, b.centroid); sim >= min {
				out = append(out, Pair{A: a.ID, B: b.ID, Similarity: sim})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// SameNamed lists pairs of profiles that share a name (or an alias, or
// names same says are one) and that the user hasn't said are different
// people, likest first: one person whose voice Learn kept in two
// profiles, or two people with one name. Similarity is -2 when either
// has nothing learnt.
func (s *Store) SameNamed() []Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	var out []Pair
	for i, a := range s.profiles {
		for _, b := range s.profiles[i+1:] {
			if contains(a.Apart, b.ID) {
				continue
			}
			if name := s.sharedNameLocked(a, b); name != "" {
				out = append(out, Pair{A: a.ID, B: b.ID, Similarity: Similarity(a.centroid, b.centroid), Name: name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	return out
}

// sharedNameLocked is a name of a's that b has too ("" if none), a's
// own name first. Only the same name, ignoring case and spacing: the
// loose match learning uses (a cut-off "Kevin Gold" for "Kevin
// Goldsmith") paired a bare "Kevin" with every Kevin, three different
// people (2026-11-12).
func (s *Store) sharedNameLocked(a, b *Profile) string {
	for _, n := range append([]string{a.Name}, a.Aliases...) {
		for _, m := range append([]string{b.Name}, b.Aliases...) {
			if sameExactName(n, m) {
				return n
			}
		}
	}
	return ""
}

// sameExactName: the same name once case and spacing are set aside.
func sameExactName(a, b string) bool {
	norm := func(x string) string { return strings.ToLower(strings.Join(strings.Fields(x), " ")) }
	return a != "" && norm(a) == norm(b)
}

// Closest is the profile most like v and how alike (nil if none).
func (s *Store) Closest(v Voice) (*Profile, float64) { return s.ClosestExcept(v, "") }

// ClosestExcept is Closest leaving out profile skip (the one the owner
// said a voice isn't).
func (s *Store) ClosestExcept(v Voice, skip string) (*Profile, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	c := v.Centroid()
	var best *Profile
	bestSim := -2.0
	for _, p := range s.profiles {
		if p.ID == skip {
			continue
		}
		if sim := Similarity(c, p.centroid); sim > bestSim {
			best, bestSim = p, sim
		}
	}
	if best == nil {
		return nil, 0
	}
	cp := *best
	return &cp, bestSim
}

func (s *Store) byIDLocked(id string) *Profile {
	for _, p := range s.profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Store) removeLocked(id string) {
	kept := s.profiles[:0]
	for _, p := range s.profiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	s.profiles = kept
	for _, p := range s.profiles {
		kept := p.Apart[:0]
		for _, a := range p.Apart {
			if a != id {
				kept = append(kept, a)
			}
		}
		p.Apart = kept
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func appendOnce(xs []string, x string) []string {
	if contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Move says what a profile learnt from one meeting was someone else's:
// it leaves the profile (never learnt by it again) and teaches the
// profile toID instead, as the user's naming, or, with toID "", a new
// profile named toName. v is the meeting's voice for the profile: what
// it learnt, or, for a meeting already taken out, the voice worked out
// again from the meeting.
func (s *Store) Move(fromID, sessionID string, at time.Time, v Voice, toID, toName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLocked()
	from := s.byIDLocked(fromID)
	if from == nil {
		return fmt.Errorf("no voice profile %q", fromID)
	}
	if v.N == 0 {
		return fmt.Errorf("no voice to move")
	}
	var to *Profile
	switch {
	case toID != "":
		if to = s.byIDLocked(toID); to == nil {
			return fmt.Errorf("no voice profile %q", toID)
		}
		if to == from {
			return fmt.Errorf("that's the same profile")
		}
	case toName != "":
		to = &Profile{ID: newID(), Name: toName, Created: time.Now()}
		s.profiles = append(s.profiles, to)
	default:
		return fmt.Errorf("no profile to move to")
	}
	kept := from.Meetings[:0]
	for _, h := range from.Meetings {
		if h.Session != sessionID {
			kept = append(kept, h)
		}
	}
	from.Meetings = kept
	from.Excluded = appendOnce(from.Excluded, sessionID)
	from.refresh(s.params.MinLearn)

	to.Excluded = removeString(to.Excluded, sessionID)
	merged := false
	for i := range to.Meetings {
		if to.Meetings[i].Session == sessionID {
			to.Meetings[i].Voice.Merge(v)
			to.Meetings[i].Source = FromNamed
			merged = true
		}
	}
	if !merged {
		to.Meetings = append(to.Meetings, Heard{Session: sessionID, At: at, Source: FromNamed, Voice: v})
	}
	to.refresh(s.params.MinLearn)
	var keptForgotten []string
	for _, f := range s.forgotten {
		if !s.sameLocked(f, to.Name) {
			keptForgotten = append(keptForgotten, f)
		}
	}
	s.forgotten = keptForgotten
	return s.saveLocked()
}

func removeString(xs []string, x string) []string {
	var out []string
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// OwnerPath is where the user's own voice (from their microphone) is kept
// for model.
func OwnerPath(dataDir, model string) string {
	return filepath.Join(dataDir, "voices", model+"-you.json")
}

// LoadOwner reads the user's voice; a missing file is an empty voice.
func LoadOwner(path string) (Voice, error) {
	var v Voice
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	return v, json.Unmarshal(b, &v)
}

// SaveOwner writes the user's voice.
func SaveOwner(path string, v Voice) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Normalized is emb scaled to unit length (nil if empty or zero), so
// fingerprints of different loudness add up evenly.
func Normalized(emb []float32) []float32 {
	var v Voice
	v.Add(emb)
	return v.Centroid()
}
