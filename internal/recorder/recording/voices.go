package recording

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/toolpath"
	"github.com/anielsud/musubi/internal/recorder/videohint"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// The Voices page manages the voice profiles (MeetingConfig.VoiceProfiles):
// the people Tomoe knows by voice, pairs of profiles that may be one
// person, and voices from recent meetings nobody has named yet.

// The Voices page's views are the control protocol's types, so the
// Musubi app reads them with the JSON names Tomoe's window used.
type (
	VoiceClip        = control.VoiceClip
	TileRef          = control.TileRef
	VoiceMeetingView = control.VoiceMeetingView
	VoiceProfileView = control.VoiceProfileView
	NamedByVoiceView = control.NamedByVoiceView
	VoiceDoubtView   = control.VoiceDoubtView
	VoicePairView    = control.VoicePairView
	SameNameView     = control.SameNameView
	UnknownVoice     = control.UnknownVoice
	VoicesView       = control.VoicesView
)

const (
	// unknownDays and unknownSeconds pick the voices worth asking about:
	// from meetings this recent, speaking this long.
	unknownDays    = 14
	unknownSeconds = 30
	// namedSeconds is the least speech a voice-named speaker needs to be
	// asked about: shorter clips are too short to judge.
	namedSeconds = 3
	// suggestAbove is how alike a nameless voice and a profile must be to
	// suggest it ("sounds like"): below the naming bar, as the user
	// decides.
	suggestAbove = 0.5
	// clipSeconds caps a clip's length.
	clipSeconds = 8
)

// unnamedLabel is a label with no name: "Person 3", or one named only by a
// room's tile.
var unnamedLabel = regexp.MustCompile(`^Person \d+$`)

func isUnnamed(label string) bool {
	if unnamedLabel.MatchString(label) {
		return true
	}
	name := session.HintName(label)
	return name != "" && diarize.IsRoomTile(name)
}

// voiceStores are the profile stores that exist, one per speaker model.
func (a *Recorder) voiceStores() (map[string]*voices.Store, error) {
	cfg := a.config()
	out := map[string]*voices.Store{}
	for _, m := range models.SpeakerModels {
		if _, err := os.Stat(voices.Path(config.DataDir(), m.ID)); err != nil {
			continue
		}
		s, err := diarize.VoiceStore(cfg.Meeting, m.ID)
		if err != nil {
			return nil, err
		}
		out[m.ID] = s
	}
	return out, nil
}

