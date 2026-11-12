package recording

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// The Voices page's review lists (who's this, named by voice, window or
// voice) come from saved sessions. Reading every session file each time
// the page opens took most of a second on three months of meetings, so
// the lists are gathered once, in the background, when the recorder
// starts, and kept in memory (reviewIndex). Each saved session is read
// again when the recorder saves it (saveSession) and when its file or
// fingerprints change on disk (a diarization worker, a migration): the
// Voices op stats the session folders, and reads only what changed. What
// depends on the voice profiles (whom a voice sounds like, whether a
// window's name has a profile) is worked out when the lists are read, from
// fingerprints kept in the index, so a profile edit needs no rebuild.

// reviewIndex is the review lists' sessions, by ID. The zero value is an
// index not built yet.
type reviewIndex struct {
	mu       sync.Mutex
	started  bool
	ready    bool
	done     chan struct{}
	sessions map[string]*sessionReview
	took     time.Duration
}

// sessionReview is what one saved session gives the review lists.
type sessionReview struct {
	title string
	at    time.Time
	// mod and gobMod are session.json's and diarization.gob's when read
	// (gobMod zero without fingerprints).
	mod, gobMod time.Time
	// model is the speaker model of the session's fingerprints.
	model   string
	unknown []unknownEntry
	named   []NamedByVoiceView
	doubts  []doubtEntry
}

// unknownEntry is a nameless voice and its fingerprints (N 0 when the
// session has none), for "sounds like".
type unknownEntry struct {
	view  UnknownVoice
	voice voices.Voice
}

// doubtEntry is a window-or-voice doubt; NoProfile is filled in when read.
type doubtEntry struct {
	view  VoiceDoubtView
	model string
}

// startReviewIndex gathers the review lists in the background, once.
func (a *Recorder) startReviewIndex() {
	ix := &a.review
	ix.mu.Lock()
	if ix.started {
		ix.mu.Unlock()
		return
	}
	ix.started = true
	ix.done = make(chan struct{})
	if ix.sessions == nil {
		ix.sessions = map[string]*sessionReview{}
	}
	ix.mu.Unlock()
	go func() {
		began := time.Now()
		for _, id := range sessionIDs() {
			r := a.readReview(id, nil, nil)
			ix.mu.Lock()
			// A session saved while this ran is in already, and newer.
			if _, ok := ix.sessions[id]; !ok && r != nil {
				ix.sessions[id] = r
			}
			ix.mu.Unlock()
		}
		ix.mu.Lock()
		ix.ready, ix.took = true, time.Since(began)
		close(ix.done)
		n := len(ix.sessions)
		ix.mu.Unlock()
		fmt.Printf("voices: review lists gathered from %d sessions in %v\n", n, ix.took.Round(time.Millisecond))
	}()
}

// waitReviewIndex starts the index if need be and waits until it is
// built, returning how long the build took.
func (a *Recorder) waitReviewIndex() time.Duration {
	a.startReviewIndex()
	<-a.review.done
	a.review.mu.Lock()
	defer a.review.mu.Unlock()
	return a.review.took
}

