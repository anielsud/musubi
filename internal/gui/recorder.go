package gui

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/anielsud/musubi/internal/knowledge"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/dataformat"
	"github.com/anielsud/musubi/internal/source/tomoe"
)

// The recorder screens (docs/recorder-screens.md): Musubi asks its
// recorder over the control socket and never writes the recorder's folder
// itself, so an edit can't race a recording in progress. The recorder's
// live feed is always followed for its state and diagnostics; meetings,
// Live and voices follow whichever recorder is of record, and edits are
// offered only once Musubi's is (while Tomoe is of record the recorder
// copies Tomoe's voices at each recording, overwriting edits made here).

// recorderState is what the app keeps from the recorder's feed: the last
// status, and recent diagnostics and window looks for screens opened
// mid-meeting.
type recorderState struct {
	mu        sync.Mutex
	connected bool
	status    *control.Status
	diags     []control.Diag
	looks     []control.LookView
	lookSess  string
}

const (
	keepDiags = 300
	keepLooks = 300
	// keepThumbs: live looks carry their thumbnail inline; only the newest
	// keep it, older ones are fetched from the session when shown.
	keepThumbs = 40
)

func (a *App) recClient() (*control.Client, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready: %s", a.err)
	}
	return control.NewClient(a.eng.Cfg.RecorderControlSocket()), nil
}

func (a *App) recCall(op string, args, out any) error {
	c, err := a.recClient()
	if err != nil {
		return err
	}
	return c.Call(a.ctx, op, args, out)
}

// requireEditable refuses an edit while Tomoe is of record.
func (a *App) requireEditable() error {
	if a.eng == nil {
		return fmt.Errorf("not ready: %s", a.err)
	}
	if !a.eng.Cfg.MusubiRecords() {
		return fmt.Errorf("voice and speaker edits work once Musubi records your meetings (Settings → Recording)")
	}
	return nil
}

// RecorderView is the Recording card: the recorder's state, or why there
// is none.
type RecorderView struct {
	Running  bool            `json:"running"`
	Status   *control.Status `json:"status,omitempty"`
	OfRecord string          `json:"ofRecord"`
	Editable bool            `json:"editable"`
	// Relabel: a renamed voice profile's name can go onto its meetings'
	// labels ([recorder] relabel).
	Relabel bool   `json:"relabel"`
	Error   string `json:"error,omitempty"`
}

func (a *App) RecorderState() RecorderView {
	v := RecorderView{OfRecord: "tomoe"}
	if a.eng == nil {
		v.Error = a.err
		return v
	}
	v.OfRecord, v.Editable, v.Relabel = a.eng.Cfg.Recorder.OfRecord, a.eng.Cfg.MusubiRecords(), a.eng.Cfg.Recorder.Relabel
	var st control.Status
	switch err := a.recCall(control.OpStatus, nil, &st); {
	case errors.Is(err, control.ErrNotRunning):
	case err != nil:
		v.Error = err.Error()
	default:
		v.Running, v.Status = true, &st
	}
	return v
}

// RecorderStart starts a recording the detection missed.
func (a *App) RecorderStart() (*control.Status, error) {
	var st control.Status
	return &st, a.recCall(control.OpStart, control.StartArgs{}, &st)
}

// RecorderStop stops one it caught by mistake (or that is over).
func (a *App) RecorderStop() (*control.Status, error) {
	var st control.Status
	return &st, a.recCall(control.OpStop, nil, &st)
}

// recorderAgent is the recorder's LaunchAgent (scripts/install-recorder.sh).
const recorderAgent = "com.anielsud.musubi.recorder"

// RestartRecorder restarts the recorder so settings it reads only at
// start apply. Never during a recording: the restart would cut it off,
// and the rest of the meeting wouldn't be recorded.
func (a *App) RestartRecorder() error {
	var st control.Status
	err := a.recCall(control.OpStatus, nil, &st)
	switch {
	case errors.Is(err, control.ErrNotRunning):
		// launchd starts it again; kickstart does that too.
	case err != nil:
		return err
	case st.Recording:
		return fmt.Errorf("the recorder is recording a meeting; restart it once the meeting is over")
	}
	return startRecorderAgent(a.ctx, true, a.recorderInstall())
}

