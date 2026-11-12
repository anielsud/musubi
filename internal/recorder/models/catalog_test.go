package models

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// serve is a Fetcher answering from bodies by URL (404 otherwise).
func serve(bodies map[string][]byte, asked *[]string) Fetcher {
	return func(url string) (io.ReadCloser, int64, error) {
		*asked = append(*asked, url)
		b, ok := bodies[url]
		if !ok {
			return nil, 0, errors.New("HTTP 404")
		}
		return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
	}
}

// listDir names everything under dir, for checking a folder wasn't touched.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A model is used from whichever folder has all of it, own first.
func TestCheckLooksInFallbackFolders(t *testing.T) {
	own, tomoe := t.TempDir(), t.TempDir()
	touch(t, tomoe, coreModelFiles()...)
	touch(t, own, SileroVADFile)
	s := NewManagerIn([]string{own, tomoe}).Check()
	if !s.Ready() || !s.DiarizationReady() {
		t.Fatalf("models in the fallback folder not found: %+v", s)
	}
	if s.VADPath != filepath.Join(own, SileroVADFile) {
		t.Errorf("VAD from %s, want the own folder's", s.VADPath)
	}
	if filepath.Dir(filepath.Dir(s.EncoderPath)) != tomoe {
		t.Errorf("Parakeet from %s, want Tomoe's", s.EncoderPath)
	}
	// Missing everywhere: expected in the own folder.
	cohere, _ := ASRModelByID(ASRModelCohere)
	if s.ASRModelReady(cohere) || s.ASRModelDir(cohere) != filepath.Join(own, cohere.Subdir) {
		t.Errorf("missing model at %s", s.ASRModelDir(cohere))
	}
	// Half a model in the own folder doesn't hide a whole one in Tomoe's.
	touch(t, own, filepath.Join(ParakeetSubdir, encoderFile))
	if s := NewManagerIn([]string{own, tomoe}).Check(); !s.ParakeetReady || s.ParakeetPartial {
		t.Errorf("partial own copy won: ready %v partial %v", s.ParakeetReady, s.ParakeetPartial)
	}
}

func TestCatalogInstalledAndSize(t *testing.T) {
	own, tomoe := t.TempDir(), t.TempDir()
	touch(t, tomoe, coreModelFiles()...)
	s := NewManagerIn([]string{own, tomoe}).Check()
	for _, c := range Catalog() {
		ok, size := s.Installed(c.ID)
		want := c.ID == ASRModelParakeetV3 || c.ID == VADModelID || c.ID == SpeakerModelEres2Net || c.ID == SegmentationModelID
		if ok != want || (size > 0) != want {
			t.Errorf("%s: installed %v size %d, want %v", c.ID, ok, size, want)
		}
	}
	if ok, size := s.Installed(ASRModelParakeetV3); !ok || size != 4 {
		t.Errorf("parakeet size %d, want its four one-byte files", size)
	}
	seen := map[string]bool{}
	for _, c := range Catalog() {
		if seen[c.ID] {
			t.Errorf("%s twice", c.ID)
		}
		seen[c.ID] = true
		if c.Task == "" || c.Name == "" {
			t.Errorf("%+v", c)
		}
	}
}

// DownloadModel writes the own folder only, stages archives, and skips
// what a fallback folder already has.
func TestDownloadModel(t *testing.T) {
	own, tomoe := t.TempDir(), t.TempDir()
	touch(t, tomoe, SileroVADFile)
	before := listDir(t, tomoe)
	var asked []string
	am, _ := ASRModelByID(ASRModelParakeetV2En)
	archive := tarBz2(t, map[string]string{
		am.Subdir + "/encoder.int8.onnx": "e", am.Subdir + "/decoder.int8.onnx": "d",
		am.Subdir + "/joiner.int8.onnx": "j", am.Subdir + "/tokens.txt": "t",
	})
	m := NewManagerIn([]string{own, tomoe}).WithFetcher(serve(map[string][]byte{am.URL: archive}, &asked))

	if err := m.DownloadModel(VADModelID, nil); err != nil || len(asked) != 0 {
		t.Errorf("VAD in Tomoe's folder: err %v, fetched %v", err, asked)
	}
	var steps []string
	if err := m.DownloadModel(am.ID, func(step string, _, _ int64) { steps = append(steps, step) }); err != nil {
		t.Fatal(err)
	}
	if !m.Check().ASRModelReady(am) || m.Check().ASRModelDir(am) != filepath.Join(own, am.Subdir) {
		t.Errorf("not installed in the own folder: %s", m.Check().ASRModelDir(am))
	}
	if len(steps) == 0 || steps[0] != am.Name {
		t.Errorf("progress steps %v", steps)
	}
	if left, _ := filepath.Glob(filepath.Join(own, ".download-*")); len(left) != 0 {
		t.Errorf("staging folders left: %v", left)
	}

	// A failed archive leaves nothing behind.
	cohere, _ := ASRModelByID(ASRModelCohere)
	if err := m.DownloadModel(cohere.ID, nil); err == nil {
		t.Error("a 404 counted as a download")
	}
	if _, err := os.Stat(filepath.Join(own, cohere.Subdir)); !os.IsNotExist(err) {
		t.Errorf("a failed download left %s (%v)", cohere.Subdir, err)
	}
	if left, _ := filepath.Glob(filepath.Join(own, ".download-*")); len(left) != 0 {
		t.Errorf("staging folders left: %v", left)
	}
	if err := m.DownloadModel("nope", nil); err == nil {
		t.Error("an unknown id was accepted")
	}
	if after := listDir(t, tomoe); len(after) != len(before) {
		t.Errorf("Tomoe's folder changed: %v → %v", before, after)
	}
}

// A forced download writes the own folder, never over the copy it found
// in a fallback folder.
func TestForcedDownloadWritesOwnFolderOnly(t *testing.T) {
	own, tomoe := t.TempDir(), t.TempDir()
	touch(t, tomoe, SpeakerEmbeddingFile)
	var asked []string
	m := NewManagerIn([]string{own, tomoe}).WithFetcher(serve(map[string][]byte{SpeakerEmbeddingURL: []byte("fresh")}, &asked))
	if err := m.DownloadSpeakerModel(true, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(tomoe, SpeakerEmbeddingFile)); string(b) != "x" {
		t.Errorf("Tomoe's copy overwritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(own, SpeakerEmbeddingFile)); string(b) != "fresh" {
		t.Errorf("own copy %q", b)
	}
	if got := m.Check().SpeakerEmbeddingPath; got != filepath.Join(own, SpeakerEmbeddingFile) {
		t.Errorf("used from %s, want the own folder", got)
	}
}
