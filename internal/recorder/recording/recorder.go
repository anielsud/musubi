// Package recording is the recorder's long-running side: it watches for
// meetings, records each one through the live pipeline and saves it the
// way Tomoe's app does, with the live feed as its only output besides
// the session files. Tomoe's app is the source (internal/backend);
// docs/recorder-provenance.md lists which parts were lifted and which
// rewritten.
package recording

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/live"
	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/meeting"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/speaker"
	"github.com/anielsud/musubi/internal/recorder/transcribe"
	"github.com/anielsud/musubi/internal/recorder/videohint"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// meetingLanguages are the languages a meeting can be recorded in. The
// recorder has no multilingual mode, so it is English only.
var meetingLanguages = []string{"en"}

// Recorder records meetings. One recording runs at a time; saving a
// stopped one happens in the background, one save after another.
type Recorder struct {
	// cfg is replaced whole by reload_config, never changed in place, so
	// whoever took the pointer (under mu) keeps a consistent config.
	cfg *config.Config
	// bootCfg is the config the engines were built with: a reload that
	// differs from it in what is only read at start waits for a restart.
	bootCfg *config.Config
	restart []string // needsRestart(bootCfg, cfg) at the last reload; under mu
	store   *session.Store
	// musubiConfig is Musubi's config file, whose [recorder.*] tables
	// overlay Tomoe's (config.LoadRecorder).
	musubiConfig string
	// modelFetch downloads models (download_model); nil is HTTP. Tests
	// replace it.
	modelFetch models.Fetcher

	// bundle holds the transcription/streaming engines, speaker
	// embedder and model manager (see engineBundle). Guarded by mu.
	bundle   engineBundle
	tracker  *speaker.Tracker
	detector *meeting.Detector

	// engineLeases counts decoding jobs using the current bundle (see
	// leaseEnginesLocked), so it isn't closed while anything still
	// decodes with it. Guarded by mu.
	engineLeases int
	// sessionRelease ends the current recording's engine lease; it moves
	// to the session's saveRequest on stop, since pass-2 refinement keeps
	// decoding until the save has its last segment.
	sessionRelease func()

	mu sync.Mutex
	// rec is the recording in progress, nil when idle.
	rec         *activeRecording
	currentSess *session.Session
	// videoHintCancel ends the current recording's meeting-window
	// watcher, system-audio follower and mic watch.
	videoHintCancel context.CancelFunc
	// meetingDiar diarizes the current session while it records (nil if
	// that's off); see diarize.SessionDiarizer.
	meetingDiar *diarize.SessionDiarizer
	// liveFeed serves the transcript to local programs while
	// [meeting] live_feed is on; nil when off. See livefeed.go.
	liveFeed *livefeed.Server
	// closing is set once Shutdown starts; no recording starts after it.
	closing bool

	// configWatchStop stops the config.toml watcher that retunes speaker
	// clustering live. nil if none was started.
	configWatchStop func()

	// videoHintMu guards audioSource, what the automatic system-audio
	// source is capturing (see followMeetingAudio), and the current
	// recording's hint session and looks so far (for the looks op). Taken
	// after mu when both are needed, never before.
	videoHintMu sync.Mutex
	audioSource string
	hints       *hintSession
	videoLooks  []videohint.Look

	// saves carries stopped recordings to saveLoop, which persists them
	// in order: two diarizations at once would share sherpa-onnx state.
	// Buffered so a stop never waits for an earlier save.
	saves     chan *saveRequest
	saverDone chan struct{}
	// stopping counts StopSession calls between taking the recording and
	// queueing its save; Shutdown waits for them before closing saves.
	stopping sync.WaitGroup

	// begin starts capture for StartSession and persist saves a stopped
	// recording; tests swap them for fakes so the start/stop logic runs
	// without audio devices or models.
	begin   func(micDevice, monitorDevice, lang, platform string) error
	persist func(*saveRequest)
	// copyVoices brings Tomoe's voice files over before each recording.
	copyVoices func() error

	// What the Musubi app sees and changes through the control socket
	// (control.go). Guarded by mu.
	src sourceState
	// saving are sessions stopped but not yet saved: not to be deleted
	// or edited under the saver.
	saving map[string]bool
	// busy says what long job is running, for Status.Busy.
	busy string
	// notes are extra health lines: a fix that failed, settings that
	// need a restart.
	notes map[string]string
	// health caches the tools' problems (healthLines); see refreshHealth.
	health   []string
	healthAt time.Time
	// defaultMic caches the name of the system's default input, for
	// naming a "default" mic selection in Status (refreshDefaultMic).
	defaultMic   string
	defaultMicAt time.Time

	// The source check's probes (sourcecheck.go); tests swap them.
	probes sourceProbes

	// review is the Voices page's review lists (reviewindex.go).
	review reviewIndex

	// learnMu is held while every saved meeting is learnt (learn_saved,
	// the relearn for new rules: relearn.go), one at a time.
	learnMu sync.Mutex
	// relearnStores, when set (tests), lists the stores the relearn
	// checks instead of voiceStores.
	relearnStores func() (map[string]*voices.Store, error)

	// refused is why the data folder can't be used (a newer Musubi wrote
	// it: dataformat.ErrNewer), set before Start; nil normally.
	refused error
}

