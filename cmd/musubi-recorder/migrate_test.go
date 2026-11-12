package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/anielsud/musubi/internal/config"
)

// migrateFixture is a synthetic Tomoe (three sessions, voices, models and
// a config file), a recorder folder with one shadow session and its own
// voices, and a Musubi config that has switched to Musubi's recorder.
type migrateFixture struct {
	root, tomoe, tomoeConfig, data, musubi string
}

const (
	sinceText = "2026-11-12T10:00:00-05:00"
	musubiCfg = `# my settings
owner = { name = "Kai Moreno" }

[recorder]
of_record = "musubi"
since = ` + sinceText + `

[recorder.meeting]
speaker_threshold = 0.7 # tuned by hand
`
	tomoeCfg = `[audio]
device = "Desk Mic"

[transcription]
two_pass = true
model_path = "/old/tomoe/models"

[meeting]
speaker_threshold = 0.6
mic_voices = false
not_a_recorder_key = 3

[hotkey]
key = "F5"
`
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newMigrateFixture(t *testing.T) *migrateFixture {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("MUSUBI_CONFIG", "")
	f := &migrateFixture{
		root:        root,
		tomoe:       filepath.Join(root, "tomoe"),
		tomoeConfig: filepath.Join(root, "xdg-config", "tomoe", "config.toml"),
		data:        filepath.Join(root, "recorder"),
		musubi:      filepath.Join(root, "musubi.toml"),
	}
	for id, at := range map[string]string{
		"s1": "2026-11-10T09:00:00-05:00",
		"s2": "2026-11-11T09:00:00-05:00",
		"s3": "2026-11-12T11:00:00-05:00", // after since: Musubi's recorder has it
	} {
		dir := filepath.Join(f.tomoe, "sessions", id)
		write(t, filepath.Join(dir, "session.json"), fmt.Sprintf(`{"id":%q,"title":"Weekly","created_at":%q,"duration":60,"sources":["mic"],"segments":[],"audio_path":%q}`,
			id, at, filepath.Join(dir, "audio.m4a")))
		write(t, filepath.Join(dir, "audio.m4a"), "audio of "+id)
	}
	write(t, filepath.Join(f.tomoe, "voices", "eres2net-en.json"), `{"tomoe":"profiles"}`)
	write(t, filepath.Join(f.tomoe, "voices", "eres2net-en-you.json"), `{"tomoe":"owner"}`)
	write(t, filepath.Join(f.tomoe, "voices", "eres2net-en.json.bak-1"), `old`)
	write(t, filepath.Join(f.tomoe, "models", "silero_vad.onnx"), "vad")
	write(t, filepath.Join(f.tomoe, "models", "sherpa-parakeet", "tokens.txt"), "tokens")
	write(t, f.tomoeConfig, tomoeCfg)
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{"eres2net-en.json", "eres2net-en-you.json"} {
		os.Chtimes(filepath.Join(f.tomoe, "voices", p), past, past)
	}

	// The recorder's own: a shadow recording, an older profile store and
	// an owner voice learnt since the switch.
	write(t, filepath.Join(f.data, "sessions", "shadow", "session.json"), `{"id":"shadow","created_at":"2026-11-12T09:00:00-05:00"}`)
	write(t, filepath.Join(f.data, "voices", "eres2net-en.json"), `{"recorder":"old profiles"}`)
	older := past.Add(-24 * time.Hour)
	os.Chtimes(filepath.Join(f.data, "voices", "eres2net-en.json"), older, older)
	write(t, filepath.Join(f.data, "voices", "eres2net-en-you.json"), `{"recorder":"owner"}`)
	write(t, f.musubi, musubiCfg)
	return f
}

// treeSum is a checksum of every name, mode, time and byte under paths.
func treeSum(t *testing.T, paths ...string) string {
	t.Helper()
	return sumTree(t, true, paths...)
}

// filesSum is treeSum without folders' times, which a run's scratch
// folder (migrate-tmp) moves.
func filesSum(t *testing.T, paths ...string) string {
	t.Helper()
	return sumTree(t, false, paths...)
}

func sumTree(t *testing.T, dirTimes bool, paths ...string) string {
	t.Helper()
	h := sha256.New()
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			mtime := info.ModTime().UnixNano()
			if d.IsDir() && !dirTimes {
				mtime = 0
			}
			fmt.Fprintf(h, "%s %v %d\n", p, info.Mode(), mtime)
			if d.Type().IsRegular() {
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				h.Write(b)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (f *migrateFixture) run(t *testing.T, o migrateOptions) (string, error) {
	t.Helper()
	o.data, o.tomoe, o.musubiConfig = f.data, f.tomoe, f.musubi
	var out bytes.Buffer
	err := migrateFromTomoe(&out, o, time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC))
	return out.String(), err
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrateFromTomoe(t *testing.T) {
	f := newMigrateFixture(t)
	tomoeBefore := treeSum(t, f.tomoe, f.tomoeConfig)

	out, err := f.run(t, migrateOptions{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := treeSum(t, f.tomoe, f.tomoeConfig); got != tomoeBefore {
		t.Fatal("Tomoe's folder or config changed")
	}
	// Sessions before since, with audio_path pointed at the copy; never
	// the one after it; the shadow session untouched.
	for _, id := range []string{"s1", "s2"} {
		dir := filepath.Join(f.data, "sessions", id)
		h, err := readSessionHeader(dir)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if h.AudioPath != filepath.Join(dir, "audio.m4a") {
			t.Errorf("%s audio_path %q", id, h.AudioPath)
		}
		if got := read(t, filepath.Join(dir, "audio.m4a")); got != "audio of "+id {
			t.Errorf("%s audio %q", id, got)
		}
		if s := read(t, filepath.Join(dir, "session.json")); !strings.Contains(s, `"title":"Weekly"`) {
			t.Errorf("%s session.json otherwise changed: %s", id, s)
		}
	}
	if _, err := os.Stat(filepath.Join(f.data, "sessions", "s3")); err == nil {
		t.Error("s3, after since, was copied")
	}
	if _, err := os.Stat(filepath.Join(f.data, "migrate-tmp")); err == nil {
		t.Error("migrate-tmp left behind")
	}
	// Voices: Tomoe's older-than-ours owner voice kept ours; the
	// profiles replaced, with the old ones backed up; backups not copied.
	if got := read(t, filepath.Join(f.data, "voices", "eres2net-en.json")); got != `{"tomoe":"profiles"}` {
		t.Errorf("profiles %s", got)
	}
	if got := read(t, filepath.Join(f.data, "voices", "eres2net-en-you.json")); got != `{"recorder":"owner"}` {
		t.Errorf("owner voice %s", got)
	}
	if got := read(t, filepath.Join(f.data, "voices.bak-20261112-150000", "eres2net-en.json")); got != `{"recorder":"old profiles"}` {
		t.Errorf("backup %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.data, "voices", "eres2net-en.json.bak-1")); err == nil {
		t.Error("a Tomoe backup was copied")
	}
	if !strings.Contains(out, "eres2net-en-you.json: the recorder's is newer") {
		t.Errorf("didn't say it kept the newer voice:\n%s", out)
	}
	// Models.
	if read(t, filepath.Join(f.data, "models", "sherpa-parakeet", "tokens.txt")) != "tokens" || read(t, filepath.Join(f.data, "models", "silero_vad.onnx")) != "vad" {
		t.Error("models not copied")
	}
	// Settings: Tomoe's keys only where Musubi has none, model_path the
	// recorder's, comments kept, tomoe off, a backup.
	text := read(t, f.musubi)
	for _, want := range []string{"# my settings", "speaker_threshold = 0.7 # tuned by hand", `tomoe = "off"`} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "not_a_recorder_key") || strings.Contains(text, "F5") || strings.Contains(text, "0.6") {
		t.Errorf("config took keys it shouldn't:\n%s", text)
	}
	c, err := appconfig.Load(f.musubi)
	if err != nil {
		t.Fatal(err)
	}
	if !c.TomoeOff() || c.Recorder.Audio["device"] != "Desk Mic" || c.Recorder.Transcription["two_pass"] != true ||
		c.Recorder.Transcription["model_path"] != filepath.Join(f.data, "models") || c.Recorder.Meeting["mic_voices"] != false ||
		c.Recorder.Meeting["speaker_threshold"] != 0.7 {
		t.Errorf("recorder config %+v", c.Recorder)
	}
	if read(t, f.musubi+".bak-20261112-150000-standalone") != musubiCfg {
		t.Error("config backup isn't the file as it was")
	}

	// Again: nothing to do, nothing changes (a repair run).
	dataSum, cfgText := filesSum(t, f.data), read(t, f.musubi)
	if out, err := f.run(t, migrateOptions{}); err != nil {
		t.Fatalf("re-run: %v\n%s", err, out)
	}
	if filesSum(t, f.data) != dataSum || read(t, f.musubi) != cfgText {
		t.Error("a re-run changed something")
	}
	if treeSum(t, f.tomoe, f.tomoeConfig) != tomoeBefore {
		t.Fatal("Tomoe's folder or config changed")
	}

	// --session copies that one, whatever its date.
	if out, err := f.run(t, migrateOptions{session: "s3"}); err != nil {
		t.Fatalf("--session: %v\n%s", err, out)
	}
	if h, err := readSessionHeader(filepath.Join(f.data, "sessions", "s3")); err != nil || h.AudioPath != filepath.Join(f.data, "sessions", "s3", "audio.m4a") {
		t.Errorf("--session s3: %+v %v", h, err)
	}
	for _, bad := range []string{"nope", "../tomoe", ".hidden"} {
		if _, err := f.run(t, migrateOptions{session: bad}); err == nil {
			t.Errorf("--session %q accepted", bad)
		}
	}
	if treeSum(t, f.tomoe, f.tomoeConfig) != tomoeBefore {
		t.Fatal("Tomoe's folder or config changed")
	}
}

// --until replaces since; a session the recorder has is never overwritten
// (only its audio_path repaired, when it still names Tomoe's audio).
func TestMigrateUntilAndNoOverwrite(t *testing.T) {
	f := newMigrateFixture(t)
	mine := `{"id":"s2","created_at":"2026-11-11T09:00:00-05:00","title":"mine"}`
	write(t, filepath.Join(f.data, "sessions", "s2", "session.json"), mine)
	out, err := f.run(t, migrateOptions{until: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(f.data, "sessions", "s1", "session.json")); err != nil {
		t.Error("s1 not copied")
	}
	if got := read(t, filepath.Join(f.data, "sessions", "s2", "session.json")); got != mine {
		t.Errorf("s2 overwritten: %s", got)
	}

	// A copy made by hand still pointing at Tomoe's audio is repaired.
	s3 := filepath.Join(f.data, "sessions", "s3")
	if err := copyTree(filepath.Join(f.tomoe, "sessions", "s3"), s3); err != nil {
		t.Fatal(err)
	}
	tomoeBefore := treeSum(t, f.tomoe)
	if out, err := f.run(t, migrateOptions{until: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if h, _ := readSessionHeader(s3); h.AudioPath != filepath.Join(s3, "audio.m4a") {
		t.Errorf("audio_path not repaired: %q", h.AudioPath)
	}
	if treeSum(t, f.tomoe) != tomoeBefore {
		t.Fatal("Tomoe's folder changed")
	}
}

// A dry run writes nothing anywhere; a config still of record "tomoe" or
// --data in Tomoe's folder is refused before anything is copied.
func TestMigrateDryRunAndRefusals(t *testing.T) {
	f := newMigrateFixture(t)
	before := treeSum(t, f.tomoe, f.tomoeConfig, f.data, f.musubi)
	out, err := f.run(t, migrateOptions{dryRun: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if treeSum(t, f.tomoe, f.tomoeConfig, f.data, f.musubi) != before {
		t.Error("a dry run changed files")
	}
	for _, want := range []string{"session s1", "would copy", `would set [recorder] tomoe = "off"`, "[recorder.audio] device = Desk Mic"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run doesn't say %q:\n%s", want, out)
		}
	}

	write(t, f.musubi, strings.Replace(musubiCfg, `of_record = "musubi"`, `of_record = "tomoe"`, 1))
	if _, err := f.run(t, migrateOptions{}); err == nil || !strings.Contains(err.Error(), "of_record") {
		t.Errorf("of_record tomoe: %v", err)
	}
	var out2 bytes.Buffer
	err = migrateFromTomoe(&out2, migrateOptions{data: filepath.Join(f.tomoe, "inside"), tomoe: f.tomoe, musubiConfig: f.musubi}, time.Now())
	if err == nil {
		t.Error("--data inside Tomoe's folder accepted")
	}
	if _, err := os.Stat(filepath.Join(f.data, "sessions", "s1")); err == nil {
		t.Error("copied despite a refusal")
	}
}
