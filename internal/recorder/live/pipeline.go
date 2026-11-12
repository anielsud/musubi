package live

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/sigfix"
	"github.com/anielsud/musubi/internal/recorder/speaker"
	"github.com/anielsud/musubi/internal/recorder/transcribe"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// The speech detector works on 16 kHz audio in windows of 512 samples
// (32 ms), the size every capturer delivers.
const (
	vadSampleRate = 16000
	vadWindowSize = 512

	// minLiveAudioSamples gates the first live-partial emission (see
	// liveState/emitLivePartial): a speaker assignment needs enough
	// accumulated speech for a stable embedding, so pass 1's partial
	// text is buffered silently until this much speech has accumulated,
	// then shown (and grows from there on every subsequent partial).
	// Half a second is imperceptible as a delay but avoids computing a
	// speaker off a handful of frames.
	minLiveAudioSamples = vadSampleRate / 2
)

// refinementJob is one pass-2 request: re-decode a completed segment's
// audio through the (offline, higher-quality) Engine and supersede the
// pass-1 text already emitted for it.
//
// unannounced marks an utterance nobody has seen yet: pass 1 had no words
// for it by the time it ended (a quick "ok" often finishes before the
// streaming model says anything). Pass 2 decides whether it exists at
// all: words make it a new segment, silence drops it. Its label waits
// for that verdict too (embedding is kept for it), so a burst of noise
// that decodes to nothing never moves a speaker cluster.
type refinementJob struct {
	id        string
	samples   []float32
	speaker   string
	decision  speaker.AssignDecision // see speakerLabel; "" if speaker is unset
	startTime float64
	endTime   float64
	source    SourceType
	pass1Text string // fallback if refinement produces nothing usable

	unannounced bool
	embedding   []float32 // taken on the pipeline goroutine, for unannounced jobs

	// absorb are pending lines a TurnMode turn merged into this one,
	// withdrawn once its text is final.
	absorb []string
}

// liveState tracks pass 1's in-progress utterance for one pipeline: the
// growing partial text, whether (and what) a live segment has already
// been shown for it, and the audio accumulated so far for an early —
// but, once decided, stable — speaker assignment. Zero value is "no
// utterance in progress."
type liveState struct {
	partial   string
	shown     string // what consumers last saw on the live line
	id        string
	speaker   string
	startTime float64
	audio     []float32
}

func (ls *liveState) reset() {
	*ls = liveState{}
}

// sourceState is one source pipeline's state: its VAD, its optional pass-1
// streaming session, and pass 1's in-progress utterance. Split out of
// processPipeline so Replay can drive the exact same per-window logic
// from recorded audio instead of a live capturer.
type sourceState struct {
	source     SourceType
	vad        *sherpa.VoiceActivityDetector
	streamSess transcribe.StreamingSession
	live       liveState
	// speaking is whether the last window was speech; silentFor how long
	// (seconds) it's been quiet before that (see Config.OnMonitorSpeechStart).
	speaking  bool
	silentFor float64
	// lastSpeechAt is when (session seconds) the VAD last heard speech:
	// TurnMode's line is only sent for a long pause once nobody on this
	// source has spoken for that long.
	lastSpeechAt float64
}

// newSourceState creates the VAD and (if configured) pass-1 streaming
// session for one source. Returns nil if the VAD can't be created.
func (c *Coordinator) newSourceState(source SourceType) *sourceState {
	// Each source gets its own detector: they keep state between windows.
	minSilence, maxSpeech := c.cfg.utteranceBounds()
	silero := sherpa.SileroVadModelConfig{
		Model:              c.cfg.VADPath,
		WindowSize:         vadWindowSize,
		Threshold:          0.5,
		MinSpeechDuration:  0.25,
		MinSilenceDuration: float32(minSilence),
		MaxSpeechDuration:  float32(maxSpeech),
	}
	// One thread on the CPU is plenty for a 32 ms window, and leaves the
	// cores to the decoders. The detector holds up to a minute of
	// finished speech until drainVAD collects it.
	const heldSeconds = 60
	vad := sherpa.NewVoiceActivityDetector(&sherpa.VadModelConfig{
		SileroVad:  silero,
		SampleRate: vadSampleRate,
		NumThreads: 1,
		Provider:   "cpu",
	}, heldSeconds)
	if vad == nil {
		return nil
	}
	// The detector's constructor can reinstall ONNX Runtime's crash
	// handler in a form Go aborts on (see sigfix).
	sigfix.AfterSherpa()
	st := &sourceState{source: source, vad: vad}

	// Pass 1 (optional): a streaming session for this source, giving
	// incremental partial text as audio arrives rather than only once a
	// whole VAD segment completes. Nil (falls back to today's
	// single-pass, synchronous-decode-on-completion behavior) if the
	// realtime model isn't configured/available.
	if c.cfg.StreamingEngine != nil {
		streamSess, err := c.cfg.StreamingEngine.NewSession()
		if err != nil {
			fmt.Printf("live: failed to start streaming session for %s (falling back to non-realtime): %v\n", source, err)
		} else {
			st.streamSess = streamSess
		}
	}
	return st
}

// close releases the source's VAD and streaming session.
func (st *sourceState) close() {
	if st.streamSess != nil {
		st.streamSess.Close()
	}
	sherpa.DeleteVoiceActivityDetector(st.vad)
}

// processPipeline runs a single source pipeline: reads windows → VAD → transcribe → emit segments.
func (c *Coordinator) processPipeline(ctx context.Context, sc *audio.StreamCapturer, source SourceType) {
	defer c.wg.Done()

	st := c.newSourceState(source)
	if st == nil {
		return
	}
	defer st.close()

	windows := sc.Windows()
	for {
		var window []float32
		more := false
		select {
		case <-ctx.Done():
			// Stopped: what's been heard is still finished below.
		case window, more = <-windows:
		}
		if !more {
			// Cancelled, or the capturer closed its stream.
			c.finishSource(st)
			return
		}
		c.processWindow(st, window)
	}
}

// finishSource flushes the VAD and processes whatever it still holds,
// once the source has no more audio coming.
func (c *Coordinator) finishSource(st *sourceState) {
	st.vad.Flush()
	c.drainVAD(st.vad, st.source, st.streamSess, &st.live)
	c.finishLive(st.source, &st.live)
	if c.cfg.TurnMode {
		c.flushTurn(st.source)
	}
}

