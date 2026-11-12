package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/audiosources"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/live"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/meetingaudio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/sigfix"
)

// The control socket (internal/recorder/control) is how the Musubi app
// works the recorder: Handle carries out each request. Only the recorder
// writes its folder, so every edit of a session or a voice profile comes
// through here and never races a recording.

// Handle is the control server's handler.
func (a *Recorder) Handle(ctx context.Context, op string, args json.RawMessage) (any, error) {
	// A sherpa-onnx constructor run since the last repair may have put its
	// crash handler back without SA_ONSTACK (Tomoe repaired it on every
	// call from its window for the same reason).
	sigfix.AfterSherpa()
	switch op {
	case control.OpStatus:
		return a.Status(), nil
	case control.OpStart:
		var p control.StartArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return a.startFromControl(p)
	case control.OpStop:
		if _, err := a.StopSession(); err != nil {
			return nil, err
		}
		return a.Status(), nil
	case control.OpSources:
		return listSources(), nil
	case control.OpSetSource:
		var p control.SetSourceArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return a.setSource(p)
	case control.OpReloadConfig:
		var p control.ReloadArgs
		if len(args) > 0 {
			if err := decode(args, &p); err != nil {
				return nil, err
			}
		}
		if p.MusubiConfig != "" {
			if err := config.StoreMusubiPath(config.DataDir(), p.MusubiConfig); err != nil {
				return nil, err
			}
			a.SetMusubiConfig(p.MusubiConfig)
		}
		return a.reloadConfig()

	case control.OpSession:
		var p control.SessionArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return a.sessionForControl(p.Session)
	case control.OpDelete:
		var p control.SessionArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.deleteSession(p.Session)

	case control.OpRenameSpeaker:
		var p control.SpeakerArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.RenameSpeaker(p.Session, p.Label, p.Name)
	case control.OpNotThem:
		var p control.SpeakerArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.NotThem(p.Session, p.Label)
	case control.OpSkipVoiceName:
		var p control.SpeakerArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.SkipVoiceName(p.Session, p.Label)
	case control.OpSkipVoiceDoubt:
		var p control.SpeakerArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.SkipVoiceDoubt(p.Session, p.Label)

	case control.OpVoices:
		return a.Voices()
	case control.OpRenameVoice, control.OpMergeVoices, control.OpKeepApart, control.OpForgetVoice,
		control.OpStartOver, control.OpConfirmVoice, control.OpRemoveVoiceMeeting, control.OpMoveVoiceMeeting:
		var p control.VoiceArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.editVoice(op, p)
	case control.OpLearnSaved:
		n, err := a.learnSaved()
		if err != nil {
			return nil, err
		}
		return control.Count{N: n}, nil
	case control.OpVoiceClip:
		var p control.VoiceClip
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		b, err := a.cutVoiceClip(p)
		if err != nil {
			return nil, err
		}
		return control.Media{Type: "audio/mp4", Data: b}, nil
	case control.OpDiscardedClip:
		var p control.VoiceArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return a.DiscardedVoiceClip(p.Model, p.ID, p.Session)

	case control.OpLooks:
		var p control.SessionArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return a.Looks(p.Session)
	case control.OpLookTile:
		var p control.LookArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		b, err := a.LookTile(p.Session, p.Look)
		if err != nil {
			return nil, err
		}
		return control.Media{Type: "image/jpeg", Data: b}, nil
	case control.OpLookThumb:
		var p control.LookArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		b, err := a.LookThumb(p.Session, p.Look)
		if err != nil {
			return nil, err
		}
		return control.Media{Type: "image/jpeg", Data: b}, nil

	case control.OpTools:
		return a.Tools(), nil
	case control.OpFixTool:
		var p control.ToolArgs
		if err := decode(args, &p); err != nil {
			return nil, err
		}
		return nil, a.FixTool(p.ID)
	}
	return nil, fmt.Errorf("%w %q", control.ErrUnknownOp, op)
}

// decode reads a request's args; none is the zero value.
func decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad args: %w", err)
	}
	return nil
}

