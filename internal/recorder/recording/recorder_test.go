package recording

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/meeting"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// fakeRecorder is a Recorder whose recordings capture nothing: begin
// makes the session current and persist notes what would be saved, so
// the start/stop logic runs without devices or models.
type fakeRecorder struct {
	*Recorder
	mu      sync.Mutex
	begun   []string // platform of each recording begun
	saved   []*session.Session
	copies  int
	failing bool // begin fails, as with no microphone
}

// newTestRecorder is a recorder with nothing loaded, writing under a
// temporary data folder.
func newTestRecorder(t *testing.T) *Recorder {
	t.Helper()
	config.SetDataDir(t.TempDir())
	t.Cleanup(func() { config.SetDataDir("") })
	cfg := config.DefaultConfig()
	// Never this Mac's models, and never the network.
	cfg.Transcription.ModelPath = filepath.Join(t.TempDir(), "tomoe-models")
	r := New(cfg)
	r.copyVoices = func() error { return nil }
	r.probes.defaultMic = func() string { return "" } // never this Mac's devices
	r.modelFetch = func(string) (io.ReadCloser, int64, error) { return nil, 0, errors.New("no network in tests") }
	return r
}

func newFakeRecorder(t *testing.T) *fakeRecorder {
	f := &fakeRecorder{Recorder: newTestRecorder(t)}
	n := 0
	f.begin = func(_, _, lang, platform string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failing {
			return errors.New("no such device")
		}
		n++
		f.begun = append(f.begun, platform)
		f.currentSess = &session.Session{ID: fmt.Sprintf("rec-%d", n), Platform: platform, Language: lang, CreatedAt: time.Now()}
		f.rec = &activeRecording{}
		return nil
	}
	f.persist = func(req *saveRequest) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.saved = append(f.saved, req.stopped)
	}
	f.copyVoices = func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.copies++
		return nil
	}
	return f
}

// play runs the recorder on events, then stops it the way SIGTERM does
// and waits for the shutdown to finish.
func (f *fakeRecorder) play(events ...meeting.MeetingEvent) {
	ch := make(chan meeting.MeetingEvent)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.run(ctx, ch)
		close(done)
	}()
	for _, ev := range events {
		ch <- ev
	}
	// The loop finishes handling one event before it can see the next
	// or the cancellation, so the shutdown comes after the last event.
	cancel()
	<-done
}

func started(p meeting.Platform) meeting.MeetingEvent {
	return meeting.MeetingEvent{Type: meeting.MeetingStarted, Platform: p}
}

func stopped(p meeting.Platform) meeting.MeetingEvent {
	return meeting.MeetingEvent{Type: meeting.MeetingStopped, Platform: p}
}

func (f *fakeRecorder) savedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, s := range f.saved {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestCallIsRecordedAndSaved(t *testing.T) {
	f := newFakeRecorder(t)
	f.play(started(meeting.PlatformTeams), stopped(meeting.PlatformTeams))
	if fmt.Sprint(f.begun) != "[Teams]" {
		t.Errorf("recordings begun for %v, want [Teams]", f.begun)
	}
	if ids := f.savedIDs(); fmt.Sprint(ids) != "[rec-1]" {
		t.Fatalf("saved %v, want [rec-1]", ids)
	}
	s := f.saved[0]
	if s.EndedAt.IsZero() || s.Duration < 0 {
		t.Errorf("saved session not finalized: ended %v, duration %v", s.EndedAt, s.Duration)
	}
	if f.Recording() {
		t.Error("still recording after the call ended")
	}
}

func TestStopWithoutStartIsIgnored(t *testing.T) {
	f := newFakeRecorder(t)
	f.play(stopped(meeting.PlatformZoom), stopped(meeting.PlatformUnknown))
	if len(f.begun) != 0 || len(f.savedIDs()) != 0 {
		t.Errorf("begun %v, saved %v; want nothing", f.begun, f.savedIDs())
	}
	if _, err := f.StopSession(); !errors.Is(err, errIdle) {
		t.Errorf("StopSession while idle = %v, want errIdle", err)
	}
}

func TestStartWhileRecordingKeepsTheFirst(t *testing.T) {
	f := newFakeRecorder(t)
	f.play(started(meeting.PlatformTeams), started(meeting.PlatformZoom), stopped(meeting.PlatformTeams))
	if fmt.Sprint(f.begun) != "[Teams]" {
		t.Errorf("recordings begun for %v, want only the first call's", f.begun)
	}
	if ids := f.savedIDs(); fmt.Sprint(ids) != "[rec-1]" {
		t.Errorf("saved %v, want [rec-1]", ids)
	}
}

func TestBackToBackCallsAreSavedInOrder(t *testing.T) {
	f := newFakeRecorder(t)
	f.play(started(meeting.PlatformTeams), stopped(meeting.PlatformTeams), started(meeting.PlatformZoom), stopped(meeting.PlatformZoom))
	if ids := f.savedIDs(); fmt.Sprint(ids) != "[rec-1 rec-2]" {
		t.Errorf("saved %v, want [rec-1 rec-2]", ids)
	}
	if f.copies != 2 {
		t.Errorf("voices copied %d times, want once per recording (2)", f.copies)
	}
}

func TestShutdownMidRecordingSavesIt(t *testing.T) {
	f := newFakeRecorder(t)
	f.play(started(meeting.PlatformTeams)) // ends with a shutdown, no stop
	if ids := f.savedIDs(); fmt.Sprint(ids) != "[rec-1]" {
		t.Fatalf("saved %v, want the recording cut short by shutdown", ids)
	}
	if f.Recording() {
		t.Error("still recording after shutdown")
	}
	if err := f.StartSession("default", "", "", "Teams"); !errors.Is(err, errClosing) {
		t.Errorf("StartSession after shutdown = %v, want errClosing", err)
	}
	f.Shutdown() // a second shutdown is harmless
}

func TestFailedStartLeavesRecorderIdle(t *testing.T) {
	f := newFakeRecorder(t)
	f.failing = true
	f.play(started(meeting.PlatformTeams), stopped(meeting.PlatformTeams))
	if f.Recording() || len(f.savedIDs()) != 0 {
		t.Errorf("recording %v, saved %v; want neither after a failed start", f.Recording(), f.savedIDs())
	}
}

// A stop racing the shutdown must still get its save queued: Shutdown
// waits for it before closing the queue.
func TestStopRacingShutdownIsSaved(t *testing.T) {
	f := newFakeRecorder(t)
	if err := f.StartSession("default", "", "", "Teams"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = f.StopSession() }()
	go func() { defer wg.Done(); f.Shutdown() }()
	wg.Wait()
	if ids := f.savedIDs(); fmt.Sprint(ids) != "[rec-1]" {
		t.Errorf("saved %v, want [rec-1] exactly once", ids)
	}
}