// Voices is the Voices page: every profile, likest pairs, and nameless
// voices from recent meetings.
func (a *Recorder) Voices() (*VoicesView, error) {
	view := &VoicesView{Enabled: a.config().Meeting.VoiceProfiles, Profiles: []VoiceProfileView{}, Pairs: []VoicePairView{}, Unknown: []UnknownVoice{}, NamedByVoice: []NamedByVoiceView{}, Doubts: []VoiceDoubtView{}, SameName: []SameNameView{}}
	titles := a.sessionTitles()
	stores, err := a.voiceStores()
	if err != nil {
		return nil, err
	}
	for model, s := range stores {
		for _, p := range s.Profiles() {
			v := VoiceProfileView{Model: model, ID: p.ID, Name: p.Name, Meetings: len(p.Meetings), Fingerprints: p.Voice().N, LastHeard: p.LastHeard(),
				Heard: []VoiceMeetingView{}, Rejected: len(p.Rejected), StartedOver: len(p.Meetings) == 0 && len(p.Excluded) > 0,
				InUse: s.InUse(&p), Confirmed: p.Confirmed}
			for i, h := range p.Meetings {
				var rest voices.Voice
				for j, o := range p.Meetings {
					if j != i {
						rest.Merge(o.Voice)
					}
				}
				agree := 0.0
				if rest.N > 0 {
					agree = voices.Similarity(h.Voice.Centroid(), rest.Centroid())
				}
				v.Heard = append(v.Heard, VoiceMeetingView{Session: h.Session, Title: titles[h.Session], At: h.At, Source: h.Source, Fingerprints: h.Voice.N, Agreement: agree, Confirmed: h.Confirmed, Clip: VoiceClip{Session: h.Session, Start: h.Clip.Start, End: h.Clip.End},
					Tiles: tilesOf(h.Session, append([]string{p.Name}, p.Aliases...), tilesPerMeeting)})
			}
			sort.Slice(v.Heard, func(i, j int) bool { return v.Heard[i].At.After(v.Heard[j].At) })
			v.TakenOut = []VoiceMeetingView{}
			for _, sid := range p.Excluded {
				v.TakenOut = append(v.TakenOut, VoiceMeetingView{Session: sid, Title: titles[sid], Tiles: tilesOf(sid, append([]string{p.Name}, p.Aliases...), tilesPerMeeting)})
			}
			for _, h := range v.Heard {
				if len(h.Tiles) > 0 {
					v.Face = &h.Tiles[0]
					break
				}
			}
			var latest time.Time
			for _, h := range p.Meetings {
				switch h.Source {
				case voices.FromTile:
					v.FromTiles++
				case voices.FromNamed:
					v.FromNamed++
				}
				if h.At.After(latest) && h.Clip.End > h.Clip.Start {
					latest, v.Clip = h.At, VoiceClip{Session: h.Session, Start: h.Clip.Start, End: h.Clip.End}
				}
			}
			view.Profiles = append(view.Profiles, v)
		}
		// A same-named pair is asked about once, as "Two profiles named
		// X?", not again as "Same person?".
		asked := map[[2]string]bool{}
		byID := map[string]*VoiceProfileView{}
		for i := range view.Profiles {
			if view.Profiles[i].Model == model {
				byID[view.Profiles[i].ID] = &view.Profiles[i]
			}
		}
		for _, pr := range s.SameNamed() {
			a, b := byID[pr.A], byID[pr.B]
			if a == nil || b == nil {
				continue
			}
			asked[[2]string{pr.A, pr.B}], asked[[2]string{pr.B, pr.A}] = true, true
			view.SameName = append(view.SameName, SameNameView{Model: model, A: pr.A, B: pr.B, Name: pr.Name, Similarity: pr.Similarity,
				MeetingsA: a.Meetings, MeetingsB: b.Meetings, ClipA: a.Clip, ClipB: b.Clip})
		}
		for _, pr := range s.LookAlike(voices.DefaultParams().Threshold) {
			if !asked[[2]string{pr.A, pr.B}] {
				view.Pairs = append(view.Pairs, VoicePairView{Model: model, A: pr.A, B: pr.B, Similarity: pr.Similarity})
			}
		}
	}
	sort.SliceStable(view.Profiles, func(i, j int) bool { return view.Profiles[i].LastHeard.After(view.Profiles[j].LastHeard) })
	sort.SliceStable(view.Pairs, func(i, j int) bool { return view.Pairs[i].Similarity > view.Pairs[j].Similarity })
	sort.SliceStable(view.SameName, func(i, j int) bool { return view.SameName[i].Similarity > view.SameName[j].Similarity })
	view.Unknown, view.NamedByVoice, view.Doubts = a.recentVoices(stores)
	return view, nil
}

// sessionTitles maps saved sessions' IDs to their titles.
func (a *Recorder) sessionTitles() map[string]string {
	out := map[string]string{}
	if list, err := a.store.List(); err == nil {
		for _, s := range list {
			out[s.ID] = s.Title
		}
	}
	return out
}

