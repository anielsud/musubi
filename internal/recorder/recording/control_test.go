package recording

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// shortDir is a short temporary folder for sockets (macOS limits their
// paths to about 100 bytes, which t.TempDir can pass).
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// controlled is a fake recorder (no devices, no models) behind a real
// control socket, with the sources each recording began with.
type controlled struct {
	*fakeRecorder
	client *control.Client
	mu     sync.Mutex
	began  [][2]string // mic, system
}

func newControlled(t *testing.T) *controlled {
	t.Helper()
	c := &controlled{fakeRecorder: newFakeRecorder(t)}
	n := 0
	c.begin = func(mic, system, lang, platform string) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		n++
		c.began = append(c.began, [2]string{mic, system})
		c.currentSess = &session.Session{ID: fmt.Sprintf("rec-%d", n), Platform: platform, CreatedAt: time.Now()}
		c.rec = &activeRecording{}
		return nil
	}
	c.probes = sourceProbes{
		mics:          func(string) []control.Source { return nil },
		listenMic:     func(context.Context, string, func()) []float32 { return nil },
		hearSystem:    func(context.Context) bool { return false },
		systemInstead: func(string) control.Source { return control.Source{} },
		defaultMic:    func() string { return "Desk Speakerphone" },
	}
	srv, err := control.Listen(control.SocketPath(shortDir(t)), c.Handle)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	c.client = control.NewClient(srv.Path())
	c.client.Timeout = 10 * time.Second
	return c
}

func (c *controlled) call(t *testing.T, op string, args, out any) error {
	t.Helper()
	return c.client.Call(context.Background(), op, args, out)
}

func (c *controlled) must(t *testing.T, op string, args, out any) {
	t.Helper()
	if err := c.call(t, op, args, out); err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// saveSession writes a synthetic saved session.
func saveSession(t *testing.T, a *Recorder, sess *session.Session) {
	t.Helper()
	if err := a.store.Save(sess); err != nil {
		t.Fatal(err)
	}
}

func synthetic(id string) *session.Session {
	return &session.Session{
		ID: id, Title: "Planning", CreatedAt: time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC), Sources: []string{"mic", "monitor"},
		Segments: []session.Segment{
			{ID: "seg-1", Speaker: "You", Text: "Shall we start?", StartTime: 0, EndTime: 2, Source: "mic"},
			{ID: "seg-2", Speaker: "Person 2", Text: "Yes, the numbers first.", StartTime: 2, EndTime: 5, Source: "monitor"},
			{ID: "seg-3", Speaker: "Person 2", Text: "Then the plan.", StartTime: 5, EndTime: 7, Source: "monitor"},
		},
	}
}

func TestControlStartStopAndStatus(t *testing.T) {
	c := newControlled(t)
	var st control.Status
	c.must(t, control.OpStatus, nil, &st)
	if st.Recording || st.Mic.ID != "" || st.Mic.Name != "Desk Speakerphone (system default)" || st.Version != Version {
		t.Errorf("idle status = %+v", st)
	}

	c.must(t, control.OpStart, control.StartArgs{Platform: "Zoom"}, &st)
	if !st.Recording || st.Session == nil || st.Session.ID != "rec-1" || st.Session.Platform != "Zoom" {
		t.Errorf("status after start = %+v", st)
	}
	if err := c.call(t, control.OpStart, nil, nil); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("second start: %v", err)
	}
	c.must(t, control.OpStop, nil, &st)
	if st.Recording {
		t.Errorf("status after stop = %+v", st)
	}
	if err := c.call(t, control.OpStop, nil, nil); err == nil {
		t.Error("stop while idle gave no error")
	}
	c.Shutdown()
	if ids := c.savedIDs(); fmt.Sprint(ids) != "[rec-1]" {
		t.Errorf("saved %v", ids)
	}
	// The configured sources: the default mic and, on macOS, the
	// automatic system source.
	if mic := c.began[0][0]; mic != "default" {
		t.Errorf("began with mic %q", mic)
	}
}

// A manual start's sources win; "none" is no mic.
func TestControlStartWithSources(t *testing.T) {
	c := newControlled(t)
	c.must(t, control.OpStart, control.StartArgs{Mic: "none", System: "everything"}, nil)
	if got := c.began[0]; got != [2]string{"", "everything"} {
		t.Errorf("began with %q", got)
	}
}

