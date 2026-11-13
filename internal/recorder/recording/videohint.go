package recording

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/diarize"
	"github.com/anielsud/musubi/internal/recorder/live"
	"github.com/anielsud/musubi/internal/recorder/meetingaudio"
	"github.com/anielsud/musubi/internal/recorder/session"
	"github.com/anielsud/musubi/internal/recorder/videohint"
)

// hintSession is what a recording session's hint watcher feeds.
type hintSession struct {
	sess        *session.Session
	coordinator *live.Coordinator
	diar        *diarize.SessionDiarizer // nil: the previous pipeline (names go to the live tracker)
	log         *videohint.LookLog
	watcher     *videohint.Watcher
	changes     *live.ChangeLog // speaker-change signals for TurnMode
}

// newHintWatcher creates the session's meeting-window watcher. It runs
// once startHintWatcher has the session; Burst may be called before.
func (a *Recorder) newHintWatcher() (*videohint.Watcher, *hintSession) {
	hs := &hintSession{}
	learn, check := a.cfg.Meeting.VideoHintTiming()
	hs.watcher = videohint.NewWatcher(videohint.WatchConfig{
		Source:        a.cfg.Meeting.VideoHintWindow,
		LearnInterval: learn,
		CheckInterval: check,
		NeedsLearning: func() bool {
			if a.cfg.Meeting.RecordForTuning {
				return true // look at the learning rate throughout
			}
			if hs.diar != nil {
				return hs.diar.NeedsNames()
			}
			return a.tracker == nil || a.tracker.HasUnnamedSpeakers()
		},
		OnLook: func(l videohint.Look) { a.onLook(hs, l) },
	})
	if a.cfg.Meeting.RecordForTuning {
		hs.watcher.SetOnFullFrame(func(id int, jpeg []byte) {
			if err := hs.log.WriteFull(id, jpeg); err != nil {
				fmt.Printf("videohint: saving frame %d: %v\n", id, err)
			}
		})
		hs.watcher.SetOnWindowShot(func(lookID, windowID int, thumb bool, jpeg []byte) {
			if err := hs.log.WriteWindowShot(lookID, windowID, thumb, jpeg); err != nil {
				fmt.Printf("videohint: saving window %d: %v\n", windowID, err)
			}
		})
	}
	return hs.watcher, hs
}

// startHintWatcher runs hs's watcher for the session until ctx ends, and
// turns the live pass's "this speaker has no name yet" signals into
// bursts of looks.
func (a *Recorder) startHintWatcher(ctx context.Context, hs *hintSession) {
	hs.log = videohint.NewLookLog(filepath.Join(config.SessionDir(), hs.sess.ID))
	go hs.watcher.Run(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hs.coordinator.HintNeeded():
				hs.watcher.Burst()
			}
		}
	}()
}

// onLook records a look with the session and hands a read name to
// whatever attributes names.
func (a *Recorder) onLook(hs *hintSession, l videohint.Look) {
	if err := hs.log.Write(l); err != nil {
		fmt.Printf("videohint: recording look %d: %v\n", l.ID, err)
	}
	if l.Usable && l.Name != "" {
		if hs.changes != nil && hs.coordinator != nil {
			hs.changes.NameSeen(hs.coordinator.SessionTime(l.Time), l.Name)
		}
		if hs.diar != nil {
			hs.diar.AddHint(hs.coordinator.SessionTime(l.Time), l.Name)
		} else if a.tracker != nil {
			a.tracker.SetHintForRecent(l.Name, videohint.HintAttachMaxAge)
		}
	} else if hs.diar != nil && len(l.Candidates) > 1 {
		hs.diar.AddCandidates(hs.coordinator.SessionTime(l.Time), l.Candidates)
	}
}

// followMeetingAudio reports what the automatic system-audio source is
// capturing, and while it's still the whole system (the call hadn't
// started making sound yet), checks every few seconds for a meeting app
// to move to, so notification sounds stay out of the transcript once
// the call is going. Does nothing for a fixed source (auto nil).
func (a *Recorder) followMeetingAudio(ctx context.Context, auto *meetingaudio.Auto) {
	a.videoHintMu.Lock()
	a.audioSource = ""
	if auto != nil {
		a.audioSource = auto.Current()
	}
	current := a.audioSource
	a.videoHintMu.Unlock()
	if auto == nil {
		return
	}
	fmt.Printf("system audio: capturing %s\n", current)
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// A source that stopped sending audio (the window it was
				// tied to closed) is replaced; the whole system moves to
				// the meeting app once that makes sound. Checked for the
				// whole recording, not just until the first move.
				name, ok := auto.Recover()
				if !ok {
					name, ok = auto.TryMeetingApp()
				}
				if ok {
					fmt.Printf("system audio: now capturing %s\n", name)
					a.videoHintMu.Lock()
					a.audioSource = name
					a.videoHintMu.Unlock()
				}
			}
		}
	}()
}

// micSilentAfter is how long a silent mic goes before the warning.
const micSilentAfter = 20 * time.Second

// watchMic logs when the mic goes silent for micSilentAfter, and when
// sound returns.
func (a *Recorder) watchMic(ctx context.Context, c *live.Coordinator, device string) {
	name := micName(device)
	warned := false
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			quiet := c.MicQuietFor()
			switch {
			case !warned && quiet >= micSilentAfter:
				warned = true
				fmt.Printf("mic %q: no signal for %.0fs\n", name, quiet.Seconds())
			case warned && quiet < 5*time.Second:
				warned = false
				fmt.Printf("mic %q: signal back\n", name)
			}
		}
	}
}

// micName is the device a mic selection records from: "default" is the
// system's default input, named.
func micName(device string) string {
	if device != "default" {
		return device
	}
	devices, err := audio.ListDevices()
	if err != nil {
		return "the default mic"
	}
	for _, d := range devices {
		if d.IsDefault && d.DeviceType == audio.Input {
			return d.Name + " (the system default)"
		}
	}
	return "the default mic"
}
