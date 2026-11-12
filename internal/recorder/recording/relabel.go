package recording

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// Renaming a voice profile changes only the profile; the meetings it
// identified keep the old name in their speaker labels. A relabel puts
// the profile's current name on those labels, in the recorder's own
// saved sessions, and records each change so it can be undone
// (docs/recorder-screens.md, "Renaming a voice profile renames its
// speakers"). Labels only: what was said is never touched.

// relabelLogName is the relabel log in the recorder's folder: one JSON
// line per label changed, and one per batch undone.
const relabelLogName = "relabels.jsonl"

// relabelMu serialises relabels and their undo: each reads and rewrites
// many sessions.
var relabelMu sync.Mutex

type relabelEntry struct {
	Batch    string    `json:"batch"`
	At       time.Time `json:"at"`
	Model    string    `json:"model,omitempty"`
	Profile  string    `json:"profile,omitempty"`
	Session  string    `json:"session,omitempty"`
	OldLabel string    `json:"old_label,omitempty"`
	NewLabel string    `json:"new_label,omitempty"`
	OldName  string    `json:"old_name,omitempty"`
	NewName  string    `json:"new_name,omitempty"`
	Lines    int       `json:"lines,omitempty"`
	// Undone marks a line recording that the batch was reversed.
	Undone bool `json:"undone,omitempty"`
}

var batchID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func newBatch() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// labelName is the person a label names: "Ana" of "Person 3 (Ana)" or of
// a label the user set to "Ana"; "" for "You", "Person 3" and the like.
func labelName(label string) string {
	if n := session.HintName(label); n != "" {
		return n
	}
	l := strings.TrimSpace(label)
	if l == "" || l == "You" || unnamedLabel.MatchString(l) || strings.HasPrefix(l, "Person ") {
		return ""
	}
	return l
}

// withName is label carrying name instead: "Person 3 (Ana)" keeps its
// person, a plain name is replaced.
func withName(label, name string) string {
	if session.HintName(label) != "" {
		return personLabel.FindString(label) + " (" + name + ")"
	}
	return name
}