func (a *App) RecorderSources() (*control.Sources, error) {
	var s control.Sources
	return &s, a.recCall(control.OpSources, nil, &s)
}

// RecorderSetSource switches a source of the recording in progress.
func (a *App) RecorderSetSource(kind, id string) (*control.Status, error) {
	if kind != "mic" && kind != "system" {
		return nil, fmt.Errorf("no such source kind %q", kind)
	}
	var st control.Status
	return &st, a.recCall(control.OpSetSource, control.SetSourceArgs{Kind: kind, ID: id}, &st)
}

func (a *App) RecorderTools() ([]control.ToolStatus, error) {
	var ts []control.ToolStatus
	return ts, a.recCall(control.OpTools, nil, &ts)
}

func (a *App) RecorderFixTool(id string) error {
	return a.recCall(control.OpFixTool, control.ToolArgs{ID: id}, nil)
}

// CopyText puts a fix's command on the clipboard, for the owner to paste
// into a terminal.
func (a *App) CopyText(s string) error {
	return wruntime.ClipboardSetText(a.ctx, s)
}

// RecorderDiags are the recent live-labelling events.
func (a *App) RecorderDiags() []control.Diag {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return append([]control.Diag(nil), a.rec.diags...)
}

// LiveLooks are the looks at the meeting window so far in the recording
// in progress (or the last one), and its session.
type LiveLooks struct {
	Session string             `json:"session"`
	Looks   []control.LookView `json:"looks"`
}

func (a *App) RecorderLiveLooks() LiveLooks {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return LiveLooks{a.rec.lookSess, append([]control.LookView{}, a.rec.looks...)}
}

// RecorderLooks are a saved meeting's looks.
func (a *App) RecorderLooks(session string) ([]control.LookView, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("not a session id")
	}
	var ls []control.LookView
	return ls, a.recCall(control.OpLooks, control.SessionArgs{Session: session}, &ls)
}

// Voices is the recorder's voice view: profiles, pairs that may be one
// person, and the review queue.
func (a *App) Voices() (*control.VoicesView, error) {
	var v control.VoicesView
	return &v, a.recCall(control.OpVoices, nil, &v)
}

// speakerOps answer a review item or rename a speaker in a session.
var speakerOps = map[string]bool{
	control.OpRenameSpeaker: true, control.OpNotThem: true, control.OpSkipVoiceName: true, control.OpSkipVoiceDoubt: true,
	control.OpSkipReturnedVoice: true,
}

// RecorderSpeaker applies a speaker op to a session's label (session ""
// for the recording in progress, rename only). A saved meeting's rename
// is relabelled in Musubi when it is only that ([recorder] relabel);
// any other change is ingested, which re-extracts that meeting.
func (a *App) RecorderSpeaker(op, session, label, name string) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	if !speakerOps[op] {
		return fmt.Errorf("no such speaker op %q", op)
	}
	name = strings.TrimSpace(name)
	if label == "" || len(label) > 200 || len(name) > 200 || strings.ContainsAny(name, "\n\r") {
		return fmt.Errorf("a speaker and a one-line name are needed")
	}
	if op == control.OpRenameSpeaker && name == "" {
		return fmt.Errorf("a name is needed")
	}
	if session == "" && op != control.OpRenameSpeaker {
		return fmt.Errorf("only renaming works on the recording in progress")
	}
	if session != "" && !validSession(session) {
		return fmt.Errorf("not a session id")
	}
	if err := a.recCall(op, control.SpeakerArgs{Session: session, Label: label, Name: name}, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	if session != "" {
		// A pure relabel is taken in without re-extracting the meeting
		// ([recorder] relabel); anything else is imported again.
		if op == control.OpRenameSpeaker && a.relabelRenamedSpeaker(session) {
			return nil
		}
		a.updateSoon()
	}
	return nil
}

var voiceOps = map[string]bool{
	control.OpRenameVoice: true, control.OpMergeVoices: true, control.OpKeepApart: true, control.OpForgetVoice: true, control.OpDeleteVoice: true,
	control.OpStartOver: true, control.OpConfirmVoice: true, control.OpRemoveVoiceMeeting: true, control.OpConfirmVoiceMeeting: true, control.OpMoveVoiceMeeting: true,
}