// activeRecording is what a recording in progress needs to be stopped.
type activeRecording struct {
	coordinator *live.Coordinator // nil only for a test's fake recording
	// drained closes when the coordinator's last line and revision are in
	// the session (emitSessionSegments).
	drained <-chan struct{}
}

// saveRequest is a stopped recording waiting for persistSession: the
// session and the pipeline that still holds its audio.
type saveRequest struct {
	stopped      *session.Session
	capture      *live.Coordinator
	diar         *diarize.SessionDiarizer // nil unless diarizing during the meeting
	segmentsDone <-chan struct{}          // see activeRecording.drained
	release      func()                   // ends the session's engine lease; see Recorder.sessionRelease
}

// pendingSaves is how many stopped recordings may wait for the saver.
const pendingSaves = 16

var (
	errClosing   = errors.New("the recorder is shutting down")
	errRecording = errors.New("a recording is already in progress")
	errIdle      = errors.New("nothing is recording")
)

// New is a recorder for cfg with nothing loaded: Start loads the models
// and begins watching for meetings. Sessions, voices and the live
// socket go under config.DataDir, which the caller has already pointed
// at the recorder's own folder.
func New(cfg *config.Config) *Recorder {
	r := &Recorder{
		cfg:          cfg,
		bootCfg:      cfg,
		store:        session.NewStore(config.SessionDir()),
		musubiConfig: config.MusubiPath(),
		saves:        make(chan *saveRequest, pendingSaves),
		saverDone:    make(chan struct{}),
		saving:       map[string]bool{},
		notes:        map[string]string{},
		probes:       defaultProbes(),
	}
	r.begin = r.beginLocked
	r.persist = r.persistSession
	r.copyVoices = func() error { return CopyVoices(config.TomoeDataDir(), config.DataDir()) }
	go r.saveLoop()
	return r
}

