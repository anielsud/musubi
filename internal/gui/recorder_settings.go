package gui

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/config"
	recconfig "github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// Settings → Recording: which recorder is of record, and the recorder's
// settings carried over from Tomoe's window (hotkey, dictation, languages,
// GPU and hotwords stay behind). Each is written to Musubi's config under
// [recorder.<table>], which wins over Tomoe's config for the recorder; a
// key not set there shows, and keeps, Tomoe's value.

// RecField is one recorder setting. Type "bool", "int", "float",
// "string" or "select" (one of Options).
type RecField struct {
	Table   string      `json:"table"` // "audio", "transcription" or "meeting"
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Help    string      `json:"help,omitempty"`
	Type    string      `json:"type"`
	Step    float64     `json:"step,omitempty"`
	Min     float64     `json:"min"`
	Options []RecOption `json:"options,omitempty"`
	Value   any         `json:"value"`
	// Set: Musubi's config sets it; otherwise Value is Tomoe's.
	Set bool `json:"set"`
	// Restart: the recorder reads it only when it starts (its engines are
	// built with it), so a change waits for a restart.
	Restart bool `json:"restart,omitempty"`
}

type RecOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type RecGroup struct {
	Title  string     `json:"title"`
	Fields []RecField `json:"fields"`
}

var recGroups = []RecGroup{
	{"Sources", []RecField{
		{Table: "audio", Key: "device", Label: "Microphone", Type: "string", Help: "The microphone a recording starts with; \"default\" is the system's."},
		{Table: "meeting", Key: "monitor_device", Label: "Meeting audio", Type: "string", Help: "The second source a recording starts with: \"everything\" (all system audio) or \"none\" (microphone only)."},
		{Restart: true, Table: "meeting", Key: "auto_detect", Label: "Notice meetings by themselves", Type: "bool", Help: "Start recording when an app starts using the microphone and speaker together."},
	}},
	{"Meeting window", []RecField{
		{Table: "meeting", Key: "video_hint_window", Label: "Watch", Type: "string", Help: "Which window to read the active speaker from: empty for the Teams or Zoom meeting window, \"none\" for off, or an app's name."},
		{Table: "meeting", Key: "video_hint_learn_interval", Label: "Look while learning", Type: "float", Step: 0.05, Min: 0.1, Help: "Seconds between looks while someone speaking has no name yet, or right after a speaker change."},
		{Table: "meeting", Key: "video_hint_check_interval", Label: "Look while checking", Type: "float", Step: 0.25, Min: 0.25, Help: "Seconds between looks when everyone speaking already has a name."},
	}},
	{"Voices", []RecField{
		{Restart: true, Table: "meeting", Key: "mic_voices", Label: "Other voices on your microphone", Type: "bool", Help: "Tell your voice from others' on your own microphone (someone beside you, or an in-person meeting). Off: everything on the microphone is you."},
		{Table: "meeting", Key: "voice_profiles", Label: "Voice profiles", Type: "bool", Help: "Learn each person's voice from meetings where their own tile was lit or you named them (fingerprints only, on this Mac), and name voices the meeting window can't."},
		{Restart: true, Table: "meeting", Key: "speaker_model", Label: "Speaker voice model", Type: "select", Help: "Tells voices apart, live and after the meeting.", Options: []RecOption{
			{"auto", "Automatic (English-trained for English)"}, {"eres2net-en", "ERes2Net, English-trained"}, {"eres2net-base", "ERes2Net base"}}},
	}},
	{"Transcription", []RecField{
		{Restart: true, Table: "transcription", Key: "model", Label: "Transcription model", Type: "select", Help: "Automatic uses Cohere Transcribe for English, the most accurate there; Parakeet is about 3x faster.", Options: []RecOption{
			{"auto", "Automatic"}, {"cohere-transcribe", "Cohere Transcribe"}, {"parakeet-v2-en", "Parakeet v2, English only"}, {"parakeet-v3", "Parakeet v3"}}},
		{Restart: true, Table: "transcription", Key: "two_pass", Label: "Live text while people speak", Type: "bool", Help: "Shows English meetings' text as it is spoken, refined when each utterance ends."},
		{Restart: true, Table: "transcription", Key: "decoding_method", Label: "Decoding", Type: "select", Options: []RecOption{
			{"greedy_search", "Greedy (fastest)"}, {"modified_beam_search", "Beam search"}}},
		{Restart: true, Table: "transcription", Key: "max_active_paths", Label: "Beam width", Type: "int", Min: 1, Help: "With beam search."},
	}},
	{"Meeting speakers", []RecField{
		{Table: "meeting", Key: "speaker_threshold", Label: "Speaker match threshold", Type: "float", Step: 0.01, Help: "Higher splits voices into more speakers (0–1)."},
		{Table: "meeting", Key: "sticky_threshold_margin", Label: "Sticky-speaker margin", Type: "float", Step: 0.01, Help: "Near-misses joining the last speaker; 0 is off."},
		{Table: "meeting", Key: "sticky_grace_window", Label: "Sticky-speaker window", Type: "float", Step: 0.5, Help: "Seconds."},
		{Table: "meeting", Key: "min_assign_duration", Label: "Short-segment length", Type: "float", Step: 0.1, Help: "Seconds; shorter replies join the last speaker. 0 is off."},
		{Table: "meeting", Key: "short_segment_grace_window", Label: "Short-segment window", Type: "float", Step: 1, Help: "Seconds."},
	}},
	{"After the meeting", []RecField{
		{Table: "meeting", Key: "split_on_speaker_change", Label: "Split lines at speaker changes", Type: "bool", Help: "When the pass after the meeting hears a different voice mid-line, give it its own line (experimental)."},
		{Table: "meeting", Key: "diarize_during_meeting", Label: "Work out speakers during the meeting", Type: "bool", Help: "Labels improve as the meeting goes and final ones are ready when it ends; uses more CPU during the call."},
		{Table: "meeting", Key: "diarize_stride", Label: "Fingerprint every Nth window", Type: "int", Min: 1, Help: "CPU use during the call: higher uses less; 2 is about as accurate as 1."},
		{Table: "meeting", Key: "diarize_recluster", Label: "Recluster every", Type: "float", Step: 5, Min: 5, Help: "Seconds between updates of the labels during the meeting."},
		{Table: "meeting", Key: "diarize_room_split", Label: "Split meeting rooms into their people", Type: "bool", Help: "Keep a meeting room's audio apart and tell its voices apart more finely, using the meeting window."},
		{Table: "meeting", Key: "refine_speaker_cuts", Label: "Refine speaker cuts", Type: "bool", Help: "After each meeting, move each cut between speakers to where the voice changes (about 2 minutes of one core per meeting hour)."},
		{Table: "meeting", Key: "record_for_tuning", Label: "Record for tuning", Type: "bool", Help: "Full detail for working out the best settings afterwards (about 250 MB an hour, more CPU); turn it off after."},
	}},
}