func sameNameFold(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// RelabelProfileMeetings puts profile id's name on the labels that carry
// one of its old names (its aliases), in every saved session it
// identified. See the package docs above and docs/recorder-screens.md for
// which labels qualify.
func (a *Recorder) RelabelProfileMeetings(model, id string) (*control.RelabelResult, error) {
	relabelMu.Lock()
	defer relabelMu.Unlock()
	s, err := a.voiceStore(model)
	if err != nil {
		return nil, err
	}
	var prof *voices.Profile
	for _, p := range s.Profiles() {
		if p.ID == id {
			prof = &p
			break
		}
	}
	if prof == nil {
		return nil, fmt.Errorf("no voice profile %q", id)
	}
	res := &control.RelabelResult{Batch: newBatch(), Changed: []control.RelabelChange{}, Left: []control.RelabelLeft{}}
	var old []string
	for _, n := range prof.Aliases {
		if strings.TrimSpace(n) != "" && !sameNameFold(n, prof.Name) {
			old = append(old, n)
		}
	}
	if len(old) == 0 {
		return res, nil
	}
	isOld := func(n string) bool {
		for _, o := range old {
			if sameNameFold(o, n) {
				return true
			}
		}
		return false
	}
	heard := map[string][]voices.Clip{}
	for _, h := range prof.Meetings {
		heard[h.Session] = append(heard[h.Session], h.Clip)
	}
	excluded := map[string]bool{}
	for _, e := range prof.Excluded {
		excluded[e] = true
	}
	profVoice := prof.LearnedVoice(s.MinLearn())
	if profVoice.N == 0 {
		profVoice = prof.Voice()
	}
	threshold := s.Params().Threshold

	list, err := a.store.List()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, summary := range list {
		if excluded[summary.ID] {
			continue
		}
		named := namedBy(summary, id)
		if len(named) == 0 && heard[summary.ID] == nil {
			continue
		}
		// Load it again under the recorder's checks: not the recording in
		// progress, not one being saved.
		sess, err := a.editable(summary.ID)
		if err != nil {
			continue
		}
		cands := oldNameLabels(sess, isOld)
		if len(cands) == 0 {
			continue
		}
		var chosen []string
		switch {
		case len(named) > 0:
			for _, l := range cands {
				if named[l] {
					chosen = append(chosen, l)
				} else {
					res.Left = append(res.Left, control.RelabelLeft{Session: sess.ID, Label: l, Why: "the profile didn't name this speaker here"})
				}
			}
		case hasName(sess, prof.Name):
			// The profile's speaker here already has its name (relabelled
			// before): the others with the old name are someone else.
			for _, l := range cands {
				res.Left = append(res.Left, control.RelabelLeft{Session: sess.ID, Label: l, Why: "another speaker here already has the profile's name"})
			}
		case len(cands) == 1:
			chosen = cands
		default:
			var left []control.RelabelLeft
			chosen, left = a.byVoice(sess, cands, profVoice, threshold, heard[sess.ID])
			res.Left = append(res.Left, left...)
		}
		if len(chosen) == 0 {
			continue
		}
		var entries []relabelEntry
		for _, l := range chosen {
			nl := withName(l, prof.Name)
			n := relabelSession(sess, l, nl, labelName(l), prof.Name)
			if n == 0 {
				continue
			}
			res.Changed = append(res.Changed, control.RelabelChange{Session: sess.ID, OldLabel: l, NewLabel: nl, Lines: n})
			entries = append(entries, relabelEntry{Batch: res.Batch, At: now, Model: model, Profile: id, Session: sess.ID,
				OldLabel: l, NewLabel: nl, OldName: labelName(l), NewName: prof.Name, Lines: n})
		}
		if len(entries) == 0 {
			continue
		}
		// The log first: a session saved without its entry couldn't be
		// undone, an entry whose session didn't save undoes nothing.
		if err := appendRelabelLog(entries); err != nil {
			return res, err
		}
		if err := a.store.Save(sess); err != nil {
			return res, fmt.Errorf("session %s: %w", sess.ID, err)
		}
	}
	return res, nil
}

// namedBy is the labels profile id named in sess, by voice name or by a
// line's voice match.
func namedBy(sess *session.Session, id string) map[string]bool {
	out := map[string]bool{}
	for _, vn := range sess.VoiceNames {
		if vn.Profile == id {
			out[vn.Label] = true
		}
	}
	for _, seg := range sess.Segments {
		if seg.VoiceMatch != nil && seg.VoiceMatch.Profile == id {
			out[seg.Speaker] = true
		}
	}
	return out
}

// hasName reports whether a final line of sess is labelled with name.
func hasName(sess *session.Session, name string) bool {
	for _, seg := range sess.Segments {
		if seg.Status == "" && sameNameFold(labelName(seg.Speaker), name) {
			return true
		}
	}
	return false
}

// oldNameLabels are sess's final lines' labels naming an old name, in
// order.
func oldNameLabels(sess *session.Session, isOld func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, seg := range sess.Segments {
		if seg.Status != "" || seen[seg.Speaker] {
			continue
		}
		seen[seg.Speaker] = true
		if n := labelName(seg.Speaker); n != "" && isOld(n) {
			out = append(out, seg.Speaker)
		}
	}
	sort.Strings(out)
	return out
}

// byVoice picks, among several labels carrying an old name in a session
// the profile only heard (a room's tile covers everyone in the room), the
// ones whose voice is the profile's: as alike as naming requires. Without
// fingerprints it falls back to the label speaking during the profile's
// clip from this session.
func (a *Recorder) byVoice(sess *session.Session, cands []string, prof voices.Voice, threshold float64, clips []voices.Clip) (chosen []string, left []control.RelabelLeft) {
	p, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err == nil && prof.N > 0 {
		c := prof.Centroid()
		for _, l := range cands {
			learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(x string) string {
				if x == l {
					return x
				}
				return ""
			}))
			if len(learned) == 0 || learned[0].Voice.N == 0 {
				left = append(left, control.RelabelLeft{Session: sess.ID, Label: l, Why: "no voice fingerprints for this speaker"})
				continue
			}
			if sim := voices.Similarity(learned[0].Voice.Centroid(), c); sim >= threshold {
				chosen = append(chosen, l)
			} else {
				left = append(left, control.RelabelLeft{Session: sess.ID, Label: l, Why: fmt.Sprintf("another voice (%.2f like the profile)", sim)})
			}
		}
		return chosen, left
	}
	// No fingerprints: the label heard most during the profile's clips.
	overlap := map[string]float64{}
	for _, seg := range sess.Segments {
		if seg.Status != "" {
			continue
		}
		for _, c := range clips {
			if o := min(seg.EndTime, c.End) - max(seg.StartTime, c.Start); o > 0 {
				overlap[seg.Speaker] += o
			}
		}
	}
	best, bestO := "", 0.0
	for _, l := range cands {
		if overlap[l] > bestO {
			best, bestO = l, overlap[l]
		}
	}
	for _, l := range cands {
		if l == best {
			chosen = append(chosen, l)
			continue
		}
		left = append(left, control.RelabelLeft{Session: sess.ID, Label: l, Why: "several speakers carry the old name and the profile's clip isn't this one"})
	}
	return chosen, left
}

