package live

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/transcribe"
)

// mockStreamingSession is a pass-1 decoder that never hears anything;
// the tests set pass 1's text on liveState directly.
type mockStreamingSession struct{ fed, resets int }

func (m *mockStreamingSession) Feed([]float32) (string, error) {
	m.fed++
	return "", nil
}
func (m *mockStreamingSession) Reset() { m.resets++ }
func (m *mockStreamingSession) Close() {}

// runRefineWorker runs pass 2 over the jobs queued so far and returns
// when they're all done.
func runRefineWorker(c *Coordinator) {
	close(c.refineCh)
	c.refineWG.Add(1)
	c.refineWorker()
}

// receiveNone fails if ch holds a segment.
func receiveNone(t *testing.T, ch <-chan session.Segment, what string) {
	t.Helper()
	if len(ch) > 0 {
		t.Errorf("unexpected %s: %+v", what, <-ch)
	}
}

// backedUp is a Coordinator whose pass-2 queue (two slots) is full.
func backedUp(eng transcribe.Engine) *Coordinator {
	c := New(Config{Engine: eng, SegmentBufferSize: 2})
	for len(c.refineCh) < cap(c.refineCh) {
		c.refineCh <- refinementJob{}
	}
	return c
}

// An utterance pass 1 had no words for appears only once pass 2 hears
// speech in it, as a new final line; if pass 2 hears none, nothing shows.
func TestUnannouncedUtterance(t *testing.T) {
	for _, tc := range []struct {
		source  SourceType
		decoded string
		want    string // "" = no line
	}{
		{SourceMic, " ok then ", "ok then"},
		{SourceMonitor, "   ", ""},
	} {
		c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: tc.decoded, Language: "en"}}})
		sess := &mockStreamingSession{}
		c.handleSegment(tc.source, make([]float32, 12000), sess, &liveState{})
		if sess.resets != 1 {
			t.Errorf("%s: pass 1 reset %d times, want once", tc.source, sess.resets)
		}
		receiveNone(t, c.segmentCh, "line before pass 2")

		runRefineWorker(c)
		receiveNone(t, c.segmentUpdateCh, "update to a line never shown")
		lines := len(c.segmentCh)
		if tc.want == "" {
			if lines != 0 {
				t.Errorf("%s: %d lines for audio with no speech", tc.source, lines)
			}
			continue
		}
		seg := <-c.segmentCh
		if seg.Text != tc.want || seg.Status != "" || seg.Speaker != "You" {
			t.Errorf("%s: %+v, want a final %q from You", tc.source, seg, tc.want)
		}
		if st := c.Stats(); st.MicSegments != 1 {
			t.Errorf("%s: stats %+v, want the line counted", tc.source, st)
		}
	}
}

// With pass 2's queue full, an unannounced utterance is decoded on the
// spot rather than lost.
func TestUnannouncedDecodedWhenQueueFull(t *testing.T) {
	c := backedUp(&mockEngine{result: &transcribe.Result{Text: "sure"}})
	c.handleSegment(SourceMonitor, make([]float32, 12000), &mockStreamingSession{}, &liveState{})
	if len(c.segmentCh) != 1 {
		t.Fatalf("%d lines, want the utterance decoded at once", len(c.segmentCh))
	}
	if seg := <-c.segmentCh; seg.Text != "sure" || seg.Status != "" {
		t.Errorf("line %+v", seg)
	}
	if st := c.Stats(); st.MonitorSegments != 1 {
		t.Errorf("stats %+v", st)
	}
}

// A live line turns pending when its utterance ends; with pass 2's queue
// full, pass 1's words are then made final straight away.
func TestLiveLineFinalWhenQueueFull(t *testing.T) {
	c := backedUp(&mockEngine{})
	live := liveState{partial: " see you then ", shown: "see you", id: "seg-5", speaker: "You", startTime: 2}
	c.handleSegment(SourceMic, make([]float32, 12000), &mockStreamingSession{}, &live)

	if pending := <-c.segmentUpdateCh; pending.ID != "seg-5" || pending.Status != "pending" || pending.Text != "see you then" {
		t.Errorf("first update %+v, want seg-5 pending with pass 1's last words", pending)
	}
	if final := <-c.segmentUpdateCh; final.ID != "seg-5" || final.Status != "" || final.Text != "see you then" {
		t.Errorf("second update %+v, want seg-5 final", final)
	}
	if live.id != "" || live.partial != "" {
		t.Errorf("live state kept: %+v", live)
	}
}