// RecordingSettings is Settings → Recording: the recorder of record and
// the recorder's settings with their current values.
type RecordingSettings struct {
	OfRecord string     `json:"ofRecord"`
	Since    time.Time  `json:"since"`
	Groups   []RecGroup `json:"groups"`
	// TomoeConfig: where unset values come from, or why they are defaults.
	TomoeConfig string `json:"tomoeConfig"`
}

func (a *App) RecordingSettings() (*RecordingSettings, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("no dataset open")
	}
	tc, note := tomoeConfig()
	return &RecordingSettings{
		OfRecord:    a.eng.Cfg.Recorder.OfRecord,
		Since:       a.eng.Cfg.Recorder.Since,
		Groups:      recordingGroups(a.eng.Cfg.Recorder, tc),
		TomoeConfig: note,
	}, nil
}

// tomoeConfig is Tomoe's config as the recorder reads it, or its
// defaults when there is none.
func tomoeConfig() (*recconfig.Config, string) {
	p := recconfig.Path()
	c, err := recconfig.Load(p)
	if err != nil {
		return recconfig.DefaultConfig(), "Tomoe's defaults (no readable " + p + ")"
	}
	return c, p
}

func recordingGroups(r config.Recorder, tc *recconfig.Config) []RecGroup {
	out := make([]RecGroup, 0, len(recGroups))
	for _, g := range recGroups {
		ng := RecGroup{Title: g.Title}
		for _, f := range g.Fields {
			if v, ok := overrides(r, f.Table)[f.Key]; ok {
				f.Value, f.Set = v, true
			} else {
				f.Value = tomlField(tc, f.Table, f.Key)
			}
			ng.Fields = append(ng.Fields, f)
		}
		out = append(out, ng)
	}
	return out
}

func overrides(r config.Recorder, table string) map[string]any {
	switch table {
	case "audio":
		return r.Audio
	case "transcription":
		return r.Transcription
	case "meeting":
		return r.Meeting
	}
	return nil
}

