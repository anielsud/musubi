package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/dataformat"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// migrateCmd makes Musubi stand alone: it copies what the recorder and the
// app still read from Tomoe (its sessions, voices, models and recording
// settings) into the recorder's folder and Musubi's config, then sets
// [recorder] tomoe = "off" (docs/recorder-screens.md, "Standalone"). It only
// reads Tomoe's folder and config, never writes them, and never overwrites
// a session the recorder already has, so it can be run again to repair.
var migrateCmd = &cobra.Command{
	Use:   "migrate-from-tomoe",
	Short: "Copy Tomoe's sessions, voices, models and settings in, then stop reading Tomoe",
	Long: `Copies, into the recorder's folder (--data):
  sessions  every Tomoe session folder the recorder doesn't have, created
            before --until (default: Musubi's [recorder] since; later ones
            Musubi's recorder recorded itself), as APFS clones where it can;
            each copy's audio_path is pointed at its own audio.m4a
  voices    Tomoe's voice files, unless the recorder's copy of one is newer
            (what is replaced is kept in voices.bak-<date>)
  models    each of Tomoe's models not already there
and into Musubi's config (--musubi-config, backed up first as
<name>.bak-<date>-standalone):
  settings  every key Tomoe's config.toml sets in [audio], [transcription]
            and [meeting] that [recorder.*] doesn't, with model_path the
            recorder's own models folder
  tomoe     [recorder] tomoe = "off", once everything else succeeded.

Nothing in Tomoe's folder or config is written. A recorder session already
present is never overwritten. Run again to repair: what is there is left;
--session <uuid> copies that one session only, whatever its date. A failure
leaves partial copies only in the recorder's folder and exits non-zero.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var o migrateOptions
		o.data, _ = cmd.Flags().GetString("data")
		o.tomoe, _ = cmd.Flags().GetString("tomoe")
		o.musubiConfig, _ = cmd.Flags().GetString("musubi-config")
		until, _ := cmd.Flags().GetString("until")
		o.session, _ = cmd.Flags().GetString("session")
		o.dryRun, _ = cmd.Flags().GetBool("dry-run")
		if until != "" {
			t, err := time.Parse(time.RFC3339, until)
			if err != nil {
				return fmt.Errorf("--until: want a time like 2026-11-12T10:00:00-05:00: %w", err)
			}
			o.until = t
		}
		return migrateFromTomoe(os.Stdout, o, time.Now())
	},
}

func init() {
	migrateCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder to copy into (never Tomoe's)")
	migrateCmd.Flags().String("tomoe", config.TomoeDataDir(), "Tomoe's data folder, read only")
	migrateCmd.Flags().String("musubi-config", "", "Musubi's config file to write the settings and tomoe = \"off\" into (default: $MUSUBI_CONFIG, else the one the Musubi app last named, else ~/.config/musubi/config.toml)")
	migrateCmd.Flags().String("until", "", "skip Tomoe sessions created at or after this time, RFC 3339 (default: the Musubi config's [recorder] since)")
	migrateCmd.Flags().String("session", "", "copy only this Tomoe session (its folder name), whatever its date; nothing else is done")
	migrateCmd.Flags().Bool("dry-run", false, "say what would be copied and written, and change nothing")
	rootCmd.AddCommand(migrateCmd)
}

type migrateOptions struct {
	data, tomoe, musubiConfig string
	until                     time.Time
	session                   string
	dryRun                    bool
}

// migration is one run: where things come from and go, and what failed.
type migration struct {
	w                io.Writer
	dryRun           bool
	now              time.Time
	tomoe, data      string // absolute
	tomoeSessions    string
	sessions, models string // the recorder's
	tmp              string // where copies are made before they are renamed into place
	failed           int
}

func (m *migration) say(format string, args ...any) {
	if m.dryRun {
		format = "[dry run] " + format
	}
	fmt.Fprintf(m.w, format+"\n", args...)
}

func (m *migration) fail(format string, args ...any) {
	m.failed++
	fmt.Fprintf(m.w, "FAILED: "+format+"\n", args...)
}

func migrateFromTomoe(w io.Writer, o migrateOptions, now time.Time) error {
	data, err := resolveDataDir(o.data)
	if err != nil {
		return err
	}
	if err := dataformat.Check(data); err != nil {
		return err
	}
	tomoe, err := filepath.Abs(o.tomoe)
	if err != nil {
		return err
	}
	if within(realPath(data), realPath(tomoe)) || within(realPath(tomoe), realPath(data)) {
		return fmt.Errorf("--data %s and --tomoe %s overlap; the recorder's folder must be apart from Tomoe's", data, tomoe)
	}
	if st, err := os.Stat(filepath.Join(tomoe, "sessions")); err != nil || !st.IsDir() {
		return fmt.Errorf("no Tomoe sessions folder at %s", filepath.Join(tomoe, "sessions"))
	}
	musubiPath := o.musubiConfig
	if musubiPath == "" {
		musubiPath = config.ResolveMusubiPath(data)
	}
	if st, err := os.Stat(musubiPath); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("no Musubi config at %s (--musubi-config)", musubiPath)
	}
	mc, err := appconfig.Load(musubiPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", musubiPath, err)
	}
	until := o.until
	if until.IsZero() {
		until = mc.Recorder.Since
	}
	if o.session == "" {
		// Tomoe off with Tomoe still of record would import meetings from
		// Tomoe and look for their files in the recorder's folder.
		if !mc.MusubiRecords() {
			return fmt.Errorf("%s: [recorder] of_record is %q; switch to Musubi's recorder first (Settings → Recording)", musubiPath, mc.Recorder.OfRecord)
		}
		if until.IsZero() {
			return fmt.Errorf("%s has no [recorder] since; give --until", musubiPath)
		}
	}

	m := &migration{
		w: w, dryRun: o.dryRun, now: now, tomoe: tomoe, data: data,
		tomoeSessions: filepath.Join(tomoe, "sessions"),
		sessions:      filepath.Join(data, "sessions"),
		models:        filepath.Join(data, "models"),
		tmp:           filepath.Join(data, "migrate-tmp"),
	}
	m.say("from Tomoe %s into %s; settings into %s", tomoe, data, musubiPath)
	if !m.dryRun {
		if err := os.MkdirAll(m.sessions, 0o700); err != nil {
			return err
		}
		// Leftovers of an interrupted run are partial copies.
		if err := os.RemoveAll(m.tmp); err != nil {
			return err
		}
		if err := os.MkdirAll(m.tmp, 0o700); err != nil {
			return err
		}
		defer os.RemoveAll(m.tmp)
	}

	if o.session != "" {
		if !validFolderName(o.session) {
			return fmt.Errorf("--session %q is not a session folder name", o.session)
		}
		if _, err := os.Stat(filepath.Join(m.tomoeSessions, o.session)); err != nil {
			return fmt.Errorf("no Tomoe session %s", o.session)
		}
		m.copySession(o.session, time.Time{})
		return m.result(nil)
	}

	m.copySessions(until)
	m.copyVoices()
	m.copyModels()
	if err := m.writeSettings(musubiPath, mc); err != nil {
		m.fail("settings: %v", err)
	}
	return m.result(func() error { return m.turnTomoeOff(musubiPath, mc) })
}

// result reports the run; then, only if nothing failed, runs last.
func (m *migration) result(last func() error) error {
	if m.failed == 0 && last != nil {
		if err := last(); err != nil {
			m.fail("%v", err)
		}
	}
	if m.failed > 0 {
		if last != nil {
			fmt.Fprintln(m.w, "[recorder] tomoe left as it was: run again once the failures are fixed")
		}
		return fmt.Errorf("%d step(s) failed", m.failed)
	}
	m.say("done")
	return nil
}

// validFolderName is a single folder name, never a path.
func validFolderName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`) && !strings.HasPrefix(s, ".")
}