// If pass 1's hypothesis goes blank after a live line went up, the line
// keeps the words it showed, and pass 2 is queued for it.
func TestLiveLineKeepsShownWords(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}})
	live := liveState{shown: "one moment", id: "seg-8", speaker: "You", startTime: 1}
	c.handleSegment(SourceMic, make([]float32, 12000), &mockStreamingSession{}, &live)

	if u := <-c.segmentUpdateCh; u.ID != "seg-8" || u.Status != "pending" || u.Text != "one moment" {
		t.Errorf("update %+v", u)
	}
	if len(c.refineCh) != 1 {
		t.Fatalf("%d pass-2 jobs, want 1", len(c.refineCh))
	}
	if job := <-c.refineCh; job.id != "seg-8" || job.unannounced || job.pass1Text != "one moment" {
		t.Errorf("job %+v", job)
	}
}

// A live line still open when its source stops is refined like any
// other, with a speaker from all of its audio rather than the early guess.
func TestFinishLive(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "and that's all"}}})
	c.finishLive(SourceMic, &liveState{})
	if len(c.refineCh) != 0 {
		t.Fatal("pass 2 queued with no live line open")
	}

	live := liveState{partial: "and that", shown: "and", id: "seg-2", speaker: "Person 3", startTime: 4, audio: make([]float32, 12000)}
	c.finishLive(SourceMonitor, &live)
	if live.id != "" {
		t.Error("live state kept")
	}
	runRefineWorker(c)
	// No embedder: the whole-utterance label for meeting audio is "Other".
	if u := <-c.segmentUpdateCh; u.ID != "seg-2" || u.Text != "and that's all" || u.Status != "" || u.Speaker != "Other" {
		t.Errorf("update %+v", u)
	}
}

// Single-pass sends a final line per utterance with words in it, and
// nothing for one that decodes to nothing.
func TestSinglePass(t *testing.T) {
	for decoded, want := range map[string]int{"  ": 0, "right": 1} {
		c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: decoded}}, SkipMonitorDiarization: true})
		c.handleSegment(SourceMonitor, make([]float32, 12000), nil, &liveState{})
		if n := len(c.segmentCh); n != want {
			t.Errorf("%q: %d lines, want %d", decoded, n, want)
			continue
		}
		if want == 1 {
			if seg := <-c.segmentCh; seg.Text != "right" || seg.Status != "" || seg.Speaker != "System Audio" {
				t.Errorf("line %+v", seg)
			}
		}
		if st := c.Stats(); st.MonitorSegments != want {
			t.Errorf("%q: stats %+v", decoded, st)
		}
	}
}

// tone is n samples of a square wave at amplitude a (RMS a).
func tone(n int, a float32) []float32 {
	s := make([]float32, n)
	for i := range s {
		if i%2 == 0 {
			s[i] = a
		} else {
			s[i] = -a
		}
	}
	return s
}

func TestHandleSegment_NoiseGateDropsQuietUtterance(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "Yeah."}}, MinSpeechLevelDB: -50})
	live := liveState{shown: "yeah", id: "seg-7", speaker: "You"}
	// -60 dBFS: room noise.
	if !c.dropNoise(SourceMic, tone(8000, 0.001), &mockStreamingSession{}, &live) {
		t.Fatal("noise not dropped")
	}

	update := <-c.segmentUpdateCh
	if update.ID != "seg-7" || update.Status != session.StatusRemoved {
		t.Errorf("update = %+v, want seg-7 removed", update)
	}
	receiveNone(t, c.segmentCh, "segment for noise")
	if len(c.refineCh) != 0 {
		t.Errorf("noise was queued for transcription")
	}
	if live.id != "" {
		t.Errorf("live state not reset")
	}
}

func TestHandleSegment_NoiseGateKeepsSpeech(t *testing.T) {
	c := New(Config{Engine: &mockEngine{result: &transcribe.Result{Text: "hello"}}, MinSpeechLevelDB: -50})
	var live liveState
	// -20 dBFS: speech.
	if c.dropNoise(SourceMic, tone(8000, 0.1), nil, &live) {
		t.Fatal("speech dropped")
	}
	c.handleSegment(SourceMic, tone(8000, 0.1), nil, &live)
	if seg := <-c.segmentCh; seg.Text != "hello" {
		t.Errorf("speech dropped or changed: %+v", seg)
	}
}

