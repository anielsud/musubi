package backup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/store"
)

// fixture is a synthetic dataset laid out as by default: the recorder's
// folder and the backups root inside the data folder.
type fixture struct {
	data, rec, root, cfg string
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFixture(t *testing.T) fixture {
	base := t.TempDir()
	f := fixture{data: filepath.Join(base, "data"), cfg: filepath.Join(base, "config.toml")}
	f.rec = filepath.Join(f.data, "recorder")
	f.root = filepath.Join(f.data, "backups")
	write(t, filepath.Join(f.data, "raw", "ab", "x.json"), `{"n":1}`)
	write(t, filepath.Join(f.data, "inbox", "drop.json"), `{}`)
	write(t, filepath.Join(f.data, "profiles", "cpu.pprof"), "p")
	write(t, filepath.Join(f.data, "headshots", "someone.jpg"), "jpg")
	write(t, filepath.Join(f.data, "spend.jsonl"), "{}\n")
	write(t, filepath.Join(f.data, "registry.toml"), "")
	write(t, filepath.Join(f.data, "directory.json"), "{}")
	write(t, filepath.Join(f.data, "backups", "pre-upgrade-s3-f0-20260101-000000.db"), "old")
	write(t, filepath.Join(f.rec, "sessions", "s1", "transcript.json"), "first meeting")
	write(t, filepath.Join(f.rec, "voices", "model", "v1.json"), "voice one")
	write(t, filepath.Join(f.rec, "models", "big.onnx"), "weights")
	write(t, filepath.Join(f.rec, "logs", "recorder.log"), "log")
	write(t, filepath.Join(f.rec, "recorder.toml"), "x = 1")
	if err := unix.Mkfifo(filepath.Join(f.rec, "live.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, "data_dir = \"x\"\n")
	return f
}

func (f fixture) source(st *store.Store) Source {
	s := Source{Dataset: "test set", DataDir: f.data, RecorderDir: f.rec, ConfigFiles: []string{f.cfg}, AppVersion: "v-test"}
	if st != nil {
		s.CopyDB = st.BackupTo
	}
	return s
}

func openStore(t *testing.T, f fixture) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(f.data, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// forget adds n rows to the forget list, through a second connection.
func forget(t *testing.T, f fixture, n int) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(f.data, "musubi.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO forgotten(source, ref, at) VALUES ('test', ?, 0)`, time.Now().String()+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMakeCoversEverything(t *testing.T) {
	f := newFixture(t)
	st := openStore(t, f)
	defer st.Close()
	forget(t, f, 2)
	if !exists(filepath.Join(f.data, "musubi.db-wal")) {
		t.Fatal("expected the open database to have a WAL")
	}
	release, err := HoldOpen(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	now := time.Date(2026, 11, 12, 9, 30, 0, 0, time.Local)
	b, err := Make(f.root, f.source(st), Manual, "by hand", now)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "test-set-20261112-093000-manual" {
		t.Errorf("name %s", b.Name)
	}
	for _, p := range []string{"manifest.json", "data/musubi.db", "data/raw/ab/x.json", "data/inbox/drop.json", "data/profiles/cpu.pprof",
		"data/headshots/someone.jpg", "data/spend.jsonl", "data/registry.toml", "data/directory.json",
		"recorder/sessions/s1/transcript.json", "recorder/voices/model/v1.json", "recorder/recorder.toml", "recorder/logs/recorder.log", "config/config.toml"} {
		if !exists(filepath.Join(b.Dir, p)) {
			t.Errorf("backup lacks %s", p)
		}
	}
	for _, p := range []string{"data/musubi.db-wal", "data/musubi.db-shm", "data/backups", "data/recorder", "data/" + LockName,
		"recorder/models", "recorder/live.sock"} {
		if exists(filepath.Join(b.Dir, p)) {
			t.Errorf("backup has %s", p)
		}
	}
	m, err := Read(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	schema, format := store.Versions()
	if m.Kind != Manual || m.Reason != "by hand" || m.Dataset != "test set" || m.AppVersion != "v-test" || m.Format != Format ||
		m.Schema != schema || m.DBFormat != format || m.Counts["forgotten"] != 2 || m.DataDir != f.data || m.RecorderDir != f.rec ||
		len(m.Parts) != 3 || len(m.Skipped) != 1 || m.Bytes == 0 || !m.Created.Equal(now) {
		js, _ := json.Marshal(m)
		t.Errorf("manifest %s", js)
	}
	// A clone or a copy: changing the original leaves the backup alone.
	write(t, filepath.Join(f.data, "raw", "ab", "x.json"), "changed")
	if got := read(t, filepath.Join(b.Dir, "data", "raw", "ab", "x.json")); got != `{"n":1}` {
		t.Errorf("backup follows the original: %q", got)
	}
}

// Without clones (another file system, Linux) the same is copied.
func TestMakeWithoutClones(t *testing.T) {
	cloneFn = func(string, string) error { return errors.New("no clones") }
	t.Cleanup(func() { cloneFn = clone })
	f := newFixture(t)
	os.Symlink("raw", filepath.Join(f.data, "link"))
	b, err := Make(f.root, f.source(nil), Manual, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"data/raw/ab/x.json", "data/link", "recorder/sessions/s1/transcript.json", "recorder/voices/model/v1.json"} {
		if !exists(filepath.Join(b.Dir, p)) {
			t.Errorf("backup lacks %s", p)
		}
	}
	for _, p := range []string{"data/backups", "data/recorder", "recorder/models", "recorder/live.sock"} {
		if exists(filepath.Join(b.Dir, p)) {
			t.Errorf("backup has %s", p)
		}
	}
	if b.Bytes < int64(len(`{"n":1}`)) {
		t.Errorf("bytes %d", b.Bytes)
	}
}

// A recorder folder outside the data folder is backed up as well, and
// the data folder's own backups/ (pre-upgrade copies) is still left out.
func TestMakeRecorderElsewhere(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(filepath.Dir(f.data), "rec")
	if err := os.Rename(f.rec, other); err != nil {
		t.Fatal(err)
	}
	f.rec = other
	root := filepath.Join(filepath.Dir(f.data), "elsewhere")
	b, err := Make(root, f.source(nil), Manual, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(b.Dir, "recorder/sessions/s1/transcript.json")) || exists(filepath.Join(b.Dir, "data/backups")) {
		t.Error("recorder elsewhere: wrong contents")
	}
	if exists(filepath.Join(b.Dir, "data/musubi.db")) {
		t.Error("a database out of nowhere")
	}
}

func TestRetention(t *testing.T) {
	f := newFixture(t)
	t0 := time.Date(2026, 11, 1, 8, 0, 0, 0, time.Local)
	c := config.Backup{Daily: true, KeepDaily: 2, BeforeChanges: true, KeepBefore: 1}
	if !DailyDue(f.root, "test set", c, t0) {
		t.Error("no daily yet: due")
	}
	for i := 0; i < 4; i++ {
		if _, err := Snapshot(f.root, f.source(nil), Daily, "daily", c.KeepDaily, t0.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Make(f.root, f.source(nil), Manual, "", t0); err != nil {
		t.Fatal(err)
	}
	other := f.source(nil)
	other.Dataset = "test"
	if _, err := Snapshot(f.root, other, Daily, "daily", 1, t0); err != nil {
		t.Fatal(err)
	}
	for i, reason := range []string{"musubi forget", "musubi vectors encode"} {
		if _, err := BeforeChange(f.root, f.source(nil), c, reason, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := List(f.root, "test set")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[Kind]int{}
	for _, b := range all {
		kinds[b.Kind]++
	}
	if kinds[Daily] != 2 || kinds[Manual] != 1 || kinds[Before] != 1 || len(all) != 4 {
		t.Errorf("kept %v", kinds)
	}
	if all[0].Kind != Daily || !all[0].Created.Equal(t0.Add(72*time.Hour)) {
		t.Errorf("newest first: %+v", all[0].Manifest)
	}
	for _, b := range all {
		if b.Kind == Before && b.Reason != "musubi vectors encode" {
			t.Errorf("kept the older before-change backup: %s", b.Reason)
		}
	}
	if o, _ := List(f.root, "test"); len(o) != 1 {
		t.Errorf("the other dataset's: %d", len(o))
	}
	if DailyDue(f.root, "test set", c, t0.Add(80*time.Hour)) || !DailyDue(f.root, "test set", c, t0.Add(96*time.Hour)) {
		t.Error("daily due wrong")
	}
	if DailyDue(f.root, "test set", config.Backup{Daily: false, KeepDaily: 7}, t0.Add(1000*time.Hour)) {
		t.Error("daily off, yet due")
	}
	if dir, err := BeforeChange(f.root, f.source(nil), config.Backup{}, "x", t0); dir != "" || err != nil {
		t.Errorf("before_changes off still backed up: %q %v", dir, err)
	}
}

func TestLegacyListed(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.root, "test-set-20261010-101010", "musubi.db"), "db")
	write(t, filepath.Join(f.root, "test-set-b-20261010-101010", "musubi.db"), "db") // dataset "test set b"
	all, err := List(f.root, "test set")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Legacy || all[0].Created.Day() != 10 {
		t.Fatalf("legacy: %+v", all)
	}
	if _, err := Restore(f.root, all[0], f.source(nil), false, time.Now()); err == nil {
		t.Error("restored a legacy backup")
	}
}

func TestRestore(t *testing.T) {
	f := newFixture(t)
	st := openStore(t, f)
	forget(t, f, 1)
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)
	b, err := Make(f.root, f.source(st), Before, "musubi forget", t0)
	if err != nil {
		t.Fatal(err)
	}
	// What happens after the backup: a row, a new file, a lost file, a new
	// recording.
	forget(t, f, 2)
	write(t, filepath.Join(f.data, "new.txt"), "new")
	os.Remove(filepath.Join(f.data, "raw", "ab", "x.json"))
	write(t, filepath.Join(f.rec, "sessions", "s2", "transcript.json"), "second meeting")
	write(t, filepath.Join(f.rec, "voices", "model", "v1.json"), "voice one, relearnt")

	// The app has it open: refused.
	release, err := HoldOpen(f.data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(f.root, b, f.source(nil), false, t0.Add(time.Minute)); !errors.Is(err, ErrInUse) {
		t.Errorf("restored into an open dataset: %v", err)
	}
	release()
	if _, err := Restore(f.root, b, f.source(st), false, t0.Add(time.Minute)); err == nil {
		t.Error("restored while the store is open")
	}
	st.Close()

	undo, err := Restore(f.root, b, f.source(nil), false, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.data, "new.txt")) || !exists(filepath.Join(f.data, "raw", "ab", "x.json")) {
		t.Error("data folder not as backed up")
	}
	if fi, err := store.ReadFileInfo(filepath.Join(f.data, "musubi.db")); err != nil || fi.Counts["forgotten"] != 1 {
		t.Errorf("database not as backed up: %+v %v", fi, err)
	}
	if exists(filepath.Join(f.data, "musubi.db-wal")) {
		t.Error("the replaced database's WAL left beside the restored one")
	}
	// Without the recorder's part, recordings and voices stay as they are.
	if !exists(filepath.Join(f.rec, "sessions", "s2")) || read(t, filepath.Join(f.rec, "voices", "model", "v1.json")) != "voice one, relearnt" {
		t.Error("recorder's folder changed by a data-only restore")
	}
	for _, p := range []string{"backups/pre-upgrade-s3-f0-20260101-000000.db", "recorder/models/big.onnx"} {
		if !exists(filepath.Join(f.data, p)) {
			t.Errorf("restore removed %s", p)
		}
	}
	ents, _ := os.ReadDir(f.data)
	for _, e := range ents {
		if len(e.Name()) > len(asidePrefix) && e.Name()[:len(asidePrefix)] == asidePrefix {
			t.Errorf("left %s", e.Name())
		}
	}
	// The undo backup is the dataset as it was just before.
	if undo.Kind != Undo || undo.Reason != "before restore" || undo.Counts["forgotten"] != 3 || !exists(filepath.Join(undo.Dir, "data", "new.txt")) {
		t.Errorf("undo: %+v", undo.Manifest)
	}

	// Undoing the restore brings the later state back.
	if _, err := Restore(f.root, undo, f.source(nil), false, t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(f.data, "new.txt")) {
		t.Error("undo didn't bring new.txt back")
	}
	if fi, _ := store.ReadFileInfo(filepath.Join(f.data, "musubi.db")); fi.Counts["forgotten"] != 3 {
		t.Errorf("undo: %d forgotten", fi.Counts["forgotten"])
	}

	// With the recorder's part: sessions and voices as backed up, models
	// and logs left alone.
	if _, err := Restore(f.root, b, f.source(nil), true, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.rec, "sessions", "s2")) || read(t, filepath.Join(f.rec, "voices", "model", "v1.json")) != "voice one" {
		t.Error("recorder's folder not as backed up")
	}
	if !exists(filepath.Join(f.rec, "models", "big.onnx")) || !exists(filepath.Join(f.rec, "logs", "recorder.log")) || !exists(filepath.Join(f.rec, "live.sock")) {
		t.Error("restore touched models, logs or the socket")
	}
	// The restored database opens.
	st2, err := store.Open(filepath.Join(f.data, "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
}

func TestRestoreRefuses(t *testing.T) {
	f := newFixture(t)
	b, err := Make(f.root, f.source(nil), Manual, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other := f.source(nil)
	other.DataDir = t.TempDir()
	if _, err := Restore(f.root, b, other, false, time.Now()); err == nil {
		t.Error("restored into another data folder")
	}
	newer := b
	newer.Schema = 1 << 20
	if _, err := Restore(f.root, newer, f.source(nil), false, time.Now()); err == nil {
		t.Error("restored a newer database")
	}
	noRec := f.source(nil)
	noRec.RecorderDir = t.TempDir()
	if _, err := Restore(f.root, b, noRec, true, time.Now()); err == nil {
		t.Error("restored the recorder's part into another folder")
	}
	if all, _ := List(f.root, "test set"); len(all) != 1 {
		t.Errorf("a refused restore left backups: %d", len(all))
	}
}