// Idle, set_source chooses the next recording's source; it is used once.
func TestControlSetSourceForNextRecording(t *testing.T) {
	c := newControlled(t)
	var st control.Status
	c.must(t, control.OpSetSource, control.SetSourceArgs{Kind: "mic", ID: "USB Mic"}, &st)
	if st.Mic.ID != "USB Mic" {
		t.Errorf("status mic = %+v", st.Mic)
	}
	c.must(t, control.OpSetSource, control.SetSourceArgs{Kind: "system", ID: "4242"}, &st)
	if st.System.ID != "4242" {
		t.Errorf("status system = %+v", st.System)
	}
	if err := c.call(t, control.OpSetSource, control.SetSourceArgs{Kind: "camera"}, nil); err == nil {
		t.Error("unknown kind accepted")
	}
	c.must(t, control.OpStart, nil, nil)
	c.must(t, control.OpStop, nil, nil)
	c.must(t, control.OpStart, nil, nil)
	if c.began[0] != [2]string{"USB Mic", "4242"} || c.began[1][0] != "default" {
		t.Errorf("recordings began with %v", c.began)
	}
}

// A recording without switchable sources keeps the choice for the next
// recording and says so.
func TestControlSetSourceDuringRecordingWithoutSwitcher(t *testing.T) {
	c := newControlled(t)
	c.must(t, control.OpStart, nil, nil)
	c.mu.Lock()
	c.Recorder.mu.Lock()
	c.src.beginLocked(context.Background(), nil, "", nil, "none", nil, nil, nil)
	c.Recorder.mu.Unlock()
	c.mu.Unlock()
	err := c.call(t, control.OpSetSource, control.SetSourceArgs{Kind: "mic", ID: "USB Mic"}, nil)
	if err == nil || !strings.Contains(err.Error(), "next recording") {
		t.Fatalf("err = %v, want a next-recording note", err)
	}
	c.must(t, control.OpStop, nil, nil)
	var st control.Status
	c.must(t, control.OpStatus, nil, &st)
	if st.Mic.ID != "USB Mic" {
		t.Errorf("idle mic after a deferred switch = %+v", st.Mic)
	}
}

func TestControlSessionAndDelete(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("saved-1"))
	saveSession(t, c.Recorder, synthetic("saved-12"))

	var got session.Session
	c.must(t, control.OpSession, control.SessionArgs{Session: "saved-1"}, &got)
	if got.ID != "saved-1" || len(got.Segments) != 3 {
		t.Errorf("session = %+v", got)
	}
	if err := c.call(t, control.OpSession, control.SessionArgs{Session: "saved"}, nil); err == nil {
		t.Error("a prefix loaded a session")
	}
	for _, bad := range []string{"", "../saved-1", "a/b", ".."} {
		if err := c.call(t, control.OpDelete, control.SessionArgs{Session: bad}, nil); err == nil {
			t.Errorf("delete %q accepted", bad)
		}
	}

	c.must(t, control.OpStart, nil, nil)
	if err := c.call(t, control.OpDelete, control.SessionArgs{Session: "rec-1"}, nil); err == nil || !strings.Contains(err.Error(), "recording") {
		t.Errorf("delete of the recording in progress: %v", err)
	}
	var cur session.Session
	c.must(t, control.OpSession, control.SessionArgs{}, &cur)
	if cur.ID != "rec-1" {
		t.Errorf("current session = %+v", cur)
	}

	c.must(t, control.OpDelete, control.SessionArgs{Session: "saved-1"}, nil)
	if _, err := os.Stat(filepath.Join(config.SessionDir(), "saved-1")); !os.IsNotExist(err) {
		t.Errorf("session folder still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.SessionDir(), "saved-12")); err != nil {
		t.Errorf("another session went too: %v", err)
	}
}

// A session stopped but not yet saved can't be deleted or edited.
func TestControlRefusesSessionBeingSaved(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("saving-1"))
	c.Recorder.mu.Lock()
	c.saving["saving-1"] = true
	c.Recorder.mu.Unlock()
	for _, op := range []string{control.OpDelete, control.OpRenameSpeaker} {
		err := c.call(t, op, control.SpeakerArgs{Session: "saving-1", Label: "Person 2", Name: "Ana"}, nil)
		if err == nil || !strings.Contains(err.Error(), "being saved") {
			t.Errorf("%s: %v", op, err)
		}
	}
}

