package recording

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/toolpath"
)

// The recorder's health: every external tool, model and (on macOS)
// permission it uses, each with a fix if it's missing, as Tomoe's Tools
// page listed them. The Musubi app shows them on its Status tab.

// Tool groups, in the order the Tools page shows them.
const (
	groupTools       = "Command-line tools"
	groupModels      = "Models"
	groupPermissions = "Permissions"
)

type (
	ToolStatus = control.ToolStatus
	ToolFix    = control.ToolFix
)

// Tools reports every external dependency, model and permission the
// recorder uses, with a fix for anything missing.
func (a *Recorder) Tools() []ToolStatus {
	cfg := a.config()
	tools := []ToolStatus{ffmpegStatus(), diarizeWorkerStatus()}
	tools = append(tools, modelStatuses(&cfg)...)
	tools = append(tools, permissionStatuses()...)
	return tools
}

// healthLines are the problems among tools, one line each, for
// Status.Health.
func healthLines(tools []ToolStatus) []string {
	var out []string
	for _, t := range tools {
		if t.OK {
			continue
		}
		detail, _, _ := strings.Cut(t.Detail, " · ")
		out = append(out, fmt.Sprintf("%s: %s", t.Name, detail))
	}
	return out
}

// fixRunning tracks fixes in progress, so a double click can't start the
// same install twice.
var (
	fixMu      sync.Mutex
	fixRunning = map[string]bool{}
)

// FixTool starts the "action" fix for a tool (see Tools) in the
// background and returns at once. Progress shows in Status.Busy on the
// live feed; a failure becomes a health line until the next try, and
// health is checked again when it ends.
func (a *Recorder) FixTool(id string) error {
	fix, err := a.fixFor(id)
	if err != nil {
		return err
	}
	fixMu.Lock()
	if fixRunning[id] {
		fixMu.Unlock()
		return fmt.Errorf("already running")
	}
	fixRunning[id] = true
	fixMu.Unlock()

	a.setNote("fix-"+id, "")
	a.setBusy("Fixing " + id)
	go func() {
		note, err := fix()
		fixMu.Lock()
		delete(fixRunning, id)
		fixMu.Unlock()
		switch {
		case err != nil:
			a.setNote("fix-"+id, fmt.Sprintf("Fixing %s failed: %v", id, err))
		case note != "":
			a.setNote("fix-"+id, note)
		}
		a.setBusy("")
		a.refreshHealth(true)
	}()
	return nil
}

// fixFor returns the function that runs id's fix, returning an optional
// note for the user. Models are Tomoe's (the recorder only reads its
// model folder), so they have no action here: their fix is a command.
func (a *Recorder) fixFor(id string) (func() (string, error), error) {
	if f := platformFix(a, id); f != nil {
		return f, nil
	}
	return nil, fmt.Errorf("no automatic fix for %q", id)
}

// runStreaming runs cmd, showing each line of its output as the busy
// state for id.
func (a *Recorder) runStreaming(id string, cmd *exec.Cmd) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			a.setBusy("Fixing " + id + ": " + sc.Text())
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	return err
}

func ffmpegStatus() ToolStatus {
	t := ToolStatus{
		ID: "ffmpeg", Name: "ffmpeg", Group: groupTools,
		NeededFor: "Saving meeting audio and cutting voice clips",
	}
	if path, err := toolpath.FFmpeg(); err == nil {
		t.OK, t.Detail = true, path
	} else {
		t.Detail = "Not found: meetings are saved without audio"
		t.Fix = ffmpegFix()
	}
	return t
}

func diarizeWorkerStatus() ToolStatus {
	t := ToolStatus{
		ID: "diarize-worker", Name: "musubi-recorder (diarization worker)", Group: groupTools,
		NeededFor: "Relabeling speakers after a meeting is saved, when diarizing during the meeting is off or fails",
	}
	if path := session.FindDiarizeWorker(); path != "" {
		t.OK, t.Detail = true, path
	} else {
		t.Detail = "Not found next to the recorder or on PATH: speakers aren't relabeled after saving"
		t.Fix = commandFix("Copy rebuild command", "make recorder-app   # run in the musubi source folder")
	}
	return t
}

// modelStatuses lists each model with what it's needed for. They all
// share one fix: Tomoe downloads them, into its own folder.
func modelStatuses(cfg *config.Config) []ToolStatus {
	s := models.NewManager(cfg.Transcription.ModelPath).WithLiveModel(cfg.Transcription.LiveModel).Check()
	fix := commandFix("Copy download command", "tomoe model download   # or Download in Tomoe's Tools page")
	entry := func(id, name, neededFor string, required, ok bool) ToolStatus {
		t := ToolStatus{ID: id, Name: name, Group: groupModels, Required: required, NeededFor: neededFor, OK: ok}
		if ok {
			t.Detail = "Downloaded"
		} else {
			t.Detail = "Not downloaded"
			t.Fix = fix
		}
		return t
	}
	parakeet := entry("model-parakeet", "Parakeet TDT (speech recognition)", "All transcription", true, s.ParakeetReady)
	if s.ParakeetPartial {
		parakeet.Detail = "Incomplete download"
	}
	streaming := entry("model-streaming", "Live model: "+s.EnglishStreamingName, "Live text while people speak (two-pass)", false, s.EnglishStreamingReady)
	if !cfg.Transcription.TwoPass {
		// Only two-pass uses it, and that's off: not missing, just unused.
		streaming.OK, streaming.Fix = true, nil
		if s.EnglishStreamingReady {
			streaming.Detail = "Downloaded (two-pass is off)"
		} else {
			streaming.Detail = "Not needed while two-pass is off"
		}
	}
	list := []ToolStatus{
		parakeet,
		entry("model-vad", "Silero VAD", "Detecting when someone is speaking", true, s.VADReady),
		streaming,
		entry("model-speaker", "Speaker embedding: "+models.SpeakerModels[0].Name, "Telling remote speakers apart in meetings", false, s.SpeakerEmbeddingReady),
	}
	for _, sm := range models.SpeakerModelsNeeded(cfg.Meeting.SpeakerModel, meetingLanguages) {
		if sm.ID != models.SpeakerModels[0].ID {
			list = append(list, entry("model-speaker-"+sm.ID, "Speaker embedding: "+sm.Name, "Telling speakers apart in meetings (speaker_model)", false, s.SpeakerModelReady(sm)))
		}
	}
	for _, am := range models.ASRModelsNeeded(cfg.Transcription.Model, meetingLanguages) {
		list = append(list, entry("model-asr-"+am.ID, "Transcription: "+am.Name, "More accurate transcription (transcription.model)", false, s.ASRModelReady(am)))
	}
	list = append(list,
		entry("model-segmentation", "Speaker segmentation", "Relabeling speakers after a meeting is saved", false, s.SpeakerSegmentationReady),
	)
	if cfg.Meeting.VideoHintsOn() {
		list = append(list, entry("model-ocr", "Text reading (PP-OCRv5)", "Reading names off the meeting window (video hints)", false, s.OCRReady()))
	}
	for i := range list {
		list[i].Detail += " · " + s.ModelDir
	}
	return list
}

// commandFix is a fix the user runs in a terminal.
func commandFix(label, command string) *ToolFix {
	return &ToolFix{Kind: "command", Label: label, Command: command}
}
