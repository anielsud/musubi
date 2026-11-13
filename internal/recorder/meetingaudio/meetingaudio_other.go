//go:build !darwin

package meetingaudio

import "github.com/anielsud/musubi/internal/recorder/audio"

// System audio is captured on macOS only; elsewhere a recording hears the
// microphone alone. These keep the callers building there.

// NewMonitorSource has no system audio to offer here: (nil, nil).
func NewMonitorSource(string) (*audio.StreamCapturer, error) { return nil, nil }

// Auto never exists off macOS; its methods are for the callers' sake.
type Auto struct{}

// Recover never moves anything here.
func (*Auto) Recover() (string, bool) { return "", false }

// Current is "": nothing is captured.
func (*Auto) Current() string { return "" }

// TryMeetingApp never finds an app to move to here.
func (*Auto) TryMeetingApp() (string, bool) { return "", false }

// NewAutoMonitorSource has no system audio to offer here: (nil, nil, nil).
func NewAutoMonitorSource() (*audio.StreamCapturer, *Auto, error) { return nil, nil, nil }