// recentVoices are, from recent saved meetings: the speakers with no name
// who spoke for a while, each with the profile it's closest to if that's
// close enough to suggest; and the speakers profiles named.
func (a *Recorder) recentVoices(stores map[string]*voices.Store) ([]UnknownVoice, []NamedByVoiceView, []VoiceDoubtView) {
	named, doubts := []NamedByVoiceView{}, []VoiceDoubtView{}
	list, err := a.store.List()
	if err != nil {
		return nil, named, doubts
	}
	since := time.Now().AddDate(0, 0, -unknownDays)
	out := []UnknownVoice{}
	for _, sess := range list {
		if sess.CreatedAt.Before(since) {
			continue
		}
		full, err := a.store.Load(sess.ID)
		if err != nil {
			continue
		}
		type spoke struct {
			seconds float64
			longest session.Segment
		}
		by := map[string]*spoke{}
		byVoice := map[string]session.VoiceName{}
		for _, vn := range full.VoiceNames {
			byVoice[vn.Label] = vn
		}
		byDoubt := map[string]session.VoiceDoubt{}
		for _, vd := range full.VoiceDoubts {
			byDoubt[vd.Label] = vd
		}
		for _, seg := range full.Segments {
			_, voiceNamed := byVoice[seg.Speaker]
			_, doubted := byDoubt[seg.Speaker]
			if !isUnnamed(seg.Speaker) && !voiceNamed && !doubted {
				continue
			}
			sp := by[seg.Speaker]
			if sp == nil {
				sp = &spoke{}
				by[seg.Speaker] = sp
			}
			d := seg.EndTime - seg.StartTime
			sp.seconds += d
			if d > sp.longest.EndTime-sp.longest.StartTime {
				sp.longest = seg
			}
		}
		var cands []UnknownVoice
		for label, sp := range by {
			start := sp.longest.StartTime
			clip := VoiceClip{Session: sess.ID, Start: start, End: min(sp.longest.EndTime, start+clipSeconds)}
			if vn, ok := byVoice[label]; ok {
				if vn.Skipped || sp.seconds < namedSeconds {
					continue
				}
				named = append(named, NamedByVoiceView{Session: sess.ID, Title: full.Title, At: full.CreatedAt, Label: label, Name: vn.Name, Model: vn.Model, Profile: vn.Profile,
					Similarity: vn.Similarity, Seconds: sp.seconds, Clip: clip, Window: vn.Window})
				continue
			}
			if vd, ok := byDoubt[label]; ok {
				if !vd.Skipped && sp.seconds >= namedSeconds {
					s := stores[vd.Model]
					doubts = append(doubts, VoiceDoubtView{Session: sess.ID, Title: full.Title, At: full.CreatedAt, Label: label, Window: vd.Window, Name: vd.Name,
						Similarity: vd.Similarity, Seconds: sp.seconds, Clip: clip, NoProfile: s == nil || !s.Knows(vd.Window)})
				}
				continue
			}
			if sp.seconds < unknownSeconds {
				continue
			}
			cands = append(cands, UnknownVoice{
				Session: sess.ID, Title: full.Title, At: full.CreatedAt, Label: label, Seconds: sp.seconds,
				Clip: VoiceClip{Session: sess.ID, Start: start, End: min(sp.longest.EndTime, start+clipSeconds)},
			})
		}
		if len(cands) == 0 {
			continue
		}
		a.suggest(full, cands, stores)
		out = append(out, cands...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Seconds > out[j].Seconds
	})
	sort.SliceStable(named, func(i, j int) bool {
		if !named[i].At.Equal(named[j].At) {
			return named[i].At.After(named[j].At)
		}
		return named[i].Seconds > named[j].Seconds
	})
	sort.SliceStable(doubts, func(i, j int) bool {
		if !doubts[i].At.Equal(doubts[j].At) {
			return doubts[i].At.After(doubts[j].At)
		}
		return doubts[i].Seconds > doubts[j].Seconds
	})
	return out, named, doubts
}

// suggest fills in the profile each nameless voice sounds most like, from
// the meeting's fingerprints.
func (a *Recorder) suggest(sess *session.Session, cands []UnknownVoice, stores map[string]*voices.Store) {
	p, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil || stores[info.Model] == nil {
		return
	}
	for i := range cands {
		label := cands[i].Label
		learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(l string) string {
			if l == label {
				return l
			}
			return ""
		}))
		if len(learned) == 0 {
			continue
		}
		if prof, sim := stores[info.Model].Closest(learned[0].Voice); prof != nil && sim >= suggestAbove {
			cands[i].SoundsLike, cands[i].Similarity = prof.Name, sim
		}
	}
}