// AllowVoiceName takes a name off a speaker model's never-learnt names.
func (a *App) AllowVoiceName(model, name string) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if model == "" || name == "" || len(name) > 200 || strings.ContainsAny(name, "\n\r") {
		return fmt.Errorf("a model and a name are needed")
	}
	if err := a.recCall(control.OpAllowName, control.VoiceArgs{Model: model, Name: name}, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	return nil
}

// RecorderVoice applies a voice profile op.
func (a *App) RecorderVoice(op string, args control.VoiceArgs) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	if !voiceOps[op] {
		return fmt.Errorf("no such voice op %q", op)
	}
	args.Name = strings.TrimSpace(args.Name)
	if args.Model == "" || args.ID == "" || strings.ContainsAny(args.Name, "\n\r") || len(args.Name) > 200 {
		return fmt.Errorf("a voice profile is needed")
	}
	if op == control.OpRenameVoice && args.Name == "" {
		return fmt.Errorf("a name is needed")
	}
	args.To = strings.TrimSpace(args.To)
	if args.To != "" && (op != control.OpMoveVoiceMeeting || !validProfileID(args.To)) {
		return fmt.Errorf("not a voice profile")
	}
	if op == control.OpMoveVoiceMeeting && args.To == "" && args.Name == "" {
		return fmt.Errorf("a profile or a name is needed")
	}
	if args.Session != "" && !validSession(args.Session) {
		return fmt.Errorf("not a session id")
	}
	if op == control.OpConfirmVoiceMeeting && args.Session == "" {
		return fmt.Errorf("a meeting is needed")
	}
	if err := a.recCall(op, args, nil); err != nil {
		return err
	}
	a.emit("voices:changed", nil)
	return nil
}

// validProfileID: profile IDs are short hex (voices.newID); anything
// else is refused before it reaches the recorder.
func validProfileID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// DiscardedClip is where a meeting taken out of a profile has the voice.
func (a *App) DiscardedClip(model, id, session string) (*control.VoiceClip, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("not a session id")
	}
	var c control.VoiceClip
	return &c, a.recCall(control.OpDiscardedClip, control.VoiceArgs{Model: model, ID: id, Session: session}, &c)
}

// MeetingSpeakers is a meeting's speakers, read from its session file
// (never written here), and whether the recorder may correct them.
type MeetingSpeakers struct {
	Session  string         `json:"session"`
	Speakers []SpeakerCount `json:"speakers"`
	// Editable: Musubi's recorder is of record and recorded this meeting.
	// Why says otherwise.
	Editable bool   `json:"editable"`
	Why      string `json:"why,omitempty"`
}

type SpeakerCount struct {
	Label   string  `json:"label"`
	Lines   int     `json:"lines"`
	Seconds float64 `json:"seconds"`
}

func (a *App) MeetingSpeakers(itemID string) (*MeetingSpeakers, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("not ready")
	}
	sid, ok := sessionOfItem(itemID)
	if !ok {
		return nil, nil
	}
	m := &MeetingSpeakers{Session: sid, Speakers: []SpeakerCount{}}
	dir := a.eng.Cfg.SessionDir(sid)
	if dir == "" {
		m.Why = "The recording's files aren't on this Mac."
		return m, nil
	}
	if err := dataformat.Check(filepath.Dir(filepath.Dir(dir))); err != nil {
		m.Why = "The recorder's folder was written by a newer Musubi; install that version again to see or correct this meeting's speakers."
		return m, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return nil, err
	}
	m.Speakers = countSpeakers(b)
	switch {
	case !a.eng.Cfg.MusubiRecords():
		m.Why = "Speakers can be corrected here once Musubi records your meetings (Settings → Recording); until then, in Tomoe."
	case filepath.Dir(dir) != filepath.Clean(a.eng.Cfg.RecorderSessions()):
		m.Why = "Tomoe recorded this meeting, before Musubi did; its speakers can be corrected in Tomoe."
	default:
		m.Editable = true
	}
	return m, nil
}

