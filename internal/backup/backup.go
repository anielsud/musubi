// Package backup makes, lists, prunes and restores snapshots of a whole
// dataset: the database, everything else in the data folder, the
// recorder's folder and the config (docs/data-safety.md, items 2 and 3).
// The app and the command line both use it.
//
// A backup is a folder under the backups root (DefaultRoot), named
// <dataset>-<time>-<kind>:
//
//	manifest.json  what it is (Manifest)
//	data/          the data folder: musubi.db (a consistent copy), raw/,
//	               inbox/, profiles/, headshots/, spend.jsonl, …
//	recorder/      the recorder's folder: sessions/, voices/, its small
//	               files (not models/: they download again)
//	config/        config.toml
//
// Files are APFS clones where the disk allows, so a backup takes room only
// as the originals change; the database is always a real copy.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/store"
)

// Format is the layout of a backup folder; a restore refuses a newer one.
const Format = 1

const manifestName = "manifest.json"

// Kind is why a backup was made, and so how long it is kept.
type Kind string

const (
	// Manual: "Back up now" or `musubi backup now`. Never removed.
	Manual Kind = "manual"
	// Daily: once a day while the app is open; [backup] keep_daily kept.
	Daily Kind = "daily"
	// Before: before a step that deletes or rewrites stored data in bulk;
	// [backup] keep_before kept.
	Before Kind = "before"
	// Undo: the dataset as it was before a restore. Never removed.
	Undo Kind = "undo"
)

// Manifest says what a backup is, so a restore knows.
type Manifest struct {
	Format     int       `json:"format"`
	Kind       Kind      `json:"kind"`
	Reason     string    `json:"reason"`
	Dataset    string    `json:"dataset"`
	Created    time.Time `json:"created"`
	AppVersion string    `json:"app_version"`
	// Schema and DBFormat are the database's (store.Versions); Counts its
	// row counts (store.FileInfo).
	Schema   int              `json:"schema"`
	DBFormat int              `json:"db_format"`
	Counts   map[string]int64 `json:"counts,omitempty"`
	// DataDir and RecorderDir are where the parts came from: a restore
	// puts them back only there.
	DataDir     string   `json:"data_dir"`
	RecorderDir string   `json:"recorder_dir,omitempty"`
	Parts       []string `json:"parts"`
	Skipped     []string `json:"skipped,omitempty"`
	// Bytes is the logical size; clones share most of it with the
	// originals.
	Bytes int64 `json:"bytes"`
}

// Backup is one backup folder.
type Backup struct {
	Manifest
	Dir  string `json:"dir"`
	Name string `json:"name"`
	// Legacy: made before backups had a manifest (database, raw, inbox
	// and config side by side). Listed, restored only by hand.
	Legacy bool `json:"legacy,omitempty"`
}

// Has reports whether the backup holds a part ("data", "recorder",
// "config").
func (b Backup) Has(part string) bool {
	for _, p := range b.Parts {
		if p == part {
			return true
		}
	}
	return false
}

// Source is the dataset a backup is made of.
type Source struct {
	Dataset     string
	DataDir     string
	RecorderDir string // "": none
	ConfigFiles []string
	AppVersion  string
	// CopyDB writes a consistent copy of the open database to dst; nil
	// when no Store has it open (it is copied from its file).
	CopyDB func(dst string) error
}

// SourceFor is the dataset of cfg (loaded from cfgPath); st is its open
// store, or nil.
func SourceFor(cfg config.Config, cfgPath string, st *store.Store, appVersion string) Source {
	s := Source{Dataset: config.DatasetName(cfgPath), DataDir: cfg.DataDir, RecorderDir: cfg.Recorder.Data, AppVersion: appVersion}
	if cfgPath != "" {
		s.ConfigFiles = []string{cfgPath}
	}
	if st != nil {
		s.CopyDB = st.BackupTo
	}
	return s
}

// DefaultRoot is where backups go: ~/.local/share/musubi/backups, as
// "Back up now" always wrote them.
func DefaultRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "musubi", "backups")
}

// Prefix starts the names of a dataset's backups.
func Prefix(dataset string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == ':' || r == filepath.Separator {
			return '-'
		}
		return r
	}, dataset) + "-"
}

// mu keeps one backup or restore at a time in this process: a daily
// snapshot and one before a change could otherwise prune each other's.
var mu sync.Mutex

const dbName = "musubi.db"