func TestTooQuietMicRelativeToUser(t *testing.T) {
	c := New(Config{Engine: &mockEngine{}, MinSpeechLevelDB: -50, MicLevelMarginDB: 20})
	for i := 0; i < 10; i++ {
		if c.tooQuiet(SourceMic, -18) {
			t.Fatal("the user's own level dropped")
		}
	}
	if !c.tooQuiet(SourceMic, -45) {
		t.Error("faint mic speech 27 dB below the user kept")
	}
	if c.tooQuiet(SourceMic, -30) {
		t.Error("mic speech 12 dB below the user dropped")
	}
	if c.tooQuiet(SourceMonitor, -45) {
		t.Error("the margin applied to remote audio")
	}
	if !c.tooQuiet(SourceMonitor, -55) {
		t.Error("remote audio below the floor kept")
	}
}

func TestTurnMode(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "words"}}
	changeAt := 30.0
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30,
		SpeakerChanged: func(from, to float64) bool { return from <= changeAt && changeAt <= to }})
	utter := func(src SourceType, s, e float64) { c.addToTurn(src, make([]float32, int((e-s)*16000)), s, e) }
	drain := func() (n int, spans [][2]float64) {
		for {
			select {
			case seg := <-c.segmentCh:
				n++
				spans = append(spans, [2]float64{seg.StartTime, seg.EndTime})
			default:
				return
			}
		}
	}
	utter(SourceMonitor, 0, 3)
	utter(SourceMonitor, 3.6, 6) // short pause: same turn
	if n, _ := drain(); n != 0 {
		t.Fatalf("a turn was sent before it ended (%d)", n)
	}
	utter(SourceMic, 7, 8) // the other side speaks: the remote turn ends
	if n, spans := drain(); n != 1 || spans[0] != [2]float64{0, 6} {
		t.Fatalf("after the mic spoke: %d lines %v, want one 0-6", n, spans)
	}
	utter(SourceMonitor, 9, 12) // mic turn ends
	if n, _ := drain(); n != 1 {
		t.Fatalf("mic turn not sent when the call spoke")
	}
	utter(SourceMonitor, 30.2, 33) // a new voice signaled at 30: new turn
	if n, spans := drain(); n != 1 || spans[0] != [2]float64{9, 12} {
		t.Fatalf("speaker change: %d lines %v, want 9-12 sent", n, spans)
	}
	utter(SourceMonitor, 36, 38) // pause over 2 s: new turn
	if n, _ := drain(); n != 1 {
		t.Fatalf("long pause did not end the turn")
	}
	c.flushTurn(SourceMonitor)
	if n, _ := drain(); n != 1 {
		t.Fatalf("open turn not sent at the end")
	}
}

func TestTurnModeTwoPass(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "the whole turn"}}
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30})
	sess := &mockStreamingSession{}
	// Two utterances, each with a pass-1 line showing.
	first := liveState{partial: "the whole", id: "seg-1", speaker: "Person 1"}
	c.handleSegment(SourceMonitor, make([]float32, 16000), sess, &first)
	second := liveState{partial: "turn", id: "seg-2", speaker: "Person 1"}
	c.handleSegment(SourceMonitor, make([]float32, 16000), sess, &second)
	for i := 0; i < 2; i++ {
		if u := <-c.segmentUpdateCh; u.Status != "pending" {
			t.Fatalf("pass-1 line %d not shown as pending: %+v", i, u)
		}
	}
	if len(c.refineCh) != 0 {
		t.Fatal("an utterance was refined before its turn ended")
	}
	c.flushTurn(SourceMonitor)
	runRefineWorker(c)
	final := <-c.segmentUpdateCh
	if final.ID != "seg-1" || final.Text != "the whole turn" || final.Status != "" {
		t.Errorf("turn line = %+v, want seg-1 final with the turn's text", final)
	}
	if gone := <-c.segmentUpdateCh; gone.ID != "seg-2" || gone.Status != session.StatusRemoved {
		t.Errorf("absorbed line = %+v, want seg-2 removed", gone)
	}
}