func TestControlRenameSpeakerSaved(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("s-1"))
	c.must(t, control.OpRenameSpeaker, control.SpeakerArgs{Session: "s-1", Label: "Person 2", Name: "Ana Example"}, nil)
	sess, err := c.store.Load("s-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range sess.Segments[1:] {
		if seg.Speaker != "Ana Example" || seg.LiveSpeaker != "Person 2" {
			t.Errorf("line %s = %q (live %q)", seg.ID, seg.Speaker, seg.LiveSpeaker)
		}
	}
	if err := c.call(t, control.OpRenameSpeaker, control.SpeakerArgs{Session: "s-1", Label: "Person 9", Name: "X"}, nil); err == nil {
		t.Error("renaming a label with no lines gave no error")
	}
	if err := c.call(t, control.OpRenameSpeaker, control.SpeakerArgs{Session: "s-1", Label: "You", Name: "X"}, nil); err == nil {
		t.Error("renaming the mic gave no error")
	}
}

// Renaming during the recording relabels the lines in memory and sends
// each to the feed, with a "rename" diag.
func TestControlRenameSpeakerLive(t *testing.T) {
	c := newControlled(t)
	dir := shortDir(t)
	srv, err := livefeed.Listen(filepath.Join(dir, "live.sock"), livefeed.Options{Snapshot: c.liveFeedSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	c.Recorder.mu.Lock()
	c.liveFeed = srv
	c.Recorder.mu.Unlock()
	defer c.stopLiveFeed()

	c.must(t, control.OpStart, nil, nil)
	c.Recorder.mu.Lock()
	c.currentSess.Segments = synthetic("x").Segments
	c.Recorder.mu.Unlock()

	conn, err := net.Dial("unix", srv.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for srv.Clients() != 1 {
		time.Sleep(5 * time.Millisecond)
	}
	c.must(t, control.OpRenameSpeaker, control.SpeakerArgs{Label: "Person 2", Name: "Ana Example"}, nil)

	var renamed, diags int
	deadline := time.Now().Add(5 * time.Second)
	for (renamed < 2 || diags < 2) && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		if !sc.Scan() {
			break
		}
		var m struct {
			Type    string          `json:"type"`
			Segment session.Segment `json:"segment"`
			Diag    control.Diag    `json:"diag"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		switch {
		case m.Type == "segment" && m.Segment.Speaker == "Ana Example":
			renamed++
		case m.Type == "diag" && m.Diag.Kind == diagRename && m.Diag.Label == "Ana Example":
			diags++
		}
	}
	if renamed != 2 || diags != 2 {
		t.Errorf("feed got %d renamed lines and %d rename diags, want 2 and 2", renamed, diags)
	}
}

func TestControlVoiceReviewOnSavedSession(t *testing.T) {
	c := newControlled(t)
	sess := synthetic("v-1")
	sess.VoiceNames = []session.VoiceName{{Label: "Person 2", Name: "Ana Example", Profile: "p1", Model: "m"}}
	sess.VoiceDoubts = []session.VoiceDoubt{{Label: "Person 2", Window: "Ana Example", Name: "Bo Example"}}
	saveSession(t, c.Recorder, sess)

	c.must(t, control.OpSkipVoiceName, control.SpeakerArgs{Session: "v-1", Label: "Person 2"}, nil)
	c.must(t, control.OpSkipVoiceDoubt, control.SpeakerArgs{Session: "v-1", Label: "Person 2"}, nil)
	got, _ := c.store.Load("v-1")
	if !got.VoiceNames[0].Skipped || !got.VoiceDoubts[0].Skipped {
		t.Errorf("voice names %+v, doubts %+v: want both skipped", got.VoiceNames, got.VoiceDoubts)
	}
	if err := c.call(t, control.OpSkipVoiceName, control.SpeakerArgs{Session: "v-1", Label: "Person 7"}, nil); err == nil {
		t.Error("skipping a label not named by voice gave no error")
	}
	// No fingerprints saved: not_them can't teach the profile.
	if err := c.call(t, control.OpNotThem, control.SpeakerArgs{Session: "v-1", Label: "Person 2"}, nil); err == nil {
		t.Error("not_them without fingerprints gave no error")
	}
}

func TestControlVoicesWithoutProfiles(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("v-2"))
	var v control.VoicesView
	c.must(t, control.OpVoices, nil, &v)
	if v.Profiles == nil || v.Pairs == nil || v.NamedByVoice == nil || v.Doubts == nil || len(v.Profiles) != 0 {
		t.Errorf("voices = %+v, want empty lists", v)
	}
	var n control.Count
	c.must(t, control.OpLearnSaved, nil, &n)
	if n.N != 0 {
		t.Errorf("learnt %d people from a meeting without fingerprints", n.N)
	}
	if err := c.call(t, control.OpRenameVoice, control.VoiceArgs{ID: "p1", Name: "X"}, nil); err == nil {
		t.Error("a voice edit without a model gave no error")
	}
	if err := c.call(t, control.OpVoiceClip, control.VoiceClip{Session: "v-2", Start: 1, End: 3}, nil); err == nil {
		t.Error("a clip from a meeting without audio gave no error")
	}
}

func TestControlSameNamedProfilesAndMoveByID(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("v-3"))
	model := models.SpeakerModels[0].ID
	store, err := diarize.VoiceStore(c.config().Meeting, model)
	if err != nil {
		t.Fatal(err)
	}
	voice := func(axis int) voices.Voice {
		var v voices.Voice
		emb := make([]float32, 4)
		emb[axis] = 1
		for range 20 {
			v.Add(emb)
		}
		return v
	}
	// Two people named alike: the second voice is unlike the first, so
	// it gets a profile of its own.
	_, _ = store.Learn("v-1", time.Now(), []voices.Learned{{Name: "Ana Example", Source: voices.FromTile, Voice: voice(0)}})
	_, _ = store.Learn("v-3", time.Now(), []voices.Learned{{Name: "Ana Example", Source: voices.FromTile, Voice: voice(1)}})
	_, _ = store.Learn("v-1", time.Now(), []voices.Learned{{Name: "Bo Example", Source: voices.FromTile, Voice: voice(2)}})
	var v control.VoicesView
	c.must(t, control.OpVoices, nil, &v)
	if len(v.SameName) != 1 || v.SameName[0].Name != "Ana Example" || v.SameName[0].MeetingsA != 1 || v.SameName[0].MeetingsB != 1 {
		t.Fatalf("sameName = %+v", v.SameName)
	}
	pair := v.SameName[0]
	c.must(t, control.OpKeepApart, control.VoiceArgs{Model: model, ID: pair.A, Other: pair.B}, nil)
	c.must(t, control.OpVoices, nil, &v)
	if len(v.SameName) != 0 {
		t.Errorf("after keeping them apart, sameName = %+v", v.SameName)
	}

	// Move v-3's meeting from one Ana to Bo, by Bo's ID.
	ids := map[string]string{}
	var from string
	for _, p := range v.Profiles {
		ids[p.Name] = p.ID
		if p.Name == "Ana Example" && len(p.Heard) == 1 && p.Heard[0].Session == "v-3" {
			from = p.ID
		}
	}
	c.must(t, control.OpMoveVoiceMeeting, control.VoiceArgs{Model: model, ID: from, Session: "v-3", To: ids["Bo Example"]}, nil)
	c.must(t, control.OpVoices, nil, &v)
	for _, p := range v.Profiles {
		if p.ID == ids["Bo Example"] && p.Meetings != 2 {
			t.Errorf("Bo after the move: %d meetings", p.Meetings)
		}
		if p.ID == from && p.Meetings != 0 {
			t.Errorf("the Ana it left: %d meetings", p.Meetings)
		}
	}
	if err := c.call(t, control.OpMoveVoiceMeeting, control.VoiceArgs{Model: model, ID: ids["Bo Example"], Session: "v-3", To: "ffffffffffffffff"}, nil); err == nil {
		t.Error("a move to a profile that doesn't exist gave no error")
	}
}

func TestControlLooks(t *testing.T) {
	c := newControlled(t)
	saveSession(t, c.Recorder, synthetic("l-1"))
	dir := filepath.Join(config.SessionDir(), "l-1")
	looks := `{"id":1,"time":"2026-03-04T10:00:05Z","stage":"named","detail":"read","name":"Ana Example","usable":true,"ring":{"x":1,"y":2}}` + "\n" +
		`{"id":2,"time":"2026-03-04T10:00:06Z","stage":"no-ring","detail":"none","usable":false,"thumb_of":1}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "looks.jsonl"), []byte(looks), 0o644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "looks"), 0o755)
	os.WriteFile(filepath.Join(dir, "looks", "1.jpg"), []byte("thumb"), 0o644)
	os.WriteFile(filepath.Join(dir, "looks", "1-tile.jpg"), []byte("tile"), 0o644)

	var views []control.LookView
	c.must(t, control.OpLooks, control.SessionArgs{Session: "l-1"}, &views)
	if len(views) != 2 || views[0].Name != "Ana Example" || views[0].SessionTime != 5 || len(views[0].Ring) == 0 || views[1].ThumbOf != 1 {
		t.Errorf("looks = %+v", views)
	}
	var m control.Media
	c.must(t, control.OpLookThumb, control.LookArgs{Session: "l-1", Look: 1}, &m)
	if m.Type != "image/jpeg" || string(m.Data) != "thumb" {
		t.Errorf("thumb = %+v", m)
	}
	c.must(t, control.OpLookTile, control.LookArgs{Session: "l-1", Look: 1}, &m)
	if string(m.Data) != "tile" {
		t.Errorf("tile = %q", m.Data)
	}
	if err := c.call(t, control.OpLookTile, control.LookArgs{Session: "../l-1", Look: 1}, nil); err == nil {
		t.Error("a path in the session ID was accepted")
	}
}

func TestControlToolsAndUnknownOp(t *testing.T) {
	c := newControlled(t)
	var tools []control.ToolStatus
	c.must(t, control.OpTools, nil, &tools)
	ids := map[string]bool{}
	for _, tl := range tools {
		ids[tl.ID] = true
	}
	if !ids["ffmpeg"] || !ids["model-parakeet"] || !ids["model-vad"] {
		t.Errorf("tools %v lack ffmpeg or the models", ids)
	}
	for _, tl := range tools {
		if tl.ID == "model-vad" && (tl.Fix == nil || tl.Fix.Kind != "action" || tl.Fix.Label != "Download") {
			t.Errorf("a missing model's fix is %+v, want the Download action", tl.Fix)
		}
	}
	if err := c.call(t, "make_coffee", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown operation") {
		t.Errorf("unknown op: %v", err)
	}
}

func TestControlReloadConfig(t *testing.T) {
	c := newControlled(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "tomoe"), 0o755)
	os.WriteFile(filepath.Join(xdg, "tomoe", "config.toml"), []byte("[audio]\ndevice = \"Desk Mic\"\n"), 0o644)
	musubi := filepath.Join(t.TempDir(), "config.toml")
	// As if the recorder had started from these files: its engines were
	// built with what a reload of them gives.
	os.WriteFile(musubi, []byte("[recorder.meeting]\nmonitor_device = \"everything\"\n"), 0o644)
	c.SetMusubiConfig(musubi)
	c.must(t, control.OpReloadConfig, nil, nil)
	c.mu.Lock()
	c.bootCfg = c.cfg
	c.mu.Unlock()
	os.WriteFile(musubi, []byte("[recorder.meeting]\nmonitor_device = \"everything\"\n[recorder.transcription]\ntwo_pass = true\n"), 0o644)

	var st control.Status
	c.must(t, control.OpReloadConfig, nil, &st)
	if st.Mic.ID != "Desk Mic" || st.System.ID != "everything" {
		t.Errorf("sources after reload = %+v / %+v", st.Mic, st.System)
	}
	if !strings.Contains(strings.Join(st.Health, "\n"), "Restart the recorder to apply the new transcription") {
		t.Errorf("health %q lacks the restart note", st.Health)
	}
	if len(st.Restart) != 1 || st.Restart[0] != "transcription" {
		t.Errorf("restart = %q, want [transcription]", st.Restart)
	}

	// Changed back to what the engines were built with: nothing waits.
	os.WriteFile(musubi, []byte("[recorder.meeting]\nmonitor_device = \"everything\"\n"), 0o644)
	st = control.Status{}
	c.must(t, control.OpReloadConfig, nil, &st)
	if len(st.Restart) != 0 || strings.Contains(strings.Join(st.Health, "\n"), "Restart the recorder") {
		t.Errorf("after changing back: restart %q, health %q", st.Restart, st.Health)
	}

	os.WriteFile(musubi, []byte("[recorder.meeting]\nmonitor_device = 3\n"), 0o644)
	if err := c.call(t, control.OpReloadConfig, nil, nil); err == nil {
		t.Error("a bad Musubi config reloaded")
	}
	if c.config().Meeting.MonitorDevice != "everything" {
		t.Error("a failed reload changed the config")
	}
}

