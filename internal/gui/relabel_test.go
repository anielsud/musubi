package gui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/pipeline"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/store"
)

func writeRecSession(t *testing.T, dir, id string, speakers ...string) {
	t.Helper()
	var segs []map[string]any
	for i, sp := range speakers {
		segs = append(segs, map[string]any{"speaker": sp, "text": "words number " + string(rune('a'+i)), "start_time": float64(i * 5), "end_time": float64(i*5 + 4)})
	}
	b, _ := json.Marshal(map[string]any{"id": id, "title": "Weekly sync", "created_at": "2026-03-04T10:00:00Z", "duration": 60.0, "segments": segs})
	os.MkdirAll(filepath.Join(dir, id), 0o755)
	if err := os.WriteFile(filepath.Join(dir, id, "session.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func itemSegments(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	it, _, err := st.RelabelItem(id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(it.Segments)
	return string(b)
}

// A profile's relabel asks the recorder, then relabels Musubi's copy; undo
// reverses both; the switch and a running update refuse it; a speaker
// renamed on a saved meeting is relabelled, not imported again.
func TestRelabelProfileMeetings(t *testing.T) {
	a, f, ev, dir := testApp(t, "musubi")
	st, err := store.Open(filepath.Join(dir, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a.eng.Store = st
	sessions := a.eng.Cfg.RecorderSessions()
	writeRecSession(t, sessions, sessA, "Person 3 (Corner Tile)", "You", "Person 3 (Corner Tile)")
	r, err := a.eng.Runner(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s pipeline.Stats
	if err := r.Ingest(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if err := r.Process(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	const batch = "0123456789abcdef"
	f.results[control.OpRelabelProfileMeetings] = control.RelabelResult{Batch: batch, Changed: []control.RelabelChange{{Session: sessA, OldLabel: "Person 3 (Corner Tile)", NewLabel: "Person 3 (Ana Ruiz)", Lines: 2}}}
	f.results[control.OpUndoRelabel] = control.RelabelResult{Batch: batch}
	// What the recorder does to the file.
	writeRecSession(t, sessions, sessA, "Person 3 (Ana Ruiz)", "You", "Person 3 (Ana Ruiz)")

	a.eng.Cfg.Recorder.Relabel = false
	if _, err := a.RelabelProfileMeetings("eres2net-en", "0a1b2c3d4e5f6071"); err == nil {
		t.Error("relabel ran with [recorder] relabel off")
	}
	a.eng.Cfg.Recorder.Relabel = true
	a.updating = true
	if _, err := a.RelabelProfileMeetings("eres2net-en", "0a1b2c3d4e5f6071"); err == nil {
		t.Error("relabel ran during an update")
	}
	a.updating = false
	if _, err := a.RelabelProfileMeetings("eres2net-en", "../x"); err == nil {
		t.Error("a malformed profile id was sent")
	}
	if len(f.ops()) != 0 {
		t.Fatalf("refused relabels reached the recorder: %v", f.ops())
	}
	res, err := a.RelabelProfileMeetings("eres2net-en", "0a1b2c3d4e5f6071")
	if err != nil {
		t.Fatal(err)
	}
	if f.last().Op != control.OpRelabelProfileMeetings || len(res.Meetings) != 1 || res.Meetings[0].Lines != 2 || len(res.Skipped) != 0 {
		t.Fatalf("result %+v, skipped %v", res, res.Skipped)
	}
	if segs := itemSegments(t, st, "tomoe:"+sessA); strings.Contains(segs, "Corner Tile") || !strings.Contains(segs, "Ana Ruiz") {
		t.Errorf("item after the relabel: %s", segs)
	}
	if a.updating || !ev.has("meetings:changed") {
		t.Errorf("updating %v, events %v", a.updating, ev.seen)
	}

	if _, err := a.UndoRelabel("not-a-batch"); err == nil {
		t.Error("a malformed batch was accepted")
	}
	writeRecSession(t, sessions, sessA, "Person 3 (Corner Tile)", "You", "Person 3 (Corner Tile)")
	if _, err := a.UndoRelabel(batch); err != nil {
		t.Fatal(err)
	}
	if f.last().Op != control.OpUndoRelabel {
		t.Errorf("undo asked %s", f.last().Op)
	}
	if segs := itemSegments(t, st, "tomoe:"+sessA); !strings.Contains(segs, "Corner Tile") {
		t.Errorf("item after the undo: %s", segs)
	}

	// A speaker renamed on the saved meeting: relabelled, no import queued.
	writeRecSession(t, sessions, sessA, "Ana Ruiz", "You", "Ana Ruiz")
	a.updateAgain = false
	if err := a.RecorderSpeaker(control.OpRenameSpeaker, sessA, "Person 3 (Corner Tile)", "Ana Ruiz"); err != nil {
		t.Fatal(err)
	}
	if segs := itemSegments(t, st, "tomoe:"+sessA); !strings.Contains(segs, `"speaker":"Ana Ruiz"`) {
		t.Errorf("item after the rename: %s", segs)
	}
}

// "Not them" relabels the meeting's profile-named speakers back to
// unnamed in the recorder and in Musubi's copy when Musubi can take it in
// without re-extracting; otherwise the recorder only takes the meeting
// out of the profile and the result says why the labels stayed.
func TestReturnVoiceMeeting(t *testing.T) {
	a, f, _, dir := testApp(t, "musubi")
	st, err := store.Open(filepath.Join(dir, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a.eng.Store = st
	sessions := a.eng.Cfg.RecorderSessions()
	writeRecSession(t, sessions, sessA, "Person 3 (Corner Tile)", "You", "Person 3 (Corner Tile)")
	r, err := a.eng.Runner(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s pipeline.Stats
	if err := r.Ingest(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if err := r.Process(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	const profile = "0a1b2c3d4e5f6071"
	// The session as the recorder would write it.
	writeRecSession(t, sessions, sessA, "Person 3", "You", "Person 3")
	rewritten, err := os.ReadFile(filepath.Join(sessions, sessA, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeRecSession(t, sessions, sessA, "Person 3 (Corner Tile)", "You", "Person 3 (Corner Tile)")
	f.results[control.OpReturnVoiceMeeting] = control.ReturnResult{Batch: "0123456789abcdef", Session: rewritten,
		Changed: []control.RelabelChange{{Session: sessA, OldLabel: "Person 3 (Corner Tile)", NewLabel: "Person 3", Lines: 2}}}

	if _, err := a.ReturnVoiceMeeting("eres2net-en", profile, "../x"); err == nil {
		t.Error("a malformed session was sent")
	}
	if len(f.ops()) != 0 {
		t.Fatalf("refused calls reached the recorder: %v", f.ops())
	}

	// Refused: the "rewrite" changes a line's words, not only a label.
	changed := strings.Replace(string(rewritten), "words number a", "other words", 1)
	f.results[control.OpReturnVoiceMeeting] = control.ReturnResult{Session: json.RawMessage(changed),
		Changed: []control.RelabelChange{{Session: sessA, OldLabel: "Person 3 (Corner Tile)", NewLabel: "Person 3", Lines: 2}}}
	res, err := a.ReturnVoiceMeeting("eres2net-en", profile, sessA)
	if err != nil {
		t.Fatal(err)
	}
	if res.Relabelled || res.Why == "" || strings.Contains(string(f.last().Args), `"relabel":true`) {
		t.Errorf("refused: %+v, last args %s", res, f.last().Args)
	}
	if segs := itemSegments(t, st, "tomoe:"+sessA); !strings.Contains(segs, "Corner Tile") {
		t.Errorf("item after a refused relabel: %s", segs)
	}

	// Accepted.
	f.results[control.OpReturnVoiceMeeting] = control.ReturnResult{Batch: "0123456789abcdef", Session: rewritten,
		Changed: []control.RelabelChange{{Session: sessA, OldLabel: "Person 3 (Corner Tile)", NewLabel: "Person 3", Lines: 2}}}
	writeRecSession(t, sessions, sessA, "Person 3", "You", "Person 3") // what the recorder does
	n := len(f.ops())
	res, err = a.ReturnVoiceMeeting("eres2net-en", profile, sessA)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Relabelled || res.Why != "" || res.Recorder.Session != nil || len(f.ops()) != n+2 || !strings.Contains(string(f.last().Args), `"relabel":true`) {
		t.Errorf("accepted: %+v, ops %v, last args %s", res, f.ops()[n:], f.last().Args)
	}
	if segs := itemSegments(t, st, "tomoe:"+sessA); strings.Contains(segs, "Corner Tile") || !strings.Contains(segs, `"speaker":"Person 3"`) {
		t.Errorf("item after the relabel: %s", segs)
	}
	if a.updating {
		t.Error("left updating")
	}
}