// countSpeakers totals a session file's final lines by speaker, the most
// heard first.
func countSpeakers(sessionJSON []byte) []SpeakerCount {
	var s struct {
		Segments []struct {
			Speaker string  `json:"speaker"`
			Start   float64 `json:"start_time"`
			End     float64 `json:"end_time"`
			Status  string  `json:"status"`
		} `json:"segments"`
	}
	out := []SpeakerCount{}
	if json.Unmarshal(sessionJSON, &s) != nil {
		return out
	}
	by := map[string]*SpeakerCount{}
	for _, g := range s.Segments {
		if g.Status != "" || g.Speaker == "" {
			continue
		}
		c := by[g.Speaker]
		if c == nil {
			c = &SpeakerCount{Label: g.Speaker}
			by[g.Speaker] = c
		}
		c.Lines++
		if g.End > g.Start {
			c.Seconds += g.End - g.Start
		}
	}
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// DeleteMeeting deletes a recording through the recorder, then forgets
// the meeting in Musubi, taking its statements out of their threads; it
// is never imported again. The UI asks first.
func (a *App) DeleteMeeting(itemID string) error {
	if err := a.requireEditable(); err != nil {
		return err
	}
	sid, ok := sessionOfItem(itemID)
	if !ok {
		return fmt.Errorf("not a recording")
	}
	// An update could be threading this meeting; forgetting it under that
	// run would leave half its statements placed.
	a.mu.Lock()
	busy := a.updating || a.running != ""
	a.mu.Unlock()
	if busy {
		return fmt.Errorf("Musubi is updating; delete the meeting once it is done")
	}
	if err := a.backupBefore("deleting a meeting"); err != nil {
		return err
	}
	if err := a.recCall(control.OpDelete, control.SessionArgs{Session: sid}, nil); err != nil {
		return err
	}
	h, used, err := knowledge.ForgetItem(a.eng.Store, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		// Never imported: with the recording gone, nothing will import it.
		used = true
	} else if err != nil {
		return fmt.Errorf("the recording is deleted, but forgetting it in Musubi failed: %w", err)
	}
	if !used && h != "" {
		if rs, err := raw.Open(a.eng.Cfg.RawDir()); err == nil {
			rs.Delete(h)
		}
	}
	if a.live != nil {
		a.live.Reload()
		a.emit("app:status", a.Status())
	}
	a.emit("voices:changed", nil)
	a.emit("meetings:changed", itemID)
	return nil
}

// sessionOfItem is a recording item's session id ("tomoe:<uuid>").
func sessionOfItem(itemID string) (string, bool) {
	sid, ok := strings.CutPrefix(itemID, tomoe.Name+":")
	return sid, ok && validSession(sid)
}

// validSession accepts session folder names only (lowercase UUIDs), so an
// id can never reach outside a sessions folder or into another op.
func validSession(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f'):
			return false
		}
	}
	return true
}

// updateSoon ingests an edited meeting: now, or after the update under
// way (which may have read the file before the edit).
func (a *App) updateSoon() {
	a.mu.Lock()
	busy := a.updating || a.running != ""
	if busy {
		a.updateAgain = true
	}
	a.mu.Unlock()
	if !busy {
		a.Update()
	}
}