// sessionHeader is what the migration reads of a session.json.
type sessionHeader struct {
	CreatedAt time.Time `json:"created_at"`
	AudioPath string    `json:"audio_path"`
}

func readSessionHeader(dir string) (sessionHeader, error) {
	var h sessionHeader
	b, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return h, fmt.Errorf("session.json doesn't parse: %w", err)
	}
	return h, nil
}

func (m *migration) copySessions(until time.Time) {
	ents, err := os.ReadDir(m.tomoeSessions)
	if err != nil {
		m.fail("sessions: %v", err)
		return
	}
	var copied, present, later, skipped int
	for _, e := range ents {
		if !e.IsDir() || !validFolderName(e.Name()) {
			continue
		}
		switch m.copySession(e.Name(), until) {
		case sessionCopied:
			copied++
		case sessionPresent:
			present++
		case sessionLater:
			later++
		default:
			skipped++
		}
	}
	m.say("sessions: %d copied, %d already there, %d at or after %s (Musubi's recorder has those), %d skipped or failed",
		copied, present, later, until.Format(time.RFC3339), skipped)
}

type sessionOutcome int

const (
	sessionCopied sessionOutcome = iota
	sessionPresent
	sessionLater
	sessionSkipped
)

// copySession copies Tomoe session id into the recorder's sessions unless
// it is there already or (with until set) was created at or after until.
func (m *migration) copySession(id string, until time.Time) sessionOutcome {
	src, dst := filepath.Join(m.tomoeSessions, id), filepath.Join(m.sessions, id)
	if _, err := os.Lstat(dst); err == nil {
		m.repairAudioPath(id)
		return sessionPresent
	}
	h, err := readSessionHeader(src)
	if errors.Is(err, fs.ErrNotExist) {
		m.say("session %s: skipped, no session.json (still recording?)", id)
		return sessionSkipped
	}
	if err != nil {
		m.fail("session %s: %v", id, err)
		return sessionSkipped
	}
	if !until.IsZero() && !h.CreatedAt.Before(until) {
		return sessionLater
	}
	if m.dryRun {
		m.say("session %s (%s): would copy", id, h.CreatedAt.Format("2006-01-02 15:04"))
		return sessionCopied
	}
	tmp := filepath.Join(m.tmp, id)
	if err := cloneTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		m.fail("session %s: copying: %v", id, err)
		return sessionSkipped
	}
	if err := pointAudioAt(tmp, h.AudioPath, src, dst); err != nil {
		os.RemoveAll(tmp)
		m.fail("session %s: %v", id, err)
		return sessionSkipped
	}
	if _, err := readSessionHeader(tmp); err != nil {
		os.RemoveAll(tmp)
		m.fail("session %s: the copy's %v", id, err)
		return sessionSkipped
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		m.fail("session %s: %v", id, err)
		return sessionSkipped
	}
	m.say("session %s (%s): copied", id, h.CreatedAt.Format("2006-01-02 15:04"))
	return sessionCopied
}

