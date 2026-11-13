package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const tomoeFile = `
[audio]
device = "Desk Mic"

[transcription]
two_pass = true
decoding_method = "modified_beam_search"

[meeting]
monitor_device = "everything"
speaker_threshold = 0.6
mic_voices = false
`

// A key in [recorder.*] wins; every key it leaves out keeps Tomoe's value
// (or the default where Tomoe's file has none), and the app's own
// [recorder] keys are ignored.
func TestLoadRecorderOverlaysKeyByKey(t *testing.T) {
	tomoe := writeConfig(t, tomoeFile)
	musubi := writeConfig(t, `
[live]
tomoe_socket = "/elsewhere"

[recorder]
of_record = "musubi"
data = "/somewhere"
since = 2026-11-11T00:00:00Z

[recorder.audio]
device = "Headset"

[recorder.meeting]
speaker_threshold = 0.7
source_check = false
`)
	cfg, err := LoadRecorder(tomoe, musubi)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"audio.device (Musubi)", cfg.Audio.Device, "Headset"},
		{"meeting.speaker_threshold (Musubi)", cfg.Meeting.SpeakerThreshold, 0.7},
		{"meeting.source_check (Musubi)", cfg.Meeting.SourceCheck, false},
		{"meeting.monitor_device (Tomoe)", cfg.Meeting.MonitorDevice, "everything"},
		{"meeting.mic_voices (Tomoe)", cfg.Meeting.MicVoices, false},
		{"transcription.two_pass (Tomoe)", cfg.Transcription.TwoPass, true},
		{"transcription.decoding_method (Tomoe)", cfg.Transcription.DecodingMethod, "modified_beam_search"},
		{"meeting.turn_max_gap (default)", cfg.Meeting.TurnMaxGap, DefaultConfig().Meeting.TurnMaxGap},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// Without [recorder] tables, or without Musubi's file at all, the config
// is exactly Tomoe's.
func TestLoadRecorderWithoutRecorderTablesIsTomoes(t *testing.T) {
	tomoe := writeConfig(t, tomoeFile)
	want := mustLoad(t, tomoe)
	for name, musubi := range map[string]string{
		"no file":         filepath.Join(t.TempDir(), "missing.toml"),
		"no tables":       writeConfig(t, "[live]\ntomoe_socket = \"/x\"\n"),
		"only [recorder]": writeConfig(t, "[recorder]\nof_record = \"tomoe\"\n"),
	} {
		got, err := LoadRecorder(tomoe, musubi)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: config differs from Tomoe's:\n got %+v\nwant %+v", name, got, want)
		}
	}
}

// No Tomoe file: defaults, with Musubi's keys over them.
func TestLoadRecorderWithoutTomoeFile(t *testing.T) {
	musubi := writeConfig(t, "[recorder.transcription]\ntwo_pass = true\n")
	cfg, err := LoadRecorder(filepath.Join(t.TempDir(), "none.toml"), musubi)
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultConfig()
	want.Transcription.TwoPass = true
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want defaults with two_pass", cfg)
	}
}

// A wrong type in Musubi's tables is an error and changes nothing.
func TestOverlayMusubiTypeErrorLeavesConfig(t *testing.T) {
	cfg := DefaultConfig()
	musubi := writeConfig(t, "[recorder.meeting]\nspeaker_threshold = \"high\"\n")
	if _, err := OverlayMusubi(cfg, musubi); err == nil || !strings.Contains(err.Error(), "recorder") {
		t.Fatalf("err = %v, want a [recorder] error", err)
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Error("config changed by a failed overlay")
	}
}

func TestMusubiPathFromEnv(t *testing.T) {
	t.Setenv("MUSUBI_CONFIG", "/tmp/x/config.toml")
	if got := MusubiPath(); got != "/tmp/x/config.toml" {
		t.Errorf("MusubiPath = %q", got)
	}
}

// [recorder] of_record = "musubi" is read from Musubi's file, with or
// without [recorder.*] tables; "tomoe" or no key is the default.
func TestOverlayMusubiOfRecord(t *testing.T) {
	for text, want := range map[string]bool{
		"[recorder]\nof_record = \"musubi\"\n":                                   true,
		"[recorder]\nof_record = \"musubi\"\n[recorder.audio]\ndevice = \"X\"\n": true,
		"[recorder]\nof_record = \"tomoe\"\n":                                    false,
		"[recorder.audio]\ndevice = \"X\"\n":                                     false,
	} {
		cfg := DefaultConfig()
		if _, err := OverlayMusubi(cfg, writeConfig(t, text)); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if cfg.MusubiOfRecord != want {
			t.Errorf("%q: MusubiOfRecord = %v, want %v", text, cfg.MusubiOfRecord, want)
		}
	}
	cfg := DefaultConfig()
	if _, err := OverlayMusubi(cfg, writeConfig(t, "[recorder]\nof_record = \"both\"\n")); err == nil {
		t.Error("an unknown of_record was accepted")
	}
}

