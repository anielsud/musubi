package gui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
	recmodels "github.com/anielsud/musubi/internal/recorder/models"
)

// ONNX is a provider: the recorder's models (transcription, live text,
// speaker voices, speech detection, segmentation, text reading), run in
// the recorder's process by sherpa-onnx and ONNX Runtime. Settings → AI
// providers shows it beside Ollama, Settings → Models lists its models
// and downloads missing ones through the recorder (which writes its own
// model folder; Musubi never does), and Settings → Processing chooses
// the transcription and speaker voice models.

const onnxSource = "onnx"

// recTaskUse is what each recorder task is shown as "used by".
var recTaskUse = map[string]string{
	recmodels.TaskTranscription: "Transcription",
	recmodels.TaskLive:          "Live text",
	recmodels.TaskSpeaker:       "Speaker voices",
	recmodels.TaskVAD:           "Speech detection",
	recmodels.TaskSegmentation:  "Speakers after the meeting",
	recmodels.TaskOCR:           "Reading names",
}

// recModels asks the recorder for its models. Short: Settings waits on it.
func (a *App) recModels() ([]control.RecModel, error) {
	c, err := a.recClient()
	if err != nil {
		return nil, err
	}
	c.Timeout = 5 * time.Second
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var out []control.RecModel
	return out, c.Call(ctx, control.OpModels, nil, &out)
}

// recModelsError is why the recorder's models aren't listed.
func recModelsError(err error) string {
	if errors.Is(err, control.ErrNotRunning) {
		return "the recorder isn't running"
	}
	return err.Error()
}

// onnxRows are the recorder's models as Models rows.
func onnxRows(list []control.RecModel) []ModelRow {
	out := make([]ModelRow, 0, len(list))
	for _, m := range list {
		r := ModelRow{Source: onnxSource, ID: m.ID, Name: m.Name, Kind: "recording", Formats: []string{onnxSource},
			Task: m.Task, Size: m.Size, Missing: !m.Installed, Restart: m.Restart}
		if m.InUse {
			r.UsedBy = []string{recTaskUse[m.Task]}
		}
		out = append(out, r)
	}
	return out
}

// onnxProvider is the ONNX card: ready when the recorder answers.
func (a *App) onnxProvider() ProviderView {
	p := ProviderView{Name: onnxSource, Kind: onnxSource, Title: "ONNX (this Mac)", Formats: []string{onnxSource}}
	if a.eng != nil {
		p.URL = a.eng.Cfg.RecorderControlSocket()
	}
	list, err := a.recModels()
	if err != nil {
		p.Error = recModelsError(err)
		return p
	}
	p.Ready = true
	p.Models = []string{}
	for _, m := range list {
		if m.Installed {
			p.Models = append(p.Models, m.Name)
		}
		if m.InUse {
			p.UsedBy = append(p.UsedBy, recTaskUse[m.Task])
		}
	}
	return p
}

var recModelIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// DownloadRecorderModel asks the recorder to download one of its models
// into its own folder. It returns once the download has started; the
// recorder's status ("recorder:status" events, Busy) shows progress.
func (a *App) DownloadRecorderModel(id string) error {
	if !recModelIDRe.MatchString(id) {
		return fmt.Errorf("model %q", id)
	}
	if err := a.recCall(control.OpDownloadModel, control.ModelArgs{ID: id}, nil); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return fmt.Errorf("the recorder isn't running, so it can't download its models")
		}
		return err
	}
	return nil
}

// recModelFields are the recorder's model choices, offered in Settings →
// Processing (not Recording) and written like the Recording settings:
// [recorder.transcription] model and [recorder.meeting] speaker_model.
var recModelFields = []RecField{
	{Restart: true, Table: "transcription", Key: "model", Label: "Transcription", Type: "select",
		Help: "Automatic uses Cohere Transcribe for English, the most accurate there; Parakeet is about 3x faster.", Options: asrOptions()},
	{Restart: true, Table: "meeting", Key: "speaker_model", Label: "Speaker voices", Type: "select",
		Help: "Tells voices apart, live and after the meeting.", Options: speakerOptions()},
}

func asrOptions() []RecOption {
	out := []RecOption{{recmodels.ASRModelAuto, "Automatic (Cohere Transcribe for English)"}}
	for _, m := range recmodels.ASRModels {
		out = append(out, RecOption{m.ID, m.Name})
	}
	return out
}

func speakerOptions() []RecOption {
	out := []RecOption{{recmodels.SpeakerModelAuto, "Automatic (English-trained for English)"}}
	for _, m := range recmodels.SpeakerModels {
		out = append(out, RecOption{m.ID, m.Name})
	}
	return out
}

// RecModelField is a model choice with what the recorder says of each
// option (when it answers).
type RecModelField struct {
	RecField
	Missing []string `json:"missing,omitempty"` // options not downloaded
	InUse   string   `json:"inUse,omitempty"`   // the model the task loads now
}

// RecordingModels is Processing's Recording group.
type RecordingModels struct {
	Fields []RecModelField `json:"fields"`
	// Error: why the recorder didn't say which models are downloaded.
	Error string `json:"error,omitempty"`
}

func (a *App) RecordingModels() (*RecordingModels, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("no dataset open")
	}
	tc, _ := tomoeConfig()
	out := &RecordingModels{}
	list, err := a.recModels()
	if err != nil {
		out.Error = recModelsError(err)
	}
	for _, f := range recModelFields {
		if v, ok := overrides(a.eng.Cfg.Recorder, f.Table)[f.Key]; ok {
			f.Value, f.Set = v, true
		} else {
			f.Value = tomlField(tc, f.Table, f.Key)
		}
		mf := RecModelField{RecField: f}
		for _, m := range list {
			if m.ConfigKey != f.Table+"."+f.Key {
				continue
			}
			if !m.Installed {
				mf.Missing = append(mf.Missing, m.ID)
			}
			if m.InUse {
				mf.InUse = m.ID
			}
		}
		out.Fields = append(out.Fields, mf)
	}
	return out, nil
}
