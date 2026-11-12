package recording

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/live"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/meetingaudio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/sigfix"
)

// StartSession starts recording a meeting: micDevice is the microphone
// ("" for none), monitorDevice the system-audio selection (see
// meetingaudio; "" is the automatic source on macOS), lang the language
// ("" for the engines' default) and platform the meeting app the title
// names, if known.
func (a *Recorder) StartSession(micDevice, monitorDevice, lang, platform string) error {
	// A sherpa-onnx constructor run since the last repair may have put
	// its crash handler back without SA_ONSTACK.
	sigfix.AfterSherpa()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return errClosing
	}
	if a.rec != nil {
		return errRecording
	}
	// Each recording starts from Tomoe's voices, so names and the owner
	// voice match what Tomoe would use for the same meeting, until the
	// recorder is of record (Musubi's [recorder] of_record): from then on
	// its own voices, edited through the Musubi app, are the ones used.
	if !a.cfg.MusubiOfRecord {
		if err := a.copyVoices(); err != nil {
			fmt.Printf("Warning: voices not copied from Tomoe: %v\n", err)
		} else {
			forgetOwnerVoices()
		}
	}
	if err := a.begin(micDevice, monitorDevice, lang, platform); err != nil {
		return err
	}
	// A source chosen for "the next recording" (set_source) is used once.
	a.src.nextMic, a.src.nextSystem = nil, nil
	a.publishStatusLocked()
	return nil
}