// sourceState is the sources of the recording in progress and what the
// source check found, guarded by Recorder.mu.
type sourceState struct {
	// The recording's, while one runs (zero otherwise).
	recording   bool
	ctx         context.Context // ends with the recording
	coordinator *live.Coordinator
	// mic is the mic selection: "" none, "default" the system's, else a
	// device name. micSwitch moves it (nil without a mic).
	mic       string
	micSwitch *audio.SwitchCapturer
	// system is the system-audio selection (meetingaudio: AutoSource,
	// "everything", a PID or NoSource); sysSwitch moves it (nil without
	// system audio), auto follows the meeting app while system is
	// AutoSource, ended by followCancel.
	system       string
	sysName      string
	sysSwitch    *audio.SwitchCapturer
	auto         *meetingaudio.Auto
	followCancel context.CancelFunc

	// nextMic and nextSystem replace the configured sources for the next
	// recording only: set_source for a change the recording in progress
	// couldn't take.
	nextMic, nextSystem *string

	// silent is a source in use with no signal for a while and suggest a
	// source heard instead (sourcecheck.go).
	silent, suggest *control.Source
	probing         map[string]bool
	lastProbe       map[string]time.Time
}

// beginLocked records a new recording's sources.
func (s *sourceState) beginLocked(ctx context.Context, c *live.Coordinator, mic string, micSwitch *audio.SwitchCapturer, system string, sysSwitch *audio.SwitchCapturer, auto *meetingaudio.Auto, followCancel context.CancelFunc) {
	*s = sourceState{
		recording: true, ctx: ctx, coordinator: c,
		mic: mic, micSwitch: micSwitch,
		system: system, sysName: meetingaudio.SourceName(system), sysSwitch: sysSwitch, auto: auto, followCancel: followCancel,
		nextMic: s.nextMic, nextSystem: s.nextSystem,
		probing: map[string]bool{}, lastProbe: map[string]time.Time{},
	}
}

// endLocked forgets the stopped recording's sources, keeping any choice
// for the next recording.
func (s *sourceState) endLocked() {
	if s.followCancel != nil {
		s.followCancel()
	}
	*s = sourceState{nextMic: s.nextMic, nextSystem: s.nextSystem}
}

// systemSelection is a recording's system-audio selection as Status
// reports it: "" means the platform default (the automatic source on
// macOS), and a recording that got no system audio has none.
func systemSelection(selection string, captured bool) string {
	if !captured {
		return meetingaudio.NoSource
	}
	if selection == "" {
		return meetingaudio.AutoSource
	}
	return selection
}

// nextSources are the sources the next recording uses: a choice made for
// it with set_source, else the configured ones.
func (a *Recorder) nextSources() (mic, system string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nextSourcesLocked()
}

func (a *Recorder) nextSourcesLocked() (mic, system string) {
	mic, system = a.cfg.Audio.Device, a.cfg.Meeting.MonitorDevice
	if a.src.nextMic != nil {
		mic = *a.src.nextMic
	}
	if a.src.nextSystem != nil {
		system = *a.src.nextSystem
	}
	if system == "" && runtime.GOOS == "darwin" {
		system = meetingaudio.AutoSource
	}
	return mic, system
}

// micSource is a mic selection as a control.Source: ID "" is the
// system default, "none" no mic.
func micSource(sel string) control.Source {
	switch sel {
	case "":
		return control.Source{Kind: "mic", ID: meetingaudio.NoSource, Name: "No microphone"}
	case "default":
		return control.Source{Kind: "mic", ID: "", Name: "System default"}
	}
	return control.Source{Kind: "mic", ID: sel, Name: sel}
}

// micSelection is a control mic ID as StartSession takes it.
func micSelection(id string) string {
	switch id {
	case "":
		return "default"
	case meetingaudio.NoSource:
		return ""
	}
	return id
}

// Status is the recorder's state now.
func (a *Recorder) Status() control.Status {
	a.refreshHealth(false)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statusLocked()
}

// statusLocked builds Status from what the recorder holds, without
// looking at devices (callers hold mu, often on the recording's path).
func (a *Recorder) statusLocked() control.Status {
	st := control.Status{Version: Version, Recording: a.rec != nil, Busy: a.busy, Restart: slices.Clone(a.restart)}
	if a.currentSess != nil {
		info := control.SessionInfo(livefeed.InfoFor(a.currentSess))
		st.Session, st.StartedAt = &info, a.currentSess.CreatedAt
	}
	if a.src.recording {
		st.Mic = micSource(a.src.mic)
		st.System = control.Source{Kind: "system", ID: a.src.system, Name: a.src.sysName}
		if a.src.auto != nil {
			a.videoHintMu.Lock()
			if a.audioSource != "" {
				st.System.Name = "Meeting app (automatic): " + a.audioSource
			}
			a.videoHintMu.Unlock()
		}
	} else {
		mic, system := a.nextSourcesLocked()
		st.Mic = micSource(mic)
		st.System = control.Source{Kind: "system", ID: system, Name: meetingaudio.SourceName(system)}
	}
	if a.src.silent != nil {
		s := *a.src.silent
		st.Silent = &s
	}
	if a.src.suggest != nil {
		s := *a.src.suggest
		st.Suggest = &s
	}
	st.Health = append(st.Health, a.health...)
	for _, k := range slices.Sorted(maps.Keys(a.notes)) {
		st.Health = append(st.Health, a.notes[k])
	}
	return st
}

