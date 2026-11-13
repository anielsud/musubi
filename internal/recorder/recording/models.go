package recording

import (
	"fmt"
	"slices"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/models"
)

// The recorder's models as the Musubi app lists them (OpModels), and
// downloading one (OpDownloadModel). Downloads go to the recorder's own
// model folder (config.OwnModelDir); models already in Tomoe's folder
// are used from there and never downloaded again.

// modelManager finds the models cfg uses: the recorder's own folder
// first, then Tomoe's (config.ModelDirs).
func modelManager(cfg *config.Config) *models.Manager {
	return models.NewManagerIn(config.ModelDirs(cfg)).WithLiveModel(cfg.Transcription.LiveModel)
}

// modelUse says, for each catalog model, whether recordings use it now
// (inUse: what each task actually loads, after any fallback for a model
// not downloaded) and whether the config asks for it (wanted: what the
// task would load once everything is downloaded).
func modelUse(cfg *config.Config, s *models.Status) (inUse, wanted map[string]bool) {
	inUse, wanted = map[string]bool{}, map[string]bool{}
	for _, lang := range meetingLanguages {
		m, _ := s.ASRModelFor(cfg.Transcription.Model, lang)
		inUse[m.ID] = true
		wanted[models.ResolveASRModel(cfg.Transcription.Model, lang).ID] = true
		sm, _, _ := s.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang)
		inUse[sm.ID] = true
		wanted[models.ResolveSpeakerModel(cfg.Meeting.SpeakerModel, lang).ID] = true
	}
	if cfg.Transcription.TwoPass {
		id := models.ResolveLiveModel(cfg.Transcription.LiveModel).ID
		wanted[id] = true
		inUse[id] = s.EnglishStreamingReady
	}
	for _, id := range []string{models.VADModelID, models.SegmentationModelID} {
		wanted[id], inUse[id] = true, true
	}
	if cfg.Meeting.VideoHintsOn() {
		wanted[models.OCRModelID], inUse[models.OCRModelID] = true, true
	}
	// A model not downloaded isn't in use, whatever the config says.
	for id := range inUse {
		if ok, _ := s.Installed(id); !ok {
			delete(inUse, id)
		}
	}
	return inUse, wanted
}

// Models lists every model the recorder can use, with whether it is
// downloaded and in use.
func (a *Recorder) Models() []control.RecModel {
	cfg := a.config()
	s := modelManager(&cfg).Check()
	inUse, _ := modelUse(&cfg, s)
	var out []control.RecModel
	for _, c := range models.Catalog() {
		ok, size := s.Installed(c.ID)
		out = append(out, control.RecModel{
			ID: c.ID, Name: c.Name, Task: c.Task, Installed: ok, Size: size,
			Selectable: c.ConfigKey != "", InUse: inUse[c.ID], ConfigKey: c.ConfigKey, Restart: c.Restart,
		})
	}
	return out
}

// DownloadModel starts downloading a catalog model into the recorder's
// own folder and returns at once. Progress shows in Status.Busy on the
// live feed; a failure becomes a health line until the next try. One
// download runs at a time.
func (a *Recorder) DownloadModel(id string) error {
	c, ok := models.CatalogByID(id)
	if !ok {
		return fmt.Errorf("no model %q", id)
	}
	cfg := a.config()
	if config.OwnModelDir() == "" {
		// Offline commands run in Tomoe's folder, which the recorder
		// never writes.
		return fmt.Errorf("the recorder has no model folder of its own")
	}
	mgr := modelManager(&cfg).WithFetcher(a.modelFetch)
	if done, _ := mgr.Check().Installed(id); done {
		return nil
	}
	fixMu.Lock()
	for k := range fixRunning {
		if strings.HasPrefix(k, "model:") {
			fixMu.Unlock()
			return fmt.Errorf("another model is downloading; try again when it finishes")
		}
	}
	key := "model:" + id
	fixRunning[key] = true
	fixMu.Unlock()

	a.setNote(key, "")
	a.setBusy("Downloading " + c.Name)
	go func() {
		last := -1
		err := mgr.DownloadModel(id, func(step string, done, total int64) {
			if total <= 0 {
				return
			}
			if pct := int(100 * done / total); pct != last {
				last = pct
				a.setBusy(fmt.Sprintf("Downloading %s: %d%% of %d MB", step, pct, total>>20))
			}
		})
		fixMu.Lock()
		delete(fixRunning, key)
		fixMu.Unlock()
		switch {
		case err != nil:
			a.setNote(key, fmt.Sprintf("Downloading %s failed: %v", c.Name, err))
			fmt.Printf("models: %s: %v\n", id, err)
		case c.Restart:
			if _, wanted := modelUse(&cfg, mgr.Check()); wanted[id] {
				a.setNote(key, "Restart the recorder to start using "+c.Name)
			}
			fmt.Printf("models: %s downloaded\n", id)
		default:
			fmt.Printf("models: %s downloaded\n", id)
		}
		a.setBusy("")
		a.refreshHealth(true)
	}()
	return nil
}

// toolModel is the catalog model a Tools entry's ID stands for
// (modelStatuses), for its Download fix.
func toolModel(toolID string, cfg *config.Config) (string, bool) {
	switch toolID {
	case "model-parakeet":
		return models.ASRModelParakeetV3, true
	case "model-vad":
		return models.VADModelID, true
	case "model-streaming":
		return models.ResolveLiveModel(cfg.Transcription.LiveModel).ID, true
	case "model-speaker":
		return models.SpeakerModels[0].ID, true
	case "model-segmentation":
		return models.SegmentationModelID, true
	case "model-ocr":
		return models.OCRModelID, true
	}
	for _, p := range []string{"model-speaker-", "model-asr-"} {
		if id, ok := strings.CutPrefix(toolID, p); ok && slices.ContainsFunc(models.Catalog(), func(c models.CatalogModel) bool { return c.ID == id }) {
			return id, true
		}
	}
	return "", false
}

// downloadRequired downloads the models a recording can't start without
// (models.Manager.Download) into the recorder's own folder, showing the
// progress as Status.Busy. Better models for a task (Settings → Models)
// are the owner's to download.
func (a *Recorder) downloadRequired(cfg *config.Config) error {
	defer a.setBusy("")
	last := ""
	return modelManager(cfg).WithFetcher(a.modelFetch).Download(false, func(step string, done, total int64) {
		if total <= 0 {
			return
		}
		if msg := fmt.Sprintf("Downloading %s: %d%% of %d MB", step, 100*done/total, total>>20); msg != last {
			last = msg
			a.setBusy(msg)
		}
	})
}