// Start loads what recording needs, in the order Tomoe's app does: the
// live feed first, so a consumer can connect while the models load, then
// the engines, the speaker tracker and the meeting detector.
func (a *Recorder) Start(ctx context.Context) error {
	cfg := a.currentConfig()
	if a.refused != nil {
		// Only the status: the app shows why nothing is recorded.
		fmt.Printf("data folder: %v\n", a.refused)
		a.setNote(noteFolder, "Not recording: "+a.refused.Error()+".")
		if err := a.startLiveFeed(cfg.Meeting.LiveFeed); err != nil {
			fmt.Printf("Warning: live feed not started: %v\n", err)
		}
		return nil
	}
	a.startReviewIndex()
	if err := a.startLiveFeed(cfg.Meeting.LiveFeed); err != nil {
		fmt.Printf("Warning: live feed not started: %v\n", err)
	}
	// Before the owner voice is first read (and perhaps seeded), so it
	// starts from Tomoe's; not once the recorder's own voices are of
	// record, nor with Tomoe off.
	if !cfg.CopiesTomoeVoices() {
		fmt.Println("voices: the recorder is of record or Tomoe is off; Tomoe's voices are not copied")
	} else if err := a.copyVoices(); err != nil {
		fmt.Printf("Warning: voices not copied from Tomoe: %v\n", err)
	}
	status := modelManager(cfg).Check()
	if !status.Ready() && cfg.Transcription.DownloadMissing && config.OwnModelDir() != "" {
		// A new install has none. Exiting would only be restarted every
		// minute with nothing to load, and the control socket, which the
		// app downloads through, would be gone each time.
		if err := a.downloadRequired(cfg); err != nil {
			fmt.Printf("models: %v\n", err)
		}
		status = modelManager(cfg).Check()
	}
	a.setupEngines(ctx, cfg, status)
	a.mu.Lock()
	ready := a.bundle.engines != nil
	a.mu.Unlock()
	if !ready {
		return fmt.Errorf("no transcription engine: are the models in %s?", strings.Join(config.ModelDirs(cfg), " or "))
	}
	if cfg.Meeting.VoiceProfiles {
		a.checkVoiceFiles()
	}
	// Only the recorder's own profiles: while Tomoe's are copied over
	// them they'd be learnt again at every start.
	if cfg.Meeting.VoiceProfiles && cfg.Meeting.VoiceProfileRelearn && !cfg.CopiesTomoeVoices() {
		go a.relearnForRules(ctx)
	}
	return nil
}

// noteFolder and noteVoices key the health lines for a data folder or
// voices file a newer Musubi wrote.
const (
	noteFolder = "data-folder"
	noteVoices = "voices-newer"
)

// Refuse keeps the recorder off its data folder (a newer Musubi wrote
// it): Start then only serves the status, with why as a health line, and
// every control op but the status, sources and tools is refused. Call
// before Start.
func (a *Recorder) Refuse(why error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refused = why
}

// checkVoiceFiles says in a health line when a voices file was written by
// a newer Musubi: recording carries on, but those profiles aren't learnt
// from or named by (voices.Open refuses them).
func (a *Recorder) checkVoiceFiles() {
	var newer []string
	for _, m := range models.SpeakerModels {
		if err := voices.CheckFile(voices.Path(config.DataDir(), m.ID)); err != nil {
			newer = append(newer, m.ID)
		}
	}
	line := ""
	if len(newer) > 0 {
		line = fmt.Sprintf("Voice profiles (%s) were written by a newer Musubi: meetings are recorded, but voices aren't learnt or named until that version is installed again.", strings.Join(newer, ", "))
	}
	a.setNote(noteVoices, line)
}

// SetMusubiConfig points the recorder at Musubi's config file for
// reload_config and the config watcher (config.MusubiPath by default).
func (a *Recorder) SetMusubiConfig(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.musubiConfig = path
}

// currentConfig is the config in force now (see Recorder.cfg).
func (a *Recorder) currentConfig() *config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// config is a copy of the config in force now.
func (a *Recorder) config() config.Config {
	return *a.currentConfig()
}