// Fingerprints are megabytes a meeting; the Voices page reads the same
// few each time it's opened.
var (
	savedMu    sync.Mutex
	savedCache = map[string]savedEntry{}
)

type savedEntry struct {
	mod  time.Time
	p    *diarize.Prepared
	info diarize.StreamInfo
}

func loadSavedCached(dir string) (*diarize.Prepared, diarize.StreamInfo, error) {
	st, err := os.Stat(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil, diarize.StreamInfo{}, err
	}
	savedMu.Lock()
	defer savedMu.Unlock()
	if e, ok := savedCache[dir]; ok && e.mod.Equal(st.ModTime()) {
		return e.p, e.info, nil
	}
	p, info, err := diarize.LoadSaved(dir)
	if err != nil {
		return nil, info, err
	}
	savedCache[dir] = savedEntry{st.ModTime(), p, info}
	return p, info, nil
}

func (a *Recorder) voiceStore(model string) (*voices.Store, error) {
	if model == "" {
		return nil, fmt.Errorf("no speaker model given")
	}
	return diarize.VoiceStore(a.config().Meeting, model)
}

// RenameVoice renames a profile.
func (a *Recorder) RenameVoice(model, id, name string) error {
	if name == "" {
		return fmt.Errorf("a name is needed")
	}
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Rename(id, name)
}

// MergeVoices makes two profiles one person, under into's name.
func (a *Recorder) MergeVoices(model, into, from string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Merge(into, from)
}

// KeepVoicesApart records that two profiles are different people.
func (a *Recorder) KeepVoicesApart(model, x, y string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Apart(x, y)
}

// ForgetVoice deletes a profile.
func (a *Recorder) ForgetVoice(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Forget(id)
}

// LearnVoicesFromSavedSessions teaches the profiles from every saved
// meeting with fingerprints (as `tomoe voices learn` does), returning how
// many people it learnt.
func (a *Recorder) LearnVoicesFromSavedSessions() (int, error) {
	list, err := a.store.List()
	if err != nil {
		return 0, err
	}
	m := a.config().Meeting
	people := map[string]bool{}
	for _, s := range list {
		full, err := a.store.Load(s.ID)
		if err != nil {
			continue
		}
		dir := filepath.Join(config.SessionDir(), s.ID)
		_, _ = videohint.BackfillTiles(dir)
		names, err := diarize.LearnSaved(m, dir, full, savedHints(dir, full, m.SnapReadsToInvitees))
		if err != nil {
			continue
		}
		for _, n := range names {
			people[n] = true
		}
	}
	return len(people), nil
}

// learnNamedLater teaches the profiles from a saved meeting after the
// user named a speaker in it, if profiles are on.
func (a *Recorder) learnNamedLater(sess *session.Session) {
	if !a.config().Meeting.VoiceProfiles {
		return
	}
	go func() {
		dir := filepath.Join(config.SessionDir(), sess.ID)
		if _, err := diarize.LearnSaved(a.config().Meeting, dir, sess, savedHints(dir, sess, a.config().Meeting.SnapReadsToInvitees)); err != nil {
			fmt.Printf("session %s: voice profiles not updated: %v\n", sess.ID, err)
		}
	}()
}

// savedHints are a saved session's name reads from the meeting window,
// each snapped to the meeting's invitees' spelling when snap is set (see
// MeetingConfig.SnapReadsToInvitees).
func savedHints(dir string, sess *session.Session, snap bool) []diarize.Hint {
	looks, _ := videohint.ReadLooks(dir)
	var hints []diarize.Hint
	for _, l := range looks {
		t := l.Time.Sub(sess.CreatedAt).Seconds()
		if name, candidates := l.Accepted(); name != "" {
			hints = append(hints, diarize.Hint{T: t, Name: name})
		} else if len(candidates) > 1 {
			hints = append(hints, diarize.Hint{T: t, Candidates: candidates})
		}
	}
	if snap {
		diarize.SnapHints(hints, session.ReadCalendarInvitees(dir))
	}
	return hints
}

