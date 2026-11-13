package live

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/speaker"
	"github.com/anielsud/musubi/internal/recorder/transcribe"
)

// fakeCapturer stands in for an audio device: it records nothing and
// remembers what the Coordinator asked of it.
type fakeCapturer struct {
	failStart       error
	stopped, closed atomic.Bool
}

func (f *fakeCapturer) Start() error       { return f.failStart }
func (f *fakeCapturer) Stop() error        { f.stopped.Store(true); return nil }
func (f *fakeCapturer) Samples() []float32 { return nil }
func (f *fakeCapturer) Reset()             {}
func (f *fakeCapturer) Close()             { f.closed.Store(true) }

// capture wraps f the way a live session wraps a device.
func capture(f *fakeCapturer) *audio.StreamCapturer {
	return audio.NewStreamCapturer(f, audio.DefaultWindowSize, 10)
}

// mockEngine is a decoder with a fixed answer: result and err for every
// call, whatever the audio.
type mockEngine struct {
	result  *transcribe.Result
	err     error
	decodes atomic.Int32
}

func (m *mockEngine) TranscribeDirect([]float32) (*transcribe.Result, error) {
	m.decodes.Add(1)
	return m.result, m.err
}

func (m *mockEngine) TranscribeSamples(s []float32) (*transcribe.Result, error) {
	return m.TranscribeDirect(s)
}

func (m *mockEngine) TranscribeFile(string) (*transcribe.Result, error) { return m.result, m.err }
func (m *mockEngine) Close()                                            {}

// Without mic_voices the mic is always the user, and no clustering rule
// is involved; meeting audio with no embedder can't be told apart.
func TestSpeakerWithoutFingerprints(t *testing.T) {
	c := &Coordinator{}
	for _, tc := range []struct {
		source       SourceType
		want         string
		wantDecision speaker.AssignDecision
	}{
		{SourceMic, "You", ""},
		{SourceMonitor, "Other", ""},
	} {
		got, decision := c.assignSpeaker(tc.source, make([]float32, 16000), 0)
		if got != tc.want || decision != tc.wantDecision {
			t.Errorf("%s: %q (%q), want %q (%q)", tc.source, got, decision, tc.want, tc.wantDecision)
		}
	}
}

func TestAssignSpeakerMonitorSkipDiarization(t *testing.T) {
	c := &Coordinator{
		cfg: Config{SkipMonitorDiarization: true},
	}

	label, decision := c.assignSpeaker(SourceMonitor, []float32{0.1, 0.2}, 0)
	if label != "System Audio" {
		t.Errorf("monitor speaker with SkipMonitorDiarization = %q, want %q", label, "System Audio")
	}
	if decision != "" {
		t.Errorf("decision = %q, want empty (diarization skipped)", decision)
	}
}

func TestAssignSpeakerMicUnaffectedBySkipDiarization(t *testing.T) {
	c := &Coordinator{
		cfg: Config{SkipMonitorDiarization: true},
	}

	label, _ := c.assignSpeaker(SourceMic, []float32{0.1, 0.2}, 0)
	if label != "You" {
		t.Errorf("mic speaker with SkipMonitorDiarization = %q, want %q (mic is unaffected)", label, "You")
	}
}

// Segment IDs are numbered from 1 in the order they're handed out; saved
// sessions and replays rely on the "seg-N" form.
func TestSegmentIDsNumberUp(t *testing.T) {
	c := New(Config{})
	got := []string{c.nextSegID(), c.nextSegID(), c.nextSegID()}
	for i, id := range []string{"seg-1", "seg-2", "seg-3"} {
		if got[i] != id {
			t.Errorf("ID %d = %q, want %q", i+1, got[i], id)
		}
	}
}

// The source names are saved in every segment (Segment.Source).
func TestSourceNames(t *testing.T) {
	if string(SourceMic)+","+string(SourceMonitor) != "mic,monitor" {
		t.Errorf("sources are %q and %q", SourceMic, SourceMonitor)
	}
}

// SegmentBufferSize sizes the output queue, 64 when unset.
func TestOutputQueueSize(t *testing.T) {
	for size, want := range map[int]int{0: 64, -3: 64, 32: 32} {
		if got := cap(New(Config{SegmentBufferSize: size}).segmentCh); got != want {
			t.Errorf("SegmentBufferSize %d: queue of %d, want %d", size, got, want)
		}
	}
}

// What the coordinator relies on from the tracker: a returning voice gets
// its earlier label back, a different one a new label.
func TestTrackerKeepsReturningVoice(t *testing.T) {
	tr := speaker.NewTracker(0.7)
	a, b := []float32{0, 0, 1, 0}, []float32{0, 0, 0, 1}
	first, _ := tr.Assign(a, 3*time.Second)
	other, _ := tr.Assign(b, 3*time.Second)
	again, _ := tr.Assign(a, 3*time.Second)
	if first != "Person 1" || other != "Person 2" || again != first {
		t.Errorf("labels %q, %q, %q; want Person 1, Person 2, Person 1", first, other, again)
	}
}

