package recording

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
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
	// Taken once: the closures below outlive this call, and reload_config
	// may replace a.cfg meanwhile.
	cfg := a.cfg
	learn, check := cfg.Meeting.VideoHintTiming()
	hs.watcher = videohint.NewWatcher(videohint.WatchConfig{
		Source:        cfg.Meeting.VideoHintWindow,
		LearnInterval: learn,
		CheckInterval: check,
		NeedsLearning: func() bool {
			if cfg.Meeting.RecordForTuning {
				return true // look at the learning rate throughout
			}
			if hs.diar != nil {
				return hs.diar.NeedsNames()
			}
			return a.tracker == nil || a.tracker.HasUnnamedSpeakers()
		},
		OnLook: func(l videohint.Look) { a.onLook(hs, l) },
	})
	if cfg.Meeting.RecordForTuning {
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
	a.videoHintMu.Lock()
	a.videoLooks = nil
	a.hints = hs
	a.videoHintMu.Unlock()
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

// onLook records a look with the session, hands a read name to
// whatever attributes names, and sends the look to the live feed (the
// Musubi app's hint timeline).
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
	meta := l
	meta.Thumb = nil
	a.videoHintMu.Lock()
	current := a.hints == hs
	if current {
		a.videoLooks = append(a.videoLooks, meta)
	}
	a.videoHintMu.Unlock()
	if current {
		a.mu.Lock()
		if a.liveFeed != nil {
			a.liveFeed.Look(hs.sess.ID, func() control.LookView { return toLookView(l, hs.sess.CreatedAt, true) })
		}
		a.mu.Unlock()
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
					a.publishStatus()
				}
			}
		}
	}()
}

// micSilentAfter is how long a silent mic goes before the warning.
const micSilentAfter = 20 * time.Second

// watchMic logs when the mic goes silent for micSilentAfter, and when
// sound returns, and tells the source check (markSilent, clearSilent).
func (a *Recorder) watchMic(ctx context.Context, c *live.Coordinator, device string) {
	if device == "default" {
		// So that a silent default mic is reported by its device's name.
		a.refreshDefaultMic(true)
	}
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
			case quiet >= micSilentAfter:
				if !warned {
					fmt.Printf("mic %q: no signal for %.0fs\n", name, quiet.Seconds())
				}
				warned = true
				a.markSilent("mic")
			case warned && quiet < 5*time.Second:
				warned = false
				fmt.Printf("mic %q: signal back\n", name)
				a.clearSilent("mic")
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
	if name := defaultInputName(); name != "" {
		return name + " (the system default)"
	}
	return "the default mic"
}

// defaultInputName is the name of the system's default input device, ""
// if it can't be found.
func defaultInputName() string {
	devices, err := audio.ListDevices()
	if err != nil {
		return ""
	}
	for _, d := range devices {
		if d.IsDefault && d.DeviceType == audio.Input {
			return d.Name
		}
	}
	return ""
}