// cutVoiceClip cuts a stretch of a meeting's audio (the meeting's side,
// not the mic) as AAC in MP4, for the Musubi app to play: a tenth of the
// WAV Tomoe's window played, which matters inline in a JSON reply.
func (a *Recorder) cutVoiceClip(clip VoiceClip) ([]byte, error) {
	if clip.End <= clip.Start {
		return nil, fmt.Errorf("no clip")
	}
	dir, err := sessionDir(clip.Session)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "audio.m4a")
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("this meeting's audio isn't saved")
	}
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return nil, err
	}
	dur := min(clip.End-clip.Start, clipSeconds)
	// A meeting with a mic has three tracks: the mix, the mic, the meeting
	// (session.SaveAudioM4A); one with a single source has just it.
	for _, track := range []string{"0:a:2", "0:a:0"} {
		// Raw AAC in ADTS frames: it streams from a pipe like the fragmented
		// MP4 did, but WebKit starts playing it at once, where a fragmented
		// MP4 made the app's webview lag before a clip played.
		cmd := exec.Command(ffmpeg, "-v", "error", "-ss", fmt.Sprintf("%.2f", clip.Start), "-t", fmt.Sprintf("%.2f", dur),
			"-i", path, "-map", track, "-ac", "1", "-ar", "16000", "-c:a", "aac", "-b:a", "48k",
			"-f", "adts", "pipe:1")
		var out bytes.Buffer
		cmd.Stdout = &out
		if cmd.Run() == nil && out.Len() > minClipBytes {
			return out.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("couldn't cut the clip")
}

// minClipBytes is more than a few stray frames: an ffmpeg run that found
// no audio for the track writes little or nothing.
const minClipBytes = 1024

// unforgetVoice lets a name the user just gave be learnt again, if it
// was forgotten.
func (a *Recorder) unforgetVoice(name string) {
	stores, err := a.voiceStores()
	if err != nil {
		return
	}
	for _, s := range stores {
		_ = s.Unforget(name)
	}
}

// correctVoiceName, when the user renames a speaker a profile named in a
// saved meeting to someone else, records that the voice isn't the
// profile's person, and drops the voice name from the session (the
// caller saves it).
func (a *Recorder) correctVoiceName(sess *session.Session, label, name string) {
	kept := sess.VoiceNames[:0]
	for _, vn := range sess.VoiceNames {
		if vn.Label != label {
			kept = append(kept, vn)
			continue
		}
		if !diarize.SameName(vn.Name, name) {
			if err := a.rejectVoice(sess, vn, label); err != nil {
				fmt.Printf("session %s: couldn't record a wrong voice name: %v\n", sess.ID, err)
			}
		}
	}
	sess.VoiceNames = kept
}

// dropVoiceDoubt forgets the doubt about a speaker the user just named.
func dropVoiceDoubt(sess *session.Session, label string) {
	kept := sess.VoiceDoubts[:0]
	for _, vd := range sess.VoiceDoubts {
		if vd.Label != label {
			kept = append(kept, vd)
		}
	}
	sess.VoiceDoubts = kept
}

// rejectVoice records that the voice labeled label in sess isn't vn's
// profile's person. Call before relabeling the lines.
func (a *Recorder) rejectVoice(sess *session.Session, vn session.VoiceName, label string) error {
	p, info, err := loadSavedCached(filepath.Join(config.SessionDir(), sess.ID))
	if err != nil {
		return err
	}
	learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(l string) string {
		if l == label {
			return l
		}
		return ""
	}))
	if len(learned) == 0 {
		return fmt.Errorf("no fingerprints for %q", label)
	}
	store, err := a.voiceStore(vn.Model)
	if err != nil {
		return err
	}
	return store.Reject(vn.Profile, sess.ID, sess.CreatedAt, learned[0].Voice)
}

// personLabel is the anonymous part of a label: "Person 3" of
// "Person 3 (Ana)".
var personLabel = regexp.MustCompile(`^Person \d+`)

