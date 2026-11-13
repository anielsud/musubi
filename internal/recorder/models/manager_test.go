package models

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// touch creates each file (and its folders) under root with dummy content.
func touch(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		p := filepath.Join(root, r)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// coreModelFiles are the files Download fetches, relative to the model folder.
func coreModelFiles() []string {
	return []string{
		filepath.Join(ParakeetSubdir, encoderFile),
		filepath.Join(ParakeetSubdir, decoderFile),
		filepath.Join(ParakeetSubdir, joinerFile),
		filepath.Join(ParakeetSubdir, tokensFile),
		SileroVADFile,
		SpeakerEmbeddingFile,
		filepath.Join(PyannoteSegmentationSubdir, PyannoteSegmentationFile),
	}
}

func TestCheckReportsWhatIsOnDisk(t *testing.T) {
	cases := []struct {
		name                       string
		files                      []string
		parakeet, partial, vad, ok bool
		diarize                    bool
	}{
		{name: "empty"},
		{name: "vad only", files: []string{SileroVADFile}, vad: true},
		{
			name:     "parakeet missing joiner",
			files:    []string{filepath.Join(ParakeetSubdir, encoderFile), filepath.Join(ParakeetSubdir, decoderFile), filepath.Join(ParakeetSubdir, tokensFile)},
			parakeet: false, partial: true,
		},
		{name: "everything", files: coreModelFiles(), parakeet: true, vad: true, ok: true, diarize: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			touch(t, dir, c.files...)
			s := NewManager(dir).Check()
			if s.ModelDir != dir {
				t.Errorf("ModelDir = %q, want %q", s.ModelDir, dir)
			}
			if s.ParakeetReady != c.parakeet || s.ParakeetPartial != c.partial || s.VADReady != c.vad {
				t.Errorf("parakeet %v partial %v vad %v; want %v %v %v", s.ParakeetReady, s.ParakeetPartial, s.VADReady, c.parakeet, c.partial, c.vad)
			}
			if s.Ready() != c.ok || s.DiarizationReady() != c.diarize {
				t.Errorf("Ready %v DiarizationReady %v; want %v %v", s.Ready(), s.DiarizationReady(), c.ok, c.diarize)
			}
		})
	}
}

func TestCheckIgnoresDirectoriesNamedLikeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, SileroVADFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if NewManager(dir).Check().VADReady {
		t.Error("a directory counted as the VAD model")
	}
}

func TestStatusStringNamesEachState(t *testing.T) {
	s := &Status{ModelDir: "/m", ParakeetReady: true}
	out := s.String()
	for _, want := range []string{"Model dir: /m", "Parakeet TDT INT8: ready", "Silero VAD: not downloaded", "Diarization: not ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("String() lacks %q:\n%s", want, out)
		}
	}
	if out := (&Status{ParakeetPartial: true}).String(); !strings.Contains(out, "incomplete") {
		t.Errorf("partial Parakeet not reported:\n%s", out)
	}
}

func TestModelDir(t *testing.T) {
	if got := NewManager("/some/where").ModelDir(); got != "/some/where" {
		t.Errorf("ModelDir() = %q", got)
	}
}

// tarBz2 builds an archive of files (name to content), compressed by the
// system bzip2 since the standard library only decompresses.
func tarBz2(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for name, body := range files {
		if d := filepath.Dir(name); d != "." {
			if err := tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bzip2", "-c")
	cmd.Stdin = &raw
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("no bzip2 to build a test archive: %v", err)
	}
	return out
}

func TestExtractTarBz2WritesFiles(t *testing.T) {
	archive := tarBz2(t, map[string]string{"pkg/a.onnx": "alpha", "pkg/sub/b.txt": "beta"})
	dest := t.TempDir()
	if err := extractTarBz2(bytes.NewReader(archive), dest); err != nil {
		t.Fatalf("extractTarBz2: %v", err)
	}
	for rel, want := range map[string]string{"pkg/a.onnx": "alpha", "pkg/sub/b.txt": "beta"} {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "pkg/a.onnx")); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the archive's 0600", info.Mode().Perm())
	}
}

