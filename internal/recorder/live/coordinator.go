package live

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/speaker"
	"github.com/anielsud/musubi/internal/recorder/transcribe"
)

// SourceType names where a segment's audio came from; it is also the
// Segment.Source string saved with each line.
type SourceType string

// The two sources a meeting records: the user's microphone, and the
// meeting's own audio.
const (
	SourceMic     SourceType = "mic"
	SourceMonitor SourceType = "monitor"
)

// Config is everything a Coordinator needs. Only Engine and at least one
// capturer are required; every other field adds a feature or tunes one.
type Config struct {
	// MicCapturer and MonitorCapturer are the two audio streams; a nil
	// one is simply not transcribed.
	MicCapturer     *audio.StreamCapturer
	MonitorCapturer *audio.StreamCapturer
	// Engine decodes finished utterances (single-pass, and pass 2 of
	// two-pass).
	Engine transcribe.Engine
	// Embedder and Tracker label meeting-audio speakers ("Person N");
	// with either nil, those lines are "Other".
	Embedder *speaker.Embedder
	Tracker  *speaker.Tracker
	// MicEmbedder, OwnerVoice and MicOtherBelow tell the user's voice from
	// others' on the microphone (config mic_voices): an utterance whose
	// fingerprint is less like OwnerVoice (the user's, L2-normalized) than
	// MicOtherBelow is clustered with Tracker as another speaker. Any of
	// them unset labels the mic "You", as before. MicEmbedder is the mic
	// pipeline's own (an Embedder isn't safe for concurrent use).
	MicEmbedder   *speaker.Embedder
	OwnerVoice    []float32
	MicOtherBelow float64
	// HeardOwner, if set, is given fingerprints that are surely the
	// user's, so their saved voice keeps up with them. Called from one
	// goroutine at a time.
	HeardOwner func(emb []float32)
	// VADPath is the Silero speech-detector model each source loads.
	VADPath string
	// SegmentBufferSize sizes the output and work queues (64 if unset).
	SegmentBufferSize int
	// StreamingEngine enables two-pass transcription (optional, nil to
	// keep today's single-pass behavior): pass 1 is StreamingEngine,
	// decoded incrementally so text appears as it's spoken rather than
	// only once a whole VAD segment completes; pass 2 re-decodes the
	// same completed segment through Engine (offline, allowed to be
	// slower/better) and supersedes pass 1's text once ready. See
	// pipeline.go's drainVAD.
	StreamingEngine transcribe.StreamingEngine
	// SkipMonitorDiarization disables speaker embedding/clustering for
	// the monitor source, labeling every monitor-source segment
	// "System Audio" instead of attempting "Person N" identification.
	// Set this when the monitor source is a whole-system audio tap
	// (macOS's "Everything" source-picker option) rather than one
	// specific app's audio: a system-wide tap can mix multiple
	// unrelated audio streams together (notifications, music, several
	// apps at once), so per-embedding speaker clustering isn't
	// meaningful there the way it is for one app's own audio. Defaults
	// to false (diarize). The app no longer sets it: "Everything" is
	// diarized too, since in a meeting it's mostly the call (and a
	// notification sound is just one more short "speaker").
	SkipMonitorDiarization bool
	// Timings, if set, collects per-stage processing time (see Timings).
	Timings *Timings

	// MinSilenceDuration is the pause (seconds) that ends an utterance,
	// and MaxSpeechDuration the longest utterance before it's cut. Each
	// utterance gets one speaker embedding, so these set how much audio
	// a speaker label is based on. 0 means the defaults, 0.5 and 30.
	MinSilenceDuration float64
	MaxSpeechDuration  float64
	// MinSpeechLevelDB drops an utterance whose audio is quieter than this
	// (RMS, dBFS) before it's transcribed: the speech detector triggers
	// on room noise near the noise floor and the model then invents words
	// for it ("Yeah.", "Goodbye"). 0 keeps every utterance.
	MinSpeechLevelDB float64
	// MicLevelMarginDB drops mic utterances more than this many dB below
	// the mic's typical (median) level: the user's own voice is loud on
	// their microphone; faint speech is background. 0 turns it off.
	MicLevelMarginDB float64

	// TurnMode (experimental, single-pass only) decodes a speaker's turn
	// once instead of each utterance: consecutive utterances from one
	// source collect into one line until the other source speaks, a pause
	// longer than TurnMaxGap, the line would pass TurnMaxSeconds, or (call
	// audio) SpeakerChanged reports a change between them. Longer lines
	// give the model more context; cutting at speaker changes keeps one
	// speaker per line.
	TurnMode bool
	// DecodeAsync (single-pass) decodes each finished line on its own
	// worker, in order, so the pipeline keeps reading audio while a decode
	// runs, and a finished line is never dropped. Decoding inline once
	// stalled the pipeline for seconds per line on a loaded machine. false
	// decodes inline, as before.
	DecodeAsync    bool
	TurnMaxSeconds float64
	TurnMaxGap     float64
	// DecodePad is seconds of silence added before and after the audio of
	// every decode: models that decode a whole utterance at once can drop
	// a last syllable that ends abruptly. 0 adds none.
	DecodePad float64
	// TurnInterjection is the longest utterance (seconds) from one side
	// that doesn't end the other side's turn in TurnMode (a backchannel).
	// 0: any does.
	TurnInterjection float64
	// SpeakerChanged reports whether a speaker-change signal (a new voice
	// starting, the meeting window's highlight moving) fell between from
	// and to (session seconds). Nil: no signal beyond the rules above.
	SpeakerChanged func(from, to float64) bool
	// HighlightChanges returns the times in [from, to] at which the
	// meeting window's highlight moved to another person. When set (and
	// in TurnMode), an utterance from the meeting audio is cut at each
	// such moment, HighlightLag seconds earlier (the highlight follows the
	// voice), so a speaker who cuts in without a pause gets their own
	// line instead of sharing one with whoever was talking.
	HighlightChanges func(from, to float64) []float64
	HighlightLag     float64

	// ProbePrefixes, for measurement only, also labels each utterance
	// from just its first N seconds for every N listed that's shorter
	// than the utterance, read-only (speaker.Tracker.Peek) just before
	// the real assignment, recording them in Probes. Costs one embedding
	// per prefix.
	ProbePrefixes []float64
	Probes        *Probes

	// ReplayProgress, if set, is called by Replay every few seconds of
	// audio with the analysis windows done and the total, for tools that
	// report progress. Ignored when capturing live.
	ReplayProgress func(done, total int)

	// WindowSize and WindowStep (seconds), for measurement only, also
	// match overlapping windows within each utterance longer than
	// WindowSize against the known speakers (speaker.Tracker.PeekMatch),
	// recording them in Probes: whether labeling within an utterance
	// catches speaker changes that have no pause. 0 = off.
	WindowSize, WindowStep float64

	// MonitorAudio, if set, receives all of the monitor source's audio,
	// speech or not, one capture window at a time, with the session time
	// (seconds) at the window's end: what diarizing during the meeting
	// runs on (see diarize.Stream). Called on the pipeline goroutine, so
	// it must return quickly.
	MonitorAudio func(samples []float32, endTime float64)

	// OnMonitorSpeechStart, if set, is called when the monitor source's
	// speech starts after at least speechStartPause of quiet: the
	// earliest sign of a speaker change. On the pipeline goroutine, so it
	// must return quickly.
	OnMonitorSpeechStart func()
}

