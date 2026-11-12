//go:build !darwin

package meetingaudio

import (
	"errors"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
)

var errNoSystemAudio = errors.New("system audio is captured on macOS only")

// NewSwitchableMonitorSource has no system audio to offer here.
func NewSwitchableMonitorSource(string) (*audio.StreamCapturer, *audio.SwitchCapturer, *Auto, error) {
	return nil, nil, nil, nil
}

// Switcher is nil here: there is no capture.
func (*Auto) Switcher() *audio.SwitchCapturer { return nil }

// SwitchTo can't switch anything here.
func SwitchTo(*audio.SwitchCapturer, string) error { return errNoSystemAudio }

// AutoOn can't capture anything here.
func AutoOn(*audio.SwitchCapturer) (*Auto, error) { return nil, errNoSystemAudio }

// SourceName names a selection for people.
func SourceName(sourceHint string) string {
	switch sourceHint {
	case AutoSource, "":
		return "Meeting app (automatic)"
	case NoSource:
		return "No system audio"
	case "everything":
		return "Everything"
	}
	return sourceHint
}

// ProbeSystem hears nothing here.
func ProbeSystem(time.Duration) (bool, error) { return false, errNoSystemAudio }