// With [recorder] tomoe = "off" Tomoe's file isn't read at all: the
// defaults, with models in the recorder's own folder, and Musubi's keys.
func TestLoadRecorderTomoeOffIgnoresTomoesFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defer SetDataDir("")
	rec := t.TempDir()
	SetDataDir(rec)
	tomoe := writeConfig(t, tomoeFile+"\n[transcription.broken\n") // would not even parse
	musubi := writeConfig(t, `
[recorder]
tomoe = "off"

[recorder.meeting]
speaker_threshold = 0.7
`)
	cfg, err := LoadRecorder(tomoe, musubi)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TomoeOff || cfg.Audio.Device != "default" || cfg.Meeting.MonitorDevice != "" || cfg.Meeting.SpeakerThreshold != 0.7 {
		t.Errorf("TomoeOff %v device %q monitor %q threshold %v", cfg.TomoeOff, cfg.Audio.Device, cfg.Meeting.MonitorDevice, cfg.Meeting.SpeakerThreshold)
	}
	if want := filepath.Join(rec, "models"); cfg.Transcription.ModelPath != want {
		t.Errorf("model_path %q, want %q", cfg.Transcription.ModelPath, want)
	}
	if dirs := ModelDirs(cfg); len(dirs) != 1 || dirs[0] != filepath.Join(rec, "models") {
		t.Errorf("ModelDirs %v, want only the recorder's", dirs)
	}
	for _, bad := range []string{`tomoe = "maybe"`, `tomoe = true`} {
		if _, err := LoadRecorder(tomoe, writeConfig(t, "[recorder]\n"+bad+"\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if off, err := MusubiTomoeOff(filepath.Join(t.TempDir(), "none.toml")); off || err != nil {
		t.Errorf("missing file: off %v err %v", off, err)
	}
}

// With Tomoe off, Tomoe's models folder is never searched, even when
// model_path names it or no Config is at hand.
func TestModelDirsTomoeOff(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defer SetDataDir("")
	defer SetTomoeOff(false)
	rec := t.TempDir()
	SetDataDir(rec)
	cfg := DefaultConfig() // model_path: Tomoe's folder
	cfg.TomoeOff = true
	own := filepath.Join(rec, "models")
	if dirs := ModelDirs(cfg); len(dirs) != 1 || dirs[0] != own {
		t.Errorf("ModelDirs(cfg) = %v", dirs)
	}
	cfg.Transcription.ModelPath = filepath.Join(ModelDir(), "sub")
	if dirs := ModelDirs(cfg); len(dirs) != 1 {
		t.Errorf("a folder inside Tomoe's was kept: %v", dirs)
	}
	cfg.Transcription.ModelPath = "/elsewhere"
	if dirs := ModelDirs(cfg); len(dirs) != 2 || dirs[1] != "/elsewhere" {
		t.Errorf("ModelDirs = %v", dirs)
	}
	SetTomoeOff(true)
	if dirs := ModelDirs(nil); len(dirs) != 1 || dirs[0] != own {
		t.Errorf("ModelDirs(nil) = %v", dirs)
	}
	SetTomoeOff(false)
	if dirs := ModelDirs(nil); len(dirs) != 2 || dirs[1] != ModelDir() {
		t.Errorf("Tomoe on: ModelDirs(nil) = %v", dirs)
	}
}

// FileValues lists the recorder keys Tomoe's file sets, typed as Config
// reads them, and nothing else.
func TestFileValues(t *testing.T) {
	path := writeConfig(t, tomoeFile+`
[hotkey]
key = "F5"
`+"\n")
	// An unknown key in a known table is left out.
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), "[meeting]\n", "[meeting]\nnot_a_key = 3\n", 1)), 0o600)
	got, err := FileValues(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]any{
		"audio":         {"device": "Desk Mic"},
		"transcription": {"two_pass": true, "decoding_method": "modified_beam_search"},
		"meeting":       {"monitor_device": "everything", "speaker_threshold": 0.6, "mic_voices": false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FileValues = %v, want %v", got, want)
	}
	if v, ok := Field(DefaultConfig(), "meeting", "max_speech_duration"); !ok || v != 30.0 {
		t.Errorf("Field = %v, %v", v, ok)
	}
	for _, k := range [][2]string{{"meeting", "nope"}, {"nope", "device"}, {"audio", ""}} {
		if _, ok := Field(DefaultConfig(), k[0], k[1]); ok {
			t.Errorf("Field(%v) found", k)
		}
	}
}

// The owner's name comes from Musubi's [owner] table.
func TestOverlayMusubiOwner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("[owner]\nname = \"Ilse Novak\"\n"), 0o600)
	cfg := DefaultConfig()
	if _, err := OverlayMusubi(cfg, p); err != nil {
		t.Fatal(err)
	}
	if cfg.OwnerName != "Ilse Novak" {
		t.Errorf("OwnerName = %q", cfg.OwnerName)
	}
}