func TestExtractTarBz2RefusesEscapes(t *testing.T) {
	dest := t.TempDir()
	archive := tarBz2(t, map[string]string{"../outside.txt": "no"})
	if err := extractTarBz2(bytes.NewReader(archive), filepath.Join(dest, "in")); err == nil {
		t.Fatal("an entry outside the destination was accepted")
	}
	if _, err := os.Stat(filepath.Join(dest, "outside.txt")); !os.IsNotExist(err) {
		t.Error("the escaping entry was written")
	}
}

func TestDownloadFile(t *testing.T) {
	content := []byte("weights")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	destPath := filepath.Join(t.TempDir(), "nested", "model.onnx")

	var gotProgress []int64
	if err := downloadFile(srv.URL, destPath, "Test Model", func(step string, downloaded, total int64) {
		if step != "Test Model" {
			t.Errorf("progress step = %q, want %q", step, "Test Model")
		}
		if total != int64(len(content)) {
			t.Errorf("progress total = %d, want %d", total, len(content))
		}
		gotProgress = append(gotProgress, downloaded)
	}); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if len(gotProgress) == 0 || gotProgress[len(gotProgress)-1] != int64(len(content)) {
		t.Errorf("final progress = %v, want it to end at %d", gotProgress, len(content))
	}
	if got, err := os.ReadFile(destPath); err != nil || !bytes.Equal(got, content) {
		t.Errorf("file = %q, %v; want %q", got, err, content)
	}
}

func TestDownloadFileHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	destPath := filepath.Join(t.TempDir(), "model.onnx")
	err := downloadFile(srv.URL, destPath, "Test Model", nil)
	if err == nil {
		t.Fatal("a 404 counted as a download")
	}
	if _, statErr := os.Stat(destPath); !os.IsNotExist(statErr) {
		t.Error("a file was left behind for a failed download")
	}
}

func TestDownloadSkipsExistingModels(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	dir := t.TempDir()
	touch(t, dir, coreModelFiles()...)
	m := NewManager(dir)

	err := m.Download(false, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests with every model present, want 0", n)
	}
}

func TestPruneEnglishStreamingKeepsOnlyInt8Model(t *testing.T) {
	modelDir := t.TempDir()
	dir := filepath.Join(modelDir, EnglishStreamingSubdir)
	kept := []string{englishStreamingEncoderFile, englishStreamingDecoderFile, englishStreamingJoinerFile, englishStreamingTokensFile, "LICENSE"}
	fp32 := []string{"encoder-fp32.onnx", "decoder-fp32.onnx", "joiner-fp32.onnx"}
	touch(t, dir, append(append([]string(nil), kept...), fp32...)...)
	if err := os.Mkdir(filepath.Join(dir, "test_wavs.onnx"), 0o755); err != nil {
		t.Fatal(err)
	}

	pruneEnglishStreaming(dir)

	for _, f := range append(kept, "test_wavs.onnx") {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s removed: %v", f, err)
		}
	}
	for _, f := range fp32 {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s kept (%v), want removed", f, err)
		}
	}
	if !NewManager(modelDir).WithLiveModel(LiveZipformer).Check().EnglishStreamingReady {
		t.Error("pruning left the live model incomplete")
	}
}

func TestDownloadVerifiedKeepsOnlyMatchingFiles(t *testing.T) {
	body := []byte("model bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.onnx")
	sum := sha256.Sum256(body)

	if err := downloadVerified(srv.URL, dest, hex.EncodeToString(sum[:]), "test", nil); err != nil {
		t.Fatalf("matching checksum: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Fatalf("kept %q, want %q", got, body)
	}

	other := filepath.Join(dir, "bad.onnx")
	if err := downloadVerified(srv.URL, other, strings.Repeat("0", 64), "test", nil); err == nil {
		t.Fatal("a checksum mismatch was accepted")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Error("a file with the wrong checksum was left in place")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}