// processWindow feeds one audio window through VAD (and pass 1, if
// enabled), then handles any speech segments that completed.
func (c *Coordinator) processWindow(st *sourceState, window []float32) {
	if st.source == SourceMonitor && c.cfg.MonitorAudio != nil {
		c.cfg.MonitorAudio(window, c.elapsed())
	}
	if st.source == SourceMic && hasSignal(window) {
		c.micHeardAt.Store(c.now().UnixNano())
	}
	if st.source == SourceMonitor && hasSignal(window) {
		c.monitorHeardAt.Store(c.now().UnixNano())
	}
	// Feed window to VAD (must be exactly windowSize)
	if len(window) == vadWindowSize {
		vadBegan := time.Now()
		st.vad.AcceptWaveform(window)
		c.cfg.Timings.add(&c.timingsOrZero().VAD, vadBegan)
		isSpeech := st.vad.IsSpeech()

		if st.streamSess != nil {
			if isSpeech {
				// Only accumulate speech, not silence -- keeps
				// the eventual speaker embedding clean.
				st.live.audio = append(st.live.audio, window...)
			}
			feedBegan := time.Now()
			text, err := st.streamSess.Feed(window)
			c.cfg.Timings.add(&c.timingsOrZero().Streaming, feedBegan)
			if err == nil && text != st.live.partial {
				st.live.partial = text
				if text != "" {
					c.emitLivePartial(st.source, &st.live, text)
				}
			}
		}

		if st.source == SourceMonitor && c.cfg.OnMonitorSpeechStart != nil {
			if isSpeech && !st.speaking && st.silentFor >= speechStartPause {
				c.cfg.OnMonitorSpeechStart()
			}
			if isSpeech {
				st.silentFor = 0
			} else {
				st.silentFor += float64(vadWindowSize) / vadSampleRate
			}
			st.speaking = isSpeech
		}

		if isSpeech {
			st.lastSpeechAt = c.elapsed()
		}
	}

	// Process any completed speech segments
	c.drainVAD(st.vad, st.source, st.streamSess, &st.live)
	if c.cfg.TurnMode {
		c.flushStaleTurn(st.source, st.lastSpeechAt)
	}
}

// emitLivePartial publishes pass 1's growing text for the utterance in
// progress: the first call (once enough audio has accumulated for a
// speaker assignment — see minLiveAudioSamples) creates a new "live"
// segment; every call after that updates the same segment ID in place.
// "live" (not "pending") signals to consumers that this text may still
// change because the person is still talking, not just because pass 2
// hasn't run yet. The line becomes "pending" in handleSegment, when the
// detector reports the utterance finished.
func (c *Coordinator) emitLivePartial(source SourceType, live *liveState, text string) {
	if live.id == "" {
		if len(live.audio) < minLiveAudioSamples {
			return
		}
		live.id = c.nextSegID()
		live.speaker, live.shown = c.provisionalSpeaker(source, live.audio), text
		live.startTime = c.elapsed()

		seg := session.Segment{
			ID:        live.id,
			Speaker:   live.speaker,
			Text:      text,
			StartTime: live.startTime,
			EndTime:   c.elapsed(),
			Source:    string(source),
			Language:  "en", // the streaming engine is English-only today
			Status:    "live",
		}
		select {
		case c.segmentCh <- seg:
		default:
		}
		return
	}

	seg := session.Segment{
		ID:        live.id,
		Speaker:   live.speaker,
		Text:      text,
		StartTime: live.startTime,
		EndTime:   c.elapsed(),
		Source:    string(source),
		Language:  "en",
		Status:    "live",
	}
	live.shown = seg.Text
	select {
	case c.segmentUpdateCh <- seg:
	default:
	}
}

// drainVAD handles every utterance the detector has finished, oldest
// first.
// streamSess/live are pass 1's streaming state (see processPipeline)
// -- nil/unused when two-pass transcription isn't configured, in which
// case this behaves exactly as before: one synchronous decode per
// completed segment, emitted as final immediately.
func (c *Coordinator) drainVAD(vad *sherpa.VoiceActivityDetector, source SourceType, streamSess transcribe.StreamingSession, live *liveState) {
	// Front copies the utterance out, so it can be popped at the end of
	// each turn of the loop.
	for ; !vad.IsEmpty(); vad.Pop() {
		segment := vad.Front()
		if len(segment.Samples) == 0 {
			continue
		}
		if c.dropNoise(source, segment.Samples, streamSess, live) {
			continue
		}
		// The models hear the cleaned-up, peak-normalised audio, gated
		// at -40 dB.
		c.handleSegment(source, audio.ProcessPipeline(segment.Samples, vadSampleRate, -40), streamSess, live)
	}
}

// dropNoise reports whether a completed VAD segment's raw audio (before the
// DSP pipeline, which normalizes every segment to full scale) is quieter
// than Config.MinSpeechLevelDB, and if so drops it, withdrawing the live
// line if one is showing. Room noise the detector mistook for speech is
// otherwise normalized up and transcribed as invented words.
func (c *Coordinator) dropNoise(source SourceType, raw []float32, streamSess transcribe.StreamingSession, live *liveState) bool {
	level := levelDB(raw)
	if !c.tooQuiet(source, level) {
		return false
	}
	if live.id != "" {
		select {
		case c.segmentUpdateCh <- session.Segment{ID: live.id, Source: string(source), Status: session.StatusRemoved}:
		default:
		}
	}
	if streamSess != nil {
		streamSess.Reset()
	}
	live.reset()
	return true
}

// tooQuiet reports whether an utterance at level (dBFS) is below the
// fixed floor (Config.MinSpeechLevelDB) or, on the mic, more than
// Config.MicLevelMarginDB below the user's own typical level: the
// microphone is the user's, so faint speech on it is someone or
// something further away (found live: background sounds 25–35 dB below
// the user's voice transcribed as "Wow." and "I'm gonna go to bed" and
// labeled You). Utterances that pass count toward the typical level.
func (c *Coordinator) tooQuiet(source SourceType, level float64) bool {
	if c.cfg.MinSpeechLevelDB < 0 && level < c.cfg.MinSpeechLevelDB {
		return true
	}
	if source != SourceMic || c.cfg.MicLevelMarginDB <= 0 {
		return false
	}
	c.levelMu.Lock()
	defer c.levelMu.Unlock()
	if n := len(c.micLevels); n >= micLevelsBeforeGate {
		sorted := append([]float64(nil), c.micLevels...)
		sort.Float64s(sorted)
		if level < sorted[n/2]-c.cfg.MicLevelMarginDB {
			return true
		}
	}
	c.micLevels = append(c.micLevels, level)
	if len(c.micLevels) > micLevelsKept {
		c.micLevels = c.micLevels[1:]
	}
	return false
}