// A silent source is reported at once; another source with signal is
// offered once the check hears it, and both clear when the signal is
// back.
func TestSourceCheckSuggestsAnotherMic(t *testing.T) {
	c := newControlled(t)
	heard := make(chan string, 4)
	c.probes.mics = func(current string) []control.Source {
		if current != "default" {
			t.Errorf("mics asked about %q", current)
		}
		return []control.Source{micSource("Dead Mic"), micSource("USB Mic")}
	}
	c.probes.listenMic = func(_ context.Context, id string, started func()) []float32 {
		started()
		heard <- id
		if id == "USB Mic" {
			return []float32{0, 0.2, -0.1}
		}
		return make([]float32, 3)
	}
	c.must(t, control.OpStart, nil, nil)
	c.Recorder.mu.Lock()
	c.src.beginLocked(context.Background(), nil, "default", nil, "auto", nil, nil, nil)
	c.Recorder.mu.Unlock()

	c.markSilent("mic")
	deadline := time.Now().Add(5 * time.Second)
	var st control.Status
	for time.Now().Before(deadline) {
		st = c.Status()
		if st.Suggest != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Silent == nil || st.Silent.Kind != "mic" || st.Suggest == nil || st.Suggest.ID != "USB Mic" || st.SuggestNote != "" {
		t.Fatalf("status = silent %+v, suggest %+v, note %q", st.Silent, st.Suggest, st.SuggestNote)
	}
	// The silent default mic goes by its device's name, so the app can
	// say which mic to check (often one muted in the call).
	if st.Silent.Name != "Desk Speakerphone (system default)" || st.Mic.Name != st.Silent.Name {
		t.Errorf("silent mic named %q, status mic %q", st.Silent.Name, st.Mic.Name)
	}
	if a, b := <-heard, <-heard; a != "Dead Mic" || b != "USB Mic" {
		t.Errorf("listened to %q, %q", a, b)
	}
	// Still silent a tick later: no second check right away.
	c.markSilent("mic")
	select {
	case id := <-heard:
		t.Errorf("checked again at once (%q)", id)
	case <-time.After(50 * time.Millisecond):
	}

	c.clearSilent("mic")
	if st := c.Status(); st.Silent != nil || st.Suggest != nil {
		t.Errorf("after the signal came back: silent %+v, suggest %+v", st.Silent, st.Suggest)
	}
}

func TestSourceCheckOffOnlyLogs(t *testing.T) {
	c := newControlled(t)
	cfg := config.DefaultConfig()
	cfg.Meeting.SourceCheck = false
	c.Recorder.mu.Lock()
	c.cfg = cfg
	c.src.beginLocked(context.Background(), nil, "default", nil, "auto", nil, nil, nil)
	c.Recorder.mu.Unlock()
	c.markSilent("mic")
	if st := c.Status(); st.Silent != nil {
		t.Errorf("silent %+v with the check off", st.Silent)
	}
}

// System audio silent while the whole system plays something: offer
// another source; the whole system silent too: nothing to offer.
func TestSourceCheckSystem(t *testing.T) {
	for _, systemHeard := range []bool{true, false} {
		c := newControlled(t)
		c.probes.hearSystem = func(context.Context) bool { return systemHeard }
		c.probes.systemInstead = func(string) control.Source {
			return control.Source{Kind: "system", ID: "everything", Name: "Everything"}
		}
		c.Recorder.mu.Lock()
		c.src.beginLocked(context.Background(), nil, "default", nil, "4242", nil, nil, nil)
		c.Recorder.mu.Unlock()
		c.markSilent("system")
		deadline := time.Now().Add(2 * time.Second)
		for {
			c.Recorder.mu.Lock()
			done := !c.src.probing["system"]
			c.Recorder.mu.Unlock()
			if done || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		st := c.Status()
		if got := st.Suggest != nil && st.Suggest.ID == "everything"; got != systemHeard {
			t.Errorf("system heard %v: suggest %+v", systemHeard, st.Suggest)
		}
	}
}

func TestSegmentDiag(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := segmentDiag(at, diagLabel, session.Segment{ID: "seg-4", Speaker: "Person 1 (Ana Example)", Decision: "confident", Status: "pending", Text: "hi", StartTime: 1.5})
	if d.Kind != diagLabel || d.Line != "seg-4" || d.Detail != "Ana Example [Person 1, OCR+Centroid match]" || d.Fields["status"] != "refining" || d.Fields["decision"] != "confident" {
		t.Errorf("diag = %+v", d)
	}
	d = segmentDiag(at, diagLabel, session.Segment{ID: "seg-5", Speaker: "Bo Example", VoiceMatch: &session.VoiceMatch{Profile: "p2", Similarity: 0.81}})
	if d.Kind != diagMatch || d.Detail != "Bo Example" || d.Fields["voice_profile"] != "p2" || d.Fields["status"] != "final" {
		t.Errorf("voice-matched diag = %+v", d)
	}
}

// Once Musubi's config makes the recorder of record, reload_config stops
// Tomoe's voices being copied at the next recording; back to "tomoe",
// they are copied again.
func TestOfRecordStopsVoiceCopy(t *testing.T) {
	c := newControlled(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	musubi := filepath.Join(t.TempDir(), "config.toml")
	c.SetMusubiConfig(musubi)
	record := func() int {
		t.Helper()
		before := c.copies
		c.must(t, control.OpStart, nil, nil)
		c.must(t, control.OpStop, nil, nil)
		return c.copies - before
	}
	if n := record(); n != 1 {
		t.Errorf("default: voices copied %d times, want 1", n)
	}
	os.WriteFile(musubi, []byte("[recorder]\nof_record = \"musubi\"\n"), 0o644)
	c.must(t, control.OpReloadConfig, nil, nil)
	if n := record(); n != 0 {
		t.Errorf("of record: voices copied %d times, want 0", n)
	}
	os.WriteFile(musubi, []byte("[recorder]\nof_record = \"tomoe\"\n"), 0o644)
	c.must(t, control.OpReloadConfig, nil, nil)
	if n := record(); n != 1 {
		t.Errorf("back to Tomoe: voices copied %d times, want 1", n)
	}
}

// With Tomoe off ([recorder] tomoe = "off"), even while Tomoe is of
// record, Tomoe's voices are not copied and its config file is not read.
func TestTomoeOffStopsVoiceCopy(t *testing.T) {
	c := newControlled(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "tomoe"), 0o700)
	os.WriteFile(filepath.Join(xdg, "tomoe", "config.toml"), []byte("[audio]\ndevice = \"Tomoe Mic\"\n"), 0o600)
	musubi := filepath.Join(t.TempDir(), "config.toml")
	c.SetMusubiConfig(musubi)
	defer config.SetTomoeOff(false)
	os.WriteFile(musubi, []byte("[recorder]\ntomoe = \"off\"\n"), 0o644)
	c.must(t, control.OpReloadConfig, nil, nil)
	before := c.copies
	c.must(t, control.OpStart, nil, nil)
	c.must(t, control.OpStop, nil, nil)
	if n := c.copies - before; n != 0 {
		t.Errorf("tomoe off: voices copied %d times, want 0", n)
	}
	if cfg := c.config(); !cfg.TomoeOff || cfg.Audio.Device != "default" {
		t.Errorf("tomoe off: TomoeOff %v, device %q (Tomoe's file read?)", cfg.TomoeOff, cfg.Audio.Device)
	}
}

// reload_config naming the app's config file switches to it and keeps it
// for the next start; of_record in that file then stops the voice copy.
func TestReloadTakesAppConfig(t *testing.T) {
	c := newControlled(t)
	t.Setenv("MUSUBI_CONFIG", "")
	app := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(app, []byte("[recorder]\nof_record = \"musubi\"\n"), 0o600)
	c.must(t, control.OpReloadConfig, control.ReloadArgs{MusubiConfig: app}, nil)
	if got := config.ResolveMusubiPath(config.DataDir()); got != app {
		t.Errorf("stored path %s, want %s", got, app)
	}
	if !c.config().MusubiOfRecord {
		t.Error("of_record from the app's file was not read")
	}
	if err := c.call(t, control.OpReloadConfig, control.ReloadArgs{MusubiConfig: "relative.toml"}, nil); err == nil {
		t.Error("a relative path was taken")
	}
}

// fakeTap is a recording's system audio: Recorded says start until the
// probe has been heard, then everything (the capture caught up).
type fakeTap struct {
	mu    sync.Mutex
	audio []float32
	n     int
}

func (f *fakeTap) Recorded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeTap) RecordedRange(from, to int) []float32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	from, to = max(from, 0), min(to, f.n)
	if from >= to {
		return nil
	}
	return append([]float32(nil), f.audio[from:to]...)
}

