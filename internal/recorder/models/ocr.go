package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// OCRModel is one of the text detection/recognition models video hints use
// to read names off the meeting window (internal/ocr).
type OCRModel struct {
	Name   string
	File   string
	URL    string
	SHA256 string
}

// ocrBaseURL pins RapidOCR's v3.9.2 release of the PaddleOCR PP-OCRv5 models
// in ONNX form (Apache-2.0). The checksums are the ones RapidOCR publishes
// for these files (python/rapidocr/default_models.yaml).
const ocrBaseURL = "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.9.2/onnx/PP-OCRv5/"

// ocrSubdir holds the OCR models under the model directory.
const ocrSubdir = "ocr-ppocrv5"

// OCR models: text detection (finds the lines) and English recognition
// (reads one line).
var (
	OCRDetector = OCRModel{
		Name:   "PP-OCRv5 text detection (mobile)",
		File:   "ch_PP-OCRv5_det_mobile.onnx",
		URL:    ocrBaseURL + "det/ch_PP-OCRv5_det_mobile.onnx",
		SHA256: "4d97c44a20d30a81aad087d6a396b08f786c4635742afc391f6621f5c6ae78ae",
	}
	OCRRecognizer = OCRModel{
		Name:   "PP-OCRv5 English text recognition (mobile)",
		File:   "en_PP-OCRv5_rec_mobile.onnx",
		URL:    ocrBaseURL + "rec/en_PP-OCRv5_rec_mobile.onnx",
		SHA256: "c3461add59bb4323ecba96a492ab75e06dda42467c9e3d0c18db5d1d21924be8",
	}
	ocrModels = []OCRModel{OCRDetector, OCRRecognizer}
)

// OCRModelPath is where m lives in the model directory.
func (s *Status) OCRModelPath(m OCRModel) string {
	return filepath.Join(s.dirFor(ocrSubdir, m.File), ocrSubdir, m.File)
}

// OCRReady reports whether both OCR models are present.
func (s *Status) OCRReady() bool {
	for _, m := range ocrModels {
		if !fileExists(s.OCRModelPath(m)) {
			return false
		}
	}
	return true
}

// DownloadOCRModels downloads the OCR models that aren't present (all of
// them if force), each checked against its pinned SHA256 before it's kept.
func (m *Manager) DownloadOCRModels(force bool, onProgress ProgressFunc) error {
	status := m.Check()
	for _, om := range ocrModels {
		if !force && fileExists(status.OCRModelPath(om)) {
			continue
		}
		fmt.Printf("Downloading %s...\n", om.Name)
		path := filepath.Join(m.modelDir, ocrSubdir, om.File)
		if err := downloadVerifiedWith(m.fetcher(), om.URL, path, om.SHA256, om.Name, onProgress); err != nil {
			return fmt.Errorf("downloading %s: %w", om.Name, err)
		}
	}
	return nil
}

// downloadVerified downloads url to destPath through a temporary file,
// hashing as it writes, and only moves it into place if its SHA256 is
// want: a truncated, tampered or substituted file is never left looking
// like a downloaded model. want "" checks nothing but still never leaves
// a partial file in place.
func downloadVerified(url, destPath, want, step string, onProgress ProgressFunc) error {
	return downloadVerifiedWith(httpFetch, url, destPath, want, step, onProgress)
}

// downloadVerifiedWith is downloadVerified through fetch.
func downloadVerifiedWith(fetch Fetcher, url, destPath, want, step string, onProgress ProgressFunc) error {
	body, total, err := fetch(url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), filepath.Base(destPath)+".*.part")
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed

	hash := sha256.New()
	var downloaded int64
	progress := writerFunc(func(p []byte) (int, error) {
		downloaded += int64(len(p))
		reportProgress(onProgress, step, downloaded, total)
		return len(p), nil
	})
	_, copyErr := io.Copy(io.MultiWriter(tmp, hash, progress), body)
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("writing file: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("writing file: %w", closeErr)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); want != "" && got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s (not kept)", url, got, want)
	}
	return os.Rename(tmpPath, destPath)
}