// dataKeep are names in the data folder that are not part of the dataset
// as a backup sees it: the database (copied on its own) and its journal,
// the pre-upgrade copies, the lock, and work folders of this package.
func dataKeep(name string) bool {
	switch name {
	case dbName, dbName + "-wal", dbName + "-shm", dbName + "-journal", "backups", LockName:
		return true
	}
	return strings.HasPrefix(name, asidePrefix)
}

// recorderKeep are names in the recorder's folder a backup leaves out
// (models: large, downloaded again) or a restore leaves in place (logs:
// the recorder writes them while it runs).
func recorderKeep(name string) bool {
	return name == "models" || name == "logs" || strings.HasPrefix(name, asidePrefix)
}

const asidePrefix = ".restore-old-"

func within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// special are paths never copied into a part however deep they sit: the
// backups root, and the other part's folder when it is inside this one.
func special(root string, src Source) []string {
	out := []string{root}
	if src.RecorderDir != "" {
		out = append(out, src.RecorderDir)
	}
	return out
}

func skipper(dir string, keep func(string) bool, specials []string, self string) (func(string, fs.DirEntry) bool, func(string) bool) {
	skip := func(path string, d fs.DirEntry) bool {
		if filepath.Dir(path) == dir && keep(d.Name()) {
			return true
		}
		for _, s := range specials {
			if filepath.Clean(s) != filepath.Clean(self) && within(path, s) {
				return true
			}
		}
		t := d.Type()
		return !(t.IsDir() || t.IsRegular() || t&fs.ModeSymlink != 0) // sockets
	}
	holds := func(path string) bool {
		for _, s := range specials {
			if filepath.Clean(s) != filepath.Clean(self) && within(s, path) {
				return true
			}
		}
		return false
	}
	return skip, holds
}

// copyPart copies dir's entries (those keep doesn't hold back) into
// dst, returning the bytes copied.
func copyPart(dir, dst string, keep func(string) bool, specials []string) (int64, error) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return 0, err
	}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	skip, holds := skipper(dir, keep, specials, dir)
	var total int64
	for _, e := range ents {
		n, err := copyTree(filepath.Join(dir, e.Name()), filepath.Join(dst, e.Name()), skip, holds)
		total += n
		if err != nil {
			return total, fmt.Errorf("copying %s: %w", filepath.Join(dir, e.Name()), err)
		}
	}
	return total, nil
}

// Make backs src up into a new folder under root, and returns it. A
// backup that fails part way is removed, never listed.
func Make(root string, src Source, kind Kind, reason string, now time.Time) (Backup, error) {
	mu.Lock()
	defer mu.Unlock()
	return makeLocked(root, src, kind, reason, now)
}

func makeLocked(root string, src Source, kind Kind, reason string, now time.Time) (Backup, error) {
	if src.DataDir == "" {
		return Backup{}, errors.New("no data folder")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Backup{}, err
	}
	name := Prefix(src.Dataset) + now.Format("20060102-150405") + "-" + string(kind)
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(root, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = Prefix(src.Dataset) + now.Format("20060102-150405") + "-" + string(kind) + "-" + strconv.Itoa(i)
	}
	tmp := filepath.Join(root, ".partial-"+name)
	os.RemoveAll(tmp)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return Backup{}, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	m := Manifest{Format: Format, Kind: kind, Reason: reason, Dataset: src.Dataset, Created: now, AppVersion: src.AppVersion,
		DataDir: filepath.Clean(src.DataDir)}
	specials := special(root, src)

	data := filepath.Join(tmp, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		return Backup{}, err
	}
	db := filepath.Join(src.DataDir, dbName)
	if _, err := os.Stat(db); err == nil {
		dst := filepath.Join(data, dbName)
		copyDB := src.CopyDB
		if copyDB == nil {
			copyDB = func(dst string) error { return store.CopyFile(db, dst) }
		}
		if err := copyDB(dst); err != nil {
			return Backup{}, fmt.Errorf("copying the database: %w", err)
		}
		fi, err := store.ReadFileInfo(dst)
		if err != nil {
			return Backup{}, fmt.Errorf("reading the database's copy: %w", err)
		}
		m.Schema, m.DBFormat, m.Counts = fi.Schema, fi.Format, fi.Counts
	}
	n, err := copyPart(src.DataDir, data, dataKeep, specials)
	if err != nil {
		return Backup{}, err
	}
	m.Parts = append(m.Parts, "data")

	if rec := src.RecorderDir; rec != "" && !within(src.DataDir, rec) {
		if _, err := os.Stat(rec); err == nil {
			m.RecorderDir = filepath.Clean(rec)
			rn, err := copyPart(rec, filepath.Join(tmp, "recorder"), func(name string) bool {
				return name == "models" || strings.HasPrefix(name, asidePrefix)
			}, []string{root})
			if err != nil {
				return Backup{}, err
			}
			n += rn
			m.Parts = append(m.Parts, "recorder")
			if _, err := os.Stat(filepath.Join(rec, "models")); err == nil {
				m.Skipped = append(m.Skipped, "recorder/models: speech models, downloaded again when missing")
			}
		}
	}

	if len(src.ConfigFiles) > 0 {
		cdir := filepath.Join(tmp, "config")
		if err := os.Mkdir(cdir, 0o700); err != nil {
			return Backup{}, err
		}
		for _, f := range src.ConfigFiles {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			c, err := copyFile(f, filepath.Join(cdir, filepath.Base(f)), 0o600)
			if err != nil {
				return Backup{}, fmt.Errorf("copying %s: %w", f, err)
			}
			n += c
		}
		m.Parts = append(m.Parts, "config")
	}

	dbSize, _ := treeSize(filepath.Join(data, dbName))
	m.Bytes = n + dbSize
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Backup{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, manifestName), b, 0o600); err != nil {
		return Backup{}, err
	}
	dir := filepath.Join(root, name)
	if err := os.Rename(tmp, dir); err != nil {
		return Backup{}, err
	}
	ok = true
	return Backup{Manifest: m, Dir: dir, Name: name}, nil
}