// speechStartPause is the quiet before speech that counts as a likely
// speaker change (see Config.OnMonitorSpeechStart).
const speechStartPause = 0.25

// Default utterance boundaries (see Config.MinSilenceDuration).
const (
	DefaultMinSilenceDuration = 0.5
	DefaultMaxSpeechDuration  = 30.0
)

// utteranceBounds is cfg's MinSilenceDuration and MaxSpeechDuration, with
// defaults for unset values.
func (cfg Config) utteranceBounds() (minSilence, maxSpeech float64) {
	minSilence, maxSpeech = cfg.MinSilenceDuration, cfg.MaxSpeechDuration
	if minSilence <= 0 {
		minSilence = DefaultMinSilenceDuration
	}
	if maxSpeech <= 0 {
		maxSpeech = DefaultMaxSpeechDuration
	}
	return minSilence, maxSpeech
}

// Stats counts the lines each source has produced and how long the
// session has run.
type Stats struct {
	MicSegments     int
	MonitorSegments int
	Duration        time.Duration
}

// Coordinator turns a meeting's audio (the mic, the meeting's own audio,
// or both) into transcript segments: one pipeline goroutine per source,
// plus the workers that decode and refine finished lines.
type Coordinator struct {
	// micEmbedMu serializes the mic embedder (Config.MicEmbedder).
	micEmbedMu sync.Mutex
	// pass2 is the last minute of pass 2, for the log (see logPass2).
	pass2Mu   sync.Mutex
	pass2     pass2Stats
	levelMu   sync.Mutex
	micLevels []float64 // recent mic utterance levels (see tooQuiet)
	turnMu    sync.Mutex
	turns     map[SourceType]*turnBuf // TurnMode's open line per source
	turnCuts  map[string]int          // why TurnMode sent each line (see TurnCuts)
	cfg       Config
	segmentCh chan session.Segment
	// hintNeededCh is signalled when a monitor-source speaker with no
	// video hint yet is heard. The bool is a priority flag: true means
	// "bypass videohint.Poll's normal trigger debounce" (used for a
	// brand-new speaker, where a fast double-shot attempt matters more
	// than the usual rate limit), false is the ordinary debounced case.
	hintNeededCh chan bool
	startTime    time.Time

	// micHeardAt is when the mic last delivered any real signal (Unix
	// nanoseconds; 0 = not yet), for MicQuietFor.
	micHeardAt atomic.Int64
	// monitorHeardAt is the same for the meeting audio, for
	// MonitorQuietFor.
	monitorHeardAt atomic.Int64

	// segmentUpdateCh carries revisions to a segment already sent on
	// segmentCh (same ID) -- pass 2's refined text superseding pass 1's.
	// Only used when cfg.StreamingEngine is set.
	segmentUpdateCh chan session.Segment
	// refineCh queues pass-2 refinement jobs from drainVAD to
	// refineWorker. Deliberately drained to completion on shutdown (see
	// Start's closer goroutine) rather than abandoned on ctx.Done(), so
	// a segment queued for refinement right as the session stops still
	// gets its update rather than staying "pending" forever.
	refineCh chan refinementJob
	// decodeCh carries single-pass lines to decodeWorker (DecodeAsync).
	decodeCh chan decodeJob
	decodeWG sync.WaitGroup
	refineWG sync.WaitGroup

	// cancelRun ends the pipelines Start launched; wg counts them.
	cancelRun context.CancelFunc
	wg        sync.WaitGroup

	// Lines produced so far per source, for Stats.
	micLines, monitorLines atomic.Int64

	// decodeMu keeps Engine to one decode at a time: the pipelines and
	// both workers all decode, and a sherpa-onnx recognizer must not be
	// entered concurrently.
	decodeMu sync.Mutex

	// lastSegID is the number in the most recent segment ID handed out.
	lastSegID atomic.Int64

	// now is time.Now, except under Replay, which substitutes a virtual
	// clock so recorded audio can run faster than real time with the
	// same segment timestamps.
	now func() time.Time
}