// publishStatusLocked sends the state to the live feed. Caller holds mu.
func (a *Recorder) publishStatusLocked() {
	if a.liveFeed != nil {
		a.liveFeed.Status(a.statusLocked())
	}
}

// publishStatus is publishStatusLocked for callers not holding mu.
func (a *Recorder) publishStatus() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.publishStatusLocked()
}

// publishAudioLocked sends the state as an "audio" message: the silent
// source or the suggestion changed. Caller holds mu.
func (a *Recorder) publishAudioLocked() {
	if a.liveFeed != nil {
		a.liveFeed.Audio(a.statusLocked())
	}
}

// setBusy sets Status.Busy and tells the feed.
func (a *Recorder) setBusy(what string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy == what {
		return
	}
	a.busy = what
	a.publishStatusLocked()
}

// setNote sets (or with "" clears) an extra health line.
func (a *Recorder) setNote(key, line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.notes[key] == line {
		return
	}
	if line == "" {
		delete(a.notes, key)
	} else {
		a.notes[key] = line
	}
	a.publishStatusLocked()
}

// healthEvery is how long the tools' health is reused before it's looked
// at again: it reads model folders and permissions.
const healthEvery = 30 * time.Second

// refreshHealth recomputes the health lines if they're older than
// healthEvery (or always, with force), and tells the feed if they
// changed. Never called with mu held: it looks at files and permissions.
func (a *Recorder) refreshHealth(force bool) {
	a.mu.Lock()
	fresh := !force && time.Since(a.healthAt) < healthEvery
	a.mu.Unlock()
	if fresh {
		return
	}
	lines := healthLines(a.Tools())
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := strings.Join(lines, "\n") != strings.Join(a.health, "\n")
	a.health, a.healthAt = lines, time.Now()
	if changed {
		a.publishStatusLocked()
	}
}

// startFromControl starts a recording the detector missed, with the
// given sources (empty: the next recording's).
func (a *Recorder) startFromControl(p control.StartArgs) (control.Status, error) {
	mic, system := a.nextSources()
	if p.Mic != "" {
		mic = micSelection(p.Mic)
	}
	if p.System != "" {
		system = p.System
	}
	if err := a.StartSession(mic, system, "", p.Platform); err != nil {
		return control.Status{}, err
	}
	return a.Status(), nil
}

// listSources is what can be chosen now: the input devices, and the
// system-audio selections (Tomoe's ListAudioSources): the automatic
// meeting app, everything, each app making sound now, or none.
func listSources() control.Sources {
	out := control.Sources{
		Mics:   []control.Source{micSource("default")},
		System: []control.Source{},
	}
	if devices, err := audio.ListDevices(); err == nil {
		for _, d := range devices {
			if d.DeviceType == audio.Input {
				out.Mics = append(out.Mics, micSource(d.Name))
			}
		}
	}
	out.Mics = append(out.Mics, micSource(""))
	if runtime.GOOS == "darwin" {
		out.System = append(out.System,
			control.Source{Kind: "system", ID: meetingaudio.AutoSource, Name: meetingaudio.SourceName(meetingaudio.AutoSource)},
			control.Source{Kind: "system", ID: "everything", Name: "Everything"})
		if active, err := audiosources.ListActive(); err == nil {
			for _, src := range active {
				out.System = append(out.System, control.Source{Kind: "system", ID: strconv.Itoa(src.PID), Name: src.Name})
			}
		}
	}
	out.System = append(out.System, control.Source{Kind: "system", ID: meetingaudio.NoSource, Name: meetingaudio.SourceName(meetingaudio.NoSource)})
	return out
}

// errNextRecording wraps a source change the recording in progress can't
// take: it is kept for the next recording instead.
type errNextRecording struct{ why string }

