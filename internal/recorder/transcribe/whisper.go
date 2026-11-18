package transcribe

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/anielsud/musubi/internal/recorder/sigfix"
)

// WhisperFiles are a sherpa-onnx Whisper export's files.
type WhisperFiles struct{ Encoder, Decoder, Tokens string }

// whisperGuard keeps a Whisper engine's output to the allowed languages.
// Whisper detects each utterance's language, which lets a meeting switch
// between Swedish and English, but on short or unclear utterances it can
// pick an unrelated language and write in it. Such an utterance is decoded
// again with the language forced. The released Go binding can't change a
// recognizer's language (no SetConfig in v1.13.8), so each forced language
// is its own recognizer, loaded the first time it is needed: ~1 GB each
// for turbo.
type whisperGuard struct {
	files   WhisperFiles
	threads int
	allowed []string

	mu     sync.Mutex
	forced map[string]*sherpa.OfflineRecognizer
	last   string // the last allowed language detected
}

// NewWhisperEngine loads a Whisper model that detects each utterance's
// language and keeps it to allowed (see whisperGuard). One allowed
// language is simply forced; none leaves detection unchecked.
func NewWhisperEngine(files WhisperFiles, allowed []string, threads int, vadPath string) (Engine, error) {
	if threads <= 0 {
		threads = 4
	}
	lang := ""
	if len(allowed) == 1 {
		lang = allowed[0]
	}
	rec, err := newWhisperRecognizer(files, lang, threads)
	if err != nil {
		return nil, err
	}
	e := &parakeetEngine{recognizer: rec, vadConfig: vadModelConfig(vadPath)}
	if len(allowed) > 1 {
		g := &whisperGuard{files: files, threads: threads, allowed: allowed, forced: map[string]*sherpa.OfflineRecognizer{}}
		e.decodeFn = func(samples []float32) (*sherpa.OfflineRecognizerResult, error) {
			return g.decode(e.recognizer, samples)
		}
		e.closeFn = g.close
	}
	return e, nil
}

func newWhisperRecognizer(files WhisperFiles, lang string, threads int) (*sherpa.OfflineRecognizer, error) {
	rec := sherpa.NewOfflineRecognizer(&sherpa.OfflineRecognizerConfig{
		FeatConfig: sherpa.FeatureConfig{SampleRate: sampleRate, FeatureDim: 80},
		ModelConfig: sherpa.OfflineModelConfig{
			// An empty language makes sherpa-onnx detect it per decode.
			Whisper:    sherpa.OfflineWhisperModelConfig{Encoder: files.Encoder, Decoder: files.Decoder, Language: lang, Task: "transcribe", TailPaddings: -1},
			Tokens:     files.Tokens,
			NumThreads: threads,
			Provider:   "cpu",
		},
		DecodingMethod: "greedy_search",
	})
	if rec == nil {
		return nil, fmt.Errorf("couldn't load Whisper from %s", files.Encoder)
	}
	sigfix.AfterSherpa()
	return rec, nil
}

// decode decodes samples with the detecting recognizer and, when the
// language it detected isn't allowed, again with one forced.
func (g *whisperGuard) decode(detecting *sherpa.OfflineRecognizer, samples []float32) (*sherpa.OfflineRecognizerResult, error) {
	r, err := decodeWith(detecting, samples)
	if err != nil || r == nil {
		return r, err
	}
	g.mu.Lock()
	redo := redoLanguage(r.Lang, g.allowed, g.last)
	if redo == "" {
		g.last = normLang(r.Lang)
		g.mu.Unlock()
		return r, nil
	}
	rec := g.forced[redo]
	if rec == nil {
		fmt.Printf("Whisper: an utterance came out as %q; loading the %s model to decode such utterances again\n", normLang(r.Lang), redo)
		if rec, err = newWhisperRecognizer(g.files, redo, g.threads); err != nil {
			g.mu.Unlock()
			// Better the detected text than none.
			return r, nil
		}
		g.forced[redo] = rec
	}
	g.mu.Unlock()
	fr, err := decodeWith(rec, samples)
	if err != nil || fr == nil {
		return r, nil
	}
	fr.Lang = redo
	return fr, nil
}

func (g *whisperGuard) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for l, rec := range g.forced {
		sherpa.DeleteOfflineRecognizer(rec)
		delete(g.forced, l)
	}
}

// closeLanguage is the language Whisper most often means when it detects
// one of these: Swedish heard as Norwegian, Danish or Icelandic on a short
// clip. Decoding such a clip in the last language instead turned Swedish
// into made-up English on a recorded meeting
// (docs/swedish-asr-eval-2026-11.md).
var closeLanguage = map[string]string{"no": "sv", "nn": "sv", "nb": "sv", "da": "sv", "is": "sv", "fo": "sv"}

// redoLanguage is the language to decode an utterance again in, or "" to
// keep it: detected is allowed (or nothing is to check against). Otherwise
// it's the allowed language detected's is close to, else the last allowed
// language detected, as the conversation is most likely still in it, else
// the first allowed.
func redoLanguage(detected string, allowed []string, last string) string {
	d := normLang(detected)
	if len(allowed) == 0 || slices.Contains(allowed, d) {
		return ""
	}
	if c, ok := closeLanguage[d]; ok && slices.Contains(allowed, c) {
		return c
	}
	if last != "" {
		return last
	}
	return allowed[0]
}

// normLang is a language as sherpa-onnx reports it ("<|sv|>" or "sv") as
// a bare code.
func normLang(l string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l), "<|"), "|>"))
}