// recorderMedia serves /recorder-media/clip?session=&start=&end= (a
// voice clip, audio/aac) and /recorder-media/{tile,thumb}/<session>/<look>
// (image/jpeg) from the recorder, to the app's own webview. Ids are
// checked before anything is asked; types are fixed, not taken from the
// reply.
func (a *App) recorderMedia(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/recorder-media/")
	var op, ctype string
	var args any
	switch {
	case rest == "clip":
		q := r.URL.Query()
		s, err1 := strconv.ParseFloat(q.Get("start"), 64)
		e, err2 := strconv.ParseFloat(q.Get("end"), 64)
		sid := q.Get("session")
		if err1 != nil || err2 != nil || !validSession(sid) || !validClip(s, e) {
			http.NotFound(w, r)
			return
		}
		op, ctype, args = control.OpVoiceClip, "audio/aac", control.VoiceClip{Session: sid, Start: s, End: e}
	default:
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || (parts[0] != "tile" && parts[0] != "thumb") || !validSession(parts[1]) {
			http.NotFound(w, r)
			return
		}
		look, err := strconv.Atoi(parts[2])
		if err != nil || look < 0 {
			http.NotFound(w, r)
			return
		}
		op = control.OpLookTile
		if parts[0] == "thumb" {
			op = control.OpLookThumb
		}
		ctype, args = "image/jpeg", control.LookArgs{Session: parts[1], Look: look}
	}
	// The webview asks for a clip in several range requests; each was a
	// fresh cut by the recorder. A small cache answers the rest.
	key := op + "|" + r.URL.RequestURI()
	data, ok := a.recMedia.get(key)
	if !ok {
		var m control.Media
		if err := a.recCall(op, args, &m); err != nil || len(m.Data) == 0 {
			http.NotFound(w, r)
			return
		}
		data = m.Data
		a.recMedia.put(key, data)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// mediaCache keeps the last few clips and tiles the webview asked for.
// Recordings don't change under a clip or a look once saved, and an
// edit that would (a speaker renamed) doesn't change their audio or
// images.
type mediaCache struct {
	mu    sync.Mutex
	order []string
	items map[string][]byte
}

const mediaCacheSize = 64

func (c *mediaCache) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[k]
	return b, ok
}

func (c *mediaCache) put(k string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string][]byte{}
	}
	if _, ok := c.items[k]; !ok {
		c.order = append(c.order, k)
	}
	c.items[k] = b
	for len(c.order) > mediaCacheSize {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// validClip: a clip of at most a minute, within a meeting's first day.
func validClip(start, end float64) bool {
	ok := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x < 86400 }
	return ok(start) && ok(end) && end > start && end-start <= 60
}

// feedMessage is a recorder feed line, for the types the app follows.
type feedMessage struct {
	V         int               `json:"v"`
	Type      string            `json:"type"`
	SessionID string            `json:"session_id,omitempty"`
	Status    *control.Status   `json:"status,omitempty"`
	Diag      *control.Diag     `json:"diag,omitempty"`
	Look      *control.LookView `json:"look,omitempty"`
}

// followRecorder reads the Musubi recorder's live feed until ctx ends,
// whichever recorder is of record, and re-emits its state, diagnostics
// and looks as "recorder:*" events. A missing recorder is retried quietly.
func (a *App) followRecorder(ctx context.Context, path string) {
	setConn := func(up bool) {
		a.rec.mu.Lock()
		changed := a.rec.connected != up
		a.rec.connected = up
		a.rec.mu.Unlock()
		if changed {
			a.emit("recorder:conn", up)
			if up {
				// A recorder that just started may read another config
				// file than this app's: tell it which one.
				go a.reloadRecorder()
			}
		}
	}
	for ctx.Err() == nil {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			setConn(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		setConn(true)
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64<<10), 8<<20) // live looks carry a thumbnail
		for sc.Scan() {
			var m feedMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil && m.V == control.Version {
				a.recorderEvent(m)
			}
		}
		stop()
		conn.Close()
		setConn(false)
	}
}

func (a *App) recorderEvent(m feedMessage) {
	switch m.Type {
	case control.FeedStatus, control.FeedAudio:
		if m.Status == nil {
			return
		}
		a.rec.mu.Lock()
		a.rec.status = m.Status
		a.rec.mu.Unlock()
		a.emit("recorder:"+m.Type, m.Status)
	case control.FeedDiag:
		if m.Diag == nil {
			return
		}
		a.rec.mu.Lock()
		a.rec.diags = append(a.rec.diags, *m.Diag)
		if n := len(a.rec.diags); n > keepDiags {
			a.rec.diags = a.rec.diags[n-keepDiags:]
		}
		a.rec.mu.Unlock()
		a.emit("recorder:diag", m.Diag)
	case control.FeedLook:
		if m.Look == nil {
			return
		}
		a.rec.mu.Lock()
		if m.SessionID != a.rec.lookSess {
			a.rec.looks, a.rec.lookSess = nil, m.SessionID
		}
		a.rec.looks = append(a.rec.looks, *m.Look)
		if n := len(a.rec.looks); n > keepLooks {
			a.rec.looks = a.rec.looks[n-keepLooks:]
		}
		if n := len(a.rec.looks); n > keepThumbs {
			a.rec.looks[n-keepThumbs-1].Thumb = ""
		}
		a.rec.mu.Unlock()
		a.emit("recorder:look", map[string]any{"session": m.SessionID, "look": m.Look})
	}
}
