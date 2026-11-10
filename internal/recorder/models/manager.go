package models

import (
	"archive/tar"
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ProgressFunc reports progress for one download step (a human-readable
// label, e.g. "Parakeet TDT 0.6B v3 INT8"): downloaded/total are bytes
// for that step's transfer so far (total is 0 if the server didn't send
// Content-Length). Called from whatever goroutine is doing the
// download; the CLI drives a terminal progress bar from it, the GUI
// forwards it to the frontend as an event. May be nil.
type ProgressFunc func(step string, downloaded, total int64)

// reportProgress calls onProgress if it isn't nil.
func reportProgress(onProgress ProgressFunc, step string, downloaded, total int64) {
	if onProgress != nil {
		onProgress(step, downloaded, total)
	}
}

// Status is a snapshot of the model folder: where each core model's files
// are expected and whether they are there.
type Status struct {
	ParakeetReady            bool
	VADReady                 bool
	SpeakerEmbeddingReady    bool
	SpeakerSegmentationReady bool
	ParakeetPartial          bool // Parakeet folder holds some of its files, not all
	ModelDir                 string
	EncoderPath              string
	DecoderPath              string
	JoinerPath               string
	TokensPath               string
	VADPath                  string
	SpeakerEmbeddingPath     string
	SpeakerSegmentationPath  string

	// English streaming Zipformer transducer (realtime "pass 1" — see
	// internal/live's two-pass pipeline). Not required for Ready(): its
	// absence just means the live pipeline falls back to VAD+Parakeet
	// only, same as before this existed.
	EnglishStreamingReady       bool
	EnglishStreamingEncoderPath string
	EnglishStreamingDecoderPath string
	EnglishStreamingJoinerPath  string
	EnglishStreamingTokensPath  string
	// EnglishStreamingModelType and EnglishStreamingName describe the
	// configured live model (see LiveModels).
	EnglishStreamingModelType string
	EnglishStreamingName      string
}

// Manager finds, fetches and unpacks the models under one folder.
type Manager struct {
	modelDir string
	live     LiveModel
}

// NewManager creates a Manager for the given model directory, with the
// default live model (see WithLiveModel).
func NewManager(modelDir string) *Manager {
	return &Manager{modelDir: modelDir, live: ResolveLiveModel("")}
}

// WithLiveModel makes the manager check and download the live model id
// (the live_model setting) as its English streaming model.
func (m *Manager) WithLiveModel(id string) *Manager {
	m.live = ResolveLiveModel(id)
	return m
}

// ModelDir is the folder the manager works in.
func (m *Manager) ModelDir() string { return m.modelDir }

// Check looks at the model folder without changing it.
func (m *Manager) Check() *Status {
	parakeetDir := filepath.Join(m.modelDir, ParakeetSubdir)
	englishStreamingDir := filepath.Join(m.modelDir, m.live.Subdir)

	s := &Status{
		ModelDir:                    m.modelDir,
		EncoderPath:                 filepath.Join(parakeetDir, encoderFile),
		DecoderPath:                 filepath.Join(parakeetDir, decoderFile),
		JoinerPath:                  filepath.Join(parakeetDir, joinerFile),
		TokensPath:                  filepath.Join(parakeetDir, tokensFile),
		VADPath:                     filepath.Join(m.modelDir, SileroVADFile),
		SpeakerEmbeddingPath:        filepath.Join(m.modelDir, SpeakerEmbeddingFile),
		SpeakerSegmentationPath:     filepath.Join(m.modelDir, PyannoteSegmentationSubdir, PyannoteSegmentationFile),
		EnglishStreamingEncoderPath: filepath.Join(englishStreamingDir, m.live.Encoder),
		EnglishStreamingDecoderPath: filepath.Join(englishStreamingDir, m.live.Decoder),
		EnglishStreamingJoinerPath:  filepath.Join(englishStreamingDir, m.live.Joiner),
		EnglishStreamingTokensPath:  filepath.Join(englishStreamingDir, m.live.Tokens),
		EnglishStreamingModelType:   m.live.ModelType,
		EnglishStreamingName:        m.live.Name,
	}

	parakeet := []string{s.EncoderPath, s.DecoderPath, s.JoinerPath, s.TokensPath}
	have := 0
	for _, p := range parakeet {
		if fileExists(p) {
			have++
		}
	}
	s.ParakeetReady = have == len(parakeet)
	s.ParakeetPartial = have > 0 && !s.ParakeetReady
	s.VADReady = fileExists(s.VADPath)
	s.SpeakerEmbeddingReady = fileExists(s.SpeakerEmbeddingPath)
	s.SpeakerSegmentationReady = fileExists(s.SpeakerSegmentationPath)
	s.EnglishStreamingReady = allFilesExist(s.EnglishStreamingEncoderPath, s.EnglishStreamingDecoderPath, s.EnglishStreamingJoinerPath, s.EnglishStreamingTokensPath)
	return s
}

// Ready reports whether a meeting can be transcribed at all: Parakeet and
// the speech detector are both there.
func (s *Status) Ready() bool { return s.ParakeetReady && s.VADReady }

// DiarizationReady reports whether the post-meeting diarization pass has
// both of its models.
func (s *Status) DiarizationReady() bool {
	return s.SpeakerEmbeddingReady && s.SpeakerSegmentationReady
}

// String is the multi-line summary `model status` prints.
func (s *Status) String() string {
	state := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	parakeet := state(s.ParakeetReady, "ready", "not downloaded")
	if s.ParakeetPartial {
		parakeet = "incomplete (run 'tomoe model download --force')"
	}
	vad := state(s.VADReady, "ready", "not downloaded")
	speaker := state(s.SpeakerEmbeddingReady, "ready", "not downloaded")
	segmentation := state(s.SpeakerSegmentationReady, "ready", "not downloaded")
	diarization := state(s.DiarizationReady(), "ready", "not ready")
	englishStreaming := "not downloaded"
	if s.EnglishStreamingReady {
		englishStreaming = "ready"
	}
	return fmt.Sprintf("Model dir: %s\nParakeet TDT INT8: %s\nSilero VAD: %s\nSpeaker Embedding: %s\nSpeaker Segmentation: %s\nDiarization: %s\nEnglish Streaming Zipformer (realtime pass): %s",
		s.ModelDir, parakeet, vad, speaker, segmentation, diarization, englishStreaming)
}

// ensureDir makes the model folder if it isn't there yet.
func (m *Manager) ensureDir() error {
	if err := os.MkdirAll(m.modelDir, 0o755); err != nil {
		return fmt.Errorf("creating model directory: %w", err)
	}
	return nil
}

// Download downloads and extracts all required models. If force is
// true, existing models are re-downloaded. onProgress (may be nil) is
// called for each step actually downloaded; a step already present is
// skipped without a call.
func (m *Manager) Download(force bool, onProgress ProgressFunc) error {
	if err := m.ensureDir(); err != nil {
		return err
	}
	have := m.Check()

	if force || !have.ParakeetReady {
		if have.ParakeetPartial {
			// Start from an empty folder so no file from the broken
			// extraction survives next to the fresh ones.
			fmt.Println("Parakeet model is incomplete; removing it and downloading again...")
			_ = os.RemoveAll(filepath.Join(m.modelDir, ParakeetSubdir))
		}
		fmt.Println("Downloading the Parakeet TDT 0.6B v3 INT8 model...")
		if err := m.downloadAndExtractArchive(ParakeetArchiveURL, "Parakeet TDT 0.6B v3 INT8", onProgress); err != nil {
			return fmt.Errorf("Parakeet model: %w", err)
		}
	} else {
		fmt.Println("Parakeet model present; not downloading it.")
	}

	if force || !have.VADReady {
		fmt.Println("Downloading the Silero VAD model...")
		vadPath := have.VADPath
		if err := downloadFile(SileroVADURL, vadPath, "Silero VAD", onProgress); err != nil {
			return fmt.Errorf("Silero VAD model: %w", err)
		}
	} else {
		fmt.Println("Silero VAD model present; not downloading it.")
	}

	if force || !have.SpeakerEmbeddingReady {
		fmt.Println("Downloading the speaker embedding model...")
		speakerPath := have.SpeakerEmbeddingPath
		if err := downloadFile(SpeakerEmbeddingURL, speakerPath, "Speaker Embedding", onProgress); err != nil {
			return fmt.Errorf("speaker embedding model: %w", err)
		}
	} else {
		fmt.Println("Speaker embedding model present; not downloading it.")
	}

	if force || !have.SpeakerSegmentationReady {
		fmt.Println("Downloading the pyannote segmentation model...")
		if err := m.downloadAndExtractArchive(PyannoteSegmentationURL, "Pyannote Speaker Segmentation", onProgress); err != nil {
			return fmt.Errorf("pyannote segmentation model: %w", err)
		}
	} else {
		fmt.Println("Pyannote segmentation model present; not downloading it.")
	}

	if !m.Check().Ready() {
		return errors.New("models still missing after download")
	}
	return nil
}

// DownloadEnglishStreaming downloads the English streaming Zipformer used
// by two-pass transcription's realtime pass. It isn't part of Download:
// two-pass is off by default, so callers fetch it only when it's enabled
// (see config.TranscriptionConfig.TwoPass). If force is true, it's
// re-downloaded even if present.
func (m *Manager) DownloadEnglishStreaming(force bool, onProgress ProgressFunc) error {
	if !force && m.Check().EnglishStreamingReady {
		fmt.Printf("Live model %s already present, skipping.\n", m.live.Name)
		return nil
	}
	fmt.Printf("Downloading the live model, %s (realtime transcription pass)...\n", m.live.Name)
	if err := m.downloadAndExtractArchive(m.live.URL, m.live.Name, onProgress); err != nil {
		return fmt.Errorf("downloading the live model: %w", err)
	}
	if m.live.ID == LiveZipformer {
		pruneEnglishStreaming(filepath.Join(m.modelDir, EnglishStreamingSubdir))
	}
	fmt.Printf("Live model %s downloaded and extracted.\n", m.live.Name)
	return nil
}

// DownloadSpeakerModel fetches just the base speaker embedding model,
// unless it is already there and force is false.
func (m *Manager) DownloadSpeakerModel(force bool, onProgress ProgressFunc) error {
	if err := m.ensureDir(); err != nil {
		return err
	}
	speakerPath := m.Check().SpeakerEmbeddingPath
	if !force && fileExists(speakerPath) {
		fmt.Println("Speaker embedding model present; not downloading it.")
		return nil
	}
	fmt.Println("Downloading the speaker embedding model...")
	if err := downloadFile(SpeakerEmbeddingURL, speakerPath, "Speaker Embedding", onProgress); err != nil {
		return fmt.Errorf("speaker embedding model: %w", err)
	}
	return nil
}

// get requests url and hands back the response only for a 200; the
// caller closes its body.
func get(url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}
	return resp, nil
}

// downloadAndExtractArchive downloads a tar.bz2 archive and extracts it
// to the model directory, reporting step's progress via onProgress.
func (m *Manager) downloadAndExtractArchive(url, step string, onProgress ProgressFunc) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	total := resp.ContentLength
	var downloaded int64
	reader := io.TeeReader(resp.Body, writerFunc(func(p []byte) (int, error) {
		downloaded += int64(len(p))
		reportProgress(onProgress, step, downloaded, total)
		return len(p), nil
	}))
	return extractTarBz2(reader, m.modelDir)
}