// beginLocked is StartSession's real work: lease the engines, open the
// sources, start the live pipeline and everything that watches it, and
// make the new session current. On any failure nothing is left running.
// Caller holds a.mu.
func (a *Recorder) beginLocked(micDevice, monitorDevice, lang, platform string) error {
	bundle, release, err := a.leaseEnginesLocked("recording")
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			// a.mu is still held here (StartSession's own deferred
			// Unlock runs after this), and release takes it: give the
			// lease back directly instead.
			a.releaseLeaseLocked("recording")
		}
	}()

	if lang == "" {
		lang = bundle.engines.DefaultLang()
	}
	status := bundle.modelMgr.Check()

	cfg := live.Config{
		Engine:             bundle.engines.Get(lang),
		Embedder:           meetingEmbedder(a.cfg, status, bundle.embedders, lang),
		MinSilenceDuration: a.cfg.Meeting.MinSilenceDuration,
		MaxSpeechDuration:  a.cfg.Meeting.MaxSpeechDuration,
		MinSpeechLevelDB:   a.cfg.Meeting.MinSpeechLevelDB,
		MicLevelMarginDB:   a.cfg.Meeting.MicLevelMarginDB,
		Tracker:            a.tracker,
		VADPath:            status.VADPath,
		SegmentBufferSize:  64,
	}
	// The user's voice on their own mic (config mic_voices): only once
	// their voice is known; until then the mic is "You", as before.
	if a.cfg.Meeting.MicVoices && micDevice != "" && bundle.micEmbedders != nil {
		m, _, _ := status.SpeakerModelFor(a.cfg.Meeting.SpeakerModel, lang)
		owner := ownerFor(m.ID)
		if c := owner.centroid(); c != nil {
			if e := meetingEmbedder(a.cfg, status, bundle.micEmbedders, lang); e != nil {
				cfg.MicEmbedder, cfg.OwnerVoice, cfg.MicOtherBelow, cfg.HeardOwner = e, c, a.cfg.Meeting.MicOtherBelow, owner.heard
			}
		}
	}

	// Realtime pass is English-only (see internal/transcribe's
	// StreamingEngine); other languages keep today's single-pass path.
	if lang == "en" {
		cfg.StreamingEngine = bundle.streamingEngine
	}

	// The mic, if one was chosen; closed again below if anything later
	// fails, since nothing else would own it.
	closeSources := func() {
		for _, src := range []*audio.StreamCapturer{cfg.MicCapturer, cfg.MonitorCapturer} {
			if src != nil {
				src.Close()
			}
		}
	}
	// Both sources capture through a switcher, so the Musubi app can move
	// either to another device or app mid-recording (set_source).
	var micSwitch *audio.SwitchCapturer
	if micDevice != "" {
		mic, err := audio.NewCapturer(micDevice, audio.Input)
		if err != nil {
			return fmt.Errorf("opening the microphone %q: %w", micDevice, err)
		}
		micSwitch = audio.NewSwitchCapturer(mic)
		cfg.MicCapturer = audio.NewStreamCapturer(micSwitch, audio.DefaultWindowSize, audio.WindowBuffer)
	}

	// Set up monitor capturer (optional) — the second audio source
	// (a specific app or "everything" on macOS — see
	// internal/meetingaudio). "" is the automatic source on macOS.
	monCapturer, sysSwitch, audioAuto, err := meetingaudio.NewSwitchableMonitorSource(monitorDevice)
	if err != nil {
		closeSources()
		return fmt.Errorf("creating monitor capturer: %w", err)
	}
	if monCapturer != nil {
		cfg.MonitorCapturer = monCapturer
	}

	// Speaker numbering starts over with every meeting.
	if t := a.tracker; t != nil {
		t.Reset()
	}

	// Screen-based speaker-name hints (macOS only; a no-op on Linux): the
	// watcher looks at the meeting window, more often while a speaker
	// needs naming and right after a speaker change.
	watcher, hs := a.newHintWatcher()
	cfg.OnMonitorSpeechStart = watcher.Burst

	// Speaker-change signals end a turn (live.Config.TurnMode): new voices
	// from the diarizer, highlight moves from the meeting window.
	changes := &live.ChangeLog{}
	hs.changes = changes
	cfg.TurnMode, cfg.TurnMaxSeconds, cfg.TurnMaxGap = a.cfg.Meeting.TurnMode, a.cfg.Meeting.TurnMaxSeconds, a.cfg.Meeting.TurnMaxGap
	cfg.DecodePad, cfg.TurnInterjection = a.cfg.Meeting.DecodePad, a.cfg.Meeting.TurnInterjection
	cfg.DecodeAsync = a.cfg.Meeting.DecodeAsync
	cfg.SpeakerChanged = changes.Between
	if a.cfg.Meeting.SplitAtHighlight {
		cfg.HighlightChanges, cfg.HighlightLag = changes.NameChangesIn, live.DefaultHighlightLag
	}

	var md *diarize.SessionDiarizer
	if cfg.MonitorCapturer != nil && !cfg.SkipMonitorDiarization {
		if md = newMeetingDiarizer(a, a.cfg, status, lang, func(at float64) {
			watcher.Burst()
			changes.Add(at)
		}); md != nil {
			cfg.MonitorAudio = md.Feed
		}
	}

	coordinator := live.New(cfg)
	// The pipeline outlives this call; its context is the recording's,
	// ended by StopSession through coordinator.Stop.
	if err := coordinator.Start(context.Background()); err != nil {
		if md != nil {
			md.Abort()
		}
		closeSources()
		return fmt.Errorf("starting the live pipeline: %w", err)
	}

	now := time.Now()
	sess := &session.Session{ID: uuid.NewString(), Title: defaultTitle(platform, now), CreatedAt: now}
	sess.Platform, sess.Language = platform, lang
	sess.Sources = sourceNames(micDevice != "", cfg.MonitorCapturer != nil)
	a.currentSess = sess

	if md != nil {
		md.SetSessionLocked(a.currentSess) // a.mu is held, so no timeline is applied before this
	}
	a.meetingDiar = md
	started = true
	a.sessionRelease = release

	// Screen-based speaker-label hints (macOS only; no-op on Linux —
	// see internal/videohint). Its own context, since it must stop
	// when this session does.
	videoHintCtx, videoHintCancel := context.WithCancel(context.Background())
	a.videoHintCancel = videoHintCancel
	hs.sess, hs.coordinator, hs.diar = a.currentSess, coordinator, md
	a.startHintWatcher(videoHintCtx, hs)
	followCtx, followCancel := context.WithCancel(videoHintCtx)
	a.followMeetingAudio(followCtx, audioAuto)
	a.src.beginLocked(videoHintCtx, coordinator, micDevice, micSwitch, systemSelection(monitorDevice, cfg.MonitorCapturer != nil), sysSwitch, audioAuto, followCancel)
	if cfg.MonitorCapturer != nil { // never a typed nil in the interface
		a.src.meeting = cfg.MonitorCapturer
	}
	if micDevice != "" {
		go a.watchMic(videoHintCtx, coordinator, micDevice)
	}
	if cfg.MonitorCapturer != nil && a.cfg.Meeting.SourceCheck {
		go a.watchSystem(videoHintCtx, coordinator)
	}

	a.rec = &activeRecording{
		coordinator: coordinator,
		drained:     a.emitSessionSegments(coordinator.Segments(), coordinator.SegmentUpdates(), a.currentSess, md),
	}

	// Under a.mu (held throughout StartSession), like the live feed's
	// snapshot, so a client connecting now sees this session exactly once.
	a.feedSessionLocked(livefeed.StateStarted, a.currentSess)
	fmt.Printf("session %s: recording (%s, %v)\n", sess.ID, lang, sess.Sources)
	return nil
}

// sourceNames is what a session lists as heard, in the order the saved
// audio's tracks are written: the mic, then the meeting side.
func sourceNames(mic, monitor bool) []string {
	var names []string
	for _, s := range []struct {
		on   bool
		name string
	}{{mic, "mic"}, {monitor, "monitor"}} {
		if s.on {
			names = append(names, s.name)
		}
	}
	return names
}