func (e errNextRecording) Error() string {
	return e.why + "; it will be used from the next recording"
}

// setSource switches one source: on the recording in progress when it
// can be (the switcher moves capture without a gap in the saved audio),
// else for the next recording, which the error then says.
func (a *Recorder) setSource(p control.SetSourceArgs) (control.Status, error) {
	var err error
	switch p.Kind {
	case "mic":
		err = a.setMic(p.ID)
	case "system":
		err = a.setSystem(p.ID)
	default:
		return control.Status{}, fmt.Errorf("unknown source kind %q (mic or system)", p.Kind)
	}
	var next errNextRecording
	if err != nil && !errors.As(err, &next) {
		return control.Status{}, err
	}
	a.mu.Lock()
	if a.src.silent != nil && a.src.silent.Kind == p.Kind {
		a.src.silent, a.src.suggest = nil, nil
		a.publishAudioLocked()
	}
	a.publishStatusLocked()
	a.mu.Unlock()
	if err != nil {
		return control.Status{}, err
	}
	return a.Status(), nil
}

func (a *Recorder) setMic(id string) error {
	sel := micSelection(id)
	a.mu.Lock()
	recording, sw, cur := a.src.recording, a.src.micSwitch, a.src.mic
	if !recording {
		a.src.nextMic = &sel
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if sel == cur {
		return nil
	}
	if sel == "" {
		a.keepForNext(&sel, nil)
		return errNextRecording{"the microphone can't be turned off during a recording"}
	}
	if sw == nil {
		a.keepForNext(&sel, nil)
		return errNextRecording{"this recording started without a microphone"}
	}
	c, err := audio.NewCapturer(sel, audio.Input)
	if err != nil {
		return fmt.Errorf("opening the microphone %q: %w", sel, err)
	}
	if err := sw.Switch(c); err != nil {
		c.Close()
		return fmt.Errorf("switching to the microphone %q: %w", sel, err)
	}
	a.mu.Lock()
	if a.src.micSwitch == sw {
		a.src.mic = sel
	}
	a.mu.Unlock()
	fmt.Printf("mic: now recording from %q\n", micName(sel))
	return nil
}

func (a *Recorder) setSystem(id string) error {
	if id == "" && runtime.GOOS == "darwin" {
		id = meetingaudio.AutoSource
	}
	a.mu.Lock()
	recording, sw, cur, ctx := a.src.recording, a.src.sysSwitch, a.src.system, a.src.ctx
	if !recording {
		a.src.nextSystem = &id
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	if id == cur {
		return nil
	}
	if id == meetingaudio.NoSource {
		a.keepForNext(nil, &id)
		return errNextRecording{"system audio can't be turned off during a recording"}
	}
	if sw == nil {
		a.keepForNext(nil, &id)
		return errNextRecording{"this recording started without system audio"}
	}
	var auto *meetingaudio.Auto
	var err error
	if id == meetingaudio.AutoSource {
		auto, err = meetingaudio.AutoOn(sw)
	} else {
		err = meetingaudio.SwitchTo(sw, id)
	}
	if err != nil {
		return fmt.Errorf("switching system audio to %s: %w", meetingaudio.SourceName(id), err)
	}
	name := meetingaudio.SourceName(id)
	a.mu.Lock()
	if a.src.sysSwitch != sw { // the recording ended meanwhile
		a.mu.Unlock()
		return nil
	}
	if a.src.followCancel != nil {
		a.src.followCancel()
		a.src.followCancel = nil
	}
	a.src.system, a.src.sysName, a.src.auto = id, name, auto
	var followCtx context.Context
	if auto != nil {
		followCtx, a.src.followCancel = context.WithCancel(ctx)
	}
	a.mu.Unlock()
	// The follower is what moves the automatic source on to the meeting
	// app; a fixed choice has none.
	a.followMeetingAudio(followCtx, auto)
	fmt.Printf("system audio: now capturing %s\n", name)
	return nil
}

// keepForNext keeps a source change for the next recording.
func (a *Recorder) keepForNext(mic, system *string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if mic != nil {
		a.src.nextMic = mic
	}
	if system != nil {
		a.src.nextSystem = system
	}
}

// reloadConfig re-reads Tomoe's config with Musubi's [recorder.*] over it
// and applies what can be applied now: speaker clustering tuning, the
// live feed, and everything a recording reads when it starts (sources,
// line cutting, video hints), from the next recording. Settings the
// engines were built with need a restart, which the health lines say.
func (a *Recorder) reloadConfig() (control.Status, error) {
	a.mu.Lock()
	musubi := a.musubiConfig
	a.mu.Unlock()
	next, err := config.LoadRecorder(config.Path(), musubi)
	if err != nil {
		return control.Status{}, err
	}
	a.mu.Lock()
	a.cfg = next
	tracker := a.tracker
	feedOn := a.liveFeed != nil
	a.mu.Unlock()

	if tracker != nil {
		tracker.SetTuning(tuningFromConfig(next))
	}
	switch {
	case next.Meeting.LiveFeed && !feedOn:
		if err := a.startLiveFeed(true); err != nil {
			fmt.Printf("live feed: not started: %v\n", err)
		}
	case !next.Meeting.LiveFeed && feedOn:
		a.stopLiveFeed()
	}
	// Against the config the engines were built with, not the previous
	// reload: changing a setting back needs no restart.
	a.mu.Lock()
	parts := needsRestart(a.bootCfg, next)
	a.restart = parts
	a.mu.Unlock()
	if len(parts) > 0 {
		a.setNote("restart", "Restart the recorder to apply the new "+strings.Join(parts, ", ")+" settings")
	} else {
		a.setNote("restart", "")
	}
	fmt.Println("config: reloaded")
	a.publishStatus()
	return a.Status(), nil
}

// needsRestart names the settings that differ between old and next and
// are only read when the recorder starts.
func needsRestart(old, next *config.Config) []string {
	var parts []string
	if old.Transcription != next.Transcription {
		parts = append(parts, "transcription")
	}
	if old.Meeting.SpeakerModel != next.Meeting.SpeakerModel || old.Meeting.MicVoices != next.Meeting.MicVoices {
		parts = append(parts, "speaker model")
	}
	if old.Meeting.AutoDetect != next.Meeting.AutoDetect {
		parts = append(parts, "meeting detection")
	}
	return parts
}

// sessionDir is a session's folder, refusing anything that isn't a plain
// session ID (no path, no "..").
func sessionDir(id string) (string, error) {
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("bad session ID %q", id)
	}
	return filepath.Join(config.SessionDir(), id), nil
}

// loadSession loads a saved session by its full ID: the store also
// matches a prefix, which an edit or a delete must never do.
func (a *Recorder) loadSession(id string) (*session.Session, error) {
	if _, err := sessionDir(id); err != nil {
		return nil, err
	}
	sess, err := a.store.Load(id)
	if err != nil {
		return nil, err
	}
	if sess.ID != id {
		return nil, fmt.Errorf("no session %q", id)
	}
	return sess, nil
}

// editable loads a saved session for an edit, refusing the recording in
// progress (whose lines are in memory) and one still being saved.
func (a *Recorder) editable(id string) (*session.Session, error) {
	a.mu.Lock()
	recording := a.currentSess != nil && a.currentSess.ID == id
	saving := a.saving[id]
	a.mu.Unlock()
	if recording {
		return nil, fmt.Errorf("this meeting is still recording")
	}
	if saving {
		return nil, fmt.Errorf("this meeting is still being saved; try again in a moment")
	}
	return a.loadSession(id)
}

// sessionForControl is a session as stored, or for the recording in
// progress ("" or its ID) the lines it holds now.
func (a *Recorder) sessionForControl(id string) (*session.Session, error) {
	a.mu.Lock()
	if cur := a.currentSess; cur != nil && (id == "" || id == cur.ID) {
		cp := *cur
		cp.Segments = append([]session.Segment(nil), cur.Segments...)
		cp.VoiceNames = append([]session.VoiceName(nil), cur.VoiceNames...)
		cp.VoiceDoubts = append([]session.VoiceDoubt(nil), cur.VoiceDoubts...)
		a.mu.Unlock()
		return &cp, nil
	}
	a.mu.Unlock()
	if id == "" {
		return nil, errIdle
	}
	return a.loadSession(id)
}

// deleteSession deletes a saved session's folder (audio, looks and
// fingerprints included). The recording in progress and one still being
// saved are refused.
func (a *Recorder) deleteSession(id string) error {
	if _, err := a.editable(id); err != nil {
		return err
	}
	if err := a.store.Delete(id); err != nil {
		return err
	}
	fmt.Printf("session %s: deleted\n", id)
	return nil
}