// setupEngines builds everything that depends on cfg/status: the
// transcription engine(s), the optional realtime streaming and speaker
// embedding/clustering pipelines and the meeting auto-detector.
func (a *Recorder) setupEngines(ctx context.Context, cfg *config.Config, status *models.Status) {
	bundle := buildEngines(cfg, status)

	// The tracker doesn't depend on any model, so it's created even
	// without the speaker embedding model (Assign is simply never called
	// then) and survives engine reloads with its tuning intact.
	tracker := speaker.NewTracker(speaker.DefaultThreshold)
	tracker.SetTuning(tuningFromConfig(cfg))

	// Watch config.toml so clustering tuning can be retuned live -- no
	// rebuild, no relaunch. See MeetingConfig's doc comment for why this
	// exists. tracker has its own internal lock (see speaker.Tracker), so
	// this closure needs no synchronization against a.mu of its own.
	// With Tomoe off its file isn't watched (nor read): Musubi's settings
	// arrive through reload_config.
	configWatchStop := func() {}
	if !cfg.TomoeOff {
		configWatchStop = a.watchTomoeConfig(tracker)
	}

	detector := startDetector(ctx, cfg)

	// Learn the user's voice from their past meetings' mic tracks, once.
	if cfg.Meeting.MicVoices {
		seeded := map[string]bool{}
		for _, lang := range meetingLanguages {
			if m, path, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang); status.SpeakerModelReady(m) && !seeded[m.ID] {
				seeded[m.ID] = true
				go ownerSeed(m.ID, path)
			}
		}
	}

	a.mu.Lock()
	a.bundle = bundle
	a.tracker = tracker
	a.configWatchStop = configWatchStop
	a.detector = detector
	a.mu.Unlock()
}

// watchTomoeConfig retunes tracker whenever Tomoe's config file changes,
// with Musubi's [recorder.*] keys still over it; ignored once a
// reload_config has turned Tomoe off.
func (a *Recorder) watchTomoeConfig(tracker *speaker.Tracker) (stop func()) {
	return config.Watch(config.Path(), 2*time.Second, func(newCfg *config.Config) {
		a.mu.Lock()
		musubi, off := a.musubiConfig, a.cfg.TomoeOff
		a.mu.Unlock()
		if off {
			return
		}
		if _, err := config.OverlayMusubi(newCfg, musubi); err != nil {
			fmt.Printf("config: %v; tuning from Tomoe's file alone\n", err)
		}
		if newCfg.TomoeOff {
			return
		}
		tracker.SetTuning(tuningFromConfig(newCfg))
		fmt.Printf("config: reloaded speaker clustering tuning: %+v\n", tracker.Tuning())
	})
}

// engineBundle is everything built from the config and the downloaded
// models that sessions decode with.
type engineBundle struct {
	modelMgr *models.Manager
	engines  *transcribe.EngineSet
	// streamingEngine powers live transcription's realtime ("pass 1")
	// pass; nil if the English streaming model isn't downloaded or
	// two_pass is off, in which case sessions fall back to single-pass.
	// English-only today, so only wired into live.Config when the
	// selected session language is "en" — see StartSession.
	streamingEngine transcribe.StreamingEngine
	// embedders holds the speaker models meetings use, one per model
	// (see models.ResolveSpeakerModel); nil if none is downloaded.
	embedders *speaker.EmbedderSet
	// micEmbedders are the same models again for the mic pipeline
	// (config mic_voices): it runs alongside the monitor's, and an
	// Embedder isn't safe for concurrent use.
	micEmbedders *speaker.EmbedderSet
}

// engineConfig is the transcription engines' configuration, as the
// offline pipeline builds it (cmd/musubi-recorder/pipeline.go).
func engineConfig(cfg *config.Config, status *models.Status) transcribe.Config {
	return transcribe.Config{
		EncoderPath:    status.EncoderPath,
		DecoderPath:    status.DecoderPath,
		JoinerPath:     status.JoinerPath,
		TokensPath:     status.TokensPath,
		VADPath:        status.VADPath,
		DecodingMethod: cfg.Transcription.DecodingMethod,
		MaxActivePaths: cfg.Transcription.MaxActivePaths,
		Model:          cfg.Transcription.Model,
		NumThreads:     cfg.Transcription.Threads,
	}
}