// StopSession ends the recording in progress and hands it to the saver,
// returning once capture has stopped; the save itself (audio, JSON,
// diarization) runs after, in the background.
func (a *Recorder) StopSession() (*session.Session, error) {
	a.mu.Lock()
	rec := a.rec
	if rec == nil {
		a.mu.Unlock()
		return nil, errIdle
	}
	// Held from taking the recording until its save is queued, so
	// Shutdown can't close the queue in between.
	a.stopping.Add(1)
	defer a.stopping.Done()

	sess := a.currentSess
	videoHintCancel := a.videoHintCancel
	diar := a.meetingDiar
	a.meetingDiar = nil
	release := a.sessionRelease
	a.sessionRelease = nil
	a.rec, a.currentSess = nil, nil
	a.videoHintCancel = nil
	a.saving[sess.ID] = true
	silentBefore := a.src.silent != nil
	a.src.endLocked()
	a.mu.Unlock()

	if videoHintCancel != nil {
		videoHintCancel()
	}

	fmt.Printf("session %s: stopping capture\n", sess.ID)
	if rec.coordinator != nil {
		// Flushes the pipeline: the last utterance is cut and queued.
		rec.coordinator.Stop()
	}
	fmt.Printf("session %s: capture stopped, queueing save\n", sess.ID)

	ended := time.Now()
	sess.EndedAt, sess.Duration = ended, ended.Sub(sess.CreatedAt).Seconds()

	a.videoHintMu.Lock()
	a.audioSource = ""
	a.videoHintMu.Unlock()
	a.mu.Lock()
	a.feedSessionLocked(livefeed.StateStopped, sess)
	if silentBefore {
		a.publishAudioLocked()
	}
	a.publishStatusLocked()
	a.mu.Unlock()

	a.saves <- &saveRequest{stopped: sess, capture: rec.coordinator, diar: diar, segmentsDone: rec.drained, release: release}
	return sess, nil
}

// saveLoop persists stopped recordings one at a time, in the order they
// stopped, until saves is closed.
func (a *Recorder) saveLoop() {
	defer close(a.saverDone)
	for req := range a.saves {
		a.persist(req)
		a.mu.Lock()
		delete(a.saving, req.stopped.ID)
		a.mu.Unlock()
	}
}

// persistSession saves a stopped recording: its audio and session.json
// first, so the meeting is kept whatever happens next, then its final
// speaker labels, from the diarizer that ran during the meeting or else
// from the post-meeting pass in a child process.
func (a *Recorder) persistSession(req *saveRequest) {
	sess := req.stopped
	// The engine lease covers pass-2 refinement only: once the final
	// segments are in, the rest of the save (audio encoding, JSON,
	// diarization in its own process) doesn't touch the engines.
	// release is idempotent, so the deferred call is just a backstop.
	if req.release != nil {
		defer req.release()
	}

	// Refinements queued before the stop keep arriving for a while; the
	// saved text must be the refined one, with nothing left pending.
	if done := req.segmentsDone; done != nil {
		fmt.Printf("session %s: waiting for final segments\n", sess.ID)
		<-done
	}
	if req.release != nil {
		req.release()
	}
	fmt.Printf("session %s: saving\n", sess.ID)

	if tracks := audioTracks(req.capture); len(tracks) > 0 {
		path := filepath.Join(config.SessionDir(), sess.ID, "audio.m4a")
		if err := session.SaveAudioM4A(tracks, audio.CaptureSampleRate, path); err != nil {
			fmt.Printf("session %s: audio not saved: %v\n", sess.ID, err)
		} else {
			sess.AudioPath = path
		}
	}

	a.titleFromWindow(sess)

	// Written before any diarization, which can crash.
	if err := a.store.Save(sess); err != nil {
		fmt.Printf("session %s: not saved: %v\n", sess.ID, err)
	}

	// Diarized during the meeting: its final labels replace the
	// post-meeting pass (which still runs if that failed).
	if req.diar != nil && finishMeetingDiarizer(req.diar, sess) {
		if err := a.store.Save(sess); err != nil {
			fmt.Printf("Error saving session: %v\n", err)
		}
		a.feedSession(livefeed.StateSaved, sess)
		a.refineCutsLater(sess.ID)
		return
	}

	a.mu.Lock()
	modelMgr := a.bundle.modelMgr
	a.mu.Unlock()
	if modelMgr != nil && modelMgr.Check().DiarizationReady() {
		// The worker is this program run as `diarize` against the same
		// data folder; it rewrites session.json with the new labels, so a
		// sherpa-onnx crash costs only that child.
		if err := session.RunDiarizeWithRetry(sess.ID, session.DefaultDiarizeAttempts); err != nil {
			fmt.Printf("session %s: kept without post-meeting diarization: %v\n", sess.ID, err)
		}
	}
	a.feedSession(livefeed.StateSaved, sess)
}

// audioTracks is what the recording heard, as SaveAudioM4A takes it: the
// mic and the meeting side as two tracks when both captured something,
// else the one mixed track (nil if there was no audio at all).
func audioTracks(c *live.Coordinator) [][]float32 {
	if c == nil {
		return nil
	}
	if !c.IsDualSource() {
		if all := c.AudioSamples(); len(all) > 0 {
			return [][]float32{all}
		}
		return nil
	}
	mic, mon := c.MicSamples(), c.MonitorSamples()
	if len(mic) == 0 || len(mon) == 0 {
		return nil
	}
	return [][]float32{mic, mon}
}