// writerFunc adapts a func(p []byte) (int, error) to io.Writer, so
// download progress can be observed via io.TeeReader/io.MultiWriter
// without a dedicated counting type.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// pruneEnglishStreaming removes the fp32 weights the Zipformer archive
// unpacks next to the int8 ones: the live pass never loads them and they
// are most of its size on disk. Files other than .onnx stay.
func pruneEnglishStreaming(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".onnx") {
			continue
		}
		switch name {
		case englishStreamingEncoderFile, englishStreamingDecoderFile, englishStreamingJoinerFile:
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// extractTarBz2 unpacks the directories and regular files of a tar.bz2
// stream under destDir. Links and other entry types are skipped, and any
// entry whose cleaned name still contains ".." fails the whole extraction,
// so an archive can't write outside destDir.
func extractTarBz2(r io.Reader, destDir string) error {
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		name := filepath.Clean(hdr.Name)
		if strings.Contains(name, "..") {
			return fmt.Errorf("tar contains path traversal: %s", hdr.Name)
		}
		target := filepath.Join(destDir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := writeEntry(target, os.FileMode(hdr.Mode), tr); err != nil {
				return err
			}
		}
	}
}

// writeEntry writes one archive member to target, replacing any file there.
func writeEntry(target string, mode os.FileMode, src io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating parent directory for %s: %w", target, err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("creating file %s: %w", target, err)
	}
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("writing file %s: %w", target, err)
	}
	return nil
}

// downloadFile downloads a URL to a local file, reporting step's
// progress via onProgress.
func downloadFile(url, destPath, step string, onProgress ProgressFunc) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer func() { _ = f.Close() }()

	total := resp.ContentLength
	var downloaded int64
	progress := writerFunc(func(p []byte) (int, error) {
		downloaded += int64(len(p))
		reportProgress(onProgress, step, downloaded, total)
		return len(p), nil
	})
	if _, err := io.Copy(io.MultiWriter(f, progress), resp.Body); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	return nil
}

// fileExists is true for anything at path but a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// allFilesExist is true when every path passes fileExists.
func allFilesExist(paths ...string) bool {
	return !slices.ContainsFunc(paths, func(p string) bool { return !fileExists(p) })
}
