package models

// Download locations and the names their files have once extracted. These
// match Tomoe's, so a model folder Tomoe filled is reused as it is.
const (
	// Parakeet TDT 0.6B v3, int8: the multilingual offline model every
	// install has (see ASRModels).
	ParakeetArchiveURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2"
	ParakeetSubdir     = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8"

	// Silero VAD, a single file at the top of the model folder.
	SileroVADURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
	SileroVADFile = "silero_vad.onnx"

	// The base 3D-Speaker ERes2Net embedding model (SpeakerModels' first
	// entry). "recongition" is how the release tag is spelt upstream.
	SpeakerEmbeddingURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx"
	SpeakerEmbeddingFile = "3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx"

	// pyannote segmentation 3.0, for diarization: an archive holding
	// model.onnx.
	PyannoteSegmentationURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2"
	PyannoteSegmentationSubdir = "sherpa-onnx-pyannote-segmentation-3-0"
	PyannoteSegmentationFile   = "model.onnx"

	// File names inside a Parakeet-style transducer folder.
	encoderFile = "encoder.int8.onnx"
	decoderFile = "decoder.int8.onnx"
	joinerFile  = "joiner.int8.onnx"
	tokensFile  = "tokens.txt"

	// The 2023 LibriSpeech streaming Zipformer (LiveZipformer). Its
	// archive carries fp32 and int8 weights; pruneEnglishStreaming keeps
	// only the int8 ones it loads.
	// Realtime ("pass 1") transcription: this is what actually lets text
	// appear as it's spoken. Parakeet (above) is offline-only — feeding it
	// audio incrementally isn't possible, only a complete segment at a
	// time — so it stays as the "pass 2" refinement engine (see
	// internal/live's two-pass pipeline) instead of the live path.

	// EnglishStreamingArchiveURL is the download URL for the English
	// streaming Zipformer model archive.
	EnglishStreamingArchiveURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2"

	// EnglishStreamingSubdir is the directory name after extraction.
	EnglishStreamingSubdir = "sherpa-onnx-streaming-zipformer-en-2023-06-26"

	// English streaming model files inside the extraction directory (the
	// archive ships both fp32 and int8 weights together; int8 is what we
	// use, matching Parakeet's own INT8 convention).
	englishStreamingEncoderFile = "encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx"
	englishStreamingDecoderFile = "decoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx"
	englishStreamingJoinerFile  = "joiner-epoch-99-avg-1-chunk-16-left-128.int8.onnx"
	englishStreamingTokensFile  = "tokens.txt"
)
