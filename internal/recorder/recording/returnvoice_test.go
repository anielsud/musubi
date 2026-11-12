package recording

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// spokenSess is a session created ago, each speaker saying one line of
// secs seconds in turn.
func spokenSess(id string, ago time.Duration, secs float64, speakers ...string) *session.Session {
	s := &session.Session{ID: id, Title: "Review " + id, CreatedAt: time.Now().Add(-ago).Truncate(time.Second)}
	t := 0.0
	for i, sp := range speakers {
		s.Segments = append(s.Segments, session.Segment{ID: id + "-" + string(rune('a'+i)), Speaker: sp, Text: "line by " + sp,
			StartTime: t, EndTime: t + secs, Source: "monitor"})
		t += secs
	}
	return s
}

func axisVoice(axis int) voices.Voice {
	var v voices.Voice
	emb := make([]float32, 4)
	emb[axis] = 1
	for range 20 {
		v.Add(emb)
	}
	return v
}

func unknownOf(v control.VoicesView, sessionID, label string) *control.UnknownVoice {
	for i := range v.Unknown {
		if v.Unknown[i].Session == sessionID && v.Unknown[i].Label == label {
			return &v.Unknown[i]
		}
	}
	return nil
}

// "Not them" on a profile's meeting takes the meeting out of the profile
// and puts that meeting's labels the profile named back to unnamed, and
// only those; the voice is then asked about under "Who's this?" however
// old or short; "Can't tell" drops it, naming it ends the mark.
func TestReturnVoiceMeeting(t *testing.T) {
	c := newControlled(t)
	model := models.SpeakerModels[0].ID
	store, err := diarize.VoiceStore(c.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.Learn("rv-1", time.Now(), []voices.Learned{{Name: "Rhea Example", Source: voices.FromTile, Voice: axisVoice(0), Clip: voices.Clip{Start: 0, End: 5}}})
	_, _ = store.Learn("rv-2", time.Now(), []voices.Learned{{Name: "Rhea Example", Source: voices.FromTile, Voice: axisVoice(0), Clip: voices.Clip{Start: 0, End: 5}}})
	id := profileNamed(t, store, "Rhea Example")

	// rv-1: 120 days old, the profile named Person 3 by voice, for 5 s.
	old := spokenSess("rv-1", 120*24*time.Hour, 5, "You", "Person 3 (Rhea Example)", "Person 4 (Bo Example)", "Person 3 (Rhea Example)")
	old.VoiceNames = []session.VoiceName{{Label: "Person 3 (Rhea Example)", Name: "Rhea Example", Profile: id, Model: model, Similarity: 0.7}}
	old.Segments[1].VoiceMatch = &session.VoiceMatch{Profile: id, Similarity: 0.7}
	// rv-2: the profile's other meeting, by tile; untouched.
	other := spokenSess("rv-2", time.Hour, 40, "Person 2 (Rhea Example)", "You")
	for _, s := range []*session.Session{old, other} {
		saveSession(t, c.Recorder, s)
	}
	c.waitReviewIndex()

	// A preview changes nothing.
	var pre control.ReturnResult
	c.must(t, control.OpReturnVoiceMeeting, control.ReturnArgs{Model: model, ID: id, Session: "rv-1", Relabel: true, Preview: true}, &pre)
	var preSess session.Session
	if err := json.Unmarshal(pre.Session, &preSess); err != nil || preSess.Segments[1].Speaker != "Person 3" {
		t.Fatalf("preview session: %v, %+v", err, preSess.Segments)
	}
	if got := speakersOf(t, c, "rv-1"); got[1] != "Person 3 (Rhea Example)" {
		t.Errorf("a preview relabelled: %q", got)
	}
	for _, p := range store.Profiles() {
		if p.ID == id && len(p.Meetings) != 2 {
			t.Errorf("a preview took a meeting out: %d left", len(p.Meetings))
		}
	}

	var res control.ReturnResult
	c.must(t, control.OpReturnVoiceMeeting, control.ReturnArgs{Model: model, ID: id, Session: "rv-1", Relabel: true}, &res)
	if len(res.Changed) != 1 || res.Changed[0].NewLabel != "Person 3" || res.Changed[0].Lines != 2 || res.Batch == "" {
		t.Fatalf("changed = %+v, left = %+v", res.Changed, res.Left)
	}
	if got := speakersOf(t, c, "rv-1"); !eq(got, []string{"You", "Person 3", "Person 4 (Bo Example)", "Person 3"}) {
		t.Errorf("rv-1 = %q", got)
	}
	if got := speakersOf(t, c, "rv-2"); got[0] != "Person 2 (Rhea Example)" {
		t.Errorf("the profile's other meeting was relabelled: %q", got)
	}
	got, _ := c.store.Load("rv-1")
	if len(got.VoiceNames) != 0 || got.Segments[1].VoiceMatch != nil || len(got.Returned) != 1 || got.Returned[0].Label != "Person 3" || got.Returned[0].Name != "Rhea Example" {
		t.Errorf("rv-1 voice names %+v, match %+v, returned %+v", got.VoiceNames, got.Segments[1].VoiceMatch, got.Returned)
	}
	for _, p := range store.Profiles() {
		if p.ID != id {
			continue
		}
		if len(p.Meetings) != 1 || p.Meetings[0].Session != "rv-2" || len(p.Excluded) != 1 || p.Excluded[0] != "rv-1" {
			t.Errorf("profile meetings %+v, excluded %q", p.Meetings, p.Excluded)
		}
	}

	var v control.VoicesView
	c.must(t, control.OpVoices, nil, &v)
	u := unknownOf(v, "rv-1", "Person 3")
	if u == nil || !u.Returned || u.ReturnedFrom != "Rhea Example" {
		t.Fatalf("returned voice under who's this: %+v (unknown %+v)", u, v.Unknown)
	}

	// Named there: the mark goes with the old label.
	c.must(t, control.OpRenameSpeaker, control.SpeakerArgs{Session: "rv-1", Label: "Person 3", Name: "Cleo Example"}, nil)
	got, _ = c.store.Load("rv-1")
	if len(got.Returned) != 0 {
		t.Errorf("returned after naming: %+v", got.Returned)
	}
}

// A label whose "Person N" another speaker has gets a new number, so two
// speakers never become one; "Can't tell" stops asking; without Relabel
// the label stays and the result says so.
func TestReturnVoiceCollisionSkipAndNoRelabel(t *testing.T) {
	c := newControlled(t)
	model := models.SpeakerModels[0].ID
	store, err := diarize.VoiceStore(c.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.Learn("rc-1", time.Now(), []voices.Learned{{Name: "Ivo Example", Source: voices.FromTile, Voice: axisVoice(1)}})
	_, _ = store.Learn("rc-2", time.Now(), []voices.Learned{{Name: "Ivo Example", Source: voices.FromTile, Voice: axisVoice(1)}})
	id := profileNamed(t, store, "Ivo Example")
	// Learnt from the tile: the label carrying its name is the one.
	saveSession(t, c.Recorder, spokenSess("rc-1", time.Hour, 10, "Person 2 (Ivo Example)", "Person 2", "You"))
	saveSession(t, c.Recorder, spokenSess("rc-2", time.Hour, 10, "Person 1 (Ivo Example)", "You"))

	var res control.ReturnResult
	c.must(t, control.OpReturnVoiceMeeting, control.ReturnArgs{Model: model, ID: id, Session: "rc-1", Relabel: true}, &res)
	if got := speakersOf(t, c, "rc-1"); !eq(got, []string{"Person 3", "Person 2", "You"}) {
		t.Errorf("rc-1 = %q (changed %+v)", got, res.Changed)
	}
	var v control.VoicesView
	c.waitReviewIndex()
	c.must(t, control.OpVoices, nil, &v)
	if unknownOf(v, "rc-1", "Person 3") == nil {
		t.Fatalf("10 s returned voice not asked about: %+v", v.Unknown)
	}
	c.must(t, control.OpSkipReturnedVoice, control.SpeakerArgs{Session: "rc-1", Label: "Person 3"}, nil)
	c.must(t, control.OpVoices, nil, &v)
	if unknownOf(v, "rc-1", "Person 3") != nil {
		t.Errorf("still asked after can't tell: %+v", v.Unknown)
	}
	if err := c.call(t, control.OpSkipReturnedVoice, control.SpeakerArgs{Session: "rc-1", Label: "Person 2"}, nil); err == nil {
		t.Error("skipping a voice never returned gave no error")
	}

	// Without Relabel: out of the profile, label kept, said so.
	c.must(t, control.OpReturnVoiceMeeting, control.ReturnArgs{Model: model, ID: id, Session: "rc-2"}, &res)
	if got := speakersOf(t, c, "rc-2"); got[0] != "Person 1 (Ivo Example)" {
		t.Errorf("rc-2 relabelled without relabel: %q", got)
	}
	if len(res.Changed) != 0 || len(res.Left) != 1 || res.Left[0].Label != "Person 1 (Ivo Example)" {
		t.Errorf("no-relabel result %+v", res)
	}
	for _, p := range store.Profiles() {
		if p.ID == id && len(p.Meetings) != 0 {
			t.Errorf("meetings left in the profile: %+v", p.Meetings)
		}
	}
}

// The index equals a fresh read of the session files after a save, an
// answer, a relabel, a profile change, and a session written behind the
// recorder's back.
func TestReviewIndexMatchesRebuild(t *testing.T) {
	c := newControlled(t)
	model := models.SpeakerModels[0].ID
	stores := map[string]*voices.Store{}
	s, err := voices.Open(filepath.Join(t.TempDir(), "v.json"), diarize.SameName, voices.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	stores[model] = s

	a := spokenSess("ix-1", 2*time.Hour, 40, "Person 2", "You", "Person 3 (Dee Example)")
	a.VoiceNames = []session.VoiceName{{Label: "Person 3 (Dee Example)", Name: "Dee Example", Profile: "abc", Model: model, Similarity: 0.8}}
	b := spokenSess("ix-2", 3*time.Hour, 40, "Person 5 (Cy Example)", "Person 6")
	b.VoiceDoubts = []session.VoiceDoubt{{Label: "Person 5 (Cy Example)", Window: "Cy Example", Name: "Dee Example", Model: model, Similarity: 0.55}}
	saveSession(t, c.Recorder, a)
	saveSession(t, c.Recorder, b)
	c.waitReviewIndex()

	check := func(step string) {
		t.Helper()
		u, n, d, ready := c.reviewLists(stores)
		fu, fn, fd := c.freshReviewLists(stores, time.Now())
		if !ready || !reflect.DeepEqual(u, fu) || !reflect.DeepEqual(n, fn) || !reflect.DeepEqual(d, fd) {
			t.Errorf("%s: index\n %+v\n %+v\n %+v\nfresh\n %+v\n %+v\n %+v", step, u, n, d, fu, fn, fd)
		}
	}
	check("built")
	u, n, d, _ := c.reviewLists(stores)
	if len(u) != 2 || len(n) != 1 || len(d) != 1 || !d[0].NoProfile {
		t.Fatalf("built: unknown %+v, named %+v, doubts %+v", u, n, d)
	}

	saveSession(t, c.Recorder, spokenSess("ix-3", time.Hour, 45, "Person 1", "You"))
	check("save")
	c.must(t, control.OpSkipVoiceName, control.SpeakerArgs{Session: "ix-1", Label: "Person 3 (Dee Example)"}, nil)
	check("answer")
	c.must(t, control.OpRenameSpeaker, control.SpeakerArgs{Session: "ix-1", Label: "Person 2", Name: "Eli Example"}, nil)
	check("rename")

	// A profile change: Cy gets a profile in use, so the doubt has one.
	_, _ = s.Learn("ix-2", time.Now(), []voices.Learned{{Name: "Cy Example", Source: voices.FromTile, Voice: axisVoice(2)}})
	if err := s.Confirm(profileNamed(t, s, "Cy Example")); err != nil {
		t.Fatal(err)
	}
	check("profile")
	if _, _, d, _ := c.reviewLists(stores); len(d) != 1 || d[0].NoProfile {
		t.Errorf("after the profile: doubts %+v", d)
	}

	// Written behind the recorder's back (a diarization worker): seen on
	// the next read. The mtime must move.
	time.Sleep(10 * time.Millisecond)
	ext := spokenSess("ix-2", 3*time.Hour, 40, "Person 5 (Cy Example)", "Person 7")
	if err := c.store.Save(ext); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Save(spokenSess("ix-4", time.Hour, 50, "Person 9")); err != nil {
		t.Fatal(err)
	}
	check("external")
	if err := c.deleteSession("ix-4"); err != nil {
		t.Fatal(err)
	}
	check("delete")
}

// "Who's this?" covers 90 days; voice-named speakers 14.
func TestWhosThisWindow(t *testing.T) {
	c := newControlled(t)
	day := 24 * time.Hour
	saveSession(t, c.Recorder, spokenSess("w-60", 60*day, 40, "Person 2", "You"))
	saveSession(t, c.Recorder, spokenSess("w-100", 100*day, 40, "Person 2", "You"))
	saveSession(t, c.Recorder, spokenSess("w-short", day, 20, "Person 2", "You"))
	named := spokenSess("w-20", 20*day, 40, "Person 4 (Fay Example)")
	named.VoiceNames = []session.VoiceName{{Label: "Person 4 (Fay Example)", Name: "Fay Example", Profile: "abc", Model: "m"}}
	saveSession(t, c.Recorder, named)
	c.waitReviewIndex()
	var v control.VoicesView
	c.must(t, control.OpVoices, nil, &v)
	if v.Building {
		t.Fatal("still building after the wait")
	}
	if unknownOf(v, "w-60", "Person 2") == nil {
		t.Errorf("a 60-day-old voice isn't asked about: %+v", v.Unknown)
	}
	if unknownOf(v, "w-100", "Person 2") != nil || unknownOf(v, "w-short", "Person 2") != nil {
		t.Errorf("too old or too short asked about: %+v", v.Unknown)
	}
	if len(v.NamedByVoice) != 0 {
		t.Errorf("a 20-day-old voice name asked about: %+v", v.NamedByVoice)
	}
}

// Before the index is built the Voices op says so, with empty lists.
func TestVoicesBuilding(t *testing.T) {
	c := newControlled(t)
	c.review.mu.Lock()
	c.review.started, c.review.done, c.review.sessions = true, make(chan struct{}), map[string]*sessionReview{}
	c.review.mu.Unlock()
	var v control.VoicesView
	c.must(t, control.OpVoices, nil, &v)
	if !v.Building || v.Unknown == nil || len(v.Unknown) != 0 {
		t.Errorf("voices while building = %+v", v)
	}
}
