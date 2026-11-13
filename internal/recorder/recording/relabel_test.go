package recording

import (
	"os"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

func relabelSess(id string, speakers ...string) *session.Session {
	s := &session.Session{ID: id, Title: "Sync", CreatedAt: time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)}
	for i, sp := range speakers {
		s.Segments = append(s.Segments, session.Segment{ID: id + "-" + string(rune('a'+i)), Speaker: sp, Text: "line said by " + sp,
			StartTime: float64(i * 10), EndTime: float64(i*10 + 9), Source: "monitor"})
	}
	return s
}

// profileNamed finds a profile by name: the voice store is shared by the
// package's tests.
func profileNamed(t *testing.T, s *voices.Store, name string) string {
	t.Helper()
	for _, p := range s.Profiles() {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no profile %q", name)
	return ""
}

func speakersOf(t *testing.T, c *controlled, id string) []string {
	t.Helper()
	s, err := c.store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, seg := range s.Segments {
		out = append(out, seg.Speaker)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A renamed profile's old name comes off the labels of the meetings it
// identified, and only those, and only the labels; undo puts it back.
func TestRelabelProfileMeetings(t *testing.T) {
	c := newControlled(t)
	model := models.SpeakerModels[0].ID
	store, err := diarize.VoiceStore(c.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	var v voices.Voice
	for range 20 {
		v.Add([]float32{1, 0, 0, 0})
	}
	// Heard in h-1 (one label with the old name) and h-2 (two: a room's
	// tile; the profile's clip is on the second); named by voice in n-1.
	_, _ = store.Learn("h-1", time.Now(), []voices.Learned{{Name: "Corner Tile", Source: voices.FromTile, Voice: v, Clip: voices.Clip{Start: 10, End: 18}}})
	_, _ = store.Learn("h-2", time.Now(), []voices.Learned{{Name: "Corner Tile", Source: voices.FromTile, Voice: v, Clip: voices.Clip{Start: 20, End: 28}}})
	id := profileNamed(t, store, "Corner Tile")
	h1 := relabelSess("h-1", "You", "Person 3 (Corner Tile)", "Person 4 (Bo Example)", "Person 3 (Corner Tile)")
	h2 := relabelSess("h-2", "Person 1 (Corner Tile)", "You", "Person 3 (Corner Tile)")
	n1 := relabelSess("n-1", "Person 5 (Corner Tile)", "You")
	n1.VoiceNames = []session.VoiceName{{Label: "Person 5 (Corner Tile)", Name: "Corner Tile", Profile: id, Model: model, Similarity: 0.8}}
	other := relabelSess("o-1", "Person 2 (Corner Tile)", "You") // not this profile's meeting
	for _, s := range []*session.Session{h1, h2, n1, other} {
		saveSession(t, c.Recorder, s)
	}
	c.must(t, control.OpRenameVoice, control.VoiceArgs{Model: model, ID: id, Name: "Ana Example"}, nil)

	var res control.RelabelResult
	c.must(t, control.OpRelabelProfileMeetings, control.RelabelArgs{Model: model, ID: id}, &res)
	if len(res.Changed) != 3 {
		t.Fatalf("changed = %+v, left = %+v", res.Changed, res.Left)
	}
	if got := speakersOf(t, c, "h-1"); !eq(got, []string{"You", "Person 3 (Ana Example)", "Person 4 (Bo Example)", "Person 3 (Ana Example)"}) {
		t.Errorf("h-1 = %q", got)
	}
	if got := speakersOf(t, c, "h-2"); !eq(got, []string{"Person 1 (Corner Tile)", "You", "Person 3 (Ana Example)"}) {
		t.Errorf("h-2 = %q (the clip is on Person 3)", got)
	}
	if len(res.Left) != 1 || res.Left[0].Session != "h-2" || res.Left[0].Label != "Person 1 (Corner Tile)" {
		t.Errorf("left = %+v", res.Left)
	}
	n1got, _ := c.store.Load("n-1")
	if n1got.Segments[0].Speaker != "Person 5 (Ana Example)" || n1got.VoiceNames[0].Label != "Person 5 (Ana Example)" || n1got.VoiceNames[0].Name != "Ana Example" {
		t.Errorf("n-1 = %+v, voice names %+v", n1got.Segments[0], n1got.VoiceNames)
	}
	if got := speakersOf(t, c, "o-1"); got[0] != "Person 2 (Corner Tile)" {
		t.Errorf("a meeting the profile never identified was relabelled: %q", got)
	}
	h1got, _ := c.store.Load("h-1")
	for i, seg := range h1got.Segments {
		if seg.Text != h1.Segments[i].Text {
			t.Errorf("line %d text changed: %q", i, seg.Text)
		}
	}
	if h1got.Segments[1].LiveSpeaker != "Person 3 (Corner Tile)" {
		t.Errorf("live label = %q, want the old label kept", h1got.Segments[1].LiveSpeaker)
	}
	if st, err := os.Stat(relabelLogPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("relabel log: %v, %v", st, err)
	}

	// Run again: nothing left to do.
	var again control.RelabelResult
	c.must(t, control.OpRelabelProfileMeetings, control.RelabelArgs{Model: model, ID: id}, &again)
	if len(again.Changed) != 0 {
		t.Errorf("second run changed %+v", again.Changed)
	}

	var undo control.RelabelResult
	c.must(t, control.OpUndoRelabel, control.RelabelArgs{Batch: res.Batch}, &undo)
	if len(undo.Changed) != 3 {
		t.Errorf("undo changed %+v, left %+v", undo.Changed, undo.Left)
	}
	if got := speakersOf(t, c, "h-1"); !eq(got, []string{"You", "Person 3 (Corner Tile)", "Person 4 (Bo Example)", "Person 3 (Corner Tile)"}) {
		t.Errorf("h-1 after undo = %q", got)
	}
	n1got, _ = c.store.Load("n-1")
	if n1got.VoiceNames[0].Label != "Person 5 (Corner Tile)" || n1got.VoiceNames[0].Name != "Corner Tile" {
		t.Errorf("n-1 voice names after undo %+v", n1got.VoiceNames)
	}
	h1got, _ = c.store.Load("h-1")
	if h1got.Segments[1].LiveSpeaker != "" {
		t.Errorf("live label after undo = %q", h1got.Segments[1].LiveSpeaker)
	}
	if err := c.call(t, control.OpUndoRelabel, control.RelabelArgs{Batch: res.Batch}, nil); err == nil {
		t.Error("undoing twice gave no error")
	}
	if err := c.call(t, control.OpUndoRelabel, control.RelabelArgs{Batch: "../x"}, nil); err == nil {
		t.Error("a malformed batch gave no error")
	}
}

// A label the user set by hand ("jack@x.example") is a name too.
func TestRelabelPlainLabel(t *testing.T) {
	c := newControlled(t)
	model := models.SpeakerModels[0].ID
	store, err := diarize.VoiceStore(c.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	var v voices.Voice
	for range 20 {
		v.Add([]float32{0, 1, 0, 0})
	}
	_, _ = store.Learn("p-1", time.Now(), []voices.Learned{{Name: "jack@x.example", Source: voices.FromNamed, Voice: v, Clip: voices.Clip{Start: 0, End: 8}}})
	id := profileNamed(t, store, "jack@x.example")
	saveSession(t, c.Recorder, relabelSess("p-1", "jack@x.example", "You", "Person 3 (Laeka Example)"))
	c.must(t, control.OpRenameVoice, control.VoiceArgs{Model: model, ID: id, Name: "Jack Example"}, nil)
	var res control.RelabelResult
	c.must(t, control.OpRelabelProfileMeetings, control.RelabelArgs{Model: model, ID: id}, &res)
	if got := speakersOf(t, c, "p-1"); !eq(got, []string{"Jack Example", "You", "Person 3 (Laeka Example)"}) {
		t.Errorf("p-1 = %q", got)
	}
}
