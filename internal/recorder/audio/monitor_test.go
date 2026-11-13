package audio

import "testing"

// DeviceType values are stored in settings, so their numbering is fixed.
func TestDeviceTypeValuesAreStable(t *testing.T) {
	if Input != 0 || Monitor != 1 {
		t.Errorf("Input, Monitor = %d, %d; want 0, 1", Input, Monitor)
	}
	var zero DeviceInfo
	if zero.DeviceType != Input {
		t.Error("a DeviceInfo without a type should count as an input")
	}
}

func TestIsMonitorName(t *testing.T) {
	cases := map[string]bool{
		"alsa_output.usb-headset.analog-stereo.monitor": true,
		"Monitor of Built-in Audio Analog Stereo":       true,
		"MacBook Pro Microphone":                        false,
		"USB Monitor Speakers Mic":                      false,
	}
	for name, want := range cases {
		if got := isMonitorName(name); got != want {
			t.Errorf("isMonitorName(%q) = %v, want %v", name, got, want)
		}
	}
}
