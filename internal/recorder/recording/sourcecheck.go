package recording

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
//
// A mic with signal is not always a better mic: a webcam or laptop mic in
// the same room as the speakers mostly hears the far side of the call,
// which the recording already captures as system audio. Switching to it
// would record the meeting twice and the owner hardly at all. So while
// system audio is captured, what each other mic hears is compared with
// the system audio over the same moments (audio.MatchEcho), and a mic
// that only follows the speakers is not offered (Status.SuggestNote says
// so instead). Off with [meeting] source_check_echo = false.

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

	// echoLead and echoTrail are how much system audio before and after
	// the probe it is compared with: sound reaches the other mic later
	// than the system capture (output buffering, then the room), up to
	// about half a second, while the two captures' own buffering can put
	// the probe a little ahead.
	echoLead  = 600 * time.Millisecond
	echoTrail = 300 * time.Millisecond
	// echoCorr is the envelope correlation from which a probe counts as
	// hearing only the speakers. On synthetic speech through a simulated
	// room (docs/research-log.md, 2026-11-12) an echo scored at least
	// 0.8 in 98% of cases and independent speech in the room reached it
	// in 2%; a miss of that kind only waits for the next check.
	echoCorr = 0.8
	// echoCatchUp bounds the wait for the system capture to deliver the
	// moments just after the probe.
	echoCatchUp = time.Second
)

// speakersNote is Status.SuggestNote when the other mics with signal
// only hear the meeting audio.
const speakersNote = "Other microphones only hear the meeting audio from your speakers"

// meetingTap is the recording's system-audio capture as the echo check
// reads it (an *audio.StreamCapturer).
type meetingTap interface {
	Recorded() int
	RecordedRange(from, to int) []float32
}

// sourceProbes are how the check finds and hears other sources; tests
// replace them.
type sourceProbes struct {
	// mics are the input devices other than current (a mic selection).
	mics func(current string) []control.Source
	// listenMic records one input device for probeFor, calling started
	// once it captures (the moment the probe lines up with the system
	// audio); nil if it couldn't be opened.
	listenMic func(ctx context.Context, id string, started func()) []float32
	// hearSystem listens to the whole system's audio for probeFor.
	hearSystem func(ctx context.Context) bool
	// systemInstead is the source to offer when the whole system has
	// sound but current (a system selection) doesn't.
	systemInstead func(current string) control.Source
	// defaultMic names the system's default input, "" if unknown.
	defaultMic func() string
}