// New makes a Coordinator for cfg. Nothing runs until Start.
func New(cfg Config) *Coordinator {
	bufSize := 64
	if cfg.SegmentBufferSize > 0 {
		bufSize = cfg.SegmentBufferSize
	}
	c := &Coordinator{
		cfg:             cfg,
		segmentCh:       make(chan session.Segment, bufSize),
		segmentUpdateCh: make(chan session.Segment, bufSize),
		hintNeededCh:    make(chan bool, 1),
		refineCh:        make(chan refinementJob, bufSize),
		decodeCh:        make(chan decodeJob, bufSize),
		now:             time.Now,
	}
	return c
}

// Start opens the configured capturers and launches a pipeline for each,
// plus the workers. It returns once they're running; segments arrive on
// Segments (and SegmentUpdates) until Stop, when both channels close.
func (c *Coordinator) Start(ctx context.Context) error {
	ctx, c.cancelRun = context.WithCancel(ctx)
	c.startTime = c.now()

	sources := []struct {
		sc  *audio.StreamCapturer
		src SourceType
	}{{c.cfg.MicCapturer, SourceMic}, {c.cfg.MonitorCapturer, SourceMonitor}}
	var opened []*audio.StreamCapturer
	for _, s := range sources {
		if s.sc == nil {
			continue
		}
		if err := s.sc.Start(); err != nil {
			// Leave nothing recording behind a failed start.
			for _, o := range opened {
				_ = o.Stop()
			}
			return fmt.Errorf("live: starting %s capture: %w", s.src, err)
		}
		opened = append(opened, s.sc)
		c.wg.Add(1)
		go c.processPipeline(ctx, s.sc, s.src)
	}
	if len(opened) == 0 {
		return fmt.Errorf("live: no audio source to transcribe")
	}

	c.refineWG.Add(1)
	go c.refineWorker()
	if c.cfg.DecodeAsync {
		c.decodeWG.Add(1)
		go c.decodeWorker()
	}
	go c.logPass2(ctx)

	// Closer goroutine: waits for all pipelines to finish (including
	// their final VAD flush, which may still enqueue a last refinement
	// job or two), only THEN closes refineCh -- so refineWorker keeps
	// draining right up until every queued job has actually run, never
	// abandoning a "pending" segment mid-refinement -- and only after
	// refineWorker itself has finished does it close the two segment
	// channels, since a late refinement update would otherwise arrive
	// after SegmentUpdates() looks closed to a consumer.
	go func() {
		c.wg.Wait()
		// Single-pass lines still queued are decoded before the transcript
		// closes: overload makes the end of a meeting late, never short.
		close(c.decodeCh)
		c.decodeWG.Wait()
		close(c.refineCh)
		c.refineWG.Wait()
		close(c.segmentCh)
		close(c.segmentUpdateCh)
	}()
	return nil
}

