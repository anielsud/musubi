package recording

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/audiosources"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/meetingaudio"
)

// The source check: when a recording's mic or system audio has had no
// signal for a while (the wrong mic as the system default, a meeting app
// that isn't the one making sound), the recorder listens briefly to the
// other inputs and sources, each on a capture of its own so the
// recording is untouched, and offers one that has signal
// (Status.Silent, Status.Suggest, the feed's "audio" message). The Musubi
// app switches with set_source. Off with [meeting] source_check = false.

const (
	// systemSilentAfter is how long system audio goes with nothing at
	// all before it counts as silent: a call can be quiet for a while,
	// but a captured app that is the right one sends some sound within
	// a minute.
	systemSilentAfter = time.Minute
	// probeEvery is the least time between two checks of one source
	// while it stays silent, and probeFor how long each candidate is
	// listened to: a few seconds of one extra capture a minute at most.
	probeEvery = time.Minute
	probeFor   = 1500 * time.Millisecond
	// probeMaxMics bounds how many other inputs one check opens.
	probeMaxMics = 4
)

// sourceProbes are how the check finds and hears other sources; tests
// replace them.
type sourceProbes struct {
	// mics are the input devices other than current (a mic selection).
	mics func(current string) []control.Source
	// hearMic listens to one input device for probeFor.
	hearMic func(ctx context.Context, id string) bool
	// hearSystem listens to the whole system's audio for probeFor.
	hearSystem func(ctx context.Context) bool
	// systemInstead is the source to offer when the whole system has
	// sound but current (a system selection) doesn't.
	systemInstead func(current string) control.Source
}

func defaultProbes() sourceProbes {
	return sourceProbes{mics: otherMics, hearMic: hearMic, hearSystem: hearSystem, systemInstead: systemInstead}
}

// otherMics are the input devices other than the current selection (the
// default device too, when the default is current).
func otherMics(current string) []control.Source {
	devices, err := audio.ListDevices()
	if err != nil {
		return nil
	}
	var out []control.Source
	for _, d := range devices {
		if d.DeviceType != audio.Input || d.Name == current || (current == "default" && d.IsDefault) {
			continue
		}
		out = append(out, micSource(d.Name))
	}
	return out
}

// hearMic opens device on its own capture, listens for probeFor and
// reports whether it heard anything at all.
func hearMic(ctx context.Context, device string) bool {
	c, err := audio.NewCapturer(device, audio.Input)
	if err != nil {
		return false
	}
	defer c.Close()
	if err := c.Start(); err != nil {
		return false
	}
	select {
	case <-time.After(probeFor):
	case <-ctx.Done():
	}
	samples := c.Samples()
	_ = c.Stop()
	return audio.HasSignal(samples)
}

func hearSystem(context.Context) bool {
	ok, err := meetingaudio.ProbeSystem(probeFor)
	return err == nil && ok
}

// systemInstead is an app making sound other than the one captured, or
// else the whole system.
func systemInstead(current string) control.Source {
	if active, err := audiosources.ListActive(); err == nil {
		for _, s := range active {
			if id := strconv.Itoa(s.PID); id != current {
				return control.Source{Kind: "system", ID: id, Name: s.Name}
			}
		}
	}
	return control.Source{Kind: "system", ID: "everything", Name: "Everything"}
}

// markSilent records that the recording's kind source ("mic" or
// "system") has no signal, and starts a check of the others unless one
// ran lately or the check is off.
func (a *Recorder) markSilent(kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.src.recording || !a.cfg.Meeting.SourceCheck {
		return
	}
	var cur control.Source
	if kind == "mic" {
		cur = micSource(a.src.mic)
	} else {
		cur = control.Source{Kind: "system", ID: a.src.system, Name: a.src.sysName}
	}
	if a.src.silent == nil || *a.src.silent != cur {
		a.src.silent, a.src.suggest = &cur, nil
		a.publishAudioLocked()
	}
	if a.src.suggest != nil || a.src.probing[kind] || time.Since(a.src.lastProbe[kind]) < probeEvery {
		return
	}
	a.src.probing[kind], a.src.lastProbe[kind] = true, time.Now()
	go a.checkSources(a.src.ctx, kind, cur.ID)
}

// clearSilent forgets a silent kind source (its signal came back).
func (a *Recorder) clearSilent(kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.src.silent == nil || a.src.silent.Kind != kind {
		return
	}
	a.src.silent, a.src.suggest = nil, nil
	a.publishAudioLocked()
}

// checkSources listens to the other sources of kind and offers the first
// one with signal, if the silent source is still the same by then.
func (a *Recorder) checkSources(ctx context.Context, kind, current string) {
	found := a.findSource(ctx, kind, current)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.src.probing != nil {
		delete(a.src.probing, kind)
	}
	if found == nil || ctx.Err() != nil || a.src.silent == nil || a.src.silent.Kind != kind || a.src.silent.ID != current {
		return
	}
	a.src.suggest = found
	fmt.Printf("source check: %s %q is silent; %q has signal\n", kind, current, found.Name)
	a.publishAudioLocked()
}

func (a *Recorder) findSource(ctx context.Context, kind, current string) *control.Source {
	p := a.probes
	if kind == "mic" {
		for i, m := range p.mics(micSelection(current)) {
			if i == probeMaxMics || ctx.Err() != nil {
				break
			}
			if p.hearMic(ctx, micSelection(m.ID)) {
				return &m
			}
		}
		return nil
	}
	// The whole system silent too means the call is just quiet.
	if current == "everything" || !p.hearSystem(ctx) {
		return nil
	}
	s := p.systemInstead(current)
	if s.ID == current {
		return nil
	}
	return &s
}

// watchSystem marks the recording's system audio silent after
// systemSilentAfter with nothing at all, and clears it when sound
// returns. c is the recording's coordinator.
func (a *Recorder) watchSystem(ctx context.Context, c interface{ MonitorQuietFor() time.Duration }) {
	silent := false
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			quiet := c.MonitorQuietFor()
			switch {
			case quiet >= systemSilentAfter:
				if !silent {
					fmt.Printf("system audio: no signal for %.0fs\n", quiet.Seconds())
				}
				silent = true
				a.markSilent("system")
			case silent && quiet < 5*time.Second:
				silent = false
				fmt.Println("system audio: signal back")
				a.clearSilent("system")
			}
		}
	}
}