// The mic's typical level is the median of its last micLevelsKept
// utterances, once there are micLevelsBeforeGate of them.
const (
	micLevelsBeforeGate = 8
	micLevelsKept       = 100
)

// handleSegment starts one utterance the detector finished (DSP already
// applied) on its way to the transcript. Single-pass, it is decoded or
// added to its turn; two-pass, its live line (if any) turns "pending" and
// pass 2 is queued. drainVAD calls it; tests call it directly, with no
// VAD model to load.
func (c *Coordinator) handleSegment(source SourceType, samples []float32, streamSess transcribe.StreamingSession, live *liveState) {
	// The utterance ended just now; it began as long ago as it lasted.
	endTime := c.elapsed()
	startTime := endTime - float64(len(samples))/vadSampleRate
	if streamSess != nil {
		c.closeUtterance(source, samples, startTime, endTime, streamSess, live)
		return
	}
	if c.cfg.TurnMode {
		c.addToTurn(source, samples, startTime, endTime)
		return
	}
	c.singlePass(source, samples, startTime, endTime)
}

// closeUtterance is handleSegment's two-pass half: pass 1's words for the
// utterance become a pending line (the live line, relabelled, if one was
// showing), and pass 2 is queued to replace them.
func (c *Coordinator) closeUtterance(source SourceType, samples []float32, startTime, endTime float64, streamSess transcribe.StreamingSession, live *liveState) {
	heard := strings.TrimSpace(live.partial)
	// The next utterance starts pass 1 from scratch.
	streamSess.Reset()
	if heard == "" && live.id == "" {
		// Nothing was ever shown: pass 2 alone says whether this was
		// speech, as it would single-pass.
		if c.cfg.TurnMode {
			c.addTurnPart(source, turnPart{samples: samples, start: startTime, end: endTime, twoPass: true})
		} else {
			c.queueUnannounced(source, samples, startTime, endTime)
		}
		live.reset()
		return
	}
	// pass 1 can blank its hypothesis after a line went up; that line's
	// words stand until pass 2 has better.
	text := cmp.Or(heard, live.shown)

	// The live line's label came from its first moments only
	// (provisionalSpeaker); the whole utterance decides now, and may
	// change it.
	id, announced := live.id, live.id != ""
	if !announced {
		id = c.nextSegID()
	}
	spk, decision := c.assignSpeaker(source, samples, startTime)
	live.reset()

	seg := session.Segment{
		ID: id, Speaker: spk, Text: text,
		StartTime: startTime, EndTime: endTime,
		Source: string(source), Language: "en", Status: "pending",
		Decision: string(decision),
	}
	if announced {
		offer(c.segmentUpdateCh, seg)
	} else {
		offer(c.segmentCh, seg)
	}
	c.countSegment(source)

	if c.cfg.TurnMode {
		// The pending line stays until its turn is decoded as a whole.
		c.addTurnPart(source, turnPart{samples: samples, start: startTime, end: endTime, twoPass: true,
			id: id, pass1: text, speaker: spk, decision: decision})
		return
	}

	job := refinementJob{
		id: id, samples: samples, speaker: spk, decision: decision,
		startTime: startTime, endTime: endTime, source: source, pass1Text: text,
	}
	select {
	case c.refineCh <- job:
		return
	default:
	}
	// Pass 2 is too far behind to take this line, and the pipeline won't
	// wait for it: pass 1's words are final. Saying so matters, or the
	// line shows "pending" for good.
	seg.Status = ""
	offer(c.segmentUpdateCh, seg)
}

// offer sends seg if ch has room and otherwise drops it: the live
// pipeline never waits on a slow consumer.
func offer(ch chan<- session.Segment, seg session.Segment) {
	select {
	case ch <- seg:
	default:
	}
}

// queueUnannounced gives pass 2 an utterance pass 1 had no words for (see
// refinementJob). When pass 2's queue is full it is decoded right here:
// losing speech is worse than this pipeline falling behind by one decode.
func (c *Coordinator) queueUnannounced(source SourceType, samples []float32, startTime, endTime float64) {
	job := refinementJob{id: c.nextSegID(), samples: samples, source: source, unannounced: true}
	job.startTime, job.endTime = startTime, endTime
	// Embedders aren't safe across goroutines, so this is taken here
	// rather than on refineWorker.
	job.embedding = c.speakerEmbedding(source, samples)
	select {
	case c.refineCh <- job:
		return
	default:
	}
	seg, ok := c.refine(job)
	if !ok {
		return
	}
	select {
	case c.segmentCh <- seg:
	default:
	}
	c.countSegment(source)
}

// decodeJob is a single-pass line waiting for decodeWorker.
type decodeJob struct {
	source             SourceType
	samples            []float32
	startTime, endTime float64
	// speaker, if set, is the line's speaker already (a mic turn labelled
	// utterance by utterance, see addTurnPart).
	speaker  string
	decision speaker.AssignDecision
}

// singlePass decodes a finished line: on decodeWorker (DecodeAsync), so
// the pipeline goes on reading audio, else at once.
func (c *Coordinator) singlePass(source SourceType, samples []float32, startTime, endTime float64) {
	c.singlePassAs(source, samples, startTime, endTime, "", "")
}

// singlePassAs is singlePass for a line whose speaker is known (spk "":
// worked out from its audio).
func (c *Coordinator) singlePassAs(source SourceType, samples []float32, startTime, endTime float64, spk string, decision speaker.AssignDecision) {
	if !c.cfg.DecodeAsync {
		c.transcribeSinglePass(source, samples, startTime, endTime, spk, decision)
		return
	}
	c.decodeCh <- decodeJob{source, samples, startTime, endTime, spk, decision}
}

// decodeWorker decodes single-pass lines in the order they finished.
func (c *Coordinator) decodeWorker() {
	defer c.decodeWG.Done()
	for j := range c.decodeCh {
		c.transcribeSinglePass(j.source, j.samples, j.startTime, j.endTime, j.speaker, j.decision)
	}
}

