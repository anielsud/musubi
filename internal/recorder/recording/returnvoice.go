package recording

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// "Not them" on one of a voice profile's meetings (docs/recorder-screens.md,
// "Not them sends the voice back"): the meeting is taken out of the
// profile, as Discard did, and the speakers the profile named there go
// back to unnamed ("Person 3 (Ana)" → "Person 3"), marked as returned so
// the Voices page asks who they are ("Who's this?") whatever their age or
// length. Naming one there joins an existing profile or starts a new one
// (rename_speaker teaches the profiles); "Can't tell" drops it.

// ReturnVoiceMeeting carries out return_voice_meeting.
func (a *Recorder) ReturnVoiceMeeting(p control.ReturnArgs) (*control.ReturnResult, error) {
	relabelMu.Lock()
	defer relabelMu.Unlock()
	s, err := a.voiceStore(p.Model)
	if err != nil {
		return nil, err
	}
	var prof *voices.Profile
	for _, x := range s.Profiles() {
		if x.ID == p.ID {
			prof = &x
			break
		}
	}
	if prof == nil {
		return nil, fmt.Errorf("no voice profile %q", p.ID)
	}
	sess, err := a.editable(p.Session)
	if err != nil {
		return nil, err
	}
	res := &control.ReturnResult{Changed: []control.RelabelChange{}, Left: []control.RelabelLeft{}}
	labels, left := a.profileLabels(sess, prof, s)
	res.Left = append(res.Left, left...)
	if len(labels) == 0 && len(left) == 0 {
		res.Left = append(res.Left, control.RelabelLeft{Session: sess.ID, Why: "no speaker here has the profile's name"})
	}

	if !p.Relabel && !p.Preview {
		for _, l := range labels {
			res.Left = append(res.Left, control.RelabelLeft{Session: sess.ID, Label: l, Why: "label left as it was"})
		}
		return res, s.ForgetMeeting(prof.ID, sess.ID)
	}
	batch, now := newBatch(), time.Now()
	var entries []relabelEntry
	for _, l := range labels {
		nl := unnamedFor(sess, l)
		n := returnLabel(sess, l, nl, prof, p.Model, now)
		if n == 0 {
			continue
		}
		res.Changed = append(res.Changed, control.RelabelChange{Session: sess.ID, OldLabel: l, NewLabel: nl, Lines: n})
		entries = append(entries, relabelEntry{Batch: batch, At: now, Model: p.Model, Profile: prof.ID, Session: sess.ID,
			OldLabel: l, NewLabel: nl, OldName: labelName(l), Lines: n})
	}
	if p.Preview {
		b, err := json.Marshal(sess)
		if err != nil {
			return nil, err
		}
		res.Session = b
		return res, nil
	}
	if len(entries) > 0 {
		res.Batch = batch
		// The log first, as for a relabel: undo_relabel puts the labels
		// back.
		if err := appendRelabelLog(entries); err != nil {
			return res, err
		}
		if err := a.saveSession(sess); err != nil {
			return res, fmt.Errorf("session %s: %w", sess.ID, err)
		}
	}
	return res, s.ForgetMeeting(prof.ID, sess.ID)
}

// profileLabels are the labels profile prof gave speakers in sess: those
// it named by voice; else, in a meeting it learnt from a tile, the label
// carrying its name, or of several (a room's tile), those whose voice is
// the profile's (as a relabel picks them). The others are left, and why.
func (a *Recorder) profileLabels(sess *session.Session, prof *voices.Profile, s *voices.Store) ([]string, []control.RelabelLeft) {
	onLines := map[string]bool{}
	for _, seg := range sess.Segments {
		onLines[seg.Speaker] = true
	}
	var out []string
	for l := range namedBy(sess, prof.ID) {
		if onLines[l] && l != "You" {
			out = append(out, l)
		}
	}
	if len(out) > 0 {
		sort.Strings(out)
		return out, nil
	}
	names := append([]string{prof.Name}, prof.Aliases...)
	cands := oldNameLabels(sess, func(n string) bool {
		for _, x := range names {
			if sameNameFold(x, n) {
				return true
			}
		}
		return false
	})
	if len(cands) <= 1 {
		return cands, nil
	}
	var clips []voices.Clip
	for _, h := range prof.Meetings {
		if h.Session == sess.ID {
			clips = append(clips, h.Clip)
		}
	}
	pv := prof.LearnedVoice(s.MinLearn())
	if pv.N == 0 {
		pv = prof.Voice()
	}
	return a.byVoice(sess, cands, pv, s.Params().Threshold, clips)
}

var personNumber = regexp.MustCompile(`^Person (\d+)`)

// unnamedFor is the unnamed label label goes back to: its "Person N",
// unless another speaker has that label (or it has none), then a new
// one, so two speakers never become one.
func unnamedFor(sess *session.Session, label string) string {
	used := map[string]bool{}
	most := 0
	for _, seg := range sess.Segments {
		if seg.Speaker != label {
			used[seg.Speaker] = true
		}
		for _, l := range []string{seg.Speaker, seg.LiveSpeaker} {
			if m := personNumber.FindStringSubmatch(l); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n > most {
					most = n
				}
			}
		}
	}
	if base := personLabel.FindString(label); base != "" && !used[base] {
		return base
	}
	for n := most + 1; ; n++ {
		if l := "Person " + strconv.Itoa(n); !used[l] {
			return l
		}
	}
}

// returnLabel relabels label's lines newLabel (keeping their live label,
// as a rename does), drops what said the profile named them, marks the
// speaker returned, and returns how many lines changed.
func returnLabel(sess *session.Session, label, newLabel string, prof *voices.Profile, model string, at time.Time) int {
	n := 0
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if seg.Speaker != label {
			continue
		}
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), newLabel
		if seg.VoiceMatch != nil && seg.VoiceMatch.Profile == prof.ID {
			seg.VoiceMatch = nil
		}
		n++
	}
	if n == 0 {
		return 0
	}
	keptNames := sess.VoiceNames[:0]
	for _, vn := range sess.VoiceNames {
		if vn.Label != label {
			keptNames = append(keptNames, vn)
		}
	}
	sess.VoiceNames = keptNames
	dropVoiceDoubt(sess, label)
	dropReturned(sess, newLabel)
	sess.Returned = append(sess.Returned, session.ReturnedVoice{Label: newLabel, Name: prof.Name, Profile: prof.ID, Model: model, At: at})
	return n
}

// dropReturned forgets that label was returned (it has a name now).
func dropReturned(sess *session.Session, label string) {
	kept := sess.Returned[:0]
	for _, r := range sess.Returned {
		if r.Label != label {
			kept = append(kept, r)
		}
	}
	sess.Returned = kept
	if len(sess.Returned) == 0 {
		sess.Returned = nil
	}
}

// SkipReturnedVoice is "Can't tell" on a returned speaker: it keeps its
// unnamed label and isn't asked about again.
func (a *Recorder) SkipReturnedVoice(sessionID, label string) error {
	sess, err := a.editable(sessionID)
	if err != nil {
		return err
	}
	for i := range sess.Returned {
		if sess.Returned[i].Label == label {
			sess.Returned[i].Skipped = true
			return a.saveSession(sess)
		}
	}
	return fmt.Errorf("%q wasn't taken out of a voice profile", label)
}
