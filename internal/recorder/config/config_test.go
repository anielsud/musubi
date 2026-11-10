package config

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// writeConfig puts text in a config.toml under a fresh temp dir.
func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := DefaultConfig()
	tr, m := cfg.Transcription, cfg.Meeting
	checks := []struct {
		name      string
		got, want any
	}{
		{"audio.device", cfg.Audio.Device, "default"},
		{"transcription.decoding_method", tr.DecodingMethod, "greedy_search"},
		{"transcription.max_active_paths", tr.MaxActivePaths, 4},
		{"transcription.model", tr.Model, "auto"},
		{"transcription.live_model", tr.LiveModel, "auto"},
		{"transcription.two_pass", tr.TwoPass, false},
		{"meeting.default_sources", m.DefaultSources, "both"},
		{"meeting.speaker_threshold", m.SpeakerThreshold, 0.65},
		{"meeting.auto_save", m.AutoSave, true},
		{"meeting.auto_detect", m.AutoDetect, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if tr.ModelPath != ModelDir() {
		t.Errorf("transcription.model_path = %q, want ModelDir() %q", tr.ModelPath, ModelDir())
	}
}

// The recorder reads Tomoe's own config.toml while the two run side by
// side, so tables and keys it dropped must load without complaint.
func TestLoadIgnoresTomoeOnlyKeys(t *testing.T) {
	path := writeConfig(t, `[hotkey]
binding = "Super+Shift+S"
meeting_binding = "Super+Shift+X"

[audio]
device = "USB mic"

[transcription]
model = "parakeet-v3"
gpu_enabled = true
model_path = "/elsewhere/models"
hotwords_file = "/tmp/words.txt"
hotwords_score = 2.0
two_pass = true

[output]
auto_paste = false
clipboard = true
silence_timeout = 3.0

[meeting]
turn_max_seconds = 15

[multilingual]
enabled = true
languages = ["en", "bn"]
default_lang = "en"
`)
	cfg := mustLoad(t, path)
	if cfg.Audio.Device != "USB mic" || cfg.Transcription.ModelPath != "/elsewhere/models" ||
		cfg.Transcription.Model != "parakeet-v3" || !cfg.Transcription.TwoPass || cfg.Meeting.TurnMaxSeconds != 15 {
		t.Errorf("known keys not applied: %+v %+v", cfg.Audio, cfg.Transcription)
	}
	// Untouched keys keep their defaults rather than going to zero.
	if cfg.Meeting.SpeakerThreshold != 0.65 || cfg.Transcription.DecodingMethod != "greedy_search" {
		t.Errorf("defaults lost: speaker_threshold %v, decoding_method %q", cfg.Meeting.SpeakerThreshold, cfg.Transcription.DecodingMethod)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Error("a missing file loaded without error")
	}
	if _, err := Load(writeConfig(t, "[meeting\nauto_save = ")); err == nil {
		t.Error("malformed TOML loaded without error")
	}
}

func TestPathsFollowXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xc")
	t.Setenv("XDG_DATA_HOME", "/xd")
	for got, want := range map[string]string{
		Path():       "/xc/tomoe/config.toml",
		DataDir():    "/xd/tomoe",
		ModelDir():   "/xd/tomoe/models",
		SessionDir(): "/xd/tomoe/sessions",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestPathsFallBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/h")
	if got := Path(); got != "/h/.config/tomoe/config.toml" {
		t.Errorf("Path() = %q", got)
	}
	if got := ModelDir(); got != "/h/.local/share/tomoe/models" {
		t.Errorf("ModelDir() = %q", got)
	}
}

func TestExists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Exists() {
		t.Fatal("Exists() before any file was written")
	}
	if err := writeTestConfig(DefaultConfig(), Path()); err != nil {
		t.Fatal(err)
	}
	if !Exists() {
		t.Error("Exists() false after Save to Path()")
	}
}

func TestHintAnalysisDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	if got, want := HintAnalysisDir(), "/custom/data/tomoe/hint-analysis"; got != want {
		t.Errorf("HintAnalysisDir() = %q, want %q", got, want)
	}
}

func TestNewPipelineOptionsDefaultOffForExistingConfigs(t *testing.T) {
	// A config written before two-pass and the clustering rules existed
	// keeps main's behavior: every new option defaults to off.
	cfg := mustLoad(t, writeConfig(t, "[transcription]\ngpu_enabled = true\n\n[meeting]\nspeaker_threshold = 0.65\n"))
	if cfg.Transcription.TwoPass {
		t.Error("Transcription.TwoPass = true, want false (default)")
	}
	if cfg.Meeting.StickyThresholdMargin != 0 {
		t.Errorf("Meeting.StickyThresholdMargin = %v, want 0 (rule off by default)", cfg.Meeting.StickyThresholdMargin)
	}
	if cfg.Meeting.MinAssignDuration != 0 {
		t.Errorf("Meeting.MinAssignDuration = %v, want 0 (rule off by default)", cfg.Meeting.MinAssignDuration)
	}
}