// buildEngines builds an engineBundle for cfg. Anything whose models
// aren't downloaded (or fail to load) is left nil; callers already treat
// each piece as optional except engines, which StartSession requires.
func buildEngines(cfg *config.Config, status *models.Status) engineBundle {
	b := engineBundle{modelMgr: modelManager(cfg)}
	videohint.SetOCRModels(status.OCRModelPath(models.OCRDetector), status.OCRModelPath(models.OCRRecognizer))

	// Without the final models there is nothing to record with; Start
	// reports that.
	if status.Ready() {
		built, err := transcribe.NewEngineSetFromConfig(engineConfig(cfg, status), status)
		if err != nil {
			fmt.Printf("Warning: failed to create transcription engine: %v\n", err)
		} else {
			b.engines = built
		}
	}

	// Create the realtime streaming engine if the English streaming
	// model is available (see internal/live's two-pass pipeline). Not
	// required for anything else to work — sessions just fall back to
	// single-pass without it.
	twoPass := cfg.Transcription.TwoPass && status.EnglishStreamingReady
	if twoPass {
		built, err := transcribe.NewStreamingEngine(transcribe.StreamingConfig{
			EncoderPath: status.EnglishStreamingEncoderPath,
			DecoderPath: status.EnglishStreamingDecoderPath,
			JoinerPath:  status.EnglishStreamingJoinerPath,
			TokensPath:  status.EnglishStreamingTokensPath,
			ModelType:   status.EnglishStreamingModelType,
		})
		if err == nil {
			b.streamingEngine = built
		} else {
			fmt.Printf("Warning: failed to load English streaming model: %v (live transcription will use single-pass mode)\n", err)
		}
	}

	// Load the speaker model each meeting language uses, if downloaded.
	set := speaker.NewEmbedderSet()
	micSet := speaker.NewEmbedderSet()
	loaded := false
	for _, lang := range meetingLanguages {
		if m, path, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang); status.SpeakerModelReady(m) {
			if _, err := set.Get(path); err == nil {
				loaded = true
				if cfg.Meeting.MicVoices {
					_, _ = micSet.Get(path)
				}
			} else {
				fmt.Printf("Warning: failed to load speaker embedding model: %v\n", err)
			}
		}
	}
	if loaded {
		b.embedders = set
		if micSet.Len() > 0 {
			b.micEmbedders = micSet
		}
	}
	return b
}

// close releases everything in the bundle. Only safe once nothing can
// still be decoding with it (see Shutdown).
func (b engineBundle) close() {
	if b.engines != nil {
		b.engines.Close()
	}
	if b.streamingEngine != nil {
		b.streamingEngine.Close()
	}
	if b.embedders != nil {
		b.embedders.Close()
	}
	if b.micEmbedders != nil {
		b.micEmbedders.Close()
	}
}

// meetingEmbedder is the speaker embedder for a meeting in lang (see
// models.Status.SpeakerModelFor), or nil if no speaker model is available.
func meetingEmbedder(cfg *config.Config, status *models.Status, set *speaker.EmbedderSet, lang string) *speaker.Embedder {
	m, path, fellBack := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang)
	if set == nil || !status.SpeakerModelReady(m) {
		return nil
	}
	if fellBack {
		fmt.Printf("Speaker model %s isn't downloaded; using %s\n", models.ResolveSpeakerModel(cfg.Meeting.SpeakerModel, lang).Name, m.Name)
	}
	e, err := set.Get(path)
	if err != nil {
		fmt.Printf("Warning: failed to load speaker model %s: %v\n", m.Name, err)
		return nil
	}
	return e
}

// tuningFromConfig is the speaker clustering tuning cfg asks for.
func tuningFromConfig(cfg *config.Config) speaker.Tuning {
	return speaker.TuningFromSeconds(
		cfg.Meeting.SpeakerThreshold,
		cfg.Meeting.StickyGraceWindow,
		cfg.Meeting.StickyThresholdMargin,
		cfg.Meeting.MinAssignDuration,
		cfg.Meeting.ShortSegmentGraceWindow,
	)
}

