package transcribe

// Result is one decode's output. Tokens and Timestamps run in parallel
// (seconds from the start of the audio); models without timestamps leave
// Timestamps empty.
type Result struct {
	Text       string
	Tokens     []string
	Timestamps []float32
	Duration   float64 // seconds of audio decoded
	Language   string  // language the model reported, if any
}

// Engine is an offline recognizer: each call decodes audio that is
// already complete.
type Engine interface {
	// TranscribeSamples decodes 16 kHz mono float32 audio, cutting it at
	// pauses with the speech detector first when the engine has one.
	TranscribeSamples(samples []float32) (*Result, error)

	// TranscribeDirect decodes samples as one piece, for audio the caller
	// has already cut into utterances (the live pipeline).
	TranscribeDirect(samples []float32) (*Result, error)

	// TranscribeFile decodes an audio file of a type IsSupportedFormat
	// accepts.
	TranscribeFile(path string) (*Result, error)

	// Close frees the native recognizer; the engine is unusable after.
	Close()
}

// Config is what NewEngine needs: the transducer's files, the speech
// detector, and how to decode.
type Config struct {
	EncoderPath    string
	DecoderPath    string
	JoinerPath     string
	TokensPath     string
	VADPath        string // "" decodes TranscribeSamples' audio whole
	NumThreads     int    // 0 means 4
	DecodingMethod string // sherpa-onnx's: "greedy_search" (when "") or "modified_beam_search"
	MaxActivePaths int    // beam width for modified_beam_search
	// Model is the transcription model setting (config
	// transcription.model). NewEngineSetFromConfig uses the model it
	// picks for the default language, if downloaded, in place of the
	// paths above.
	Model string
	// WhisperLanguages are what a Whisper model may write (config
	// transcription.whisper_languages); empty is unchecked.
	WhisperLanguages []string
}