// transcribeSinglePass decodes one finished line and sends it as final:
// single-pass has no earlier text to supersede. spk "" labels the line
// from its own audio.
func (c *Coordinator) transcribeSinglePass(source SourceType, samples []float32, startTime, endTime float64, spk string, decision speaker.AssignDecision) {
	began := time.Now()
	defer c.cfg.Timings.utterance(began)
	c.decodeMu.Lock()
	result, err := c.decode(samples)
	c.decodeMu.Unlock()
	c.cfg.Timings.add(&c.timingsOrZero().Decode, began)

	var text string
	if err == nil && result != nil {
		text = strings.TrimSpace(result.Text)
	}
	if text == "" {
		return
	}

	// Labelling waits until there are words: audio that decodes to nothing
	// (a cough, noise) would otherwise start or pull a speaker cluster.
	if spk == "" {
		spk, decision = c.assignSpeaker(source, samples, startTime)
	}
	spk, decision = c.micWords(source, spk, decision, result.Text)

	seg := session.Segment{
		ID:        c.nextSegID(),
		Speaker:   spk,
		Text:      text,
		Language:  result.Language,
		Source:    string(source),
		StartTime: startTime,
		EndTime:   endTime,
		Decision:  string(decision),
		Words:     wordsFor(result, startTime, endTime),
	}
	if c.cfg.DecodeAsync {
		// On the worker: wait for the consumer rather than lose the line.
		c.segmentCh <- seg
	} else {
		select {
		case c.segmentCh <- seg:
		default:
		}
	}
	c.countSegment(source)
}

// finishLive closes a live line whose utterance was still going when its
// source stopped (the detector never finished it), so the saved session
// has refined text for it instead of a line left "live". Blocking on
// refineCh is fine this late: it stays open until every pipeline has
// returned, and this one has no audio left to keep up with.
func (c *Coordinator) finishLive(source SourceType, live *liveState) {
	if live.id == "" {
		return
	}
	text := cmp.Or(strings.TrimSpace(live.partial), live.shown)
	spk, decision := c.assignSpeaker(source, live.audio, live.startTime)
	if c.cfg.TurnMode {
		c.addTurnPart(source, turnPart{samples: live.audio, start: live.startTime, end: c.elapsed(), twoPass: true,
			id: live.id, pass1: text, speaker: spk, decision: decision})
		c.countSegment(source)
		live.reset()
		return
	}
	job := refinementJob{
		id: live.id, samples: live.audio, speaker: spk, decision: decision,
		startTime: live.startTime, endTime: c.elapsed(), source: source, pass1Text: text,
	}
	c.refineCh <- job
	c.countSegment(source)
	live.reset()
}

// countSegment adds a line to its source's count in Stats.
func (c *Coordinator) countSegment(source SourceType) {
	n := &c.monitorLines
	if source == SourceMic {
		n = &c.micLines
	}
	n.Add(1)
}

// refineWorker drains refinement jobs (pass 2): re-decode a segment's
// audio through the offline Engine (allowed to be slower/better than
// pass 1's streaming decode), then deliver the result: an update for a
// line already shown, a new segment for one that wasn't. It stops when
// refineCh is closed, not when the session's context ends, so work queued
// as a session stops still finishes; that is also why its sends may
// block: Start keeps both output channels open until it returns.
func (c *Coordinator) refineWorker() {
	defer c.refineWG.Done()

	for job := range c.refineCh {
		began := c.now()
		seg, kept := c.refine(job)
		c.notePass2(job, c.now().Sub(began))
		switch {
		case !kept:
		case job.unannounced:
			c.segmentCh <- seg
			c.countSegment(job.source)
		default:
			c.emitTurnLines(seg, job.absorb, true)
		}
	}
}

// refine is pass 2 for one job: the line's final segment, with Engine's
// text, or pass 1's when the decode fails or hears nothing. ok is false
// only when there is no text either way, which happens only for an
// unannounced job: nothing was shown, so nothing is lost.
func (c *Coordinator) refine(job refinementJob) (seg session.Segment, ok bool) {
	began := time.Now()
	defer c.cfg.Timings.utterance(began)
	c.decodeMu.Lock()
	result, err := c.decode(job.samples)
	c.decodeMu.Unlock()
	c.cfg.Timings.add(&c.timingsOrZero().Decode, began)

	text, lang := job.pass1Text, "en"
	var words []session.Word
	if err == nil && result != nil {
		if decoded := strings.TrimSpace(result.Text); decoded != "" {
			text, lang = decoded, cmp.Or(result.Language, lang)
			words = wordsFor(result, job.startTime, job.endTime)
		}
	}
	if text == "" {
		return seg, false
	}

	spk, decision := job.speaker, job.decision
	if job.unannounced {
		spk, decision = c.speakerLabel(job.source, job.embedding, sampleDuration(job.samples))
	}
	spk, decision = c.micWords(job.source, spk, decision, text)
	seg = session.Segment{
		ID:        job.id,
		Source:    string(job.source),
		Speaker:   spk,
		StartTime: job.startTime,
		EndTime:   job.endTime,
		Text:      text,
		Language:  lang,
		Decision:  string(decision),
		Words:     words,
	}
	return seg, true
}

// assignSpeaker determines the speaker label for a segment, and which
// rule inside speaker.Tracker.Assign produced it (see speakerLabel).
func (c *Coordinator) assignSpeaker(source SourceType, samples []float32, startTime float64) (string, speaker.AssignDecision) {
	c.probePrefixes(source, samples, startTime)
	c.probeWindows(source, samples, startTime)
	return c.speakerLabel(source, c.speakerEmbedding(source, samples), sampleDuration(samples))
}

// probePrefixes records Config.ProbePrefixes labels for an utterance.
func (c *Coordinator) probePrefixes(source SourceType, samples []float32, startTime float64) {
	if source == SourceMic || len(c.cfg.ProbePrefixes) == 0 || c.cfg.Probes == nil || c.cfg.Tracker == nil {
		return
	}
	var labels []ProbeLabel
	for _, p := range c.cfg.ProbePrefixes {
		n := int(p * vadSampleRate)
		if n >= len(samples) {
			continue
		}
		if emb := c.speakerEmbedding(source, samples[:n]); len(emb) > 0 {
			labels = append(labels, ProbeLabel{Prefix: p, Speaker: c.cfg.Tracker.Peek(emb)})
		}
	}
	c.cfg.Probes.add(startTime, labels)
}

// probeWindows records Config.WindowSize labels for an utterance.
func (c *Coordinator) probeWindows(source SourceType, samples []float32, startTime float64) {
	size, step := c.cfg.WindowSize, c.cfg.WindowStep
	if source == SourceMic || size <= 0 || c.cfg.Probes == nil || c.cfg.Tracker == nil {
		return
	}
	if step <= 0 {
		step = size / 2
	}
	n, hop := int(size*vadSampleRate), int(step*vadSampleRate)
	if len(samples) <= n {
		return
	}
	var labels []WindowLabel
	for from := 0; from+n <= len(samples); from += hop {
		emb := c.speakerEmbedding(source, samples[from:from+n])
		if len(emb) == 0 {
			continue
		}
		if label, ok := c.cfg.Tracker.PeekMatch(emb); ok {
			labels = append(labels, WindowLabel{
				Start:   startTime + float64(from)/vadSampleRate,
				End:     startTime + float64(from+n)/vadSampleRate,
				Speaker: label,
			})
		}
	}
	c.cfg.Probes.addWindows(startTime, labels)
}