func TestTurnInterjection(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "words"}}
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30, TurnInterjection: 1})
	utter := func(src SourceType, s, e float64) { c.addToTurn(src, make([]float32, int((e-s)*16000)), s, e) }
	drain := func() (spans [][2]float64) {
		for {
			select {
			case seg := <-c.segmentCh:
				spans = append(spans, [2]float64{seg.StartTime, seg.EndTime})
			default:
				return
			}
		}
	}
	utter(SourceMic, 0, 4)
	utter(SourceMonitor, 4.2, 4.8) // "mm": the mic's turn goes on
	utter(SourceMic, 5, 9)         // the mic goes on: only the interjection is sent
	if spans := drain(); len(spans) != 1 || spans[0] != [2]float64{4.2, 4.8} {
		t.Fatalf("after the mic went on: %v, want just the interjection", spans)
	}
	utter(SourceMonitor, 10, 14) // a real reply ends the mic's turn
	if spans := drain(); len(spans) != 1 || spans[0] != [2]float64{0, 9} {
		t.Fatalf("after a real reply: %v, want the mic's 0-9 turn", spans)
	}
}

func TestSplitAtHighlight(t *testing.T) {
	eng := &mockEngine{result: &transcribe.Result{Text: "words"}}
	log := &ChangeLog{}
	log.NameSeen(0.5, "Ana")
	log.NameSeen(3.5, "Bix") // Bix cut in at about 3.0 s
	log.NameSeen(3.7, "Bix")
	c := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30,
		SpeakerChanged: log.Between, HighlightChanges: log.NameChangesIn, HighlightLag: 0.5})
	c.addToTurn(SourceMonitor, make([]float32, 6*16000), 0, 6)
	c.flushTurn(SourceMonitor)
	var spans [][2]float64
	for len(c.segmentCh) > 0 {
		seg := <-c.segmentCh
		spans = append(spans, [2]float64{seg.StartTime, seg.EndTime})
	}
	if len(spans) != 2 || spans[0] != [2]float64{0, 3} || spans[1] != [2]float64{3, 6} {
		t.Fatalf("lines %v, want 0-3 and 3-6", spans)
	}

	// A flicker too close to either end doesn't cut.
	log2 := &ChangeLog{}
	log2.NameSeen(0, "Ana")
	log2.NameSeen(6.2, "Bix") // would cut at 5.7: under minHighlightPiece from the end
	c2 := New(Config{Engine: eng, TurnMode: true, TurnMaxGap: 2, TurnMaxSeconds: 30,
		SpeakerChanged: log2.Between, HighlightChanges: log2.NameChangesIn, HighlightLag: 0.5})
	c2.addToTurn(SourceMonitor, make([]float32, 6*16000), 0, 6)
	c2.flushTurn(SourceMonitor)
	if n := len(c2.segmentCh); n != 1 {
		t.Errorf("cut %d lines for a change 0.3 s from the end, want 1", n)
	}
}

// slowEngine decodes after a delay, numbering its results.
type slowEngine struct {
	mockEngine
	delay time.Duration
	n     atomic.Int32
}

func (s *slowEngine) TranscribeDirect(samples []float32) (*transcribe.Result, error) {
	time.Sleep(s.delay)
	return &transcribe.Result{Text: fmt.Sprintf("line %d", s.n.Add(1)), Language: "en"}, nil
}

func (s *slowEngine) TranscribeSamples(samples []float32) (*transcribe.Result, error) {
	return s.TranscribeDirect(samples)
}

// With DecodeAsync a slow decode doesn't hold the pipeline (it goes back
// to reading audio at once), and every line arrives, in order.
func TestSinglePassAsyncDoesNotStallOrDrop(t *testing.T) {
	eng := &slowEngine{delay: 100 * time.Millisecond}
	c := New(Config{Engine: eng, SkipMonitorDiarization: true, DecodeAsync: true})
	c.decodeWG.Add(1)
	go c.decodeWorker()
	began := time.Now()
	for i := 0; i < 5; i++ {
		c.handleSegment(SourceMonitor, make([]float32, 8000), nil, &liveState{})
	}
	if d := time.Since(began); d > 50*time.Millisecond {
		t.Errorf("the pipeline waited %s for decodes", d)
	}
	for i := 1; i <= 5; i++ {
		select {
		case seg := <-c.segmentCh:
			if want := fmt.Sprintf("line %d", i); seg.Text != want {
				t.Errorf("got %q, want %q", seg.Text, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("line %d never arrived", i)
		}
	}
	close(c.decodeCh)
	c.decodeWG.Wait()
}
