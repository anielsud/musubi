package recording

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/models"
)

// writeModelFiles creates each file under root with a few bytes.
func writeModelFiles(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		p := filepath.Join(root, r)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("weights"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func transducerFiles(subdir string) []string {
	var out []string
	for _, f := range []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"} {
		out = append(out, filepath.Join(subdir, f))
	}
	return out
}

func modelByID(t *testing.T, list []control.RecModel, id string) control.RecModel {
	t.Helper()
	for _, m := range list {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no model %q in %+v", id, list)
	return control.RecModel{}
}

// Models found in Tomoe's folder and in the recorder's own both count,
// and in_use follows what each task loads now.
func TestControlModelsListsBothFolders(t *testing.T) {
	c := newControlled(t)
	own, tomoe := config.OwnModelDir(), c.cfg.Transcription.ModelPath
	if own == "" || own == tomoe {
		t.Fatalf("own %q, Tomoe's %q", own, tomoe)
	}
	writeModelFiles(t, tomoe, append(transducerFiles(models.ParakeetSubdir), models.SileroVADFile, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx")...)
	cohere, _ := models.ASRModelByID(models.ASRModelCohere)
	writeModelFiles(t, own, filepath.Join(cohere.Subdir, "encoder.int8.onnx"), filepath.Join(cohere.Subdir, "decoder.int8.onnx"), filepath.Join(cohere.Subdir, "tokens.txt"))

	var list []control.RecModel
	c.must(t, control.OpModels, nil, &list)
	if len(list) != len(models.Catalog()) {
		t.Errorf("%d models, want the catalog's %d", len(list), len(models.Catalog()))
	}
	for _, want := range []struct {
		id                         string
		installed, inUse, selected bool
		key                        string
	}{
		{models.ASRModelCohere, true, true, true, "transcription.model"}, // auto picks it for English, and it's in the own folder
		{models.ASRModelParakeetV3, true, false, true, "transcription.model"},
		{models.ASRModelParakeetV2En, false, false, true, "transcription.model"},
		{models.SpeakerModelEres2NetEn, true, true, true, "meeting.speaker_model"},
		{models.SpeakerModelEres2Net, false, false, true, "meeting.speaker_model"},
		{models.VADModelID, true, true, false, ""},
		{models.SegmentationModelID, false, false, false, ""},
		{models.LiveFastConformer, false, false, false, ""},
	} {
		m := modelByID(t, list, want.id)
		if m.Installed != want.installed || m.InUse != want.inUse || m.Selectable != want.selected || m.ConfigKey != want.key {
			t.Errorf("%s = %+v", want.id, m)
		}
		if m.Installed != (m.Size > 0) {
			t.Errorf("%s: installed %v with size %d", want.id, m.Installed, m.Size)
		}
	}
	if m := modelByID(t, list, models.SegmentationModelID); m.Restart {
		t.Error("segmentation is read per meeting, yet marked for a restart")
	}
	if m := modelByID(t, list, models.ASRModelCohere); !m.Restart || m.Task != models.TaskTranscription {
		t.Errorf("cohere %+v", m)
	}
}

// fakeFetch serves bodies by URL and records what was asked.
type fakeFetch struct {
	mu     sync.Mutex
	bodies map[string][]byte
	asked  []string
	hold   chan struct{} // if set, each fetch waits for it
}

func (f *fakeFetch) fetch(url string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	f.asked = append(f.asked, url)
	body, ok := f.bodies[url]
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if !ok {
		return nil, 0, errors.New("HTTP 404")
	}
	return io.NopCloser(bytes.NewReader(body)), int64(len(body)), nil
}

// waitIdle waits until no download is running.
func waitIdle(t *testing.T, c *controlled) control.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st control.Status
		c.must(t, control.OpStatus, nil, &st)
		fixMu.Lock()
		running := len(fixRunning) > 0
		fixMu.Unlock()
		if !running && st.Busy == "" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("still busy: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A download goes into the recorder's own folder, never Tomoe's, and a
// failed one leaves nothing that looks installed.
func TestControlDownloadModel(t *testing.T) {
	c := newControlled(t)
	own, tomoe := config.OwnModelDir(), c.cfg.Transcription.ModelPath
	writeModelFiles(t, tomoe, "unrelated.onnx")
	before, _ := os.ReadDir(tomoe)
	f := &fakeFetch{bodies: map[string][]byte{models.SileroVADURL: []byte("vad weights")}}
	c.modelFetch = f.fetch

	c.must(t, control.OpDownloadModel, control.ModelArgs{ID: models.VADModelID}, nil)
	waitIdle(t, c)
	if b, err := os.ReadFile(filepath.Join(own, models.SileroVADFile)); err != nil || string(b) != "vad weights" {
		t.Fatalf("own folder's VAD = %q, %v", b, err)
	}
	if after, _ := os.ReadDir(tomoe); len(after) != len(before) {
		t.Errorf("Tomoe's folder changed: %v → %v", before, after)
	}
	var list []control.RecModel
	c.must(t, control.OpModels, nil, &list)
	if m := modelByID(t, list, models.VADModelID); !m.Installed || !m.InUse {
		t.Errorf("after the download %+v", m)
	}

	// Already there: nothing fetched.
	n := len(f.asked)
	c.must(t, control.OpDownloadModel, control.ModelArgs{ID: models.VADModelID}, nil)
	if waitIdle(t, c); len(f.asked) != n {
		t.Errorf("fetched again: %v", f.asked)
	}

	// A failure: a health line, no file.
	c.must(t, control.OpDownloadModel, control.ModelArgs{ID: models.SpeakerModelEres2Net}, nil)
	st := waitIdle(t, c)
	if !strings.Contains(strings.Join(st.Health, "\n"), "Downloading ERes2Net base") {
		t.Errorf("health %q lacks the failure", st.Health)
	}
	if _, err := os.Stat(filepath.Join(own, models.SpeakerEmbeddingFile)); !os.IsNotExist(err) {
		t.Errorf("a failed download left a file (%v)", err)
	}
	if left, _ := filepath.Glob(filepath.Join(own, "*.part")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}

	if err := c.call(t, control.OpDownloadModel, control.ModelArgs{ID: "../../etc"}, nil); err == nil {
		t.Error("an unknown model was accepted")
	}
}

// One download at a time; the Tools fix for a model is the same download.
func TestControlDownloadOneAtATime(t *testing.T) {
	c := newControlled(t)
	f := &fakeFetch{bodies: map[string][]byte{models.SileroVADURL: []byte("v"), models.SpeakerEmbeddingURL: []byte("s")}, hold: make(chan struct{})}
	c.modelFetch = f.fetch

	c.must(t, control.OpFixTool, control.ToolArgs{ID: "model-vad"}, nil)
	var st control.Status
	c.must(t, control.OpStatus, nil, &st)
	if !strings.HasPrefix(st.Busy, "Downloading Silero VAD") {
		t.Errorf("busy %q", st.Busy)
	}
	if err := c.call(t, control.OpDownloadModel, control.ModelArgs{ID: models.SpeakerModelEres2Net}, nil); err == nil || !strings.Contains(err.Error(), "another model") {
		t.Errorf("second download: %v", err)
	}
	close(f.hold)
	waitIdle(t, c)
	if _, err := os.Stat(filepath.Join(config.OwnModelDir(), models.SileroVADFile)); err != nil {
		t.Error(err)
	}
}

// A start with nothing downloaded fetches the required models into the
// recorder's own folder, and reports what failed.
func TestDownloadRequired(t *testing.T) {
	c := newControlled(t)
	f := &fakeFetch{bodies: map[string][]byte{}}
	c.modelFetch = f.fetch
	cfg := c.config()
	err := c.downloadRequired(&cfg)
	if err == nil || !strings.Contains(err.Error(), "Parakeet") {
		t.Fatalf("err = %v, want the Parakeet download's failure", err)
	}
	if len(f.asked) == 0 || f.asked[0] != models.ParakeetArchiveURL {
		t.Errorf("asked %v, want the Parakeet archive first", f.asked)
	}
	var st control.Status
	c.must(t, control.OpStatus, nil, &st)
	if st.Busy != "" {
		t.Errorf("still busy: %q", st.Busy)
	}
}
