package speaker

import (
	"errors"
	"fmt"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/anielsud/musubi/internal/recorder/sigfix"
)

// The speaker models all take 16 kHz mono audio.
const embeddingSampleRate = 16000

// Embedder turns a stretch of audio into a speaker fingerprint with one of
// sherpa-onnx's speaker models.
type Embedder struct {
	extractor *sherpa.SpeakerEmbeddingExtractor
}

// NewEmbedder loads the speaker model at modelPath, on the CPU with one
// thread: fingerprints are short and run beside transcription.
func NewEmbedder(modelPath string) (*Embedder, error) {
	x := sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{
		Model:      modelPath,
		NumThreads: 1,
		Provider:   "cpu",
	})
	sigfix.AfterSherpa()
	if x == nil {
		return nil, fmt.Errorf("speaker model %s did not load", modelPath)
	}
	return &Embedder{extractor: x}, nil
}

// Extract fingerprints samples (16 kHz mono). Too little audio for the
// model is an error rather than a weak fingerprint.
func (e *Embedder) Extract(samples []float32) ([]float32, error) {
	if len(samples) == 0 {
		return nil, errors.New("speaker fingerprint: no audio")
	}
	s := e.extractor.CreateStream()
	if s == nil {
		return nil, errors.New("speaker fingerprint: no stream")
	}
	defer sherpa.DeleteOnlineStream(s)
	s.AcceptWaveform(embeddingSampleRate, samples)
	s.InputFinished()
	if !e.extractor.IsReady(s) {
		return nil, errors.New("speaker fingerprint: too little audio")
	}
	v := e.extractor.Compute(s)
	if len(v) == 0 {
		return nil, errors.New("speaker fingerprint: model returned nothing")
	}
	return v, nil
}

// Dim is the length of the model's fingerprints.
func (e *Embedder) Dim() int { return e.extractor.Dim() }

// Close frees the model; Close twice is safe.
func (e *Embedder) Close() {
	if e.extractor == nil {
		return
	}
	sherpa.DeleteSpeakerEmbeddingExtractor(e.extractor)
	e.extractor = nil
}

// EmbedderSet holds an Embedder per model file, loaded on first use, so
// each meeting can use the speaker model chosen for its language.
type EmbedderSet struct {
	mu     sync.Mutex
	byPath map[string]*Embedder
}

// NewEmbedderSet returns an empty set.
func NewEmbedderSet() *EmbedderSet {
	return &EmbedderSet{byPath: map[string]*Embedder{}}
}

// Get returns the Embedder for modelPath, loading it if needed.
func (s *EmbedderSet) Get(modelPath string) (*Embedder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.byPath[modelPath]; ok {
		return e, nil
	}
	e, err := NewEmbedder(modelPath)
	if err != nil {
		return nil, err
	}
	s.byPath[modelPath] = e
	return e, nil
}

// Len is how many models are loaded.
func (s *EmbedderSet) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byPath)
}

// Close releases every loaded Embedder.
func (s *EmbedderSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, e := range s.byPath {
		e.Close()
		delete(s.byPath, p)
	}
}
