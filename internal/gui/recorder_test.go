package gui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/item"
	recconfig "github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/store"
)

const (
	sessA = "0b7e2c1a-4f3d-4a9b-8c2e-1d5f6a7b8c9d"
	sessB = "1c8f3d2b-5a4e-4b0c-9d3f-2e6a7b8c9d0e"
)

// fakeRecorder answers the control protocol from a table of results and
// records what it was asked.
type fakeRecorder struct {
	mu      sync.Mutex
	asked   []control.Request
	results map[string]any // op → result; missing: ok with no result
	fail    map[string]string
}

func (f *fakeRecorder) serve(t *testing.T, path string) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					var req control.Request
					if json.Unmarshal(sc.Bytes(), &req) != nil {
						return
					}
					f.mu.Lock()
					f.asked = append(f.asked, req)
					rep := control.Reply{V: control.Version, ID: req.ID, OK: true}
					if msg, ok := f.fail[req.Op]; ok {
						rep.OK, rep.Error = false, msg
					} else if r, ok := f.results[req.Op]; ok {
						rep.Result, _ = json.Marshal(r)
					}
					f.mu.Unlock()
					b, _ := json.Marshal(rep)
					conn.Write(append(b, '\n'))
				}
			}()
		}
	}()
}

func (f *fakeRecorder) ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.asked {
		out = append(out, r.Op)
	}
	return out
}

func (f *fakeRecorder) last() control.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[len(f.asked)-1]
}