// startDetector starts the meeting auto-detector if cfg enables it, or
// returns nil (also when it's unavailable on this system).
func startDetector(ctx context.Context, cfg *config.Config) *meeting.Detector {
	if !cfg.Meeting.AutoDetect {
		return nil
	}
	d := meeting.NewDetector()
	if err := d.Start(ctx); err != nil {
		fmt.Printf("Warning: meeting auto-detect unavailable: %v\n", err)
		return nil
	}
	return d
}

// leaseEnginesLocked returns the current engine bundle for one decoding
// job (a recording, dictation, save or re-transcription) and counts it as
// in use until release is called. release is safe to call more than
// once. Caller must hold a.mu.
func (a *Recorder) leaseEnginesLocked(what string) (bundle engineBundle, release func(), err error) {
	if a.bundle.engines == nil {
		return engineBundle{}, nil, fmt.Errorf("transcription engine not initialized (models may not be downloaded)")
	}
	a.engineLeases++
	fmt.Printf("engines: leased for %s (%d in use)\n", what, a.engineLeases)
	var once sync.Once
	return a.bundle, func() {
		once.Do(func() {
			a.mu.Lock()
			a.releaseLeaseLocked(what)
			a.mu.Unlock()
		})
	}, nil
}

// releaseLeaseLocked gives back one engine lease. Caller must hold a.mu;
// normally reached through the release func from leaseEnginesLocked.
func (a *Recorder) releaseLeaseLocked(what string) {
	a.engineLeases--
	fmt.Printf("engines: released from %s (%d in use)\n", what, a.engineLeases)
}

// Run records meetings as the detector reports them until ctx ends, then
// shuts down: a recording in progress is stopped and saved first.
func (a *Recorder) Run(ctx context.Context) {
	var events <-chan meeting.MeetingEvent
	if a.detector != nil {
		events = a.detector.Events()
	} else {
		fmt.Println("meeting auto-detect is off or unavailable: nothing will be recorded")
	}
	a.run(ctx, events)
}

// run is Run with the events given, so tests can play a detector.
func (a *Recorder) run(ctx context.Context, events <-chan meeting.MeetingEvent) {
	defer a.Shutdown()
	go a.watchHealth(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// The detector ended; keep running until told to stop,
				// so a launchd restart isn't needed to save anything.
				events = nil
				continue
			}
			a.onMeetingEvent(ev)
		}
	}
}

// watchHealth keeps Status.Health current (a permission granted, a model
// downloaded in Tomoe), telling the feed when it changes.
func (a *Recorder) watchHealth(ctx context.Context) {
	a.refreshHealth(true)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refreshHealth(true)
		}
	}
}

// Recording reports whether a meeting is being recorded.
func (a *Recorder) Recording() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rec != nil
}

// Shutdown stops a recording in progress, waits for every queued save
// (the live feed's "saved" included) and releases the models. Safe to
// call more than once.
func (a *Recorder) Shutdown() {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.closing = true
	busy := a.rec != nil
	a.mu.Unlock()

	if a.configWatchStop != nil {
		a.configWatchStop()
	}
	if busy {
		if _, err := a.StopSession(); err != nil && !errors.Is(err, errIdle) {
			fmt.Printf("shutdown: stopping the recording: %v\n", err)
		}
	}
	if a.detector != nil {
		a.detector.Stop()
	}

	// A stop the detector began just before may not have queued its save
	// yet; closing saves under it would lose that session.
	a.stopping.Wait()
	close(a.saves)
	<-a.saverDone
	// After the saves, so feed clients still get their "saved".
	a.stopLiveFeed()

	a.mu.Lock()
	bundle := a.bundle
	a.bundle = engineBundle{}
	a.mu.Unlock()
	bundle.close()
}
