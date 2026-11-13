//go:build darwin

package meetingaudio

import (
	"fmt"
	"strconv"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
	"github.com/anielsud/musubi/internal/recorder/audiosources"
	"github.com/anielsud/musubi/internal/recorder/guestaudio"
)

// The functions below let the Musubi app switch a recording's system
// audio while it records (control's set_source): every source is
// captured through an audio.SwitchCapturer, so moving to another one
// keeps the recording's audio and its transcript in one piece.

// NewSwitchableMonitorSource is NewMonitorSource with the switcher
// exposed: sw captures the selection and can be moved with SwitchTo or
// AutoOn; auto is set for AutoSource (and ""), whose follower moves it
// on its own. All nil when there is no system audio (NoSource, or
// capture unavailable: mic-only, as NewMonitorSource).
func NewSwitchableMonitorSource(sourceHint string) (stream *audio.StreamCapturer, sw *audio.SwitchCapturer, auto *Auto, err error) {
	switch sourceHint {
	case NoSource:
		return nil, nil, nil, nil
	case AutoSource, "":
		stream, auto, err = NewAutoMonitorSource()
		if stream == nil || err != nil {
			return nil, nil, nil, err
		}
		return stream, auto.sw, auto, nil
	}
	c, err := openFixed(sourceHint)
	if err != nil {
		fmt.Printf("meetingaudio: %v, starting mic-only\n", err)
		return nil, nil, nil, nil
	}
	sw = audio.NewSwitchCapturer(c)
	if err := sw.Start(); err != nil {
		c.Close()
		fmt.Printf("meetingaudio: guest audio capture unavailable, starting mic-only (%v) — check Screen Recording permission in System Settings > Privacy & Security\n", err)
		return nil, nil, nil, nil
	}
	return audio.NewStreamCapturer(sw, audio.DefaultWindowSize, audio.WindowBuffer), sw, nil, nil
}

// openFixed opens and starts a fixed selection: "everything" or an
// app's decimal PID.
func openFixed(sourceHint string) (audio.Capturer, error) {
	var c audio.Capturer
	if sourceHint == "everything" {
		c = guestaudio.NewSystemCapturer()
	} else {
		pid, err := strconv.ParseInt(sourceHint, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid source selection %q", sourceHint)
		}
		if c, err = appCapturer(int32(pid)); err != nil {
			return nil, fmt.Errorf("no window found for the selected app (PID %d): %w", pid, err)
		}
	}
	// Started here to find out now whether capture works; Start is
	// idempotent, so the switcher starting it again changes nothing.
	if err := c.Start(); err != nil {
		c.Close()
		return nil, fmt.Errorf("capture unavailable: %w", err)
	}
	return c, nil
}

// Switcher is the switcher an Auto capture moves.
func (a *Auto) Switcher() *audio.SwitchCapturer { return a.sw }

// SwitchTo moves sw to a fixed selection ("everything" or a PID). The
// previous source keeps capturing if the new one can't be opened.
func SwitchTo(sw *audio.SwitchCapturer, sourceHint string) error {
	c, err := openFixed(sourceHint)
	if err != nil {
		return err
	}
	if err := sw.Switch(c); err != nil {
		c.Close()
		return err
	}
	return nil
}

// AutoOn turns sw into an AutoSource capture again: the meeting app if
// one is making sound now, else the whole system (TryMeetingApp moves it
// to the app later).
func AutoOn(sw *audio.SwitchCapturer) (*Auto, error) {
	a := &Auto{sw: sw, current: "Everything"}
	var next audio.Capturer
	if src, ok := activeMeetingApp(); ok {
		if c, err := src.capturer(); err == nil {
			next, a.current, a.onApp = c, src.label, true
		}
	}
	if next == nil {
		next = guestaudio.NewSystemCapturer()
	}
	if err := sw.Switch(next); err != nil {
		next.Close()
		return nil, err
	}
	a.cur, a.since = next, time.Now()
	return a, nil
}

// SourceName is a selection's name for people: "Everything", an app's
// name for its PID, "Meeting app (automatic)" or "No system audio".
func SourceName(sourceHint string) string {
	switch sourceHint {
	case AutoSource, "":
		return "Meeting app (automatic)"
	case NoSource:
		return "No system audio"
	case "everything":
		return "Everything"
	}
	if pid, err := strconv.Atoi(sourceHint); err == nil {
		if active, err := audiosources.ListActive(); err == nil {
			for _, s := range active {
				if s.PID == pid {
					return s.Name
				}
			}
		}
		return "PID " + sourceHint
	}
	return sourceHint
}

// ProbeSystem listens to the whole system's audio for d, on a capture of
// its own beside any running one, and reports whether anything above
// silence played.
func ProbeSystem(d time.Duration) (bool, error) {
	c := guestaudio.NewSystemCapturer()
	defer c.Close()
	if err := c.Start(); err != nil {
		return false, err
	}
	time.Sleep(d)
	samples := c.Samples()
	_ = c.Stop()
	return audio.HasSignal(samples), nil
}