// Segments returns the channel that receives newly-transcribed segments.
func (c *Coordinator) Segments() <-chan session.Segment { return c.segmentCh }

// SegmentUpdates returns the channel that receives revisions to a
// segment already delivered on Segments() (same ID): pass 2's refined
// text superseding pass 1's, once ready. Only fires when the Config
// this Coordinator was created with set StreamingEngine.
func (c *Coordinator) SegmentUpdates() <-chan session.Segment {
	return c.segmentUpdateCh
}

// HintNeeded returns a channel signalled whenever a monitor-source
// speaker with no video hint attached yet is heard. Wire this into
// videohint.Poll's trigger parameter so a still-unlabeled speaker gets
// an immediate ring/OCR attempt instead of waiting for Poll's next
// scheduled tick — see internal/speaker.Tracker.Assign's needsHint
// return value, which is what actually decides when this fires. The
// bool is a priority flag (true = bypass Poll's normal trigger
// debounce) -- see signalHintNeeded.
func (c *Coordinator) HintNeeded() <-chan bool {
	return c.hintNeededCh
}

// signalHintNeeded sends on hintNeededCh without blocking -- if the
// single-slot buffer is already full (Poll hasn't drained the
// previous signal yet), this drops rather than stalling the
// transcription pipeline, same as every other non-blocking channel
// send in this package.
func (c *Coordinator) signalHintNeeded(priority bool) {
	select {
	case c.hintNeededCh <- priority:
	default:
	}
}