// NotThem says a speaker a profile named in a saved meeting isn't that
// person: the profile learns it, and the lines lose the name.
func (a *Recorder) NotThem(sessionID, label string) error {
	a.mu.Lock()
	recording := a.currentSess != nil && a.currentSess.ID == sessionID
	a.mu.Unlock()
	if recording {
		return fmt.Errorf("this meeting is still recording: name the speaker in the transcript instead")
	}
	sess, err := a.editable(sessionID)
	if err != nil {
		return err
	}
	var vn *session.VoiceName
	for i := range sess.VoiceNames {
		if sess.VoiceNames[i].Label == label {
			vn = &sess.VoiceNames[i]
		}
	}
	if vn == nil {
		return fmt.Errorf("%q wasn't named by voice", label)
	}
	if err := a.rejectVoice(sess, *vn, label); err != nil {
		return err
	}
	anon := personLabel.FindString(label)
	if anon == "" {
		anon = label
	}
	if vn.Window != "" {
		anon += " (" + vn.Window + ")" // the voice overruled the window: back to the window's name
	}
	for i := range sess.Segments {
		if seg := &sess.Segments[i]; seg.Speaker == label {
			seg.Speaker, seg.VoiceMatch = anon, nil
		}
	}
	kept := sess.VoiceNames[:0]
	for _, x := range sess.VoiceNames {
		if x.Label != label {
			kept = append(kept, x)
		}
	}
	sess.VoiceNames = kept
	return a.store.Save(sess)
}

// SkipVoiceName stops asking about a speaker a profile named in a saved
// meeting when its clip can't be judged (too short, noise, crosstalk).
// The profile learns nothing and the lines keep their name.
func (a *Recorder) SkipVoiceName(sessionID, label string) error {
	sess, err := a.editable(sessionID)
	if err != nil {
		return err
	}
	for i := range sess.VoiceNames {
		if sess.VoiceNames[i].Label == label {
			sess.VoiceNames[i].Skipped = true
			return a.store.Save(sess)
		}
	}
	return fmt.Errorf("%q wasn't named by voice", label)
}

// SkipVoiceDoubt stops asking about a speaker the window and the voice
// disagree on, when its clip can't settle it. The lines keep the
// window's name and teach no profile.
func (a *Recorder) SkipVoiceDoubt(sessionID, label string) error {
	sess, err := a.editable(sessionID)
	if err != nil {
		return err
	}
	for i := range sess.VoiceDoubts {
		if sess.VoiceDoubts[i].Label == label {
			sess.VoiceDoubts[i].Skipped = true
			return a.store.Save(sess)
		}
	}
	return fmt.Errorf("no doubt about %q", label)
}

// RemoveVoiceMeeting takes a meeting out of a profile: what it taught is
// dropped and it's never learnt from again.
func (a *Recorder) RemoveVoiceMeeting(model, id, sessionID string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.ForgetMeeting(id, sessionID)
}

// ConfirmVoiceMeeting records that a meeting's voice is the profile's.
func (a *Recorder) ConfirmVoiceMeeting(model, id, sessionID string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.ConfirmMeeting(id, sessionID)
}

// StartVoiceOver clears what a profile learnt, keeping its name; it learns
// from new meetings only.
func (a *Recorder) StartVoiceOver(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.StartOver(id)
}

// ConfirmVoice says a profile is the person's voice: it names voices
// before it's been heard in enough meetings.
func (a *Recorder) ConfirmVoice(model, id string) error {
	s, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	return s.Confirm(id)
}

// tilesPerMeeting is how many of a person's tiles a profile shows per
// meeting.
const tilesPerMeeting = 2

type sessionTile struct {
	look int
	name string
}

// Kept tiles by session, read from looks.jsonl (cached while it's
// unchanged).
var (
	tilesMu    sync.Mutex
	tilesCache = map[string]tilesEntry{}
)

type tilesEntry struct {
	mod   time.Time
	tiles []sessionTile
}