// tomlField reads table.key from the recorder's config by TOML name.
func tomlField(c *recconfig.Config, table, key string) any {
	v := reflect.ValueOf(c).Elem()
	for _, part := range []string{table, key} {
		found := false
		for i := 0; i < v.NumField(); i++ {
			if strings.Split(v.Type().Field(i).Tag.Get("toml"), ",")[0] == part {
				v, found = v.Field(i), true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return v.Interface()
}

func findRecField(table, key string) *RecField {
	for _, g := range recGroups {
		for i := range g.Fields {
			if g.Fields[i].Table == table && g.Fields[i].Key == key {
				return &g.Fields[i]
			}
		}
	}
	return nil
}

// recValue checks a value from the frontend against the field's type.
func recValue(f *RecField, value any) (any, error) {
	switch f.Type {
	case "bool":
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: on or off", f.Label)
		}
		return b, nil
	case "int":
		x, ok := value.(float64) // JSON numbers
		if !ok || x != math.Trunc(x) || x < f.Min || x > 1e6 {
			return nil, fmt.Errorf("%s: a whole number, %g or more", f.Label, f.Min)
		}
		return int(x), nil
	case "float":
		x, ok := value.(float64)
		if !ok || math.IsNaN(x) || math.IsInf(x, 0) || x < f.Min || x > 1e6 {
			return nil, fmt.Errorf("%s: a number, %g or more", f.Label, f.Min)
		}
		return x, nil
	case "select":
		s, _ := value.(string)
		for _, o := range f.Options {
			if o.Value == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%s: not one of the choices", f.Label)
	default:
		s, ok := value.(string)
		if !ok || strings.ContainsAny(s, "\n\r") || len(s) > 300 {
			return nil, fmt.Errorf("%s: one line of text", f.Label)
		}
		return strings.TrimSpace(s), nil
	}
}

// SetRecordingSetting writes one recorder setting to Musubi's config and
// asks the recorder to read it again. Saved even when the recorder isn't
// running: it reads the config when it starts.
func (a *App) SetRecordingSetting(table, key string, value any) error {
	if a.eng == nil {
		return fmt.Errorf("no dataset open")
	}
	f := findRecField(table, key)
	if f == nil {
		return fmt.Errorf("%s.%s isn't a recorder setting here", table, key)
	}
	v, err := recValue(f, value)
	if err != nil {
		return err
	}
	if err := config.EditFile(a.cfgPath, []config.Edit{{Section: "recorder." + table, Key: key, Value: v}}); err != nil {
		return err
	}
	return a.afterRecorderEdit()
}

// SetOfRecord switches the recorder of record. Switching to Musubi's
// records when (since), so its earlier shadow recordings aren't imported
// beside Tomoe's. The dataset reopens: ingest and Live change folders.
func (a *App) SetOfRecord(v string) error {
	if a.eng == nil {
		return fmt.Errorf("no dataset open")
	}
	if v != "tomoe" && v != "musubi" {
		return fmt.Errorf("the recorder of record is tomoe or musubi")
	}
	if v == a.eng.Cfg.Recorder.OfRecord {
		return nil
	}
	if err := config.EditFile(a.cfgPath, ofRecordEdits(v, time.Now())); err != nil {
		return err
	}
	a.reloadRecorder()
	return a.reopen("prefs:changed")
}

func ofRecordEdits(v string, now time.Time) []config.Edit {
	edits := []config.Edit{{Section: "recorder", Key: "of_record", Value: v}}
	if v == "musubi" {
		edits = append(edits, config.Edit{Section: "recorder", Key: "since", Value: now.Truncate(time.Second)})
	}
	return edits
}

// afterRecorderEdit takes the edited [recorder] tables into the open
// config without reopening the dataset (Musubi itself doesn't use them)
// and tells the recorder.
func (a *App) afterRecorderEdit() error {
	c, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	a.eng.Cfg.Recorder = c.Recorder
	a.emit("recording:changed", nil)
	return a.reloadRecorder()
}

// reloadRecorder asks the recorder to read Musubi's [recorder] settings
// again; not running is fine (it reads them when it starts).
func (a *App) reloadRecorder() error {
	err := a.recCall(control.OpReloadConfig, nil, nil)
	if errors.Is(err, control.ErrNotRunning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("saved, but the recorder couldn't take it in: %w", err)
	}
	return nil
}