// repairAudioPath points a recorder session copied from Tomoe (an earlier
// run, or by hand) at its own audio when its audio_path still names
// Tomoe's copy and its own is there.
func (m *migration) repairAudioPath(id string) {
	dst := filepath.Join(m.sessions, id)
	h, err := readSessionHeader(dst)
	if err != nil || h.AudioPath == "" || !within(h.AudioPath, m.tomoeSessions) {
		return
	}
	src := filepath.Join(m.tomoeSessions, id)
	rel, err := filepath.Rel(src, h.AudioPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
		return
	}
	if m.dryRun {
		m.say("session %s: would point audio_path at its own copy", id)
		return
	}
	if err := pointAudioAt(dst, h.AudioPath, src, dst); err != nil {
		m.fail("session %s: %v", id, err)
		return
	}
	m.say("session %s: audio_path pointed at its own copy", id)
}

// pointAudioAt rewrites the session.json in dir so an audio_path inside
// the Tomoe session src names the same file under dst, changing only that
// value's text. Other audio paths are left alone.
func pointAudioAt(dir, audio, src, dst string) error {
	if audio == "" || !within(audio, src) {
		return nil
	}
	rel, err := filepath.Rel(src, audio)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "session.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	out, err := replaceAudioPath(b, audio, filepath.Join(dst, rel))
	if err != nil {
		return err
	}
	tmp := path + ".migrate"
	if err := os.WriteFile(tmp, out, st.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// Keep the time Tomoe last saved it.
	return os.Chtimes(path, st.ModTime(), st.ModTime())
}

// replaceAudioPath swaps the JSON string old for new in a session.json
// where it appears once (as audio_path), keeping every other byte.
func replaceAudioPath(b []byte, old, new string) ([]byte, error) {
	enc := func(s string) []byte {
		var buf bytes.Buffer
		e := json.NewEncoder(&buf)
		e.SetEscapeHTML(false)
		_ = e.Encode(s)
		return bytes.TrimRight(buf.Bytes(), "\n")
	}
	for _, o := range [][]byte{enc(old), func() []byte { x, _ := json.Marshal(old); return x }()} {
		if bytes.Count(b, o) == 1 {
			out := bytes.Replace(b, o, enc(new), 1)
			var h sessionHeader
			if err := json.Unmarshal(out, &h); err != nil || h.AudioPath != new {
				break
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("couldn't point audio_path at the copy (its text isn't found once in session.json)")
}

// copyVoices brings Tomoe's voice files (*.json) over the recorder's,
// except where the recorder's copy is newer: once Musubi's recorder is of
// record, its own voices may have learnt or been corrected since.
func (m *migration) copyVoices() {
	from, to := filepath.Join(m.tomoe, "voices"), filepath.Join(m.data, "voices")
	ents, err := os.ReadDir(from)
	if errors.Is(err, fs.ErrNotExist) {
		m.say("voices: Tomoe has none")
		return
	}
	if err != nil {
		m.fail("voices: %v", err)
		return
	}
	backedUp := false
	for _, e := range ents {
		// Tomoe's backups (name.json.bak-…) and half-written saves don't
		// end in .json.
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		src, dst := filepath.Join(from, e.Name()), filepath.Join(to, e.Name())
		theirs, err := os.ReadFile(src)
		if err != nil {
			m.fail("voices %s: %v", e.Name(), err)
			continue
		}
		sst, err := os.Stat(src)
		if err != nil {
			m.fail("voices %s: %v", e.Name(), err)
			continue
		}
		ours, oerr := os.ReadFile(dst)
		if oerr == nil && bytes.Equal(ours, theirs) {
			m.say("voices %s: already the same", e.Name())
			continue
		}
		if oerr == nil {
			if dstat, err := os.Stat(dst); err == nil && dstat.ModTime().After(sst.ModTime()) {
				m.say("voices %s: the recorder's is newer (%s, Tomoe's %s); kept the recorder's", e.Name(),
					dstat.ModTime().Format("2006-01-02 15:04"), sst.ModTime().Format("2006-01-02 15:04"))
				continue
			}
		}
		if m.dryRun {
			m.say("voices %s: would copy Tomoe's", e.Name())
			continue
		}
		if oerr == nil && !backedUp {
			// The same copies as before a relearn (voices.Backup): one
			// naming, the newest five kept.
			backup, err := voices.Backup(to, m.now)
			if err != nil {
				m.fail("voices: backing up %s: %v", to, err)
				return
			}
			backedUp = true
			m.say("voices: the recorder's kept in %s", backup)
		}
		if err := writeFileAtomic(dst, theirs, sst.ModTime()); err != nil {
			m.fail("voices %s: %v", e.Name(), err)
			continue
		}
		m.say("voices %s: copied Tomoe's", e.Name())
	}
}

// writeFileAtomic writes b to path through a temporary file, with mtime.
func writeFileAtomic(path string, b []byte, mtime time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".migrate"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// copyModels clones each of Tomoe's models (a file or a folder in its
// models folder) the recorder doesn't have.
func (m *migration) copyModels() {
	from := filepath.Join(m.tomoe, "models")
	ents, err := os.ReadDir(from)
	if errors.Is(err, fs.ErrNotExist) {
		m.say("models: Tomoe has none")
		return
	}
	if err != nil {
		m.fail("models: %v", err)
		return
	}
	if !m.dryRun {
		if err := os.MkdirAll(m.models, 0o700); err != nil {
			m.fail("models: %v", err)
			return
		}
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dst := filepath.Join(m.models, e.Name())
		if _, err := os.Lstat(dst); err == nil {
			m.say("models %s: already there", e.Name())
			continue
		}
		if m.dryRun {
			m.say("models %s: would copy", e.Name())
			continue
		}
		tmp := filepath.Join(m.tmp, "model-"+e.Name())
		if err := cloneTree(filepath.Join(from, e.Name()), tmp); err != nil {
			os.RemoveAll(tmp)
			m.fail("models %s: %v", e.Name(), err)
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.RemoveAll(tmp)
			m.fail("models %s: %v", e.Name(), err)
			continue
		}
		m.say("models %s: copied", e.Name())
	}
}

// settingsEdits are the [recorder.*] keys to add to Musubi's config: each
// one Tomoe's config sets that Musubi's doesn't, and model_path pointed at
// the recorder's models.
func settingsEdits(tomoe map[string]map[string]any, have appconfig.Recorder, models string) []appconfig.Edit {
	set := map[string]map[string]any{"audio": have.Audio, "transcription": have.Transcription, "meeting": have.Meeting}
	var edits []appconfig.Edit
	for _, table := range config.RecorderSections {
		keys := make([]string, 0, len(tomoe[table]))
		for k := range tomoe[table] {
			keys = append(keys, k)
		}
		if table == "transcription" {
			if _, ok := tomoe[table]["model_path"]; !ok {
				keys = append(keys, "model_path")
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := set[table][k]; ok {
				continue
			}
			v := tomoe[table][k]
			if table == "transcription" && k == "model_path" {
				v = models
			}
			edits = append(edits, appconfig.Edit{Section: "recorder." + table, Key: k, Value: v})
		}
	}
	return edits
}

func (m *migration) writeSettings(path string, mc appconfig.Config) error {
	tomoe, err := config.FileValues(config.Path())
	if err != nil {
		return err
	}
	if p, ok := mc.Recorder.Transcription["model_path"].(string); ok && within(p, filepath.Join(m.tomoe, "models")) {
		m.say("settings: [recorder.transcription] model_path = %q is Tomoe's folder, which the recorder won't search with Tomoe off; change it to %s", p, m.models)
	}
	edits := settingsEdits(tomoe, mc.Recorder, m.models)
	if len(edits) == 0 {
		m.say("settings: every key of Tomoe's config (%s) is set in Musubi's already", config.Path())
		return nil
	}
	for _, e := range edits {
		m.say("settings: [%s] %s = %v", e.Section, e.Key, e.Value)
	}
	if m.dryRun {
		return nil
	}
	if err := m.backupConfig(path); err != nil {
		return err
	}
	if err := appconfig.EditFile(path, edits); err != nil {
		return err
	}
	m.say("settings: %d keys from %s written", len(edits), config.Path())
	return nil
}

// backupConfig keeps Musubi's config as it was before this run's first
// edit, beside it.
func (m *migration) backupConfig(path string) error {
	bak := path + ".bak-" + m.now.Format("20060102-150405") + "-standalone"
	if _, err := os.Stat(bak); err == nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(bak, b, 0o600); err != nil {
		return err
	}
	m.say("settings: %s backed up as %s", path, bak)
	return nil
}

func (m *migration) turnTomoeOff(path string, mc appconfig.Config) error {
	if mc.TomoeOff() {
		m.say("[recorder] tomoe is \"off\" already")
		return nil
	}
	if m.dryRun {
		m.say("would set [recorder] tomoe = \"off\" in %s", path)
		return nil
	}
	if err := m.backupConfig(path); err != nil {
		return err
	}
	if err := appconfig.EditFile(path, []appconfig.Edit{{Section: "recorder", Key: "tomoe", Value: "off"}}); err != nil {
		return err
	}
	m.say("set [recorder] tomoe = \"off\" in %s: restart the recorder and reopen Musubi to apply it", path)
	return nil
}

// cloneTree copies src (a file or folder) to dst, which must not exist,
// as an APFS clone where it can (cloneCopy), else byte by byte.
func cloneTree(src, dst string) error {
	if err := cloneCopy(src, dst); err == nil {
		return nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyTree(src, dst)
}

// copyTree copies src (a file or folder) to dst byte by byte, keeping
// permissions and modification times; symlinks are copied as links.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()|0o700); err != nil {
				return err
			}
		case d.Type().IsRegular():
			if err := copyFile(p, target, info.Mode().Perm()); err != nil {
				return err
			}
		default:
			return nil // sockets and the like aren't data
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