// relabelSession renames label to newLabel on sess's lines (keeping the
// live label, as a rename does) and in its voice names and doubts, and
// returns how many lines changed.
func relabelSession(sess *session.Session, label, newLabel, oldName, newName string) int {
	if label == newLabel {
		return 0
	}
	n := 0
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if seg.Speaker != label {
			continue
		}
		seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), newLabel
		n++
	}
	for i := range sess.VoiceNames {
		if vn := &sess.VoiceNames[i]; vn.Label == label {
			vn.Label = newLabel
			if sameNameFold(vn.Name, oldName) {
				vn.Name = newName
			}
		}
	}
	for i := range sess.VoiceDoubts {
		if vd := &sess.VoiceDoubts[i]; vd.Label == label {
			vd.Label = newLabel
		}
	}
	return n
}

// UndoRelabel reverses a batch: lines still labelled as the batch left
// them get their old label back. A line relabelled since is left as it
// is.
func (a *Recorder) UndoRelabel(batch string) (*control.RelabelResult, error) {
	if !batchID.MatchString(batch) {
		return nil, fmt.Errorf("not a relabel batch")
	}
	relabelMu.Lock()
	defer relabelMu.Unlock()
	entries, err := readRelabelLog()
	if err != nil {
		return nil, err
	}
	var mine []relabelEntry
	for _, e := range entries {
		if e.Batch != batch {
			continue
		}
		if e.Undone {
			return nil, fmt.Errorf("that relabel was undone already")
		}
		mine = append(mine, e)
	}
	if len(mine) == 0 {
		return nil, fmt.Errorf("no relabel %s", batch)
	}
	res := &control.RelabelResult{Batch: batch, Changed: []control.RelabelChange{}, Left: []control.RelabelLeft{}}
	bySession := map[string][]relabelEntry{}
	var order []string
	for _, e := range mine {
		if bySession[e.Session] == nil {
			order = append(order, e.Session)
		}
		bySession[e.Session] = append(bySession[e.Session], e)
	}
	for _, sid := range order {
		sess, err := a.editable(sid)
		if err != nil {
			for _, e := range bySession[sid] {
				res.Left = append(res.Left, control.RelabelLeft{Session: sid, Label: e.NewLabel, Why: err.Error()})
			}
			continue
		}
		changed := false
		for _, e := range bySession[sid] {
			n := 0
			for i := range sess.Segments {
				seg := &sess.Segments[i]
				if seg.Speaker != e.NewLabel {
					continue
				}
				seg.Speaker = e.OldLabel
				if seg.LiveSpeaker == e.OldLabel {
					seg.LiveSpeaker = ""
				}
				n++
			}
			for i := range sess.VoiceNames {
				if vn := &sess.VoiceNames[i]; vn.Label == e.NewLabel {
					vn.Label = e.OldLabel
					if sameNameFold(vn.Name, e.NewName) {
						vn.Name = e.OldName
					}
				}
			}
			for i := range sess.VoiceDoubts {
				if vd := &sess.VoiceDoubts[i]; vd.Label == e.NewLabel {
					vd.Label = e.OldLabel
				}
			}
			if n == 0 {
				res.Left = append(res.Left, control.RelabelLeft{Session: sid, Label: e.NewLabel, Why: "relabelled again since"})
				continue
			}
			changed = true
			res.Changed = append(res.Changed, control.RelabelChange{Session: sid, OldLabel: e.NewLabel, NewLabel: e.OldLabel, Lines: n})
		}
		if changed {
			if err := a.store.Save(sess); err != nil {
				return res, fmt.Errorf("session %s: %w", sid, err)
			}
		}
	}
	if err := appendRelabelLog([]relabelEntry{{Batch: batch, At: time.Now(), Undone: true}}); err != nil {
		return res, err
	}
	return res, nil
}

func relabelLogPath() string { return filepath.Join(config.DataDir(), relabelLogName) }

// appendRelabelLog adds entries to the log; it holds names, so only the
// owner may read it.
func appendRelabelLog(es []relabelEntry) error {
	if len(es) == 0 {
		return nil
	}
	f, err := os.OpenFile(relabelLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range es {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readRelabelLog() ([]relabelEntry, error) {
	f, err := os.Open(relabelLogPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []relabelEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e relabelEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Batch != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}