func defaultProbes() sourceProbes {
	return sourceProbes{mics: otherMics, listenMic: listenMic, hearSystem: hearSystem, systemInstead: systemInstead, defaultMic: defaultInputName}
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

// listenMic opens device on its own capture and records probeFor of it.
func listenMic(ctx context.Context, device string, started func()) []float32 {
	c, err := audio.NewCapturer(device, audio.Input)
	if err != nil {
		return nil
	}
	defer c.Close()
	if err := c.Start(); err != nil {
		return nil
	}
	// After Start, not before NewCapturer: opening a USB device can take
	// longer than the whole lag range the echo check allows for.
	started()
	select {
	case <-time.After(probeFor):
	case <-ctx.Done():
	}
	samples := c.Samples()
	_ = c.Stop()
	return samples
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
	var tap meetingTap
	if kind == "mic" {
		cur = a.micSourceLocked(a.src.mic)
		if a.cfg.Meeting.SourceCheckEcho {
			tap = a.src.meeting
		}
	} else {
		cur = control.Source{Kind: "system", ID: a.src.system, Name: a.src.sysName}
	}
	// The same source by kind and ID: its name can be filled in later
	// (the default mic's device name), which is no reason to start over.
	switch {
	case a.src.silent == nil || a.src.silent.Kind != cur.Kind || a.src.silent.ID != cur.ID:
		a.src.silent, a.src.suggest, a.src.suggestNote = &cur, nil, ""
		a.publishAudioLocked()
	case a.src.silent.Name != cur.Name:
		a.src.silent = &cur
		a.publishAudioLocked()
	}
	if a.src.suggest != nil || a.src.probing[kind] || time.Since(a.src.lastProbe[kind]) < probeEvery {
		return
	}
	a.src.probing[kind], a.src.lastProbe[kind] = true, time.Now()
	go a.checkSources(a.src.ctx, kind, cur.ID, tap)
}

// clearSilent forgets a silent kind source (its signal came back).
func (a *Recorder) clearSilent(kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.src.silent == nil || a.src.silent.Kind != kind {
		return
	}
	a.src.silent, a.src.suggest, a.src.suggestNote = nil, nil, ""
	a.publishAudioLocked()
}

// checkSources listens to the other sources of kind and offers the first
// one with signal (for a mic, one that hears more than the speakers when
// tap is the system audio), if the silent source is still the same by
// then. Finding none, it keeps the reason, if any, in suggestNote.
func (a *Recorder) checkSources(ctx context.Context, kind, current string, tap meetingTap) {
	found, note := a.findSource(ctx, kind, current, tap)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.src.probing != nil {
		delete(a.src.probing, kind)
	}
	if ctx.Err() != nil || a.src.silent == nil || a.src.silent.Kind != kind || a.src.silent.ID != current {
		return
	}
	if found == nil {
		if note != a.src.suggestNote {
			a.src.suggestNote = note
			a.publishAudioLocked()
		}
		if note != "" {
			fmt.Printf("source check: %s %q is silent; %s\n", kind, a.src.silent.Name, strings.ToLower(note[:1])+note[1:])
		}
		return
	}
	a.src.suggest, a.src.suggestNote = found, ""
	fmt.Printf("source check: %s %q is silent; %q has signal\n", kind, a.src.silent.Name, found.Name)
	a.publishAudioLocked()
}

func (a *Recorder) findSource(ctx context.Context, kind, current string, tap meetingTap) (*control.Source, string) {
	p := a.probes
	if kind == "mic" {
		speakers := false
		for i, m := range p.mics(micSelection(current)) {
			if i == probeMaxMics || ctx.Err() != nil {
				break
			}
			start := 0
			heard := p.listenMic(ctx, micSelection(m.ID), func() {
				if tap != nil {
					start = tap.Recorded()
				}
			})
			if !audio.HasSignal(heard) || ctx.Err() != nil {
				continue
			}
			if tap == nil { // no system audio to compare with
				return &m, ""
			}
			switch v, match := echoOf(ctx, heard, tap, start); v {
			case echoSpeakers:
				speakers = true
				fmt.Printf("source check: mic %q hears the speakers (envelope correlation %.2f at %v); not offered\n", m.Name, match.Corr, match.Lag.Round(time.Millisecond))
			case echoQuiet:
				fmt.Printf("source check: mic %q heard only steady noise while the speakers were silent; checking again later\n", m.Name)
			default:
				return &m, ""
			}
		}
		if speakers {
			return nil, speakersNote
		}
		return nil, ""
	}
	// The whole system silent too means the call is just quiet.
	if current == "everything" || !p.hearSystem(ctx) {
		return nil, ""
	}
	s := p.systemInstead(current)
	if s.ID == current {
		return nil, ""
	}
	return &s, ""
}

// echoVerdict is what the echo check made of a probe.
type echoVerdict int

const (
	// echoUnknown: not enough system audio to compare with; the probe is
	// offered, as it was before the check.
	echoUnknown echoVerdict = iota
	// echoOwn: the probe heard something the speakers don't explain.
	echoOwn
	// echoSpeakers: the probe follows the system audio.
	echoSpeakers
	// echoQuiet: the speakers played nothing and the probe heard only
	// steady noise, so nothing tells a room mic from the owner's; the
	// next check (probeEvery) may catch someone talking.
	echoQuiet
)

// echoOf compares heard, a probe whose first sample lines up with sample
// start of tap, with the system audio from echoLead before it to
// echoTrail after it.
func echoOf(ctx context.Context, heard []float32, tap meetingTap, start int) (echoVerdict, audio.EchoMatch) {
	lead := int(echoLead.Seconds() * audio.CaptureSampleRate)
	trail := int(echoTrail.Seconds() * audio.CaptureSampleRate)
	end := start + len(heard) + trail
	for deadline := time.Now().Add(echoCatchUp); tap.Recorded() < end && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return echoUnknown, audio.EchoMatch{}
		case <-time.After(20 * time.Millisecond):
		}
	}
	from := max(start-lead, 0)
	ref := tap.RecordedRange(from, end)
	if len(ref) < len(heard) {
		return echoUnknown, audio.EchoMatch{}
	}
	m := audio.MatchEcho(heard, ref, start-from)
	switch {
	case m.RefActive && m.Corr >= echoCorr:
		return echoSpeakers, m
	case m.RefActive || m.ProbeEvents:
		return echoOwn, m
	}
	return echoQuiet, m
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