var legacyName = regexp.MustCompile(`^(.*-)(\d{8}-\d{6})$`)

// List is dataset's backups under root, newest first.
func List(root, dataset string) ([]Backup, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Backup
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		b, err := Read(dir)
		if err == nil {
			if b.Dataset == dataset {
				out = append(out, b)
			}
			continue
		}
		if m := legacyName.FindStringSubmatch(e.Name()); m != nil && m[1] == Prefix(dataset) {
			t, _ := time.ParseInLocation("20060102-150405", m[2], time.Local)
			size, _ := treeSize(dir)
			out = append(out, Backup{Manifest: Manifest{Kind: Manual, Reason: "backed up by hand (older layout)", Dataset: dataset, Created: t, Bytes: size},
				Dir: dir, Name: e.Name(), Legacy: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Read reads the backup in dir.
func Read(dir string) (Backup, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return Backup{}, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Backup{}, fmt.Errorf("%s: %w", filepath.Join(dir, manifestName), err)
	}
	return Backup{Manifest: m, Dir: dir, Name: filepath.Base(dir)}, nil
}

// Prune removes dataset's backups of kind beyond the newest keep. Only
// folders with a manifest of that kind are ever removed.
func Prune(root, dataset string, kind Kind, keep int) error {
	mu.Lock()
	defer mu.Unlock()
	return pruneLocked(root, dataset, kind, keep)
}

func pruneLocked(root, dataset string, kind Kind, keep int) error {
	if kind == Manual || kind == Undo {
		return nil
	}
	all, err := List(root, dataset)
	if err != nil {
		return err
	}
	n := 0
	for _, b := range all {
		if b.Legacy || b.Kind != kind {
			continue
		}
		if n++; n > keep {
			if err := os.RemoveAll(b.Dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// Snapshot makes a backup of kind and prunes that kind to keep.
func Snapshot(root string, src Source, kind Kind, reason string, keep int, now time.Time) (Backup, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := makeLocked(root, src, kind, reason, now)
	if err != nil {
		return b, err
	}
	return b, pruneLocked(root, src.Dataset, kind, keep)
}

// BeforeChange backs src up before a step that deletes or rewrites stored
// data in bulk (reason names it), when [backup] before_changes is on; ""
// when off. An error means the step must not run.
func BeforeChange(root string, src Source, c config.Backup, reason string, now time.Time) (string, error) {
	if !c.BeforeChanges || c.KeepBefore <= 0 {
		return "", nil
	}
	b, err := Snapshot(root, src, Before, reason, c.KeepBefore, now)
	if err != nil {
		return "", fmt.Errorf("backing up before %s: %w (free some space, or set [backup] before_changes = false to go ahead without a backup)", reason, err)
	}
	return b.Dir, nil
}

// DailyDue reports whether a daily backup of dataset is due: [backup]
// daily is on and the newest is a day old or more (or there is none).
func DailyDue(root, dataset string, c config.Backup, now time.Time) bool {
	if !c.Daily || c.KeepDaily <= 0 {
		return false
	}
	all, err := List(root, dataset)
	if err != nil {
		return false
	}
	for _, b := range all {
		if b.Kind == Daily && !b.Legacy {
			return now.Sub(b.Created) >= 24*time.Hour
		}
	}
	return true
}
