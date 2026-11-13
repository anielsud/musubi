package models

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The catalog is every model the recorder can use, one entry per model
// the owner can see and download on its own (the Musubi app's Models
// screen): the transcription, live and speaker models, and the speech
// detector, speaker segmentation and text reading models.

// Tasks: what a model does in a recording.
const (
	TaskTranscription = "transcription"
	TaskLive          = "live"
	TaskSpeaker       = "speaker"
	TaskVAD           = "vad"
	TaskSegmentation  = "segmentation"
	TaskOCR           = "ocr"
)

// IDs of the models that have no config choice.
const (
	VADModelID          = "silero-vad"
	SegmentationModelID = "pyannote-segmentation-3"
	OCRModelID          = "ppocrv5-en" // both OCR files, detection and recognition
)

// CatalogModel is one model in the catalog.
type CatalogModel struct {
	ID   string // the config value where the task has a choice
	Name string
	Task string
	// ConfigKey is the recorder setting choosing among the task's models
	// ("" for none).
	ConfigKey string
	// Restart: the recorder loads it when it starts, so a new choice or a
	// fresh download is used from the next restart.
	Restart bool
}

// Catalog lists every model, by task.
func Catalog() []CatalogModel {
	var out []CatalogModel
	for _, m := range ASRModels {
		out = append(out, CatalogModel{ID: m.ID, Name: m.Name, Task: TaskTranscription, ConfigKey: "transcription.model", Restart: true})
	}
	for _, m := range LiveModels {
		out = append(out, CatalogModel{ID: m.ID, Name: m.Name, Task: TaskLive, Restart: true})
	}
	for _, m := range SpeakerModels {
		out = append(out, CatalogModel{ID: m.ID, Name: m.Name, Task: TaskSpeaker, ConfigKey: "meeting.speaker_model", Restart: true})
	}
	return append(out,
		CatalogModel{ID: VADModelID, Name: "Silero VAD (speech detection)", Task: TaskVAD, Restart: true},
		// Read for each meeting as it is saved: no restart.
		CatalogModel{ID: SegmentationModelID, Name: "Pyannote segmentation 3.0", Task: TaskSegmentation},
		CatalogModel{ID: OCRModelID, Name: "PP-OCRv5 text reading (English)", Task: TaskOCR, Restart: true},
	)
}

// CatalogByID returns the catalog entry with id.
func CatalogByID(id string) (CatalogModel, bool) {
	for _, c := range Catalog() {
		if c.ID == id {
			return c, true
		}
	}
	return CatalogModel{}, false
}

// paths are the files a catalog model is made of, where they are found
// (see Manager.Check), and the folder to count for its size ("" to count
// just the files).
func (s *Status) paths(id string) (files []string, folder string) {
	if m, ok := ASRModelByID(id); ok {
		dir := s.ASRModelDir(m)
		for _, f := range m.files() {
			files = append(files, filepath.Join(dir, f))
		}
		return files, dir
	}
	for _, m := range LiveModels {
		if m.ID == id {
			dir := filepath.Join(s.dirFor(m.Subdir, m.Encoder, m.Decoder, m.Joiner, m.Tokens), m.Subdir)
			for _, f := range []string{m.Encoder, m.Decoder, m.Joiner, m.Tokens} {
				files = append(files, filepath.Join(dir, f))
			}
			return files, dir
		}
	}
	if m, ok := SpeakerModelByID(id); ok {
		return []string{s.SpeakerModelPath(m)}, ""
	}
	switch id {
	case VADModelID:
		return []string{filepath.Join(s.dirFor("", SileroVADFile), SileroVADFile)}, ""
	case SegmentationModelID:
		dir := filepath.Join(s.dirFor(PyannoteSegmentationSubdir, PyannoteSegmentationFile), PyannoteSegmentationSubdir)
		return []string{filepath.Join(dir, PyannoteSegmentationFile)}, dir
	case OCRModelID:
		for _, m := range ocrModels {
			files = append(files, s.OCRModelPath(m))
		}
		return files, ""
	}
	return nil, ""
}

// Installed reports whether the catalog model id is downloaded (in any
// of the model folders) and its size on disk.
func (s *Status) Installed(id string) (ok bool, size int64) {
	files, folder := s.paths(id)
	if len(files) == 0 || !allFilesExist(files...) {
		return false, 0
	}
	if folder != "" {
		return true, dirSize(folder)
	}
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			size += info.Size()
		}
	}
	return true, size
}

// dirSize adds up the regular files under dir.
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// DownloadModel downloads the catalog model id into the model folder,
// unless it is already in one of the folders. Archives are unpacked into
// a temporary folder beside it and moved into place only once complete,
// and single files are written to a temporary file first, so a failed
// download never leaves a model that looks installed. Fallback folders
// are never written.
func (m *Manager) DownloadModel(id string, onProgress ProgressFunc) error {
	c, ok := CatalogByID(id)
	if !ok {
		return fmt.Errorf("no model %q", id)
	}
	if done, _ := m.Check().Installed(id); done {
		return nil
	}
	if err := m.ensureDir(); err != nil {
		return err
	}
	fetch := m.fetcher()
	if am, ok := ASRModelByID(id); ok {
		return m.downloadArchiveTo(am.URL, am.Subdir, c.Name, onProgress)
	}
	for _, lm := range LiveModels {
		if lm.ID == id {
			if err := m.downloadArchiveTo(lm.URL, lm.Subdir, c.Name, onProgress); err != nil {
				return err
			}
			if lm.ID == LiveZipformer {
				pruneEnglishStreaming(filepath.Join(m.modelDir, EnglishStreamingSubdir))
			}
			return nil
		}
	}
	if sm, ok := SpeakerModelByID(id); ok {
		return downloadVerifiedWith(fetch, sm.URL, filepath.Join(m.modelDir, sm.File), "", c.Name, onProgress)
	}
	switch id {
	case VADModelID:
		return downloadVerifiedWith(fetch, SileroVADURL, filepath.Join(m.modelDir, SileroVADFile), "", c.Name, onProgress)
	case SegmentationModelID:
		return m.downloadArchiveTo(PyannoteSegmentationURL, PyannoteSegmentationSubdir, c.Name, onProgress)
	case OCRModelID:
		for _, om := range ocrModels {
			if err := downloadVerifiedWith(fetch, om.URL, filepath.Join(m.modelDir, ocrSubdir, om.File), om.SHA256, om.Name, onProgress); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("no download for %q", id)
}

// downloadArchiveTo unpacks the tar.bz2 at url into a temporary folder in
// the model folder and moves its subdir into place, replacing an earlier
// (incomplete) copy there.
func (m *Manager) downloadArchiveTo(url, subdir, step string, onProgress ProgressFunc) error {
	tmp, err := os.MkdirTemp(m.modelDir, ".download-*")
	if err != nil {
		return fmt.Errorf("creating a download folder: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	staged := &Manager{modelDir: tmp, fetch: m.fetch}
	if err := staged.downloadAndExtractArchive(url, step, onProgress); err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}
	src := filepath.Join(tmp, subdir)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return fmt.Errorf("%s: the archive has no %s folder", step, subdir)
	}
	dst := filepath.Join(m.modelDir, subdir)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("%s: removing the earlier copy: %w", step, err)
	}
	return os.Rename(src, dst)
}