// shortDir is a folder whose socket path fits macOS's 104-byte limit.
func shortDir(t *testing.T) string {
	dir := t.TempDir()
	if len(dir) > 80 {
		var err error
		if dir, err = os.MkdirTemp("", "mrec"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	return dir
}

type events struct {
	mu   sync.Mutex
	seen []string
	data []any
}

func (e *events) emit(ev string, d any) {
	e.mu.Lock()
	e.seen = append(e.seen, ev)
	e.data = append(e.data, d)
	e.mu.Unlock()
}

func (e *events) has(ev string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.seen {
		if s == ev {
			return true
		}
	}
	return false
}

// testApp is an App over a config with the recorder's folder in a temp
// dir; ofRecord picks the recorder of record. No store unless asked.
func testApp(t *testing.T, ofRecord string) (*App, *fakeRecorder, *events, string) {
	dir := shortDir(t)
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.TomoeSessions = filepath.Join(dir, "tomoe")
	cfg.Recorder.Data = filepath.Join(dir, "rec")
	cfg.Recorder.OfRecord = ofRecord
	os.MkdirAll(cfg.Recorder.Data, 0o700)
	f := &fakeRecorder{results: map[string]any{}, fail: map[string]string{}}
	ev := &events{}
	a := &App{ctx: context.Background(), eng: &engine.Engine{Cfg: cfg}, cfgPath: filepath.Join(dir, "config.toml"), emitFn: ev.emit}
	return a, f, ev, dir
}

func TestRecorderStateRunningOrNot(t *testing.T) {
	a, f, _, _ := testApp(t, "tomoe")
	v := a.RecorderState()
	if v.Running || v.Error != "" || v.Editable || v.OfRecord != "tomoe" {
		t.Fatalf("no recorder: %+v", v)
	}
	f.results[control.OpStatus] = control.Status{Recording: true, Session: &control.SessionInfo{ID: sessA, Title: "Weekly sync"},
		Silent: &control.Source{Kind: "mic", Name: "Desk mic"}, Suggest: &control.Source{Kind: "mic", ID: "7", Name: "Headset"}}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	v = a.RecorderState()
	if !v.Running || v.Status == nil || !v.Status.Recording || v.Status.Suggest.Name != "Headset" {
		t.Fatalf("running: %+v", v)
	}
	if _, err := a.RecorderSetSource("mic", "7"); err != nil {
		t.Fatal(err)
	}
	var args control.SetSourceArgs
	json.Unmarshal(f.last().Args, &args)
	if f.last().Op != control.OpSetSource || args.ID != "7" || args.Kind != "mic" {
		t.Errorf("asked %s %+v", f.last().Op, args)
	}
	if _, err := a.RecorderSetSource("camera", "1"); err == nil {
		t.Error("an unknown source kind was sent")
	}
}

// While Tomoe is of record, no edit reaches the recorder.
func TestEditsOnlyWhenMusubiRecords(t *testing.T) {
	a, f, _, _ := testApp(t, "tomoe")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.RecorderSpeaker(control.OpRenameSpeaker, sessA, "Person 2", "Kai Moreno"); err == nil {
		t.Error("rename allowed with Tomoe of record")
	}
	if err := a.RecorderVoice(control.OpRenameVoice, control.VoiceArgs{Model: "m", ID: "p1", Name: "Kai"}); err == nil {
		t.Error("voice edit allowed with Tomoe of record")
	}
	if err := a.DeleteMeeting("tomoe:" + sessA); err == nil {
		t.Error("delete allowed with Tomoe of record")
	}
	if ops := f.ops(); len(ops) != 0 {
		t.Errorf("recorder asked %v", ops)
	}
}

func TestSpeakerEditQueuesUpdate(t *testing.T) {
	a, f, ev, _ := testApp(t, "musubi")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	a.updating = true // an update under way: the edit waits for the next
	if err := a.RecorderSpeaker(control.OpNotThem, sessA, "Person 3 (Kai Moreno)", ""); err != nil {
		t.Fatal(err)
	}
	var args control.SpeakerArgs
	json.Unmarshal(f.last().Args, &args)
	if f.last().Op != control.OpNotThem || args.Session != sessA || args.Label != "Person 3 (Kai Moreno)" {
		t.Errorf("asked %s %+v", f.last().Op, args)
	}
	if !a.updateAgain || !ev.has("voices:changed") {
		t.Errorf("update queued %v, events %v", a.updateAgain, ev.seen)
	}
	// The recording in progress: rename only, and nothing to ingest.
	a.updateAgain = false
	if err := a.RecorderSpeaker(control.OpRenameSpeaker, "", "Person 1", "Ana Ruiz"); err != nil {
		t.Fatal(err)
	}
	if a.updateAgain {
		t.Error("an update queued for the recording in progress")
	}
	n := len(f.ops())
	for _, bad := range [][3]string{
		{control.OpSkipVoiceName, "", "Person 1"},       // not on the recording in progress
		{control.OpRenameSpeaker, "../etc", "Person 1"}, // not a session id
		{control.OpRenameSpeaker, sessA, ""},            // no label
		{control.OpDelete, sessA, "Person 1"},           // not a speaker op
		{control.OpRenameSpeaker, strings.ToUpper(sessA), "Person 1"},
	} {
		if err := a.RecorderSpeaker(bad[0], bad[1], bad[2], "Ana Ruiz"); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if err := a.RecorderVoice(control.OpLearnSaved, control.VoiceArgs{Model: "m", ID: "p1"}); err == nil {
		t.Error("learn_saved passed as a voice op")
	}
	if len(f.ops()) != n {
		t.Errorf("refused edits reached the recorder: %v", f.ops()[n:])
	}
	if err := a.RecorderVoice(control.OpMoveVoiceMeeting, control.VoiceArgs{Model: "m", ID: "p1", Session: sessB, Name: " Ana Ruiz "}); err != nil {
		t.Fatal(err)
	}
	var va control.VoiceArgs
	json.Unmarshal(f.last().Args, &va)
	if va.Name != "Ana Ruiz" || va.Session != sessB {
		t.Errorf("move sent %+v", va)
	}
	// To a profile by ID.
	if err := a.RecorderVoice(control.OpMoveVoiceMeeting, control.VoiceArgs{Model: "m", ID: "p1", Session: sessB, To: "0a1b2c3d4e5f6071"}); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(f.last().Args, &va)
	if va.To != "0a1b2c3d4e5f6071" || va.Session != sessB {
		t.Errorf("move by id sent %+v", va)
	}
	n = len(f.ops())
	for _, bad := range []control.VoiceArgs{
		{Model: "m", ID: "p1", Session: sessB},                      // nowhere to move to
		{Model: "m", ID: "p1", Session: sessB, To: "../x"},          // not a profile id
		{Model: "m", ID: "p1", Session: sessB, To: "zz", Name: "x"}, // not a profile id
	} {
		if err := a.RecorderVoice(control.OpMoveVoiceMeeting, bad); err == nil {
			t.Errorf("move %+v accepted", bad)
		}
	}
	if err := a.RecorderVoice(control.OpRenameVoice, control.VoiceArgs{Model: "m", ID: "p1", To: "0a1b", Name: "x"}); err == nil {
		t.Error("to passed with a rename")
	}
	if len(f.ops()) != n {
		t.Errorf("refused moves reached the recorder: %v", f.ops()[n:])
	}
}

func TestRecorderMedia(t *testing.T) {
	a, f, _, _ := testApp(t, "tomoe")
	f.results[control.OpVoiceClip] = control.Media{Type: "text/html", Data: []byte("clip-bytes")}
	f.results[control.OpLookTile] = control.Media{Type: "image/jpeg", Data: []byte("tile-bytes")}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	h := a.AudioHandler()
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	w := get("/recorder-media/clip?session=" + sessA + "&start=12.5&end=20")
	if w.Code != 200 || w.Body.String() != "clip-bytes" || w.Header().Get("Content-Type") != "audio/aac" {
		t.Errorf("clip: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	// The webview's next range request is answered from the cache, not by
	// cutting the clip again.
	asked := len(f.ops())
	if w := get("/recorder-media/clip?session=" + sessA + "&start=12.5&end=20"); w.Code != 200 || w.Body.String() != "clip-bytes" || len(f.ops()) != asked {
		t.Errorf("second ask: %d %q, recorder asked again: %v", w.Code, w.Body.String(), len(f.ops()) != asked)
	}
	if w := get("/recorder-media/tile/" + sessA + "/42"); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Errorf("tile: %d", w.Code)
	}
	var look control.LookArgs
	json.Unmarshal(f.last().Args, &look)
	if look.Look != 42 || look.Session != sessA {
		t.Errorf("tile asked %+v", look)
	}
	n := len(f.ops())
	for _, bad := range []string{
		"/recorder-media/clip?session=../x&start=1&end=2",
		"/recorder-media/clip?session=" + sessA + "&start=5&end=2",
		"/recorder-media/clip?session=" + sessA + "&start=0&end=600",
		"/recorder-media/clip?session=" + sessA + "&start=NaN&end=2",
		"/recorder-media/tile/" + sessA + "/-1",
		"/recorder-media/tile/" + sessA + "/x",
		"/recorder-media/face/" + sessA + "/1",
		"/recorder-media/tile/" + sessA + "/1/2",
	} {
		if w := get(bad); w.Code != 404 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if len(f.ops()) != n {
		t.Errorf("bad requests reached the recorder: %v", f.ops()[n:])
	}
}

func writeSession(t *testing.T, dir, id string) {
	body := `{"id":"` + id + `","created_at":"2026-11-11T10:00:00Z","segments":[
		{"speaker":"You","start_time":0,"end_time":4,"text":"Morning."},
		{"speaker":"Person 2 (Kai Moreno)","start_time":4,"end_time":14,"text":"Shall we start?"},
		{"speaker":"Person 2 (Kai Moreno)","start_time":20,"end_time":25,"text":"Next item."},
		{"speaker":"Person 3","start_time":14,"end_time":15,"text":"draft","status":"live"}]}`
	os.MkdirAll(filepath.Join(dir, id), 0o700)
	if err := os.WriteFile(filepath.Join(dir, id, "session.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMeetingSpeakers(t *testing.T) {
	a, _, _, _ := testApp(t, "musubi")
	writeSession(t, a.eng.Cfg.RecorderSessions(), sessA)
	writeSession(t, a.eng.Cfg.TomoeSessions, sessB)
	m, err := a.MeetingSpeakers("tomoe:" + sessA)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Editable || len(m.Speakers) != 2 || m.Speakers[0].Label != "Person 2 (Kai Moreno)" || m.Speakers[0].Lines != 2 || m.Speakers[0].Seconds != 15 {
		t.Errorf("recorder's meeting: %+v", m)
	}
	// Recorded by Tomoe before the switch: shown, not editable here.
	if m, _ := a.MeetingSpeakers("tomoe:" + sessB); m == nil || m.Editable || m.Why == "" || len(m.Speakers) != 2 {
		t.Errorf("Tomoe's meeting: %+v", m)
	}
	a.eng.Cfg.Recorder.OfRecord = "tomoe"
	if m, _ := a.MeetingSpeakers("tomoe:" + sessB); m == nil || m.Editable {
		t.Errorf("Tomoe of record: %+v", m)
	}
	if m, _ := a.MeetingSpeakers("teams:abc"); m != nil {
		t.Errorf("not a recording: %+v", m)
	}
}

func TestDeleteMeetingForgetsIt(t *testing.T) {
	a, f, ev, dir := testApp(t, "musubi")
	st, err := store.Open(filepath.Join(dir, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a.eng.Store = st
	id := "tomoe:" + sessA
	if _, err := st.UpsertItem(item.Item{ID: id, Source: "tomoe", Ref: sessA, Kind: item.KindMeeting, Title: "Weekly sync", OccurredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	st.PutManifest("tomoe", sessA, "h/t3", "application/x-tomoe-session+json")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())

	f.fail[control.OpDelete] = "refused for the recording in progress"
	if err := a.DeleteMeeting(id); err == nil {
		t.Fatal("a refused delete reported done")
	}
	if gone, _ := st.IsForgotten("tomoe", sessA); gone {
		t.Fatal("forgotten though the recorder refused")
	}
	delete(f.fail, control.OpDelete)
	if err := a.DeleteMeeting(id); err != nil {
		t.Fatal(err)
	}
	var args control.SessionArgs
	json.Unmarshal(f.last().Args, &args)
	if f.last().Op != control.OpDelete || args.Session != sessA {
		t.Errorf("asked %s %+v", f.last().Op, args)
	}
	if gone, _ := st.IsForgotten("tomoe", sessA); !gone {
		t.Error("not forgotten in Musubi")
	}
	if !ev.has("meetings:changed") {
		t.Errorf("events %v", ev.seen)
	}
	// Never imported: deleting still works.
	if err := a.DeleteMeeting("tomoe:" + sessB); err != nil {
		t.Errorf("unimported meeting: %v", err)
	}
	if err := a.DeleteMeeting("teams:abc"); err == nil {
		t.Error("deleted something that isn't a recording")
	}
}

// The recorder's feed is followed whichever recorder is of record; its
// messages become events and are kept for screens opened later.
func TestFollowRecorderFeed(t *testing.T) {
	a, _, ev, _ := testApp(t, "tomoe")
	path := a.eng.Cfg.RecorderLiveSocket()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for _, l := range []string{
			`{"v":1,"type":"hello","app":"musubi-recorder"}`,
			`{"v":1,"type":"status","status":{"recording":true,"mic":{"kind":"mic","name":"Desk mic"}}}`,
			`{"v":1,"type":"audio","status":{"recording":true,"silent":{"kind":"mic","name":"Desk mic"}}}`,
			`{"v":1,"type":"diag","session_id":"` + sessA + `","diag":{"kind":"match","label":"Person 2","detail":"sounds like Kai Moreno"}}`,
			`{"v":1,"type":"look","session_id":"` + sessA + `","look":{"id":3,"stage":"ocr_hit","name":"Kai Moreno","usable":true}}`,
			`{"v":2,"type":"diag","diag":{"kind":"future"}}`,
		} {
			conn.Write([]byte(l + "\n"))
		}
		time.Sleep(200 * time.Millisecond)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.followRecorder(ctx, path)
	deadline := time.Now().Add(3 * time.Second)
	for !ev.has("recorder:look") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{"recorder:conn", "recorder:status", "recorder:audio", "recorder:diag", "recorder:look"} {
		if !ev.has(want) {
			t.Errorf("no %s in %v", want, ev.seen)
		}
	}
	if d := a.RecorderDiags(); len(d) != 1 || d[0].Detail != "sounds like Kai Moreno" {
		t.Errorf("diags %+v", d)
	}
	if l := a.RecorderLiveLooks(); l.Session != sessA || len(l.Looks) != 1 || l.Looks[0].Name != "Kai Moreno" {
		t.Errorf("looks %+v", l)
	}
}

func TestRecordingSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // Tomoe's defaults, not this Mac's config
	a, f, ev, _ := testApp(t, "tomoe")
	// The recorder's folder is the test's, so nothing reaches a real one.
	os.WriteFile(a.cfgPath, []byte("# mine\n[owner]\nname = \"Ana Ruiz\"\n\n[recorder]\ndata = \""+a.eng.Cfg.Recorder.Data+"\"\n"), 0o600)
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.SetRecordingSetting("meeting", "mic_voices", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRecordingSetting("meeting", "speaker_threshold", 0.6); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRecordingSetting("meeting", "diarize_stride", 3.0); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct {
		table, key string
		v          any
	}{
		{"meeting", "mic_voices", "yes"},
		{"meeting", "diarize_stride", 2.5},
		{"meeting", "diarize_stride", 0.0},
		{"transcription", "model", "whisper"},
		{"meeting", "live_feed", true}, // not offered
		{"hotkey", "binding", "x"},
		{"audio", "device", "a\nb"},
	} {
		if err := a.SetRecordingSetting(bad.table, bad.key, bad.v); err == nil {
			t.Errorf("%s.%s = %v accepted", bad.table, bad.key, bad.v)
		}
	}
	b, _ := os.ReadFile(a.cfgPath)
	if !strings.Contains(string(b), "# mine") || !strings.Contains(string(b), "[recorder.meeting]\nmic_voices = false\nspeaker_threshold = 0.6\ndiarize_stride = 3") {
		t.Errorf("config:\n%s", b)
	}
	if got := f.ops(); len(got) != 3 || got[0] != control.OpReloadConfig {
		t.Errorf("recorder asked %v", got)
	}
	if v, _ := a.eng.Cfg.Recorder.Meeting["mic_voices"].(bool); v || !ev.has("recording:changed") {
		t.Errorf("open config not updated: %#v", a.eng.Cfg.Recorder.Meeting)
	}
	rs, err := a.RecordingSettings()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, g := range rs.Groups {
		for _, fl := range g.Fields {
			switch fl.Key {
			case "mic_voices":
				seen++
				if !fl.Set || fl.Value != false {
					t.Errorf("mic_voices %+v", fl)
				}
			case "two_pass":
				seen++
				if fl.Set || fl.Value == nil {
					t.Errorf("unset two_pass %+v", fl)
				}
			}
		}
	}
	if seen != 2 {
		t.Errorf("fields missing: %d", seen)
	}
}

// Every recorder setting offered exists in the recorder's config, with
// the type it is shown as.
func TestRecFieldsExist(t *testing.T) {
	c := recconfig.DefaultConfig()
	for _, g := range append(recGroups, RecGroup{"Processing", recModelFields}) {
		for _, f := range g.Fields {
			v := tomlField(c, f.Table, f.Key)
			ok := false
			switch f.Type {
			case "bool":
				_, ok = v.(bool)
			case "int":
				_, ok = v.(int)
			case "float":
				_, ok = v.(float64)
			case "string", "select":
				_, ok = v.(string)
			}
			if !ok {
				t.Errorf("%s.%s is %T, shown as %s", f.Table, f.Key, v, f.Type)
			}
		}
	}
}

func TestOfRecordEditsSetSince(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 4, 5, 600, time.UTC)
	out, err := config.EditText("", ofRecordEdits("musubi", now))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(path, []byte(out), 0o600)
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.MusubiRecords() || !c.Recorder.Since.Equal(now.Truncate(time.Second)) {
		t.Errorf("%q %v", c.Recorder.OfRecord, c.Recorder.Since)
	}
	if e := ofRecordEdits("tomoe", now); len(e) != 1 {
		t.Errorf("switching back touches since: %+v", e)
	}
}

// The restart that applies start-only settings never cuts a recording.
func TestRestartRecorder(t *testing.T) {
	var calls [][]string
	old := launchctl
	launchctl = func(_ context.Context, args ...string) ([]byte, error) { calls = append(calls, args); return nil, nil }
	t.Cleanup(func() { launchctl = old })
	kicked := func() int {
		n := 0
		for _, c := range calls {
			if c[0] == "kickstart" {
				n++
			}
		}
		return n
	}

	a, f, _, _ := testApp(t, "tomoe")
	if err := a.RestartRecorder(); err != nil || kicked() != 1 {
		t.Fatalf("not running: err %v, calls %v", err, calls)
	}
	f.results[control.OpStatus] = control.Status{Recording: true}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.RestartRecorder(); err == nil || kicked() != 1 {
		t.Fatalf("recording: err %v, calls %v", err, calls)
	}
	f.results[control.OpStatus] = control.Status{Restart: []string{"transcription"}}
	if err := a.RestartRecorder(); err != nil || kicked() != 2 || calls[len(calls)-1][1] != "-k" {
		t.Fatalf("idle: err %v, calls %v", err, calls)
	}
}

// The recorder is told which config file this app uses: a dataset's,
// not the default it would find on its own.
func TestReloadNamesAppConfig(t *testing.T) {
	a, f, _, dir := testApp(t, "tomoe")
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.reloadRecorder(); err != nil {
		t.Fatal(err)
	}
	var args control.ReloadArgs
	json.Unmarshal(f.last().Args, &args)
	if f.last().Op != control.OpReloadConfig || args.MusubiConfig != filepath.Join(dir, "config.toml") {
		t.Errorf("asked %s %+v", f.last().Op, args)
	}
}

// The watchdog leaves a recorder that answers alone, waits out a short
// gap (an install), then starts it at most every watchRetry and warns
// once per outage.
func TestWatchRecorder(t *testing.T) {
	var calls [][]string
	loaded := false
	oldL, oldN := launchctl, notify
	launchctl = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[0] == "print" && !loaded {
			return nil, fmt.Errorf("not loaded")
		}
		return nil, nil
	}
	warnings := 0
	notify = func(string, string) { warnings++ }
	t.Cleanup(func() { launchctl, notify = oldL, oldN })
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o700)
	os.WriteFile(filepath.Join(home, "Library", "LaunchAgents", recorderAgent+".plist"), []byte("<plist/>"), 0o600)

	a, _, ev, _ := testApp(t, "musubi")
	t0 := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	var w RecorderWatch
	warned := false
	step := func(d time.Duration) { w, warned = a.watchStep(context.Background(), w, warned, t0.Add(d)) }

	step(0)
	step(20 * time.Second)
	if len(calls) != 0 {
		t.Fatalf("started within the grace: %v", calls)
	}
	step(40 * time.Second) // down 40 s: not loaded, so bootstrap
	if len(calls) != 2 || calls[1][0] != "bootstrap" || !ev.has("recorder:watch") {
		t.Fatalf("after the grace: %v", calls)
	}
	step(70 * time.Second) // within watchRetry: no second try
	if len(calls) != 2 {
		t.Fatalf("retried too soon: %v", calls)
	}
	step(100 * time.Second) // down 100 s: warned once
	step(110 * time.Second)
	if warnings != 1 {
		t.Fatalf("warnings %d, want 1", warnings)
	}
	loaded = true
	step(3 * time.Minute) // loaded but not answering: kickstart, no -k
	last := calls[len(calls)-1]
	if last[0] != "kickstart" || len(last) != 2 {
		t.Fatalf("loaded: %v", last)
	}
}
