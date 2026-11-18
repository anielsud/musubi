package transcribe

import (
	"errors"
	"fmt"
	"os"
	"strings"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/anielsud/musubi/internal/recorder/sigfix"
)

// sampleRate is the rate every model here takes, in Hz.
const sampleRate = 16000

// parakeetEngine is the Engine for any sherpa-onnx offline recognizer:
// Parakeet, Cohere Transcribe and the candidate models all decode through
// it. vadConfig is nil when TranscribeSamples should not cut at pauses.
type parakeetEngine struct {
	recognizer *sherpa.OfflineRecognizer
	vadConfig  *sherpa.VadModelConfig
	// decodeFn, when set, decodes in place of the recognizer alone (the
	// Whisper language guard); closeFn frees what it holds.
	decodeFn func([]float32) (*sherpa.OfflineRecognizerResult, error)
	closeFn  func()
}

// NewEngine loads the NeMo transducer at cfg's paths on the CPU.
func NewEngine(cfg Config) (Engine, error) {
	threads := cfg.NumThreads
	if threads <= 0 {
		threads = 4
	}
	method := cfg.DecodingMethod
	if method == "" {
		method = "greedy_search"
	}

	model := sherpa.OfflineModelConfig{
		Transducer: sherpa.OfflineTransducerModelConfig{Encoder: cfg.EncoderPath, Decoder: cfg.DecoderPath, Joiner: cfg.JoinerPath},
		Tokens:     cfg.TokensPath,
		NumThreads: threads,
		Provider:   "cpu",
		ModelType:  "nemo_transducer",
	}
	rec := sherpa.NewOfflineRecognizer(&sherpa.OfflineRecognizerConfig{
		FeatConfig:     sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig:    model,
		DecodingMethod: method,
		MaxActivePaths: cfg.MaxActivePaths,
	})
	if rec == nil {
		return nil, fmt.Errorf("couldn't load the transducer from %s", cfg.EncoderPath)
	}
	// Loading ONNX Runtime replaces Go's SIGSEGV handler with one Go can't
	// share; sigfix puts it right.
	sigfix.AfterSherpa()

	return &parakeetEngine{
		recognizer: rec,
		vadConfig:  vadModelConfig(cfg.VADPath),
	}, nil
}

// vadModelConfig is the speech detector TranscribeSamples segments with,
// or nil without a model.
func vadModelConfig(path string) *sherpa.VadModelConfig {
	if path == "" {
		return nil
	}
	return &sherpa.VadModelConfig{
		SileroVad: sherpa.SileroVadModelConfig{
			Model:              path,
			Threshold:          0.5,
			MinSilenceDuration: 0.5,
			MinSpeechDuration:  0.25,
			WindowSize:         512,
			MaxSpeechDuration:  30.0,
		},
		SampleRate: sampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}
}

func secondsOf(samples []float32) float64 { return float64(len(samples)) / float64(sampleRate) }

// TranscribeSamples cuts samples at pauses when the engine has a speech
// detector and decodes each piece; without one it decodes them whole.
func (e *parakeetEngine) TranscribeSamples(samples []float32) (*Result, error) {
	switch {
	case len(samples) == 0:
		return &Result{}, nil
	case e.vadConfig == nil:
		return e.decodeWhole(samples)
	default:
		return e.decodeBySpeech(samples)
	}
}

// TranscribeDirect decodes samples in one piece.
func (e *parakeetEngine) TranscribeDirect(samples []float32) (*Result, error) {
	if len(samples) == 0 {
		return &Result{}, nil
	}
	return e.decodeWhole(samples)
}

// TranscribeFile reads path (via ffmpeg unless it's WAV) and decodes it as
// TranscribeSamples does.
func (e *parakeetEngine) TranscribeFile(path string) (*Result, error) {
	if !IsSupportedFormat(path) {
		return nil, fmt.Errorf("unsupported audio format: %s", path)
	}
	wav := path
	if needsConversion(path) {
		tmp, err := convertToWAV(path)
		if err != nil {
			return nil, fmt.Errorf("converting %s to WAV: %w", path, err)
		}
		defer func() { _ = os.Remove(tmp) }()
		wav = tmp
	}
	w := sherpa.ReadWave(wav)
	if w == nil {
		return nil, fmt.Errorf("couldn't read %s as WAV", wav)
	}
	return e.TranscribeSamples(w.Samples)
}

// Close frees the recognizer; later calls do nothing.
func (e *parakeetEngine) Close() {
	if e.recognizer == nil {
		return
	}
	if e.closeFn != nil {
		e.closeFn()
	}
	sherpa.DeleteOfflineRecognizer(e.recognizer)
	e.recognizer = nil
}

var errNoStream = errors.New("failed to create offline stream")

// decode runs the recognizer over one piece of audio. A nil result with a
// nil error means the recognizer returned nothing.
func (e *parakeetEngine) decode(samples []float32) (*sherpa.OfflineRecognizerResult, error) {
	if e.decodeFn != nil {
		return e.decodeFn(samples)
	}
	return decodeWith(e.recognizer, samples)
}

// decodeWith runs rec over one piece of audio.
func decodeWith(rec *sherpa.OfflineRecognizer, samples []float32) (*sherpa.OfflineRecognizerResult, error) {
	stream := sherpa.NewOfflineStream(rec)
	if stream == nil {
		return nil, errNoStream
	}
	defer sherpa.DeleteOfflineStream(stream)
	stream.AcceptWaveform(sampleRate, samples)
	rec.Decode(stream)
	return stream.GetResult(), nil
}

func (e *parakeetEngine) decodeWhole(samples []float32) (*Result, error) {
	r, err := e.decode(samples)
	if err != nil {
		return nil, err
	}
	out := &Result{Duration: secondsOf(samples)}
	if r != nil {
		out.Text = strings.TrimSpace(r.Text)
		out.Tokens, out.Timestamps, out.Language = r.Tokens, r.Timestamps, r.Lang
	}
	return out, nil
}

// decodeBySpeech decodes each stretch of speech Silero VAD finds and joins
// their text with spaces. Tokens and timestamps are concatenated as each
// piece reports them (timestamps relative to that piece), and the language
// is the first one reported. Audio past the last whole detector window is
// not fed, and pieces that fail to decode or come out empty are left out.
func (e *parakeetEngine) decodeBySpeech(samples []float32) (*Result, error) {
	vad := sherpa.NewVoiceActivityDetector(e.vadConfig, 60.0)
	if vad == nil {
		return nil, fmt.Errorf("couldn't start the speech detector from %s", e.vadConfig.SileroVad.Model)
	}
	defer sherpa.DeleteVoiceActivityDetector(vad)
	sigfix.AfterSherpa()

	win := e.vadConfig.SileroVad.WindowSize
	for end := win; end <= len(samples); end += win {
		vad.AcceptWaveform(samples[end-win : end])
	}
	vad.Flush()

	out := &Result{Duration: secondsOf(samples)}
	var texts []string
	for ; !vad.IsEmpty(); vad.Pop() {
		seg := vad.Front()
		if len(seg.Samples) == 0 {
			continue
		}
		r, err := e.decode(seg.Samples)
		if err != nil || r == nil {
			continue
		}
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		texts = append(texts, text)
		out.Tokens = append(out.Tokens, r.Tokens...)
		out.Timestamps = append(out.Timestamps, r.Timestamps...)
		if out.Language == "" {
			out.Language = r.Lang
		}
	}
	out.Text = strings.Join(texts, " ")
	return out, nil
}