// tilesOf picks up to n of a session's kept tiles named any of names,
// spread over the meeting.
func tilesOf(sessionID string, names []string, n int) []TileRef {
	dir := filepath.Join(config.SessionDir(), sessionID)
	st, err := os.Stat(filepath.Join(dir, "looks.jsonl"))
	if err != nil {
		return []TileRef{}
	}
	tilesMu.Lock()
	e, ok := tilesCache[dir]
	if !ok || !e.mod.Equal(st.ModTime()) {
		e = tilesEntry{mod: st.ModTime()}
		looks, _ := videohint.ReadLooks(dir)
		for _, l := range looks {
			if l.HasTile || (l.Name != "" && videohint.HasTileFile(dir, l.ID)) {
				e.tiles = append(e.tiles, sessionTile{l.ID, l.Name})
			}
		}
		tilesCache[dir] = e
	}
	tilesMu.Unlock()
	var match []sessionTile
	for _, t := range e.tiles {
		for _, n := range names {
			if diarize.SameName(t.name, n) {
				match = append(match, t)
				break
			}
		}
	}
	out := []TileRef{}
	for i := 0; i < n && i < len(match); i++ {
		// First, then spread evenly to the last.
		j := 0
		if n > 1 {
			j = i * (len(match) - 1) / (n - 1)
		}
		out = append(out, TileRef{Session: sessionID, Look: match[j].look})
	}
	return out
}

// MoveVoiceMeeting says what a profile learnt from a meeting was someone
// else's: it teaches the profile toID instead, or a new profile named
// toName when toID is "" (see voices.Store.Move). A meeting already
// taken out is worked out again from the saved meeting.
func (a *Recorder) MoveVoiceMeeting(model, id, sessionID, toID, toName string) error {
	if toID == "" && toName == "" {
		return fmt.Errorf("a profile or a name is needed")
	}
	store, err := a.voiceStore(model)
	if err != nil {
		return err
	}
	var prof *voices.Profile
	for _, p := range store.Profiles() {
		if p.ID == id {
			prof = &p
			break
		}
	}
	if prof == nil {
		return fmt.Errorf("no voice profile %q", id)
	}
	sess, err := a.loadSession(sessionID)
	if err != nil {
		return err
	}
	for _, h := range prof.Meetings {
		if h.Session == sessionID {
			return store.Move(id, sessionID, sess.CreatedAt, h.Voice, toID, toName)
		}
	}
	dir := filepath.Join(config.SessionDir(), sessionID)
	p, info, err := loadSavedCached(dir)
	if err != nil {
		return err
	}
	names := append([]string{prof.Name}, prof.Aliases...)
	for _, l := range diarize.VoicesFromTiles(p, savedHints(dir, sess, a.config().Meeting.SnapReadsToInvitees), info.Offset) {
		for _, n := range names {
			if diarize.SameName(l.Name, n) {
				return store.Move(id, sessionID, sess.CreatedAt, l.Voice, toID, toName)
			}
		}
	}
	return fmt.Errorf("no voice under %s's tile in that meeting", prof.Name)
}

// DiscardedVoiceClip is a clip of the voice a profile learnt from a
// meeting it no longer learns from, worked out again from the meeting.
func (a *Recorder) DiscardedVoiceClip(model, id, sessionID string) (VoiceClip, error) {
	store, err := a.voiceStore(model)
	if err != nil {
		return VoiceClip{}, err
	}
	var names []string
	for _, p := range store.Profiles() {
		if p.ID == id {
			names = append([]string{p.Name}, p.Aliases...)
		}
	}
	sess, err := a.loadSession(sessionID)
	if err != nil {
		return VoiceClip{}, err
	}
	dir := filepath.Join(config.SessionDir(), sessionID)
	p, info, err := loadSavedCached(dir)
	if err != nil {
		return VoiceClip{}, err
	}
	for _, l := range diarize.VoicesFromTiles(p, savedHints(dir, sess, a.config().Meeting.SnapReadsToInvitees), info.Offset) {
		for _, n := range names {
			if diarize.SameName(l.Name, n) {
				return VoiceClip{Session: sessionID, Start: l.Clip.Start, End: l.Clip.End}, nil
			}
		}
	}
	return VoiceClip{}, fmt.Errorf("no voice under that tile in the meeting")
}
