package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

// With [recorder] tomoe = "off", Settings → Recording shows the recorder's
// built-in defaults for unset keys, never Tomoe's config.
func TestRecordingSettingsTomoeOff(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "tomoe"), 0o700)
	os.WriteFile(filepath.Join(xdg, "tomoe", "config.toml"), []byte("[audio]\ndevice = \"Tomoe Mic\"\n"), 0o600)
	c := config.DefaultConfig()
	device := func() any {
		tc, _ := baseRecorderConfig(c)
		for _, g := range recordingGroups(c.Recorder, tc) {
			for _, f := range g.Fields {
				if f.Table == "audio" && f.Key == "device" {
					return f.Value
				}
			}
		}
		return nil
	}
	if got := device(); got != "Tomoe Mic" || tomoeKey(c) != "on" {
		t.Errorf("tomoe on: device %v, key %s", got, tomoeKey(c))
	}
	c.Recorder.Tomoe = "off"
	if got := device(); got != "default" || tomoeKey(c) != "off" {
		t.Errorf("tomoe off: device %v, key %s", got, tomoeKey(c))
	}
}
