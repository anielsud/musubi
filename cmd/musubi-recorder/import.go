package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/dataformat"
	"github.com/anielsud/musubi/internal/recorder/live"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/recording"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/speaker"
)

// importCmd makes a session from a recording made elsewhere (a phone's,
// docs/phone.md): the audio is the session's single mic track, run
// through the offline pipeline `session replay` uses with the current
// config, then given the steps a recorded meeting gets after it ends.
var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Make a session from an outside audio file (a phone's recording)",
	Long: `Transcribes an audio file any ffmpeg reads as a meeting heard on the mic
and saves it as a session marked platform "phone": the same offline
pipeline as 'session replay' with the current config, then the steps a
recorded meeting gets afterwards (diarization, speaker labels, the
owner's voice). --started-at is when the recording began, with its zone
offset; --event-id names the calendar event (Musubi's item id) it was,
which Musubi then links it to instead of matching by time.

When the recorder is running on --data, the file is copied into its
inbox and the recorder imports it (never while a meeting records: it
waits, and a meeting that starts mid-import stops it until the meeting
is saved); this command waits for the result. Otherwise the import runs
in this process.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, _ := cmd.Flags().GetString("data")
		musubi, _ := cmd.Flags().GetString("musubi-config")
		at, _ := cmd.Flags().GetString("started-at")
		event, _ := cmd.Flags().GetString("event-id")
		id, _ := cmd.Flags().GetString("id")
		here, _ := cmd.Flags().GetBool("here")
		started, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return fmt.Errorf("--started-at: an RFC 3339 time with its zone offset, e.g. 2026-11-17T10:02:00+01:00")
		}
		if id == "" {
			id = uuid.NewString()
		}
		if !control.ValidImportID(id) {
			return fmt.Errorf("--id: a lowercase UUID")
		}
		if event != "" && !control.ValidEventID(event) {
			return fmt.Errorf("--event-id: not an event id")
		}
		dir, err := useDataDir(data)
		if err != nil {
			return err
		}
		if !here {
			done, err := importThroughRecorder(dir, args[0], control.ImportArgs{ID: id, StartedAt: started, EventID: event})
			if done || err != nil {
				return err
			}
		}
		if musubi == "" {
			musubi = config.ResolveMusubiPath(dir)
		}
		return importHere(musubi, args[0], id, started, event)
	},
}

func init() {
	importCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder (never Tomoe's)")
	importCmd.Flags().String("musubi-config", "", "Musubi's config file, whose [recorder.*] tables override Tomoe's settings (default: as `run` finds it)")
	importCmd.Flags().String("started-at", "", "when the recording began (RFC 3339 with zone offset; required)")
	importCmd.Flags().String("event-id", "", "the calendar event (Musubi item id) the recording was of")
	importCmd.Flags().String("id", "", "the session id to create, a UUID (default: a new one)")
	importCmd.Flags().Bool("here", false, "run in this process even if the recorder is running (what the recorder itself runs)")
	importCmd.Flags().MarkHidden("here")
	importCmd.MarkFlagRequired("started-at")
	rootCmd.AddCommand(importCmd)
}

// importThroughRecorder hands the import to the recorder running on dir,
// waiting for its result; done is false when no recorder is running.
func importThroughRecorder(dir, file string, p control.ImportArgs) (done bool, err error) {
	c := control.NewClient(control.SocketPath(dir))
	c.Timeout = 10 * time.Second
	var st control.Status
	switch err := c.Call(context.Background(), control.OpStatus, nil, &st); {
	case errors.Is(err, control.ErrNotRunning):
		return false, nil
	case err != nil:
		return true, fmt.Errorf("the recorder did not answer: %w", err)
	}
	if st.Recording {
		fmt.Println("A meeting is recording; the import waits until it is saved.")
	}
	inbox := control.InboxDir(dir)
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		return true, err
	}
	ext := strings.ToLower(filepath.Ext(file))
	if len(ext) < 2 || len(ext) > 6 || strings.ContainsAny(ext[1:], `./\ `) {
		ext = ".audio"
	}
	p.Path = filepath.Join(inbox, p.ID+ext)
	if err := copyPrivate(file, p.Path); err != nil {
		return true, err
	}
	p.Retry = true
	last := ""
	for {
		var is control.ImportStatus
		if err := c.Call(context.Background(), control.OpImport, p, &is); err != nil {
			return true, err
		}
		p.Retry = false
		if is.State != last {
			fmt.Printf("import %s: %s\n", is.Session, is.State)
			last = is.State
		}
		switch is.State {
		case control.ImportImported:
			return true, nil
		case control.ImportFailed:
			return true, fmt.Errorf("import failed: %s (the recorder's log has why)", is.Error)
		}
		time.Sleep(5 * time.Second)
	}
}

// copyPrivate copies src to dst (mode 0600) through a temporary file, so
// the recorder never sees half a file.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".import-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// phoneTitle is an imported session's title until Musubi names it, in
// the form the recorder's default titles take ("<app> Meeting <time>"),
// at the time the phone gave (the place it was recorded).
func phoneTitle(at time.Time) string { return "Phone Meeting " + at.Format("2006-01-02 15:04") }

// importHere imports file as session id in this process, into the folder
// config.DataDir names.
func importHere(musubiConfig, file, id string, startedAt time.Time, eventID string) error {
	if err := dataformat.Check(config.DataDir()); err != nil {
		return err
	}
	store := session.NewStore(config.SessionDir())
	if sess, err := store.Load(id); err == nil && sess.ID == id {
		if sess.Platform != recording.PlatformPhone {
			return fmt.Errorf("a session with id %s already exists", id)
		}
		fmt.Printf("import %s: already imported\n", id)
		return nil
	}
	cfg, err := loadConfig(musubiConfig)
	if err != nil {
		return err
	}
	status := models.NewManagerIn(config.ModelDirs(cfg)).WithLiveModel(cfg.Transcription.LiveModel).Check()
	if !status.Ready() {
		return fmt.Errorf("the transcription models aren't in %s", strings.Join(config.ModelDirs(cfg), " or "))
	}
	began := time.Now()
	samples, err := session.DecodeToFloat32(file)
	if err != nil {
		return err
	}
	if len(samples) < audio.CaptureSampleRate {
		return fmt.Errorf("less than a second of audio")
	}
	audioSecs := float64(len(samples)) / audio.CaptureSampleRate
	const lang = "en"
	fmt.Printf("import %s: %s of audio\n", id, formatDuration(audioSecs))

	pipe, err := loadOfflinePipelineThreads(cfg, status, lang, false, cfg.Transcription.TwoPass, cfg.Transcription.Threads)
	if err != nil {
		return err
	}
	defer pipe.Close()
	tuning := speaker.TuningFromSeconds(cfg.Meeting.SpeakerThreshold, cfg.Meeting.StickyGraceWindow,
		cfg.Meeting.StickyThresholdMargin, cfg.Meeting.MinAssignDuration, cfg.Meeting.ShortSegmentGraceWindow)
	lc := pipe.liveConfig(tuning, cfg.Transcription.TwoPass && pipe.streaming != nil)

	// Speakers as a recording labels them (recording.beginLocked): the
	// meeting's speaker model and a tracker with the configured tuning;
	// with mic_voices and the owner's voice known, the owner's lines are
	// told from the room's, and lines surely theirs teach their voice.
	saveOwner := func() {}
	if m, path, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, lang); status.SpeakerModelReady(m) {
		emb, err := speaker.NewEmbedder(path)
		if err != nil {
			return fmt.Errorf("loading the speaker model: %w", err)
		}
		defer emb.Close()
		tracker := speaker.NewTracker(speaker.DefaultThreshold)
		tracker.SetTuning(tuning)
		lc.Embedder, lc.Tracker = emb, tracker
		if cfg.Meeting.MicVoices {
			centroid, heard, save := recording.OwnerVoice(m.ID)
			if centroid != nil {
				micEmb, err := speaker.NewEmbedder(path)
				if err != nil {
					return fmt.Errorf("loading the speaker model: %w", err)
				}
				defer micEmb.Close()
				lc.MicEmbedder, lc.OwnerVoice, lc.MicOtherBelow, lc.HeardOwner = micEmb, centroid, cfg.Meeting.MicOtherBelow, heard
				saveOwner = save
				fmt.Printf("import %s: mic voices on (%s)\n", id, m.ID)
			}
		}
	}
	lc.MinSilenceDuration, lc.MaxSpeechDuration = cfg.Meeting.MinSilenceDuration, cfg.Meeting.MaxSpeechDuration
	lc.MinSpeechLevelDB, lc.MicLevelMarginDB = cfg.Meeting.MinSpeechLevelDB, cfg.Meeting.MicLevelMarginDB
	lc.TurnMode, lc.TurnMaxSeconds, lc.TurnMaxGap = cfg.Meeting.TurnMode, cfg.Meeting.TurnMaxSeconds, cfg.Meeting.TurnMaxGap
	if lc.TurnMode {
		// No meeting window or far side: no speaker-change signals.
		lc.SpeakerChanged = (&live.ChangeLog{}).Between
	}
	lc.TurnInterjection, lc.DecodePad = cfg.Meeting.TurnInterjection, cfg.Meeting.DecodePad
	lc.ReplayProgress = func(done, total int) {
		if done%10 == 0 || done == total {
			fmt.Printf("progress import %s %d/%d\n", id, done, total)
		}
	}
	res, err := live.ReplayDetailed(lc, samples, nil)
	if err != nil {
		return fmt.Errorf("transcribing: %w", err)
	}
	saveOwner()
	segs := res.Segments
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].StartTime < segs[j].StartTime })

	// The audio first, then session.json, as a recording is saved: a
	// session file is never there without its audio.
	dir := filepath.Join(config.SessionDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	audioPath := filepath.Join(dir, "audio.m4a")
	if err := session.SaveAudioM4A([][]float32{samples}, audio.CaptureSampleRate, audioPath); err != nil {
		return fmt.Errorf("saving the audio: %w", err)
	}
	dur := time.Duration(audioSecs * float64(time.Second))
	sess := &session.Session{
		ID: id, Title: phoneTitle(startedAt), Platform: recording.PlatformPhone, Language: lang,
		CreatedAt: startedAt, EndedAt: startedAt.Add(dur), Duration: audioSecs,
		Sources: []string{string(live.SourceMic)}, Segments: segs, AudioPath: audioPath, EventID: eventID,
	}
	if err := store.Save(sess); err != nil {
		return err
	}
	// The post-meeting pass, as for a recorded meeting (persistSession);
	// it relabels only the far side's lines, so a mic-only import keeps
	// its labels, but a change there reaches imports too.
	if status.DiarizationReady() {
		if err := session.RunDiarizeWithRetry(id, session.DefaultDiarizeAttempts); err != nil {
			fmt.Printf("import %s: kept without post-meeting diarization: %v\n", id, err)
		}
	}
	speakers := map[string]bool{}
	for _, s := range segs {
		speakers[s.Speaker] = true
	}
	fmt.Printf("import %s: saved, %d lines, %d speaker labels, %s of audio in %s\n", id, len(segs), len(speakers),
		formatDuration(audioSecs), time.Since(began).Round(time.Second))
	return nil
}