func TestNewPipelineOptionsOptIn(t *testing.T) {
	cfgText := "[transcription]\ntwo_pass = true\n\n[meeting]\nsticky_threshold_margin = 0.15\nmin_assign_duration = 0.7\n"
	cfg := mustLoad(t, writeConfig(t, cfgText))
	if !cfg.Transcription.TwoPass {
		t.Error("Transcription.TwoPass = false, want true")
	}
	if cfg.Meeting.StickyThresholdMargin != 0.15 || cfg.Meeting.MinAssignDuration != 0.7 {
		t.Errorf("rules = margin %v, duration %v; want 0.15, 0.7", cfg.Meeting.StickyThresholdMargin, cfg.Meeting.MinAssignDuration)
	}
}

func TestWatch_DetectsChangeAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := DefaultConfig()
	cfg.Meeting.SpeakerThreshold = 0.55
	if err := writeTestConfig(cfg, path); err != nil {
		t.Fatalf("writeTestConfig() error: %v", err)
	}

	got := make(chan *Config, 1)
	stop := Watch(path, 20*time.Millisecond, func(c *Config) {
		got <- c
	})
	defer stop()

	// Must not fire for the file's state as of when Watch started.
	select {
	case <-got:
		t.Fatal("onChange fired before any real change was made")
	case <-time.After(100 * time.Millisecond):
	}

	// Sleep first so the rewritten file gets a strictly later mtime
	// even on filesystems with coarse mtime resolution.
	time.Sleep(20 * time.Millisecond)
	cfg.Meeting.SpeakerThreshold = 0.60
	if err := writeTestConfig(cfg, path); err != nil {
		t.Fatalf("writeTestConfig() error: %v", err)
	}

	select {
	case newCfg := <-got:
		if newCfg.Meeting.SpeakerThreshold != 0.60 {
			t.Errorf("reloaded SpeakerThreshold = %v, want 0.60", newCfg.Meeting.SpeakerThreshold)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onChange never fired after a real file change")
	}
}

func TestWatch_StopStopsFurtherReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := DefaultConfig()
	if err := writeTestConfig(cfg, path); err != nil {
		t.Fatalf("writeTestConfig() error: %v", err)
	}

	var count int32
	stop := Watch(path, 10*time.Millisecond, func(c *Config) {
		atomic.AddInt32(&count, 1)
	})
	stop()

	time.Sleep(20 * time.Millisecond)
	cfg.Meeting.SpeakerThreshold = 0.9
	if err := writeTestConfig(cfg, path); err != nil {
		t.Fatalf("writeTestConfig() error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("onChange fired %d times after stop(), want 0", got)
	}
}

func TestWatch_MissingFileNeverFires(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")

	var count int32
	stop := Watch(path, 10*time.Millisecond, func(c *Config) {
		atomic.AddInt32(&count, 1)
	})
	defer stop()

	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("onChange fired %d times for a nonexistent file, want 0", got)
	}
}

func TestMeetingConfig_VideoHintTiming(t *testing.T) {
	m := MeetingConfig{VideoHintLearnInterval: 0.5, VideoHintCheckInterval: 2}
	learn, check := m.VideoHintTiming()
	if learn != 500*time.Millisecond || check != 2*time.Second {
		t.Errorf("timing = %v, %v; want 500ms, 2s", learn, check)
	}
}

func TestMeetingConfig_VideoHintTiming_FallsBackOnZero(t *testing.T) {
	var m MeetingConfig // zero value
	learn, check := m.VideoHintTiming()
	def := DefaultConfig().Meeting
	if want := time.Duration(def.VideoHintLearnInterval * float64(time.Second)); learn != want {
		t.Errorf("learn = %v, want default %v", learn, want)
	}
	if want := time.Duration(def.VideoHintCheckInterval * float64(time.Second)); check != want {
		t.Errorf("check = %v, want default %v", check, want)
	}
}

func TestMeetingConfig_VideoHintTiming_RecordForTuningLooksDensely(t *testing.T) {
	m := MeetingConfig{VideoHintLearnInterval: 1, VideoHintCheckInterval: 1, RecordForTuning: true}
	if learn, _ := m.VideoHintTiming(); learn != 350*time.Millisecond {
		t.Errorf("learn while recording for tuning = %v, want 350ms", learn)
	}
	m.VideoHintLearnInterval = 0.2 // faster than the tuning rate: kept
	if learn, _ := m.VideoHintTiming(); learn != 200*time.Millisecond {
		t.Errorf("learn = %v, want 200ms", learn)
	}
}

// The live feed is opt-in: configs without the key (every existing one)
// keep it off, and it round-trips once turned on.
func TestLiveFeedOffUnlessSet(t *testing.T) {
	if DefaultConfig().Meeting.LiveFeed {
		t.Error("DefaultConfig().Meeting.LiveFeed = true, want false")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[meeting]\nspeaker_threshold = 0.65\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Meeting.LiveFeed {
		t.Error("LiveFeed = true for a config without live_feed, want false")
	}

	cfg.Meeting.LiveFeed = true
	saved := filepath.Join(dir, "saved.toml")
	if err := writeTestConfig(cfg, saved); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(saved)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !reloaded.Meeting.LiveFeed {
		t.Error("LiveFeed didn't survive Save and Load")
	}
}

// writeTestConfig writes cfg as TOML. The recorder has no Save of its own:
// while Tomoe is the recorder of record, nothing may write Tomoe's config.
func writeTestConfig(cfg *Config, path string) error {
	b, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