// bursts is n samples of noise cut into word-like bursts (80-300 ms, with
// gaps of 50-400 ms) at amplitude amp, from seed.
func bursts(seed uint64, n int, amp float64) []float32 {
	r := rand.New(rand.NewPCG(seed, 1))
	out := make([]float32, n)
	for i := 0; i < n; {
		l := 1280 + r.IntN(3520)
		for j := 0; j < l && i+j < n; j++ {
			out[i+j] = float32(amp * math.Sin(math.Pi*float64(j)/float64(l)) * (r.Float64()*2 - 1))
		}
		i += l + 800 + r.IntN(5600)
	}
	return out
}

// While system audio is captured, a mic that only hears it from the
// speakers is not offered (and the status says why); a mic hearing
// something else is; a silent one is not. Without system audio, any mic
// with signal is offered, as before.
func TestSourceCheckEcho(t *testing.T) {
	const rate, at = 16000, 3 * 16000
	sys := bursts(1, 10*rate, 0.3)
	delayed := func(delay int, gain float64) []float32 {
		r := rand.New(rand.NewPCG(9, 9))
		out := make([]float32, rate*3/2)
		for i := range out {
			out[i] = float32(gain*float64(sys[at+i-delay]) + 0.001*r.NormFloat64())
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		probe   []float32
		tap     bool
		suggest bool
		note    string
	}{
		{"speakers", delayed(2400, 0.1), true, false, speakersNote},
		{"own voice", bursts(2, rate*3/2, 0.05), true, true, ""},
		{"silent", make([]float32, rate*3/2), true, false, ""},
		{"speakers, no system audio", delayed(2400, 0.1), false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newControlled(t)
			tap := &fakeTap{audio: sys, n: at}
			c.probes.mics = func(string) []control.Source { return []control.Source{micSource("Webcam Mic")} }
			c.probes.listenMic = func(_ context.Context, _ string, started func()) []float32 {
				started()
				tap.mu.Lock()
				tap.n = len(sys)
				tap.mu.Unlock()
				return tc.probe
			}
			c.Recorder.mu.Lock()
			c.src.beginLocked(context.Background(), nil, "default", nil, "auto", nil, nil, nil)
			if tc.tap {
				c.src.meeting = tap
			}
			c.Recorder.mu.Unlock()
			c.markSilent("mic")
			deadline := time.Now().Add(5 * time.Second)
			for {
				c.Recorder.mu.Lock()
				done := !c.src.probing["mic"]
				c.Recorder.mu.Unlock()
				if done || time.Now().After(deadline) {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			st := c.Status()
			if st.Silent == nil || st.Silent.Kind != "mic" {
				t.Fatalf("silent = %+v", st.Silent)
			}
			if got := st.Suggest != nil; got != tc.suggest || st.SuggestNote != tc.note {
				t.Errorf("suggest %+v, note %q; want suggested %v, note %q", st.Suggest, st.SuggestNote, tc.suggest, tc.note)
			}
		})
	}
}

// The default mic keeps its old name when the device can't be named.
func TestMicSourceUnnamedDefault(t *testing.T) {
	c := newControlled(t)
	c.Recorder.mu.Lock()
	defer c.Recorder.mu.Unlock()
	if got := c.micSourceLocked("default").Name; got != "System default" {
		t.Errorf("before the lookup: %q", got)
	}
	c.defaultMic = "Desk Speakerphone"
	if got := c.micSourceLocked("default").Name; got != "Desk Speakerphone (system default)" {
		t.Errorf("after the lookup: %q", got)
	}
	if got := c.micSourceLocked("USB Mic").Name; got != "USB Mic" {
		t.Errorf("a named device: %q", got)
	}
}