// sampleDuration is how long samples (at vadSampleRate) plays for.
func sampleDuration(samples []float32) time.Duration {
	return time.Duration(float64(len(samples)) / vadSampleRate * float64(time.Second))
}

// provisionalSpeaker is the label a live line goes up with, judged from
// the half second or so heard so far. It only peeks at the tracker
// (speaker.Tracker.Peek), so a guess this early never shapes a cluster;
// handleSegment labels the whole utterance when it ends.
func (c *Coordinator) provisionalSpeaker(source SourceType, samples []float32) string {
	switch {
	case source == SourceMic:
		// Settled once the utterance is complete (micLabel).
		return "You"
	case c.cfg.SkipMonitorDiarization:
		return "System Audio"
	}
	emb := c.speakerEmbedding(source, samples)
	if len(emb) == 0 {
		return "Other"
	}
	return c.cfg.Tracker.Peek(emb)
}

// speakerEmbedding is the voice fingerprint speakerLabel works from, or
// nil when the source is labelled without one. The two are separate
// because an Embedder must stay on one goroutine (the pipeline's), while
// an unannounced line is only labelled later, on refineWorker.
func (c *Coordinator) speakerEmbedding(source SourceType, samples []float32) []float32 {
	if source == SourceMic {
		if !c.micVoices() || sampleDuration(samples) < micMinOther {
			return nil
		}
		// Lines are labelled from either pipeline and the decode worker.
		c.micEmbedMu.Lock()
		emb, err := c.cfg.MicEmbedder.Extract(samples)
		c.micEmbedMu.Unlock()
		if err != nil {
			return nil
		}
		return emb
	}
	if c.cfg.SkipMonitorDiarization || c.cfg.Embedder == nil || c.cfg.Tracker == nil {
		return nil
	}
	embedBegan := time.Now()
	emb, err := c.cfg.Embedder.Extract(samples)
	c.cfg.Timings.add(&c.timingsOrZero().Embed, embedBegan)
	if err != nil {
		emb = nil
	}
	return emb
}

// speakerLabel decides who said a segment, from its fingerprint: the
// speaker label, clustering monitor-source speakers via the Tracker, and
// which rule inside Tracker.Assign produced it ("" when Assign was never
// called, e.g. "You"/"System Audio"/"Other") -- see speaker.AssignDecision
// and session.Segment.Decision, which a diagnostics view uses to show how
// each line was labeled. duration is how much audio the embedding was
// computed from (see minAssignDuration's doc comment in internal/speaker).
func (c *Coordinator) speakerLabel(source SourceType, embedding []float32, duration time.Duration) (string, speaker.AssignDecision) {
	switch {
	case source == SourceMic:
		return c.micLabel(embedding, duration)
	case c.cfg.SkipMonitorDiarization:
		return "System Audio", ""
	case len(embedding) == 0 || c.cfg.Tracker == nil:
		// Meeting audio with no fingerprint or nothing to cluster it in.
		return "Other", ""
	}

	before := c.cfg.Tracker.NumSpeakers()
	assignBegan := time.Now()
	label, needsHint := c.cfg.Tracker.Assign(embedding, duration)
	c.cfg.Timings.add(&c.timingsOrZero().Assign, assignBegan)
	decision := c.cfg.Tracker.LastDecision()
	isNew := c.cfg.Tracker.NumSpeakers() > before

	if isNew {
		// A brand-new speaker is rarer and more valuable to
		// resolve than an ordinary "still no hint" retry --
		// found live: waiting on the regular debounced trigger
		// alone made naming attempts feel too infrequent to
		// ever catch a fast-moving ring. Fire twice,
		// bypassing the usual debounce entirely: once right
		// now, and once again ~500ms later in case the ring/
		// label hadn't rendered yet on the first attempt.
		c.signalHintNeeded(true)
		go func() {
			time.Sleep(500 * time.Millisecond)
			c.signalHintNeeded(true)
		}()
	} else if needsHint {
		// A known speaker still without a name: ask for a look now
		// instead of at the poller's next tick. The signal drops if
		// nobody is listening, so labelling never waits on it.
		c.signalHintNeeded(false)
	}
	return label, decision
}

// micMinOther is the shortest mic utterance that can be someone else's:
// a short one's fingerprint is unreliable, and the mic is mostly the
// user. micSurelyOwner is the similarity above which (for an utterance of
// at least micLearnFrom) a fingerprint is added to the user's voice.
const (
	micMinOther    = 1500 * time.Millisecond
	micLearnFrom   = 2 * time.Second
	micSurelyOwner = 0.6
)

// micMinOtherWords is the fewest different words a mic utterance needs to
// be someone else's: the two utterances first labelled so on calls with
// nobody beside the user (2026-11-09) were "No. No. No. No." and laughter.
const micMinOtherWords = 4

// micWords settles a mic line's speaker once its words are known: the
// voice was judged before, and laughter or one word said over and over
// sounds unlike the user's speaking voice, so it takes enough different
// words to be someone else.
func (c *Coordinator) micWords(source SourceType, spk string, decision speaker.AssignDecision, text string) (string, speaker.AssignDecision) {
	if source == SourceMic && spk != "You" && distinctWords(text) < micMinOtherWords {
		return "You", ""
	}
	return spk, decision
}

// distinctWords counts the different words in text, ignoring case and
// punctuation.
func distinctWords(text string) int {
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' }) {
		seen[w] = true
	}
	return len(seen)
}

func (c *Coordinator) micVoices() bool {
	return c.cfg.MicEmbedder != nil && len(c.cfg.OwnerVoice) > 0 && c.cfg.MicOtherBelow > 0
}

// micLabel labels a mic utterance: "You" unless it's long enough to tell
// and its voice is unlike the user's (config mic_other_below), then a
// speaker clustered with the monitor's (no video hint is sought: the
// person is in the room, not on screen).
func (c *Coordinator) micLabel(embedding []float32, duration time.Duration) (string, speaker.AssignDecision) {
	if len(embedding) == 0 || !c.micVoices() {
		return "You", ""
	}
	norm := voices.Normalized(embedding)
	sim := voices.Similarity(c.cfg.OwnerVoice, norm)
	if sim >= c.cfg.MicOtherBelow || duration < micMinOther {
		if sim >= micSurelyOwner && duration >= micLearnFrom && c.cfg.HeardOwner != nil {
			c.cfg.HeardOwner(norm)
		}
		return "You", ""
	}
	if c.cfg.Tracker == nil {
		return "Other", ""
	}
	label, _ := c.cfg.Tracker.Assign(embedding, duration)
	return label, c.cfg.Tracker.LastDecision()
}

