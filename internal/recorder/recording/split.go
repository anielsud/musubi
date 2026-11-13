package recording

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// Splitting a label by voice (docs/recorder-screens.md, "Split a speaker
// by voice"): when one label covers two people (the owner named a
// diarized speaker that was really two voices), each of its lines in one
// saved session goes to the candidate profile its own fingerprints sound
// most like, if by a margin; lines without fingerprints, or too close to
// call, keep the label. Owner-named lines are split too: that is the
// point. Logged in relabels.jsonl, line by line, so undo_relabel puts
// exactly those lines back.

// SplitLabelByVoice carries out split_label_by_voice.
func (a *Recorder) SplitLabelByVoice(p control.SplitArgs) (*control.SplitResult, error) {
	label := strings.TrimSpace(p.Label)
	if label == "" || len(p.Into) < 2 {
		return nil, fmt.Errorf("a speaker and at least two voices to split it into are needed")
	}
	relabelMu.Lock()
	defer relabelMu.Unlock()
	sess, err := a.editable(p.Session)
	if err != nil {
		return nil, err
	}
	pr, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil {
		return nil, fmt.Errorf("this meeting's voice fingerprints can't be read: %w", err)
	}
	if info.Model == "" {
		return nil, fmt.Errorf("can't tell which speaker model made this meeting's fingerprints")
	}
	store, err := a.voiceStore(info.Model)
	if err != nil {
		return nil, err
	}
	params := store.Params()
	type cand struct {
		prof     voices.Profile
		label    string
		centroid []float32
	}
	var cands []cand
	profiles := store.Profiles()
	seen := map[string]bool{}
	for _, t := range p.Into {
		prof, err := splitProfile(profiles, t)
		if err != nil {
			return nil, err
		}
		if seen[prof.ID] {
			return nil, fmt.Errorf("%s is listed twice", prof.Name)
		}
		seen[prof.ID] = true
		tl := strings.TrimSpace(t.Label)
		if tl == "" {
			tl = prof.Name
		}
		if len(tl) > 200 || strings.ContainsAny(tl, "\n\r") {
			return nil, fmt.Errorf("a one-line label is needed")
		}
		c := splitVoice(prof, sess.ID, params).Centroid()
		if c == nil {
			return nil, fmt.Errorf("%s's voice profile has nothing learnt to compare with", prof.Name)
		}
		cands = append(cands, cand{prof, tl, c})
	}

	var lines []int
	for i, seg := range sess.Segments {
		if seg.Speaker == label && seg.Status == "" {
			lines = append(lines, i)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("no lines by %q in this meeting", label)
	}
	lv := diarize.LineVoices(pr, info.Offset, sess.Segments, lines)
	res := &control.SplitResult{Counts: make([]control.SplitCount, len(cands)), Lines: []control.SplitLine{}}
	for k, c := range cands {
		res.Counts[k] = control.SplitCount{Profile: c.prof.ID, Name: c.prof.Name, Label: c.label}
	}
	to := map[int]int{} // line → candidate
	for _, i := range lines {
		seg := sess.Segments[i]
		v := lv[i]
		ln := control.SplitLine{Start: seg.StartTime, End: seg.EndTime, Fingerprints: v.N, Best: -2, Second: -2}
		if v.N > 0 {
			vc := v.Centroid()
			best := -1
			for k, c := range cands {
				sim := voices.Similarity(vc, c.centroid)
				if sim > ln.Best {
					ln.Second, ln.Best, best = ln.Best, sim, k
				} else if sim > ln.Second {
					ln.Second = sim
				}
			}
			if best >= 0 && ln.Best-ln.Second >= params.Margin {
				to[i] = best
				ln.Label = cands[best].label
				res.Counts[best].Lines++
			}
		}
		ln.Best, ln.Second = round3(ln.Best), round3(ln.Second)
		if ln.Label == "" {
			res.Undecided++
		}
		res.Lines = append(res.Lines, ln)
	}
	if p.Preview {
		return res, nil
	}

	batch, now := newBatch(), time.Now()
	starts := make([][]float64, len(cands))
	for _, i := range lines {
		k, ok := to[i]
		if !ok || cands[k].label == label {
			continue
		}
		seg := &sess.Segments[i]
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), cands[k].label
		if seg.VoiceMatch != nil && seg.VoiceMatch.Profile != cands[k].prof.ID {
			seg.VoiceMatch = nil
		}
		starts[k] = append(starts[k], seg.StartTime)
	}
	var entries []relabelEntry
	for k, c := range cands {
		if len(starts[k]) == 0 {
			continue
		}
		entries = append(entries, relabelEntry{Batch: batch, At: now, Model: info.Model, Profile: c.prof.ID, Session: sess.ID,
			OldLabel: label, NewLabel: c.label, OldName: labelName(label), NewName: c.prof.Name, Lines: len(starts[k]), Starts: starts[k]})
	}
	if len(entries) == 0 {
		return res, nil
	}
	res.Batch = batch
	// The log first, as for a relabel: undo_relabel puts the lines back.
	if err := appendRelabelLog(entries); err != nil {
		return res, err
	}
	if err := a.saveSession(sess); err != nil {
		return res, fmt.Errorf("session %s: %w", sess.ID, err)
	}
	// Who said what changed: what the meeting teaches the profiles too.
	a.learnNamedLater(sess)
	return res, nil
}

// splitProfile is the profile a split candidate names: by ID, or by
// exactly one profile's name (ignoring case and spacing).
func splitProfile(profiles []voices.Profile, t control.SplitTarget) (voices.Profile, error) {
	if t.ID != "" {
		for _, p := range profiles {
			if p.ID == t.ID {
				return p, nil
			}
		}
		return voices.Profile{}, fmt.Errorf("no voice profile %q", t.ID)
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return voices.Profile{}, fmt.Errorf("a voice profile or a name is needed for each voice")
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	var found []voices.Profile
	for _, p := range profiles {
		if norm(p.Name) == norm(name) {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return voices.Profile{}, fmt.Errorf("no voice profile named %s", name)
	case 1:
		return found[0], nil
	}
	return voices.Profile{}, fmt.Errorf("%d voice profiles are named %s: pick one", len(found), name)
}

// splitVoice is the voice a candidate is compared by: what its other
// meetings taught, so a profile learnt partly from the very label being
// split (the owner's naming of it) doesn't vote for itself; what it
// learnt in all when the other meetings hold too little.
func splitVoice(p voices.Profile, sessionID string, params voices.Params) voices.Voice {
	var others voices.Voice
	for _, h := range p.Meetings {
		if h.Session != sessionID && h.Voice.N >= params.MinLearn {
			others.Merge(h.Voice)
		}
	}
	if others.N >= params.MinFingerprints {
		return others
	}
	if v := p.LearnedVoice(params.MinLearn); v.N > 0 {
		return v
	}
	return p.Voice()
}

// sameStart: a line's start as logged.
func sameStart(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