// Stop ends the session: the pipelines finish what they've heard, then
// the capturers release their devices. Safe to call without Start, and
// more than once.
func (c *Coordinator) Stop() {
	if c.cancelRun != nil {
		c.cancelRun()
	}
	captures := c.capturers()
	for _, sc := range captures {
		_ = sc.Stop()
	}
	// The devices are closed only once no pipeline can still read them.
	c.wg.Wait()
	for _, sc := range captures {
		sc.Close()
	}
}

// capturers lists the configured capturers, mic first.
func (c *Coordinator) capturers() []*audio.StreamCapturer {
	var out []*audio.StreamCapturer
	for _, sc := range []*audio.StreamCapturer{c.cfg.MicCapturer, c.cfg.MonitorCapturer} {
		if sc != nil {
			out = append(out, sc)
		}
	}
	return out
}

// Stats reports the lines produced so far and the session's length.
func (c *Coordinator) Stats() Stats {
	var st Stats
	st.MicSegments = int(c.micLines.Load())
	st.MonitorSegments = int(c.monitorLines.Load())
	st.Duration = c.now().Sub(c.startTime)
	return st
}

// AudioSamples is everything recorded, the mic's audio followed by the
// meeting's: meant for a session with one source (two-source sessions
// save each track with MicSamples and MonitorSamples).
func (c *Coordinator) AudioSamples() []float32 {
	var all []float32
	for _, sc := range c.capturers() {
		all = append(all, sc.AllSamples()...)
	}
	return all
}

// MicSamples is the mic's recording so far; nil without a mic.
func (c *Coordinator) MicSamples() []float32 { return recorded(c.cfg.MicCapturer) }

// MonitorSamples is the meeting audio's recording so far; nil without it.
func (c *Coordinator) MonitorSamples() []float32 { return recorded(c.cfg.MonitorCapturer) }

func recorded(sc *audio.StreamCapturer) []float32 {
	if sc == nil {
		return nil
	}
	return sc.AllSamples()
}

// IsDualSource reports whether both the mic and the meeting audio are
// being recorded.
func (c *Coordinator) IsDualSource() bool {
	return len(c.capturers()) == 2
}

// nextSegID hands out the session's segment IDs: "seg-1", "seg-2", ...
func (c *Coordinator) nextSegID() string {
	return "seg-" + fmt.Sprint(c.lastSegID.Add(1))
}

// elapsed is the session time now, in seconds.
func (c *Coordinator) elapsed() float64 {
	return c.now().Sub(c.startTime).Seconds()
}

// MicQuietFor is how long the mic has delivered essentially nothing
// (below about -80 dBFS: a disabled or wrong device, not a quiet room),
// counted from the start if it never has; 0 without a mic.
func (c *Coordinator) MicQuietFor() time.Duration {
	if c.cfg.MicCapturer == nil {
		return 0
	}
	at := c.micHeardAt.Load()
	if at == 0 {
		return c.now().Sub(c.startTime)
	}
	return c.now().Sub(time.Unix(0, at))
}

// MonitorQuietFor is MicQuietFor for the meeting audio: how long it has
// delivered nothing above about -80 dBFS (an app making no sound at all,
// or the wrong one captured); 0 without meeting audio.
func (c *Coordinator) MonitorQuietFor() time.Duration {
	if c.cfg.MonitorCapturer == nil {
		return 0
	}
	at := c.monitorHeardAt.Load()
	if at == 0 {
		return c.now().Sub(c.startTime)
	}
	return c.now().Sub(time.Unix(0, at))
}

// SessionTime converts a wall-clock time to session time (seconds since
// the session started), the time base segments use.
func (c *Coordinator) SessionTime(t time.Time) float64 {
	return t.Sub(c.startTime).Seconds()
}