// hasSignal reports whether a window holds anything above about -80
// dBFS: a live mic picks up at least room noise, a disabled one sends
// zeros.
func hasSignal(window []float32) bool {
	for _, v := range window {
		if v > 1e-4 || v < -1e-4 {
			return true
		}
	}
	return false
}

// levelDB is samples' RMS level in dBFS (-200 for silence).
func levelDB(samples []float32) float64 {
	if len(samples) == 0 {
		return -200
	}
	var sum float64
	for _, v := range samples {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms == 0 {
		return -200
	}
	return 20 * math.Log10(rms)
}

// turnBuf is TurnMode's line being collected for one source.
type turnBuf struct {
	samples    []float32
	start, end float64
	// Two-pass only: the pending lines shown for its utterances (the
	// first becomes the turn's line, the rest are withdrawn), their pass-1
	// text, and the first line's speaker label.
	twoPass  bool
	ids      []string
	pass1    []string
	speaker  string
	decision speaker.AssignDecision
}

// turnPart is one utterance added to a turn.
type turnPart struct {
	samples    []float32
	start, end float64
	twoPass    bool
	id, pass1  string // two-pass: its pending line, if one was shown
	speaker    string
	decision   speaker.AssignDecision
}

// minHighlightPiece is the shortest piece an utterance is cut into at a
// highlight change: a flicker of the highlight doesn't shred a line.
const minHighlightPiece = 0.4

// splitAtHighlight cuts a meeting-audio utterance where the meeting
// window's highlight moved to someone else (see Config.HighlightChanges).
// The first piece keeps the utterance's pending line and speaker; the
// others are new lines whose speaker is worked out from their own audio.
// Returns nil when there's nothing to cut.
func (c *Coordinator) splitAtHighlight(source SourceType, u turnPart) []turnPart {
	if source != SourceMonitor || c.cfg.HighlightChanges == nil || !c.cfg.TurnMode {
		return nil
	}
	lag := c.cfg.HighlightLag
	var cuts []float64
	last := u.start
	for _, t := range c.cfg.HighlightChanges(u.start+lag+minHighlightPiece, u.end+lag-minHighlightPiece) {
		at := t - lag
		if at-last >= minHighlightPiece && u.end-at >= minHighlightPiece {
			cuts = append(cuts, at)
			last = at
		}
	}
	if len(cuts) == 0 {
		return nil
	}
	var pieces []turnPart
	from := u.start
	for i, at := range append(cuts, u.end) {
		a := int((from - u.start) * vadSampleRate)
		b := min(len(u.samples), int((at-u.start)*vadSampleRate))
		if at == u.end {
			b = len(u.samples)
		}
		p := turnPart{samples: u.samples[a:b], start: from, end: at, twoPass: u.twoPass}
		if i == 0 {
			p.id, p.pass1, p.speaker, p.decision = u.id, u.pass1, u.speaker, u.decision
		}
		pieces = append(pieces, p)
		from = at
	}
	c.turnMu.Lock()
	if c.turnCuts == nil {
		c.turnCuts = map[string]int{}
	}
	c.turnCuts["highlight change inside an utterance"] += len(cuts)
	c.turnMu.Unlock()
	return pieces
}

// The mic's voice split (splitAtVoice): micVoiceWindow-second windows
// every micVoiceHop seconds; micVoiceRun windows in a row must agree
// before the voice counts as changed; no piece is shorter than
// micVoicePiece seconds.
const (
	micVoiceWindow = 2.0
	micVoiceHop    = 1.0
	micVoiceRun    = 2
	micVoicePiece  = 3.0
)

// splitAtVoice cuts a mic utterance where the voice changes between the
// user's and someone else's (mic_voices). The VAD only ends an utterance
// at a pause, and in a room two people often leave none, so one utterance
// can hold both. Each piece is labelled by its own voice (micLabel); the
// first keeps the utterance's pending line. Returns nil when there's
// nothing to cut.
func (c *Coordinator) splitAtVoice(source SourceType, u turnPart) []turnPart {
	if source != SourceMic || u.speaker != "" || !c.micVoices() || !c.cfg.TurnMode || u.end-u.start < 2*micVoicePiece {
		return nil
	}
	win, hop := int(micVoiceWindow*vadSampleRate), int(micVoiceHop*vadSampleRate)
	var other []bool // per window: unlike the user's voice
	for a := 0; a+win <= len(u.samples); a += hop {
		c.micEmbedMu.Lock()
		emb, err := c.cfg.MicEmbedder.Extract(u.samples[a : a+win])
		c.micEmbedMu.Unlock()
		if err != nil || len(emb) == 0 {
			return nil
		}
		other = append(other, voices.Similarity(c.cfg.OwnerVoice, voices.Normalized(emb)) < c.cfg.MicOtherBelow)
	}
	cuts := voiceCuts(other, u.start, u.end)
	if len(cuts) == 0 {
		return nil
	}
	var pieces []turnPart
	from := u.start
	for i, at := range append(cuts, u.end) {
		a := int((from - u.start) * vadSampleRate)
		b := min(len(u.samples), int((at-u.start)*vadSampleRate))
		if at == u.end {
			b = len(u.samples)
		}
		p := turnPart{samples: u.samples[a:b], start: from, end: at, twoPass: u.twoPass}
		if i == 0 {
			p.id, p.pass1 = u.id, u.pass1
		}
		if emb := c.speakerEmbedding(source, p.samples); len(emb) > 0 {
			p.speaker, p.decision = c.micLabel(emb, sampleDuration(p.samples))
		} else {
			p.speaker = "You"
		}
		pieces = append(pieces, p)
		from = at
	}
	c.turnMu.Lock()
	if c.turnCuts == nil {
		c.turnCuts = map[string]int{}
	}
	c.turnCuts["voice change inside an utterance"] += len(cuts)
	c.turnMu.Unlock()
	return pieces
}

// voiceCuts is where splitAtVoice cuts an utterance from start to end,
// given which of its windows sound unlike the user. The voice changes at
// the first of micVoiceRun windows in a row that disagree with the
// current one; the cut goes mid-way into the window overlap, where the
// new voice most likely began.
func voiceCuts(other []bool, start, end float64) []float64 {
	if len(other) < micVoiceRun {
		return nil
	}
	var cuts []float64
	last, cur := start, other[0]
	for i := 1; i+micVoiceRun <= len(other); i++ {
		if other[i] == cur {
			continue
		}
		run := true
		for _, o := range other[i : i+micVoiceRun] {
			run = run && o != cur
		}
		if !run {
			continue
		}
		at := start + float64(i)*micVoiceHop + (micVoiceWindow-micVoiceHop)/2
		if at-last >= micVoicePiece && end-at >= micVoicePiece {
			cuts = append(cuts, at)
			last = at
		}
		cur = other[i]
	}
	return cuts
}

// decode transcribes samples with Config.DecodePad seconds of silence on
// each side, and gives token times relative to samples' own start.
// Cohere drops the last syllable of audio that stops abruptly ("What'"
// for "What's up?"); the silence lets it finish the word.
func (c *Coordinator) decode(samples []float32) (*transcribe.Result, error) {
	n := int(c.cfg.DecodePad * vadSampleRate)
	if n <= 0 {
		return c.cfg.Engine.TranscribeDirect(samples)
	}
	padded := make([]float32, n, len(samples)+2*n)
	padded = append(padded, samples...)
	padded = append(padded, make([]float32, n)...)
	result, err := c.cfg.Engine.TranscribeDirect(padded)
	if result != nil {
		shift := float32(n) / vadSampleRate
		for i, t := range result.Timestamps {
			result.Timestamps[i] = max(0, t-shift)
		}
	}
	return result, err
}

// turnGapPad is the silence put between collected utterances, so the
// model hears a pause where there was one.
const turnGapPad = 0.2

func otherSource(s SourceType) SourceType {
	if s == SourceMic {
		return SourceMonitor
	}
	return SourceMic
}

// addToTurn adds a single-pass utterance to its source's turn.
func (c *Coordinator) addToTurn(source SourceType, samples []float32, start, end float64) {
	c.addTurnPart(source, turnPart{samples: samples, start: start, end: end})
}

// addTurnPart adds an utterance to its source's line, first sending the
// line (and the other source's) on when a turn ended: the other side
// spoke, a speaker-change signal fell between them, the pause was longer
// than TurnMaxGap, or the line would pass TurnMaxSeconds.
func (c *Coordinator) addTurnPart(source SourceType, u turnPart) {
	if pieces := c.splitAtHighlight(source, u); len(pieces) > 1 {
		for _, p := range pieces {
			c.addTurnPart(source, p)
		}
		return
	}
	if pieces := c.splitAtVoice(source, u); len(pieces) > 1 {
		for _, p := range pieces {
			c.addTurnPart(source, p)
		}
		return
	}
	// On the mic (mic_voices), each utterance long enough to tell is
	// labelled by its own voice, and a change of voice ends the line: in a
	// room there's no other side to end it, so one line would hold both
	// people and take the voice of whoever spoke more. Too short to tell:
	// it joins whatever line is going.
	if source == SourceMic && u.speaker == "" && c.micVoices() {
		if emb := c.speakerEmbedding(source, u.samples); len(emb) > 0 {
			u.speaker, u.decision = c.micLabel(emb, sampleDuration(u.samples))
		}
	}
	c.turnMu.Lock()
	if c.turns == nil {
		c.turns = map[SourceType]*turnBuf{}
	}
	if c.turnCuts == nil {
		c.turnCuts = map[string]int{}
	}
	type sendable struct {
		source SourceType
		buf    *turnBuf
	}
	var ready []sendable
	take := func(s SourceType) {
		if t := c.turns[s]; t != nil && len(t.samples) > 0 {
			ready = append(ready, sendable{s, t})
			c.turns[s] = nil
		}
	}
	if o := c.turns[otherSource(source)]; o != nil && len(o.samples) > 0 {
		if c.cfg.TurnInterjection > 0 && u.end-u.start <= c.cfg.TurnInterjection {
			// A backchannel ("mm", "yeah") over the other side's turn:
			// that turn goes on; the interjection is its own line.
			c.turnCuts["interjection (other turn kept)"]++
		} else {
			take(otherSource(source))
			c.turnCuts["other side spoke"]++
		}
	}
	if t := c.turns[source]; t != nil && len(t.samples) > 0 {
		maxSecs, maxGap := c.turnLimits()
		changed := c.cfg.SpeakerChanged != nil && source == SourceMonitor && c.cfg.SpeakerChanged(t.end-0.25, u.start+1)
		cut := true
		voice := source == SourceMic && c.micVoices() && t.speaker != "" && u.speaker != "" && u.speaker != t.speaker
		switch {
		case changed:
			c.turnCuts["speaker-change signal"]++
		case voice:
			c.turnCuts["voice changed"]++
		case u.start-t.end > maxGap:
			c.turnCuts["pause"]++
		case (t.end-t.start)+(u.end-u.start) > maxSecs:
			c.turnCuts["length cap"]++
		default:
			c.turnCuts["joined"]++
			cut = false
		}
		if cut {
			take(source)
		}
	}
	t := c.turns[source]
	if t == nil {
		t = &turnBuf{start: u.start, twoPass: u.twoPass, speaker: u.speaker, decision: u.decision}
		c.turns[source] = t
	} else {
		t.samples = append(t.samples, make([]float32, int(turnGapPad*vadSampleRate))...)
		if t.speaker == "" {
			t.speaker, t.decision = u.speaker, u.decision
		}
	}
	t.samples = append(t.samples, u.samples...)
	t.end = u.end
	if u.id != "" {
		t.ids = append(t.ids, u.id)
	}
	if u.pass1 != "" {
		t.pass1 = append(t.pass1, u.pass1)
	}
	c.turnMu.Unlock()
	for _, r := range ready {
		c.sendTurn(r.source, r.buf)
	}
}

func (c *Coordinator) turnLimits() (maxSecs, maxGap float64) {
	maxSecs, maxGap = c.cfg.TurnMaxSeconds, c.cfg.TurnMaxGap
	if maxSecs <= 0 {
		maxSecs = 30
	}
	if maxGap <= 0 {
		maxGap = 2
	}
	return maxSecs, maxGap
}

// sendTurn transcribes a finished turn: at once in single-pass mode; in
// two-pass mode as one refinement job that becomes the turn's first
// pending line and withdraws the others (see refineWorker).
func (c *Coordinator) sendTurn(source SourceType, t *turnBuf) {
	if !t.twoPass {
		spk, decision := "", speaker.AssignDecision("")
		if source == SourceMic && c.micVoices() {
			spk, decision = t.speaker, t.decision
		}
		c.singlePassAs(source, t.samples, t.start, t.end, spk, decision)
		return
	}
	job := refinementJob{
		samples: t.samples, speaker: t.speaker, decision: t.decision,
		startTime: t.start, endTime: t.end, source: source,
		pass1Text: strings.Join(t.pass1, " "),
	}
	if len(t.ids) == 0 {
		// Pass 1 showed nothing for this turn: pass 2 introduces it.
		job.id, job.unannounced = c.nextSegID(), true
		job.embedding = c.speakerEmbedding(source, t.samples)
		select {
		case c.refineCh <- job:
		default:
			if seg, ok := c.refine(job); ok {
				c.segmentCh <- seg
				c.countSegment(source)
			}
		}
		return
	}
	job.id, job.absorb = t.ids[0], t.ids[1:]
	select {
	case c.refineCh <- job:
	default:
		// Refinement queue backed up: the joined pass-1 text stands.
		c.emitTurnLines(session.Segment{
			ID: job.id, Speaker: t.speaker, Text: job.pass1Text, StartTime: t.start, EndTime: t.end,
			Source: string(source), Language: "en", Decision: string(t.decision),
		}, job.absorb, false)
	}
}

// emitTurnLines sends a turn's final line (as an update of its first
// pending line) and withdraws the pending lines it absorbed. block waits
// for room on the channel (the refinement worker); the live pipeline
// doesn't.
func (c *Coordinator) emitTurnLines(seg session.Segment, absorb []string, block bool) {
	send := func(s session.Segment) {
		if block {
			c.segmentUpdateCh <- s
			return
		}
		select {
		case c.segmentUpdateCh <- s:
		default:
		}
	}
	send(seg)
	for _, id := range absorb {
		send(session.Segment{ID: id, Source: seg.Source, Status: session.StatusRemoved})
	}
}

// flushStaleTurn sends source's line once the source has been quiet
// (no speech since lastSpeech) for longer than the gap that ends a turn,
// so a finished turn isn't held back until the next utterance. Measured
// from the last speech, not the line's end: an utterance still being
// spoken belongs to the turn.
func (c *Coordinator) flushStaleTurn(source SourceType, lastSpeech float64) {
	_, maxGap := c.turnLimits()
	c.turnMu.Lock()
	t := c.turns[source]
	if t == nil || len(t.samples) == 0 || c.elapsed()-max(t.end, lastSpeech) <= maxGap {
		c.turnMu.Unlock()
		return
	}
	c.turns[source] = nil
	if c.turnCuts == nil {
		c.turnCuts = map[string]int{}
	}
	c.turnCuts["quiet (stale)"]++
	c.turnMu.Unlock()
	c.sendTurn(source, t)
}

// flushTurn sends whatever line source has open (at the end of a session).
func (c *Coordinator) flushTurn(source SourceType) {
	c.turnMu.Lock()
	t := c.turns[source]
	if c.turns != nil {
		c.turns[source] = nil
	}
	c.turnMu.Unlock()
	if t != nil && len(t.samples) > 0 {
		c.sendTurn(source, t)
	}
}

// TurnCuts reports how often TurnMode cut a line for each reason, and how
// often an utterance joined the open line ("joined").
func (c *Coordinator) TurnCuts() map[string]int {
	c.turnMu.Lock()
	defer c.turnMu.Unlock()
	out := map[string]int{}
	for k, v := range c.turnCuts {
		out[k] = v
	}
	return out
}

// wordsFor is a result's word timings: from its token timestamps, or
// estimated (session.SpreadWords) for a model that returns none, so the
// line can still be split at a speaker change.
func wordsFor(result *transcribe.Result, start, end float64) []session.Word {
	if w := session.WordsFromTokens(result.Tokens, result.Timestamps, start, end); w != nil {
		return w
	}
	return session.SpreadWords(strings.TrimSpace(result.Text), start, end)
}

// pass2Stats sums a minute of pass 2: how many utterances were finalized,
// the decode time against the speech it covered, and the longest lag from
// an utterance's end to its final text. A single worker runs pass 2, so if
// decoding takes longer than the speech (a busy or swapping machine), the
// queue grows and lines stay "pending" for minutes; this makes that show
// up in the log while it happens.
type pass2Stats struct {
	n              int
	decode, speech time.Duration
	maxLag         float64 // seconds
}

func (c *Coordinator) notePass2(job refinementJob, decode time.Duration) {
	lag := c.now().Sub(c.startTime).Seconds() - job.endTime
	c.pass2Mu.Lock()
	defer c.pass2Mu.Unlock()
	c.pass2.n++
	c.pass2.decode += decode
	c.pass2.speech += sampleDuration(job.samples)
	c.pass2.maxLag = max(c.pass2.maxLag, lag)
}

// logPass2 writes one line a minute while pass 2 has work: utterances
// finalized, decode time as a share of their speech, the longest lag to
// final text, and how many utterances are still waiting.
func (c *Coordinator) logPass2(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.pass2Mu.Lock()
		st := c.pass2
		c.pass2 = pass2Stats{}
		c.pass2Mu.Unlock()
		waiting := len(c.refineCh)
		c.logBacklog()
		if st.n == 0 && waiting == 0 {
			continue
		}
		share := 0.0
		if st.speech > 0 {
			share = 100 * st.decode.Seconds() / st.speech.Seconds()
		}
		fmt.Printf("live: pass 2, last minute: %d utterances, decode %.1fs for %.1fs of speech (%.0f%%), longest lag to final text %.1fs, %d waiting\n",
			st.n, st.decode.Seconds(), st.speech.Seconds(), share, st.maxLag, waiting)
	}
}

// logBacklog reports, once a minute, audio held back because the pipeline
// is behind and lines waiting to be decoded: on an overloaded machine the
// transcript arrives late but complete.
func (c *Coordinator) logBacklog() {
	var parts []string
	for _, s := range []struct {
		name string
		sc   *audio.StreamCapturer
	}{{"meeting audio", c.cfg.MonitorCapturer}, {"mic", c.cfg.MicCapturer}} {
		if s.sc != nil {
			if b := float64(s.sc.Behind()) / vadSampleRate; b >= 1 {
				parts = append(parts, fmt.Sprintf("%.0fs of %s", b, s.name))
			}
		}
	}
	if n := len(c.decodeCh); n > 0 {
		parts = append(parts, fmt.Sprintf("%d lines to decode", n))
	}
	if len(parts) > 0 {
		fmt.Printf("live: behind, catching up: %s waiting\n", strings.Join(parts, ", "))
	}
}