// Stop releases every configured device, whether or not Start ran, and
// can be called again.
func TestStopReleasesDevices(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mic, mon bool
	}{{"mic", true, false}, {"meeting audio", false, true}, {"both", true, true}, {"none", false, false}} {
		var cfg Config
		mic, mon := &fakeCapturer{}, &fakeCapturer{}
		if tc.mic {
			cfg.MicCapturer = capture(mic)
		}
		if tc.mon {
			cfg.MonitorCapturer = capture(mon)
		}
		c := New(cfg)
		c.Stop()
		c.Stop()
		if mic.closed.Load() != tc.mic || mon.closed.Load() != tc.mon {
			t.Errorf("%s: mic closed %v, meeting audio closed %v", tc.name, mic.closed.Load(), mon.closed.Load())
		}
	}
}

// Start refuses to run with nothing to record.
func TestStartNeedsASource(t *testing.T) {
	if err := New(Config{Engine: &mockEngine{}}).Start(context.Background()); err == nil {
		t.Fatal("started with no audio source")
	}
}

// A device that won't start fails Start, and leaves nothing recording.
func TestStartFailureStopsOtherCapture(t *testing.T) {
	if err := New(Config{Engine: &mockEngine{}, MicCapturer: capture(&fakeCapturer{failStart: errors.New("no mic")})}).Start(context.Background()); err == nil {
		t.Error("started although the mic failed")
	}

	mic := &fakeCapturer{}
	c := New(Config{
		Engine:          &mockEngine{},
		MicCapturer:     capture(mic),
		MonitorCapturer: capture(&fakeCapturer{failStart: errors.New("no meeting audio")}),
	})
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("started although the meeting audio failed")
	}
	if !mic.stopped.Load() {
		t.Error("the mic was left recording after the meeting audio failed")
	}
}

// Segments hands out the channel lines are sent on.
func TestSegmentsIsTheOutputChannel(t *testing.T) {
	c := New(Config{})
	c.segmentCh <- session.Segment{ID: "seg-4"}
	if got := <-c.Segments(); got.ID != "seg-4" {
		t.Errorf("Segments() gave %+v", got)
	}
}

// Stats counts lines per source.
func TestStatsCountsPerSource(t *testing.T) {
	c := New(Config{})
	for _, s := range []SourceType{SourceMic, SourceMonitor, SourceMic, SourceMic} {
		c.countSegment(s)
	}
	if st := c.Stats(); st.MicSegments != 3 || st.MonitorSegments != 1 {
		t.Errorf("stats %+v, want 3 mic and 1 meeting-audio line", st)
	}
}

// With no capturers there's no audio to save and only one "source".
func TestRecordedAudioWithoutCapturers(t *testing.T) {
	c := New(Config{})
	if n := len(c.AudioSamples()); n != 0 {
		t.Errorf("AudioSamples: %d samples", n)
	}
	if c.MicSamples() != nil || c.MonitorSamples() != nil || c.IsDualSource() {
		t.Error("tracks reported without capturers")
	}
}

// --- Two-pass refinement (pass 2) tests ---
//
// refineWorker is pure Go coordination logic (no cgo/ONNX dependency),
// so it's tested directly here rather than through a full Start()/Stop()
// cycle, which would need a real VAD model to construct a pipeline at
// all (see processPipeline's `if vad == nil { return }` guard) — same
// reason the rest of this file's Start()-based tests never exercise
// real transcription.

func TestRefineWorker_EmitsUpdateOnSuccess(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "refined text", Language: "en"}}
	c := New(Config{Engine: eng})

	c.refineWG.Add(1)
	go c.refineWorker()

	c.refineCh <- refinementJob{
		id: "seg-1", samples: []float32{0.1, 0.2}, speaker: "Person 1",
		startTime: 1.0, endTime: 2.0, source: SourceMonitor, pass1Text: "rough text",
	}
	close(c.refineCh)

	update := <-c.segmentUpdateCh
	if update.ID != "seg-1" {
		t.Errorf("ID = %q, want %q", update.ID, "seg-1")
	}
	if update.Text != "refined text" {
		t.Errorf("Text = %q, want %q (pass-2 result)", update.Text, "refined text")
	}
	if update.Status != "" {
		t.Errorf("Status = %q, want %q (final)", update.Status, "")
	}
	if update.Speaker != "Person 1" || update.Source != string(SourceMonitor) {
		t.Errorf("Speaker/Source = %q/%q, want %q/%q", update.Speaker, update.Source, "Person 1", SourceMonitor)
	}
}

