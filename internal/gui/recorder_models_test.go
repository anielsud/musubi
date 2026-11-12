package gui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/control"
)

var testRecModels = []control.RecModel{
	{ID: "cohere-transcribe", Name: "Cohere Transcribe INT8", Task: "transcription", Installed: true, Size: 3 << 30, Selectable: true, InUse: true, ConfigKey: "transcription.model", Restart: true},
	{ID: "parakeet-v2-en", Name: "Parakeet v2", Task: "transcription", Selectable: true, ConfigKey: "transcription.model", Restart: true},
	{ID: "eres2net-en", Name: "ERes2Net English", Task: "speaker", Installed: true, Size: 25 << 20, Selectable: true, InUse: true, ConfigKey: "meeting.speaker_model", Restart: true},
	{ID: "silero-vad", Name: "Silero VAD", Task: "vad", Installed: true, Size: 600 << 10, InUse: true, Restart: true},
	{ID: "pyannote-segmentation-3", Name: "Pyannote", Task: "segmentation"},
}

func TestONNXProviderAndRows(t *testing.T) {
	a, f, _, _ := testApp(t, "musubi")
	if p := a.onnxProvider(); p.Ready || p.Error != "the recorder isn't running" || p.Kind != "onnx" {
		t.Errorf("no recorder: %+v", p)
	}
	f.results[control.OpModels] = testRecModels
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	p := a.onnxProvider()
	if !p.Ready || len(p.Models) != 3 || strings.Join(p.UsedBy, ",") != "Transcription,Speaker voices,Speech detection" {
		t.Errorf("provider %+v", p)
	}
	if r := a.TestProvider("onnx"); !r.OK || r.Models != 3 {
		t.Errorf("test %+v", r)
	}
	rows := onnxRows(testRecModels)
	if len(rows) != 5 || rows[0].Source != "onnx" || rows[0].Kind != "recording" || rows[0].Size != 3<<30 || rows[0].Missing || len(rows[0].UsedBy) != 1 {
		t.Errorf("rows %+v", rows[0])
	}
	if !rows[1].Missing || rows[1].UsedBy != nil || rows[4].Restart {
		t.Errorf("rows %+v %+v", rows[1], rows[4])
	}
	if err := a.SaveProvider("onnx", "https://example.test", nil, ""); err == nil {
		t.Error("a provider took the onnx name")
	}
}

func TestDownloadRecorderModel(t *testing.T) {
	a, f, _, _ := testApp(t, "tomoe")
	if err := a.DownloadRecorderModel("parakeet-v2-en"); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Errorf("no recorder: %v", err)
	}
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.DownloadRecorderModel("parakeet-v2-en"); err != nil {
		t.Fatal(err)
	}
	var args control.ModelArgs
	json.Unmarshal(f.last().Args, &args)
	if f.last().Op != control.OpDownloadModel || args.ID != "parakeet-v2-en" {
		t.Errorf("asked %s %+v", f.last().Op, args)
	}
	for _, bad := range []string{"", "../x", "a b", "Parakeet"} {
		if err := a.DownloadRecorderModel(bad); err == nil {
			t.Errorf("%q sent", bad)
		}
	}
}

// The model choices moved from Recording to Processing, and are written
// to the same keys.
func TestRecordingModelsInProcessing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, f, _, _ := testApp(t, "musubi")
	os.WriteFile(a.cfgPath, []byte("[recorder]\ndata = \""+a.eng.Cfg.Recorder.Data+"\"\n"), 0o600)
	rm, err := a.RecordingModels()
	if err != nil {
		t.Fatal(err)
	}
	if rm.Error != "the recorder isn't running" || len(rm.Fields) != 2 || rm.Fields[0].Value != "auto" || rm.Fields[0].Set {
		t.Errorf("no recorder: %+v", rm)
	}
	f.results[control.OpModels] = testRecModels
	f.serve(t, a.eng.Cfg.RecorderControlSocket())
	if err := a.SetRecordingSetting("transcription", "model", "parakeet-v2-en"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRecordingSetting("meeting", "speaker_model", "eres2net-base"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRecordingSetting("transcription", "model", "whisper"); err == nil {
		t.Error("an unknown model was saved")
	}
	b, _ := os.ReadFile(a.cfgPath)
	if !strings.Contains(string(b), "[recorder.transcription]\nmodel = \"parakeet-v2-en\"") || !strings.Contains(string(b), "[recorder.meeting]\nspeaker_model = \"eres2net-base\"") {
		t.Errorf("config:\n%s", b)
	}
	if ops := f.ops(); len(ops) < 2 || ops[len(ops)-1] != control.OpReloadConfig {
		t.Errorf("recorder asked %v", ops)
	}
	rm, _ = a.RecordingModels()
	tr := rm.Fields[0]
	if rm.Error != "" || tr.Value != "parakeet-v2-en" || !tr.Set || tr.InUse != "cohere-transcribe" || strings.Join(tr.Missing, ",") != "parakeet-v2-en" || !tr.Restart {
		t.Errorf("transcription %+v", tr)
	}
	rs, err := a.RecordingSettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range rs.Groups {
		for _, fl := range g.Fields {
			if (fl.Table == "transcription" && fl.Key == "model") || fl.Key == "speaker_model" {
				t.Errorf("%s.%s still in Settings → Recording", fl.Table, fl.Key)
			}
		}
	}
}