// sessionIDs are the folders in the sessions folder.
func sessionIDs() []string {
	entries, err := os.ReadDir(config.SessionDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func modTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// saveSession saves a session and takes it into the review lists.
func (a *Recorder) saveSession(sess *session.Session) error {
	if err := a.store.Save(sess); err != nil {
		return err
	}
	a.reindexSession(sess.ID, sess)
	return nil
}

// reindexSession reads one session into the review lists again (from
// sess when given, else from its file), if they have been started.
func (a *Recorder) reindexSession(id string, sess *session.Session) {
	ix := &a.review
	ix.mu.Lock()
	if !ix.started {
		ix.mu.Unlock()
		return // the build, when it runs, reads it
	}
	prev := ix.sessions[id]
	ix.mu.Unlock()
	r := a.readReview(id, sess, prev)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if r == nil {
		delete(ix.sessions, id)
		return
	}
	if cur := ix.sessions[id]; cur == nil || !r.mod.Before(cur.mod) {
		ix.sessions[id] = r
	}
}

// dropReview forgets a deleted session.
func (a *Recorder) dropReview(id string) {
	a.review.mu.Lock()
	delete(a.review.sessions, id)
	a.review.mu.Unlock()
}

// refreshReviews reads again the sessions whose file or fingerprints
// changed on disk since they were read, takes in new ones and drops
// deleted ones. It only stats files when nothing changed.
func (a *Recorder) refreshReviews() {
	ix := &a.review
	ids := sessionIDs()
	type stale struct {
		id   string
		prev *sessionReview
	}
	var todo []stale
	seen := map[string]bool{}
	ix.mu.Lock()
	for _, id := range ids {
		seen[id] = true
		dir := filepath.Join(config.SessionDir(), id)
		cur := ix.sessions[id]
		if cur == nil {
			if !modTime(filepath.Join(dir, "session.json")).IsZero() {
				todo = append(todo, stale{id, nil})
			}
			continue
		}
		if !modTime(filepath.Join(dir, "session.json")).Equal(cur.mod) ||
			(len(cur.unknown) > 0 && !modTime(filepath.Join(dir, "diarization.gob")).Equal(cur.gobMod)) {
			todo = append(todo, stale{id, cur})
		}
	}
	for id := range ix.sessions {
		if !seen[id] {
			delete(ix.sessions, id)
		}
	}
	ix.mu.Unlock()
	for _, s := range todo {
		r := a.readReview(s.id, nil, s.prev)
		ix.mu.Lock()
		if r == nil {
			delete(ix.sessions, s.id)
		} else if cur := ix.sessions[s.id]; cur == nil || !r.mod.Before(cur.mod) {
			ix.sessions[s.id] = r
		}
		ix.mu.Unlock()
	}
}

// reviewTitles maps the sessions the index knows to their titles.
func (a *Recorder) reviewTitles() map[string]string {
	a.review.mu.Lock()
	defer a.review.mu.Unlock()
	out := make(map[string]string, len(a.review.sessions))
	for id, r := range a.review.sessions {
		out[id] = r.title
	}
	return out
}

// readReview is what session id (sess, when the caller has it) gives
// the review lists; nil when it can't be read. Fingerprints of nameless
// voices are taken from prev when the session's fingerprints file is
// unchanged, else read from it.
func (a *Recorder) readReview(id string, sess *session.Session, prev *sessionReview) *sessionReview {
	dir, err := sessionDir(id)
	if err != nil {
		return nil
	}
	mod := modTime(filepath.Join(dir, "session.json"))
	if mod.IsZero() {
		return nil
	}
	if sess == nil {
		if sess, err = a.loadSession(id); err != nil {
			return nil
		}
	}
	r := &sessionReview{title: sess.Title, at: sess.CreatedAt, mod: mod}
	returned := map[string]session.ReturnedVoice{}
	for _, rv := range sess.Returned {
		returned[rv.Label] = rv
	}
	if sess.CreatedAt.Before(time.Now().AddDate(0, 0, -unknownDays)) && len(returned) == 0 {
		return r // older than any list asks about
	}

	type spoke struct {
		seconds float64
		longest session.Segment
	}
	by := map[string]*spoke{}
	byVoice := map[string]session.VoiceName{}
	for _, vn := range sess.VoiceNames {
		byVoice[vn.Label] = vn
	}
	byDoubt := map[string]session.VoiceDoubt{}
	for _, vd := range sess.VoiceDoubts {
		byDoubt[vd.Label] = vd
	}
	for _, seg := range sess.Segments {
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
	labels := make([]string, 0, len(by))
	for l := range by {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, label := range labels {
		sp := by[label]
		start := sp.longest.StartTime
		clip := VoiceClip{Session: sess.ID, Start: start, End: min(sp.longest.EndTime, start+clipSeconds)}
		if vn, ok := byVoice[label]; ok {
			if vn.Skipped || sp.seconds < namedSeconds {
				continue
			}
			r.named = append(r.named, NamedByVoiceView{Session: sess.ID, Title: sess.Title, At: sess.CreatedAt, Label: label, Name: vn.Name, Model: vn.Model, Profile: vn.Profile,
				Similarity: vn.Similarity, Seconds: sp.seconds, Clip: clip, Window: vn.Window})
			continue
		}
		if vd, ok := byDoubt[label]; ok {
			if !vd.Skipped && sp.seconds >= namedSeconds {
				r.doubts = append(r.doubts, doubtEntry{model: vd.Model, view: VoiceDoubtView{Session: sess.ID, Title: sess.Title, At: sess.CreatedAt, Label: label, Window: vd.Window, Name: vd.Name,
					Similarity: vd.Similarity, Seconds: sp.seconds, Clip: clip}})
			}
			continue
		}
		rv, back := returned[label]
		if back && rv.Skipped {
			continue // "Can't tell": not asked again
		}
		if !back && sp.seconds < unknownSeconds {
			continue
		}
		u := UnknownVoice{Session: sess.ID, Title: sess.Title, At: sess.CreatedAt, Label: label, Seconds: sp.seconds, Clip: clip}
		if back {
			u.Returned, u.ReturnedFrom = true, rv.Name
		}
		r.unknown = append(r.unknown, unknownEntry{view: u})
	}
	if len(r.unknown) > 0 {
		a.unknownVoices(dir, sess, r, prev)
	}
	return r
}

// unknownVoices fills in the fingerprints of r's nameless voices, for
// "sounds like": from prev for a voice it had with the same speech and
// an unchanged fingerprints file, else from the file. The file is read
// uncached: the index keeps only each voice's sum.
func (a *Recorder) unknownVoices(dir string, sess *session.Session, r *sessionReview, prev *sessionReview) {
	r.gobMod = modTime(filepath.Join(dir, "diarization.gob"))
	if r.gobMod.IsZero() {
		return
	}
	missing := false
	if prev != nil && prev.gobMod.Equal(r.gobMod) {
		r.model = prev.model
		for i := range r.unknown {
			for _, o := range prev.unknown {
				if o.view.Label == r.unknown[i].view.Label && o.view.Seconds == r.unknown[i].view.Seconds {
					r.unknown[i].voice = o.voice
				}
			}
			if r.unknown[i].voice.N == 0 {
				missing = true
			}
		}
	} else {
		missing = true
	}
	if !missing {
		return
	}
	p, info, err := diarize.LoadSaved(dir)
	if err != nil {
		return
	}
	r.model = info.Model
	for i := range r.unknown {
		if r.unknown[i].voice.N > 0 {
			continue
		}
		label := r.unknown[i].view.Label
		learned := diarize.VoicesFromSpeakers(p, info.Offset, diarize.SpeakerAt(sess.Segments, func(l string) string {
			if l == label {
				return l
			}
			return ""
		}))
		if len(learned) > 0 {
			r.unknown[i].voice = learned[0].Voice
		}
	}
}

// reviewLists are the review lists now, from the index: false while it
// is still being built (it is started if it wasn't).
func (a *Recorder) reviewLists(stores map[string]*voices.Store) ([]UnknownVoice, []NamedByVoiceView, []VoiceDoubtView, bool) {
	a.startReviewIndex()
	a.review.mu.Lock()
	ready := a.review.ready
	a.review.mu.Unlock()
	if !ready {
		return []UnknownVoice{}, []NamedByVoiceView{}, []VoiceDoubtView{}, false
	}
	a.refreshReviews()
	a.review.mu.Lock()
	sessions := make([]*sessionReview, 0, len(a.review.sessions))
	for _, r := range a.review.sessions {
		sessions = append(sessions, r)
	}
	a.review.mu.Unlock()
	u, n, d := assembleReviews(sessions, stores, time.Now())
	return u, n, d, true
}

// assembleReviews puts the sessions' review items together as the
// Voices page shows them: who's this from the last unknownDays (and any
// voice taken out of a profile, whatever its age), named by voice and
// window or voice from the last reviewDays, newest first, then longest.
func assembleReviews(sessions []*sessionReview, stores map[string]*voices.Store, now time.Time) ([]UnknownVoice, []NamedByVoiceView, []VoiceDoubtView) {
	unknown, named, doubts := []UnknownVoice{}, []NamedByVoiceView{}, []VoiceDoubtView{}
	unknownSince, reviewSince := now.AddDate(0, 0, -unknownDays), now.AddDate(0, 0, -reviewDays)
	for _, r := range sessions {
		recent := !r.at.Before(reviewSince)
		if recent {
			named = append(named, r.named...)
			for _, d := range r.doubts {
				v := d.view
				s := stores[d.model]
				v.NoProfile = s == nil || !s.Knows(v.Window)
				doubts = append(doubts, v)
			}
		}
		for _, e := range r.unknown {
			if r.at.Before(unknownSince) && !e.view.Returned {
				continue
			}
			v := e.view
			if s := stores[r.model]; s != nil && e.voice.N > 0 {
				if prof, sim := s.Closest(e.voice); prof != nil && sim >= suggestAbove {
					v.SoundsLike, v.Similarity = prof.Name, sim
				}
			}
			unknown = append(unknown, v)
		}
	}
	newestFirst := func(ai, bi time.Time, as, bs float64) bool {
		if !ai.Equal(bi) {
			return ai.After(bi)
		}
		return as > bs
	}
	sort.SliceStable(unknown, func(i, j int) bool {
		if unknown[i].At.Equal(unknown[j].At) && unknown[i].Seconds == unknown[j].Seconds {
			return unknown[i].Session+unknown[i].Label < unknown[j].Session+unknown[j].Label
		}
		return newestFirst(unknown[i].At, unknown[j].At, unknown[i].Seconds, unknown[j].Seconds)
	})
	sort.SliceStable(named, func(i, j int) bool {
		if named[i].At.Equal(named[j].At) && named[i].Seconds == named[j].Seconds {
			return named[i].Session+named[i].Label < named[j].Session+named[j].Label
		}
		return newestFirst(named[i].At, named[j].At, named[i].Seconds, named[j].Seconds)
	})
	sort.SliceStable(doubts, func(i, j int) bool {
		if doubts[i].At.Equal(doubts[j].At) && doubts[i].Seconds == doubts[j].Seconds {
			return doubts[i].Session+doubts[i].Label < doubts[j].Session+doubts[j].Label
		}
		return newestFirst(doubts[i].At, doubts[j].At, doubts[i].Seconds, doubts[j].Seconds)
	})
	return unknown, named, doubts
}

// freshReviewLists builds the lists from the session files now, ignoring
// the index: what the index must equal.
func (a *Recorder) freshReviewLists(stores map[string]*voices.Store, now time.Time) ([]UnknownVoice, []NamedByVoiceView, []VoiceDoubtView) {
	var sessions []*sessionReview
	for _, id := range sessionIDs() {
		if r := a.readReview(id, nil, nil); r != nil {
			sessions = append(sessions, r)
		}
	}
	return assembleReviews(sessions, stores, now)
}