func TestRefineWorker_FallsBackToPass1TextOnEngineError(t *testing.T) {
	eng := &mockEngine{err: fmt.Errorf("decode failed")}
	c := New(Config{Engine: eng})

	c.refineWG.Add(1)
	go c.refineWorker()

	c.refineCh <- refinementJob{
		id: "seg-2", samples: []float32{0.1}, speaker: "You",
		startTime: 0, endTime: 1, source: SourceMic, pass1Text: "rough text",
	}
	close(c.refineCh)

	update := <-c.segmentUpdateCh
	if update.Text != "rough text" {
		t.Errorf("Text = %q, want %q (pass-1 fallback)", update.Text, "rough text")
	}
	if update.Status != "" {
		t.Errorf("Status = %q, want %q (finalized even on refinement failure)", update.Status, "")
	}
}

func TestRefineWorker_FallsBackToPass1TextOnEmptyResult(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "   "}}
	c := New(Config{Engine: eng})

	c.refineWG.Add(1)
	go c.refineWorker()

	c.refineCh <- refinementJob{
		id: "seg-3", samples: []float32{0.1}, speaker: "You",
		pass1Text: "rough text",
	}
	close(c.refineCh)

	update := <-c.segmentUpdateCh
	if update.Text != "rough text" {
		t.Errorf("Text = %q, want %q (pass-1 fallback on blank pass-2 result)", update.Text, "rough text")
	}
}

func TestSegmentUpdatesChannelAccessor(t *testing.T) {
	c := New(Config{})
	if c.SegmentUpdates() == nil {
		t.Error("SegmentUpdates() = nil, want a channel")
	}
}

// --- Live partial (pass 1, mid-utterance) tests ---

func TestEmitLivePartial_BuffersUntilMinAudioReached(t *testing.T) {
	c := New(Config{})
	var live liveState

	// Not enough accumulated audio yet -- must not emit anything.
	live.audio = make([]float32, minLiveAudioSamples-1)
	c.emitLivePartial(SourceMic, &live, "hello")

	select {
	case seg := <-c.segmentCh:
		t.Fatalf("emitLivePartial() emitted %+v before minLiveAudioSamples was reached", seg)
	default:
	}
	if live.id != "" {
		t.Error("live.id set before minLiveAudioSamples was reached")
	}
}

func TestEmitLivePartial_FirstEmissionCreatesLiveSegment(t *testing.T) {
	c := New(Config{})
	var live liveState
	live.audio = make([]float32, minLiveAudioSamples)

	c.emitLivePartial(SourceMic, &live, "hello")

	seg := <-c.segmentCh
	if seg.Status != "live" {
		t.Errorf("Status = %q, want %q", seg.Status, "live")
	}
	if seg.Text != "hello" {
		t.Errorf("Text = %q, want %q", seg.Text, "hello")
	}
	if seg.Speaker != "You" {
		t.Errorf("Speaker = %q, want %q (mic source)", seg.Speaker, "You")
	}
	if live.id == "" {
		t.Error("live.id not set after first emission")
	}
}

func TestEmitLivePartial_SubsequentCallsUpdateInPlace(t *testing.T) {
	c := New(Config{})
	var live liveState
	live.audio = make([]float32, minLiveAudioSamples)

	c.emitLivePartial(SourceMic, &live, "hello")
	first := <-c.segmentCh

	c.emitLivePartial(SourceMic, &live, "hello world")
	update := <-c.segmentUpdateCh

	if update.ID != first.ID {
		t.Errorf("update ID = %q, want %q (same segment, same utterance)", update.ID, first.ID)
	}
	if update.Text != "hello world" {
		t.Errorf("update Text = %q, want %q", update.Text, "hello world")
	}
	if update.Status != "live" {
		t.Errorf("update Status = %q, want %q", update.Status, "live")
	}
	if update.StartTime != first.StartTime {
		t.Errorf("update StartTime = %v, want unchanged %v", update.StartTime, first.StartTime)
	}
}

func TestLiveStateReset(t *testing.T) {
	live := liveState{partial: "x", id: "seg-1", speaker: "You", startTime: 1, audio: []float32{1, 2}}
	live.reset()
	if live.partial != "" || live.id != "" || live.speaker != "" || live.startTime != 0 || live.audio != nil {
		t.Errorf("reset() left non-zero state: %+v", live)
	}
}

func TestSignalHintNeeded_DeliversPriorityFlag(t *testing.T) {
	c := New(Config{})

	c.signalHintNeeded(true)
	select {
	case priority := <-c.HintNeeded():
		if !priority {
			t.Error("priority = false, want true")
		}
	default:
		t.Fatal("HintNeeded() had nothing buffered after signalHintNeeded(true)")
	}

	c.signalHintNeeded(false)
	select {
	case priority := <-c.HintNeeded():
		if priority {
			t.Error("priority = true, want false")
		}
	default:
		t.Fatal("HintNeeded() had nothing buffered after signalHintNeeded(false)")
	}
}

func TestSignalHintNeeded_NonBlockingWhenBufferFull(t *testing.T) {
	c := New(Config{})

	c.signalHintNeeded(false)
	// The single-slot buffer is now full; a second signal must be
	// dropped rather than blocking the caller.
	done := make(chan struct{})
	go func() {
		c.signalHintNeeded(true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("signalHintNeeded() blocked with a full buffer, want a non-blocking drop")
	}
}
