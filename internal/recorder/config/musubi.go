package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// The recorder's settings are moving into Musubi's config file, under
// [recorder] (docs/recorder-screens.md, "Settings"): a key set in
// [recorder.audio], [recorder.transcription] or [recorder.meeting] wins
// over the same key in Tomoe's section of that name, and anything left
// unset still comes from Tomoe's config until Tomoe is retired.
//
// Musubi's file is read here with a small TOML read of its own rather
// than through Musubi's config package, so the recorder never depends on
// the app's settings code (or its validation of keys it doesn't know).

// MusubiPath is Musubi's config file: $MUSUBI_CONFIG if set, else
// ~/.config/musubi/config.toml (the path Musubi itself uses).
func MusubiPath() string {
	if p := os.Getenv("MUSUBI_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".config", "musubi", "config.toml")
}

// recorderSections are the [recorder.*] tables that overlay Tomoe's
// sections of the same name. Of the top-level [recorder] keys only
// of_record is read here (Config.MusubiOfRecord); data and since are the
// Musubi app's.
var recorderSections = []string{"audio", "transcription", "meeting"}

// OverlayMusubi applies the [recorder.*] tables of the Musubi config at
// path over cfg, key by key. A missing file, or one with no such tables,
// leaves cfg as it was and reports false. On a parse or type error cfg
// is left untouched.
func OverlayMusubi(cfg *Config, path string) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(b, &doc); err != nil {
		return false, fmt.Errorf("parsing %s: %w", path, err)
	}
	rec, _ := doc["recorder"].(map[string]any)
	ofRecord := false
	switch v := rec["of_record"]; v {
	case nil, "tomoe":
	case "musubi":
		ofRecord = true
	default:
		return false, fmt.Errorf("%s [recorder] of_record = %v: want \"tomoe\" or \"musubi\"", path, v)
	}
	sub := map[string]any{}
	for _, name := range recorderSections {
		if t, ok := rec[name].(map[string]any); ok && len(t) > 0 {
			sub[name] = t
		}
	}
	if len(sub) == 0 {
		cfg.MusubiOfRecord = ofRecord
		return ofRecord, nil
	}
	// Re-encoding the tables and decoding them onto a copy sets exactly
	// the keys present and keeps every other field: go-toml leaves fields
	// with no key alone.
	enc, err := toml.Marshal(sub)
	if err != nil {
		return false, fmt.Errorf("%s [recorder]: %w", path, err)
	}
	next := *cfg
	next.MusubiOfRecord = ofRecord
	if err := toml.NewDecoder(bytes.NewReader(enc)).Decode(&next); err != nil {
		return false, fmt.Errorf("%s [recorder]: %w", path, err)
	}
	*cfg = next
	return true, nil
}

// LoadRecorder is the recorder's effective config: Tomoe's file at
// tomoePath (defaults if it doesn't exist) with Musubi's [recorder.*]
// tables at musubiPath over it. A Musubi file that doesn't parse is an
// error, so a typo there is reported rather than silently ignored.
func LoadRecorder(tomoePath, musubiPath string) (*Config, error) {
	cfg, err := Load(tomoePath)
	if errors.Is(err, fs.ErrNotExist) {
		cfg, err = DefaultConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := OverlayMusubi(cfg, musubiPath); err != nil {
		return nil, err
	}
	return cfg, nil
}
